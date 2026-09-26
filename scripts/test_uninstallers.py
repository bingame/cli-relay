"""在隔离目录验证卸载器；不修改真实 PATH、CLI 配置或 Relay 数据。"""
import hashlib
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile


REPO = Path(__file__).resolve().parents[1]
SKILL = (REPO / 'skills/relay-handoff/SKILL.md').read_bytes()
DIGEST = hashlib.sha256(SKILL).hexdigest()


def check(value, message):
    if not value:
        raise AssertionError(message)


def put_skill(path, modified=False):
    path.mkdir(parents=True)
    (path / 'SKILL.md').write_bytes(b'custom' if modified else SKILL)
    (path / '.relay-sha256').write_text(DIGEST + '\n', encoding='ascii')


def test_windows(root):
    shell = shutil.which('pwsh') or shutil.which('powershell')
    check(shell, '缺少 PowerShell')
    install_dir = root / 'Relay' / 'bin'
    install_dir.mkdir(parents=True)
    (install_dir / 'relay.exe').write_bytes(b'dummy executable')
    profiles = [root / host / 'Microsoft.PowerShell_profile.ps1' for host in ('PowerShell', 'WindowsPowerShell')]
    start = '# RELAY:START - Relay completion (managed block, do not edit manually)'
    profile_text = "Write-Host 'user'\n" + start + "\nmanaged\n# RELAY:END\n"
    profiles[0].parent.mkdir(parents=True)
    profiles[0].write_bytes(b'\xef\xbb\xbf' + profile_text.encode('utf-8'))
    profiles[1].parent.mkdir(parents=True)
    profiles[1].write_bytes(b'\xff\xfe' + profile_text.encode('utf-16-le'))
    put_skill(root / 'claude' / 'skills' / 'relay-handoff')
    put_skill(root / 'codex' / 'skills' / 'relay-handoff', modified=True)
    data = root / 'relay-state'
    data.mkdir()
    (data / 'providers.db').write_bytes(b'encrypted fixture')
    env = {k: v for k, v in os.environ.items() if k.upper() in {
        'SYSTEMROOT', 'WINDIR', 'PATH', 'PATHEXT', 'COMSPEC', 'TEMP', 'TMP'}}
    env.update(USERPROFILE=str(root), LOCALAPPDATA=str(root), RELAY_HOME=str(data),
               RELAY_INSTALL_DIR=str(install_dir), RELAY_SHELL_CONFIG_DIR=str(root),
               CLAUDE_CONFIG_DIR=str(root / 'claude'), CODEX_HOME=str(root / 'codex'),
               RELAY_NO_MODIFY_PATH='1')
    command = [shell, '-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass', '-File', str(REPO / 'uninstall.ps1')]
    for _ in range(2):
        result = subprocess.run(command, env=env, cwd=root, capture_output=True, text=True, encoding='utf-8', timeout=30)
        check(result.returncode == 0, result.stdout + result.stderr)
    check(not (install_dir / 'relay.exe').exists(), '未删除 Relay 二进制')
    for index, profile in enumerate(profiles):
        raw = profile.read_bytes()
        text = raw[3:].decode('utf-8') if index == 0 else raw[2:].decode('utf-16-le')
        check((raw[:3] == b'\xef\xbb\xbf') if index == 0 else (raw[:2] == b'\xff\xfe'), '意外改变 profile 编码标记')
        check("Write-Host 'user'" in text and start not in text, '未正确清理托管补全')
    check(not (root / 'claude' / 'skills' / 'relay-handoff' / 'SKILL.md').exists(), '未删除未修改的 Skill')
    check((root / 'codex' / 'skills' / 'relay-handoff' / 'SKILL.md').read_bytes() == b'custom', '误删修改过的 Skill')
    check((data / 'providers.db').read_bytes() == b'encrypted fixture', '误删 Relay 数据')


def test_unix(root):
    install_dir = root / '.local' / 'bin'
    install_dir.mkdir(parents=True)
    (install_dir / 'relay').write_bytes(b'dummy executable')
    bashrc = root / '.bashrc'
    bashrc.write_text("export USER_FLAG=1\n# Relay\nexport PATH='" + str(install_dir) +
                      "':\"$PATH\"\n# RELAY:START completion\nmanaged\n# RELAY:END completion\n", encoding='utf-8')
    put_skill(root / '.claude' / 'skills' / 'relay-handoff')
    put_skill(root / '.codex' / 'skills' / 'relay-handoff', modified=True)
    data = root / '.relay'
    data.mkdir()
    (data / 'providers.db').write_bytes(b'encrypted fixture')
    env = dict(os.environ, HOME=str(root), SHELL='/bin/bash', RELAY_INSTALL_DIR=str(install_dir))
    for _ in range(2):
        result = subprocess.run(['sh', str(REPO / 'uninstall.sh')], env=env, cwd=root, capture_output=True, text=True, timeout=30)
        check(result.returncode == 0, result.stdout + result.stderr)
    check(not (install_dir / 'relay').exists(), '未删除 Relay 二进制')
    text = bashrc.read_text(encoding='utf-8')
    check('export USER_FLAG=1' in text and '# RELAY:START' not in text and "export PATH='" not in text,
          '未正确清理托管 shell 配置: ' + repr(text))
    check(not (root / '.claude' / 'skills' / 'relay-handoff' / 'SKILL.md').exists(), '未删除未修改的 Skill')
    check((root / '.codex' / 'skills' / 'relay-handoff' / 'SKILL.md').read_bytes() == b'custom', '误删修改过的 Skill')
    check((data / 'providers.db').read_bytes() == b'encrypted fixture', '误删 Relay 数据')


def main():
    with tempfile.TemporaryDirectory(prefix='relay-uninstall-test-') as tmp:
        root = Path(tmp)
        if os.name == 'nt':
            test_windows(root)
        else:
            test_unix(root)
    print('卸载器验证通过：二进制、托管补全和 Skill 清理；修改保护、数据保留与重复运行。')


if __name__ == '__main__':
    main()
