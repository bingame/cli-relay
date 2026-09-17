"""通过真实 Claude 和本地假服务验证 Skill 发现及 Relay 凭据隔离，不使用真实供应商。"""
import argparse
import http.server
import json
import os
from pathlib import Path
import subprocess
import tempfile
import threading


FAKE_KEY = 'relay-skill-local-fake-key'


class LocalModel(http.server.BaseHTTPRequestHandler):
    observations = []

    def log_message(self, *_args):
        pass

    def do_POST(self):
        body = self.rfile.read(int(self.headers.get('Content-Length', '0')))
        request = json.loads(body)
        self.observations.append({
            'skill': 'relay-handoff' in json.dumps(request),
            'plugin': 'relay:relay-handoff' in json.dumps(request),
            'auth': self.headers.get('Authorization') == 'Bearer ' + FAKE_KEY,
        })
        self.send_response(200)
        self.send_header('Content-Type', 'text/event-stream')
        self.end_headers()
        events = [
            {'type': 'message_start', 'message': {
                'id': 'msg_relay_skill_test', 'type': 'message', 'role': 'assistant',
                'model': 'claude-sonnet-4-5', 'content': [], 'stop_reason': None,
                'stop_sequence': None, 'usage': {'input_tokens': 1, 'output_tokens': 0}}},
            {'type': 'content_block_start', 'index': 0, 'content_block': {'type': 'text', 'text': ''}},
            {'type': 'content_block_delta', 'index': 0, 'delta': {'type': 'text_delta', 'text': 'PROBE_OK'}},
            {'type': 'content_block_stop', 'index': 0},
            {'type': 'message_delta', 'delta': {'stop_reason': 'end_turn', 'stop_sequence': None}, 'usage': {'output_tokens': 1}},
            {'type': 'message_stop'},
        ]
        for event in events:
            self.wfile.write(('event: ' + event['type'] + '\ndata: ' + json.dumps(event) + '\n\n').encode())
        self.wfile.flush()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--relay', required=True)
    parser.add_argument('--claude', required=True, help='原生 Claude 二进制路径')
    args = parser.parse_args()
    relay, claude = str(Path(args.relay).resolve()), str(Path(args.claude).resolve())
    with tempfile.TemporaryDirectory(prefix='relay-claude-skill-') as temp:
        root = Path(temp)
        config = root / 'claude'
        config.mkdir()
        # 虚构旧凭据用于确认 Relay 不会重新打开 user settings。
        (config / 'settings.json').write_text(json.dumps({'env': {'ANTHROPIC_AUTH_TOKEN': 'fake-old-global-key'}}))
        (root / 'empty-settings.json').write_text('{}')
        server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), LocalModel)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        env = {k: v for k, v in os.environ.items() if k.upper() in {
            'SYSTEMROOT', 'WINDIR', 'PATH', 'PATHEXT', 'COMSPEC', 'TEMP', 'TMP'}}
        for key in ['HOME', 'USERPROFILE', 'APPDATA', 'LOCALAPPDATA']:
            env[key] = str(root)
        env.update(CLAUDE_CONFIG_DIR=str(config), CODEX_HOME=str(root / 'codex'),
                   RELAY_HOME=str(root / 'relay'), RELAY_CLAUDE_CODE_BIN=claude,
                   RELAY_PASSPHRASE='relay-local-test-passphrase-only',
                   ANTHROPIC_AUTH_TOKEN=FAKE_KEY, ANTHROPIC_BASE_URL=f'http://127.0.0.1:{server.server_port}',
                   CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC='1', DISABLE_AUTOUPDATER='1')

        def run(argv, input_text=''):
            result = subprocess.run(argv, input=input_text, env=env, cwd=root, capture_output=True, text=True, encoding='utf-8', timeout=60)
            if result.returncode != 0:
                raise RuntimeError(f'探针进程退出码 {result.returncode}')
            if FAKE_KEY in result.stdout + result.stderr:
                raise AssertionError('测试凭据进入 CLI 输出')

        try:
            run([relay, 'skill', 'install', '--cli', 'claude-code'])
            for flags, expected in [([], True), (['--setting-sources', '', '--settings', str(root / 'empty-settings.json')], False)]:
                LocalModel.observations.clear()
                run([claude, *flags, '-p', '--model', 'claude-sonnet-4-5', 'Reply PROBE_OK'])
                assert LocalModel.observations and any(o['skill'] for o in LocalModel.observations) == expected
            run([relay, 'provider', 'add', '--id', 'probe', '--target', 'claude-code', '--base-url', env['ANTHROPIC_BASE_URL'],
                 '--model', 'claude-sonnet-4-5', '--api-key-stdin'], FAKE_KEY)
            LocalModel.observations.clear()
            run([relay, 'exec', 'claude-code', '--provider', 'probe', '--', 'Reply PROBE_OK'])
            assert LocalModel.observations
            assert all(o['auth'] for o in LocalModel.observations), '全局配置覆盖了 Relay 凭据'
            assert any(o['plugin'] for o in LocalModel.observations), '隔离启动未加载 Handoff 插件'
            print('真实 Claude + Relay 验证通过：Skill 发现、隔离插件加载、供应商凭据未被全局设置覆盖。')
        finally:
            server.shutdown()
            server.server_close()


if __name__ == '__main__':
    main()
