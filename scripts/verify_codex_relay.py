"""隔离验证 Relay CLI、原生 Codex 和 Multica 风格 stdio wrapper。

先运行 go build -o bin/relay.exe ./cmd/relay，再运行此脚本。
模型请求只指向脚本创建的 127.0.0.1 假 SSE 服务，凭据均为虚构值。
所有 Relay/Codex 配置、会话、工作目录均位于自动清理的临时目录。
"""

import argparse
from collections import deque
from contextlib import ExitStack
import http.server
import json
import os
from pathlib import Path
import queue
import shutil
import signal
import subprocess
import sys
import tempfile
import threading
import time


FAKE_KEY = 'relay-cli-local-fake-key'
FAKE_PASSPHRASE = 'relay-local-test-passphrase-only'
MODEL = 'relay-local-probe-model'
ANSWER = 'RELAY_FULL_CLI_PROBE_OK'


def check(condition, message):
    if not condition:
        raise RuntimeError(message)


def safe_error(text):
    return text.replace(FAKE_KEY, '[已移除虚构凭据]').replace(
        FAKE_PASSPHRASE, '[已移除测试口令]')[-4000:]


def start_process(argv, cwd, env):
    options = dict(cwd=cwd, env=env, stdin=subprocess.PIPE,
                   stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                   text=True, encoding='utf-8', errors='replace', bufsize=1)
    if os.name == 'nt':
        options['creationflags'] = subprocess.CREATE_NO_WINDOW
    else:
        options['start_new_session'] = True
    return subprocess.Popen([str(part) for part in argv], **options)


def stop_tree(process):
    if process.poll() is not None:
        return
    if os.name == 'nt':
        subprocess.run(['taskkill', '/PID', str(process.pid), '/T', '/F'],
                       stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                       timeout=10, creationflags=subprocess.CREATE_NO_WINDOW)
    else:
        os.killpg(process.pid, signal.SIGKILL)
    try:
        process.wait(timeout=10)
    except subprocess.TimeoutExpired:
        process.kill()


def run(argv, cwd, env, timeout, input_text=''):
    child = start_process(argv, cwd, env)
    try:
        stdout, stderr = child.communicate(input_text, timeout=timeout)
    except BaseException:
        stop_tree(child)
        raise
    check(FAKE_KEY not in stdout + stderr, 'CLI 输出泄露了测试凭据')
    check(child.returncode == 0,
          f'CLI 退出码 {child.returncode}: {safe_error(stderr)}\nstdout: {safe_error(stdout)}')
    return stdout


class LocalModel(http.server.BaseHTTPRequestHandler):
    observations = []
    observations_lock = threading.Lock()

    def log_message(self, *_args):
        pass

    def do_POST(self):
        if not self.path.startswith('/v1/responses'):
            self.send_error(404)
            return
        length = int(self.headers.get('Content-Length', '0'))
        if length > 8 * 1024 * 1024:
            self.send_error(413)
            return
        request = json.loads(self.rfile.read(length))
        with self.observations_lock:
            self.observations.append({
                'auth': self.headers.get('Authorization') == 'Bearer ' + FAKE_KEY,
                'model': request.get('model') == MODEL,
            })
            number = len(self.observations)
        item = {'id': f'msg_relay_probe_{number}', 'type': 'message',
                'role': 'assistant', 'status': 'completed',
                'content': [{'type': 'output_text', 'text': ANSWER, 'annotations': []}]}
        response = {'id': f'resp_relay_probe_{number}', 'object': 'response',
                    'status': 'completed', 'output': [item],
                    'usage': {'input_tokens': 1, 'output_tokens': 1, 'total_tokens': 2}}
        events = [
            {'type': 'response.created', 'response': {
                **response, 'status': 'in_progress', 'output': []}},
            {'type': 'response.output_item.added', 'output_index': 0, 'item': item},
            {'type': 'response.output_text.delta', 'item_id': item['id'],
             'output_index': 0, 'content_index': 0, 'delta': ANSWER},
            {'type': 'response.output_item.done', 'output_index': 0, 'item': item},
            {'type': 'response.completed', 'response': response},
        ]
        self.send_response(200)
        self.send_header('Content-Type', 'text/event-stream')
        self.end_headers()
        for event in events:
            self.wfile.write(('event: ' + event['type'] + '\ndata: ' +
                              json.dumps(event) + '\n\n').encode())
        self.wfile.flush()


class RPC:
    def __init__(self, argv, cwd, env, timeout):
        self.process = start_process(argv, cwd, env)
        self.timeout = timeout
        self.frames = queue.Queue()
        self.pending = []
        self.stderr = deque(maxlen=60)
        self.next_id = 1
        self.calls = []
        threading.Thread(target=self._stdout, daemon=True).start()
        threading.Thread(target=self._stderr, daemon=True).start()

    def _stdout(self):
        for line in self.process.stdout:
            try:
                check(FAKE_KEY not in line, 'wrapper stdout 泄露测试凭据')
                self.frames.put(json.loads(line))
            except (ValueError, RuntimeError) as error:
                self.frames.put(error)
        self.frames.put(EOFError('wrapper stdout 已结束'))

    def _stderr(self):
        for line in self.process.stderr:
            self.stderr.append(line)

    def send(self, method, params, request_id=None):
        frame = {'method': method, 'params': params}
        if request_id is not None:
            frame['id'] = request_id
        self.process.stdin.write(json.dumps(frame) + '\n')
        self.process.stdin.flush()

    def wait(self, predicate):
        for index, frame in enumerate(self.pending):
            if predicate(frame):
                return self.pending.pop(index)
        deadline = time.monotonic() + self.timeout
        while True:
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise TimeoutError('等待 RPC 响应超时；' + safe_error(''.join(self.stderr)))
            try:
                frame = self.frames.get(timeout=remaining)
            except queue.Empty as error:
                raise TimeoutError('等待 RPC 响应超时；' + safe_error(''.join(self.stderr))) from error
            if isinstance(frame, BaseException):
                raise RuntimeError(str(frame) + '; ' + safe_error(''.join(self.stderr)))
            if predicate(frame):
                return frame
            self.pending.append(frame)

    def request(self, method, params):
        request_id = self.next_id
        self.next_id += 1
        self.calls.append(method)
        self.send(method, params, request_id)
        response = self.wait(lambda frame: frame.get('id') == request_id)
        check('error' not in response,
              f'{method} 被拒绝: {safe_error(json.dumps(response.get("error"), ensure_ascii=False))}')
        return response.get('result', {})

    def close(self):
        self.process.stdin.close()
        try:
            code = self.process.wait(timeout=15)
        except subprocess.TimeoutExpired:
            stop_tree(self.process)
            raise RuntimeError('wrapper 未在 stdin EOF 后及时退出')
        check(code == 0, f'wrapper 原生退出码 {code}: {safe_error("".join(self.stderr))}')
        check(FAKE_KEY not in ''.join(self.stderr), 'wrapper stderr 泄露测试凭据')
        return code


def verify_metadata(relay_home, session_id, cwd):
    path = relay_home / 'sessions' / 'codex' / (session_id + '.meta.json')
    check(path.is_file(), '未保存 Codex session metadata')
    data = json.loads(path.read_text(encoding='utf-8'))
    check(data['session_id'] == session_id and data['provider'] == 'probe'
          and data['cli'] == 'codex' and data.get('started_at'), '会话元数据字段不符')
    check(Path(data['work_dir']).resolve() == cwd.resolve(), '会话工作目录不符')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    default_binary = 'relay.exe' if os.name == 'nt' else 'relay'
    parser.add_argument('--relay', type=Path,
                        default=Path(__file__).resolve().parents[1] / 'bin' / default_binary)
    parser.add_argument('--codex', type=Path, help='可选原生 Codex 二进制；默认验证 PATH 中的 Codex')
    parser.add_argument('--timeout', type=float, default=45)
    args = parser.parse_args()
    relay = args.relay.resolve()
    check(relay.is_file(), '请先构建 bin/relay.exe，或通过 --relay 指定二进制')
    LocalModel.observations.clear()
    server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), LocalModel)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    rpc = None
    try:
        with tempfile.TemporaryDirectory(prefix='relay-cli-integration-') as directory, ExitStack() as cleanup:
            root = Path(directory)
            relay_home, codex_home, cwd = root / 'relay', root / 'codex', root / 'work'
            codex_home.mkdir()
            cwd.mkdir()
            env = {key: value for key, value in os.environ.items()
                   if key.upper() in {'PATH', 'SYSTEMROOT', 'SYSTEMDRIVE', 'WINDIR', 'COMSPEC', 'PATHEXT'}}
            env.update({key: str(root) for key in
                        ('HOME', 'USERPROFILE', 'APPDATA', 'LOCALAPPDATA', 'TEMP', 'TMP')})
            env.update(RELAY_HOME=str(relay_home), CODEX_HOME=str(codex_home),
                       RELAY_PASSPHRASE=FAKE_PASSPHRASE, NO_COLOR='1')
            if args.codex:
                env['RELAY_CODEX_BIN'] = str(args.codex.resolve())
            (codex_home / 'config.toml').write_text(
                "[shell_environment_policy]\nfilters = { 'RELAY_*' = 'exclude' }\n",
                encoding='utf-8')
            base_url = f'http://127.0.0.1:{server.server_port}/v1'
            run([relay, 'provider', 'add', '--id', 'probe', '--target', 'codex',
                 '--base-url', base_url, '--model', MODEL, '--api-key-stdin'],
                cwd, env, args.timeout, FAKE_KEY + '\n')
            stdout = run([relay, 'exec', 'codex', '--provider', 'probe', '--',
                          '--skip-git-repo-check', '--json',
                          'Reply ' + ANSWER + ' without tools.'], cwd, env, args.timeout)
            events = [json.loads(line) for line in stdout.splitlines() if line.strip()]
            check(any(event.get('type') == 'turn.completed' for event in events),
                  'relay exec 未完成回合')
            session_id = next(event['thread_id'] for event in events
                              if event.get('type') == 'thread.started')
            verify_metadata(relay_home, session_id, cwd)
            check(LocalModel.observations and all(all(row.values()) for row in LocalModel.observations),
                  'relay exec 未向本地服务传递正确鉴权或模型')
            print(json.dumps({'步骤': 'relay exec', '退出码': 0, '正确鉴权': True,
                              'turn.completed': True, '会话元数据': True}, ensure_ascii=False))

            wrapper = root / ('relay-codex.exe' if os.name == 'nt' else 'relay-codex')
            shutil.copy2(relay, wrapper)
            env['RELAY_PROVIDER'] = 'probe'
            before = len(LocalModel.observations)
            rpc = RPC([wrapper, 'app-server', '--listen', 'stdio://'], cwd, env, args.timeout)
            cleanup.callback(stop_tree, rpc.process)
            rpc.request('initialize', {'clientInfo': {'name': 'relay-integration',
                        'title': 'Relay 集成测试', 'version': '0.1.0'},
                        'capabilities': {'experimentalApi': True}})
            rpc.send('initialized', {})
            thread = rpc.request('thread/start', {'cwd': str(cwd),
                                 'approvalPolicy': 'never', 'sandbox': 'read-only',
                                 'experimentalRawEvents': False, 'persistExtendedHistory': True})
            thread_id = thread['thread']['id']
            rpc.request('turn/start', {'threadId': thread_id, 'input': [
                {'type': 'text', 'text': 'Reply ' + ANSWER + ' without tools.'}]})
            completed = rpc.wait(lambda frame: frame.get('method') == 'turn/completed'
                                 and frame.get('params', {}).get('threadId') == thread_id)
            check(completed['params']['turn']['status'] == 'completed', 'wrapper 模型回合失败')
            history = rpc.request('thread/read', {'threadId': thread_id, 'includeTurns': True})
            items = [item for turn in history['thread']['turns'] for item in turn['items']]
            check(any(item.get('type') == 'agentMessage' and ANSWER in item.get('text', '')
                      for item in items), 'thread/read 没有返回刚生成的助手回复')
            code = rpc.close()
            verify_metadata(relay_home, thread_id, cwd)
            check(len(LocalModel.observations) > before
                  and all(all(row.values()) for row in LocalModel.observations[before:]),
                  'wrapper 未向本地服务传递正确鉴权或模型')
            print(json.dumps({'步骤': 'Multica 风格 wrapper', '退出码': code,
                              'JSON-RPC': rpc.calls, '正确鉴权': True, '及时转发': True,
                              'thread/read': True, '会话元数据': True}, ensure_ascii=False))
            rpc = None
    finally:
        if rpc is not None:
            stop_tree(rpc.process)
        server.shutdown()
        server.server_close()


if __name__ == '__main__':
    if hasattr(sys.stdout, 'reconfigure'):
        sys.stdout.reconfigure(encoding='utf-8')
        sys.stderr.reconfigure(encoding='utf-8')
    main()
