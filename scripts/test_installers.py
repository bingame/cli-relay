"""使用真实 Relay 二进制和离线下载替身验证安装器；不访问真实配置或用户 PATH。"""
import argparse
import hashlib
import os
from pathlib import Path
import platform
import shutil
import subprocess
import sys
import tarfile
import tempfile


REPO = Path(__file__).resolve().parents[1]


def check(value, message):
    if not value:
        raise AssertionError(message)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', required=True)
    parser.add_argument('--powershell', help='Windows 测试使用的 PowerShell 入口')
    args = parser.parse_args()
    binary = Path(args.binary).resolve()
    check(binary.is_file(), '缺少待测二进制')
    windows = os.name == 'nt'
    with tempfile.TemporaryDirectory(prefix="relay installer ' ") as temp:
        root = Path(temp)
        fixtures = root / 'downloads'
        fixtures.mkdir()
        check(all(byte < 128 for byte in (REPO / 'install.ps1').read_bytes()),
              'install.ps1 必须保持纯 ASCII，避免 PowerShell 原生管道按系统代码页解码时损坏')
        env = {k: v for k, v in os.environ.items() if k.upper() in {
            'SYSTEMROOT', 'WINDIR', 'PATH', 'PATHEXT', 'COMSPEC', 'TEMP', 'TMP',
            'PROCESSOR_ARCHITECTURE', 'PROCESSOR_ARCHITEW6432'}}
        for key in ['HOME', 'USERPROFILE', 'APPDATA', 'LOCALAPPDATA']:
            env[key] = str(root)
        env.update(CODEX_HOME=str(root / 'codex'), CLAUDE_CONFIG_DIR=str(root / 'claude'),
                   RELAY_HOME=str(root / 'relay-state'), RELAY_TEST_FIXTURES=str(fixtures),
                   RELAY_CODEX_BIN=str(binary), RELAY_CLAUDE_CODE_BIN=str(binary), SHELL='/bin/bash', RELAY_DOWNLOAD_MODE='direct')
        if windows:
            env['RELAY_NO_MODIFY_PATH'] = '1'
            asset = 'relay-windows-amd64.exe'
            shutil.copyfile(binary, fixtures / asset)
            target = root / 'Relay' / 'bin' / 'relay.exe'
            # 脚本及测试 wrapper 均从 UTF-8 显式解码，兼容 Windows PowerShell 5.1。
            wrapper = root / 'test.ps1'
            wrapper.write_text('''$ErrorActionPreference = 'Stop'
function Invoke-RestMethod { param($Uri, $TimeoutSec) return @{ tag_name = 'v0.1.0' } }
function gh {
    $global:LASTEXITCODE = 0
    if ($args[0] -eq 'api') { return 'v0.1.0' }
    $asset = $args[[Array]::IndexOf($args, '--pattern') + 1]
    $destination = $args[[Array]::IndexOf($args, '--output') + 1]
    Copy-Item -LiteralPath (Join-Path $env:RELAY_TEST_FIXTURES $asset) -Destination $destination
}
function Invoke-WebRequest {
    param($Uri, $OutFile, $TimeoutSec, [switch]$UseBasicParsing)
    $name = ([Uri]$Uri).Segments[-1]
    Copy-Item -LiteralPath (Join-Path $env:RELAY_TEST_FIXTURES $name) -Destination $OutFile
}
try {
    Invoke-Expression ([IO.File]::ReadAllText($env:RELAY_TEST_INSTALLER, [Text.Encoding]::UTF8))
} catch { Write-Error $_; exit 1 }
''', encoding='utf-8')
            env['RELAY_TEST_INSTALLER'] = str(REPO / 'install.ps1')
            shell = args.powershell or shutil.which('powershell') or shutil.which('pwsh')
            check(shell, '缺少 PowerShell')
            command = [shell, '-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass', '-File', str(wrapper)]
        else:
            target_os = 'darwin' if sys.platform == 'darwin' else 'linux'
            arch = 'arm64' if platform.machine() in ['aarch64', 'arm64'] else 'amd64'
            asset = f'relay-{target_os}-{arch}.tar.gz'
            with tarfile.open(fixtures / asset, 'w:gz') as archive:
                archive.add(binary, arcname='relay')
            target = root / '.local' / 'bin' / 'relay'
            mocks = root / 'mocks'
            mocks.mkdir()
            curl = mocks / 'curl'
            curl.write_text('#!' + sys.executable + '''
import os, sys, shutil
from pathlib import Path
args = sys.argv[1:]
urls = [s for s in args if s.startswith('https://')]
if not urls: sys.exit(2)
url = urls[0]
if url.endswith('/releases/latest'):
    print('https://github.com/bingame/cli-relay/releases/tag/v0.1.0', end='')
else:
    shutil.copyfile(Path(os.environ['RELAY_TEST_FIXTURES']) / url.rsplit('/', 1)[1], args[args.index('-o')+1])
''', encoding='utf-8')
            curl.chmod(0o755)
            gh = mocks / 'gh'
            gh.write_text('#!' + sys.executable + '''
import os, sys, shutil
from pathlib import Path
args = sys.argv[1:]
if args[0] == 'api': print('v0.1.0')
else: shutil.copyfile(Path(os.environ['RELAY_TEST_FIXTURES']) / args[args.index('--pattern')+1], args[args.index('--output')+1])
''', encoding='utf-8')
            gh.chmod(0o755)
            env['PATH'] = str(mocks) + os.pathsep + env['PATH']
            command = ['sh', str(REPO / 'install.sh')]

        digest = hashlib.sha256((fixtures / asset).read_bytes()).hexdigest()
        checksums = fixtures / 'checksums.txt'
        checksums.write_text(digest + '  ' + asset + '\n', encoding='ascii')

        def install(success=True):
            result = subprocess.run(command, cwd=root, env=env, capture_output=True, timeout=90)
            check((result.returncode == 0) == success,
                  f'安装器退出码异常 {result.returncode}: ' + result.stdout.decode('utf-8', 'replace') + result.stderr.decode('utf-8', 'replace'))

        install()
        check(target.read_bytes() == binary.read_bytes(), '安装后的二进制不一致')
        for cli in ['codex', 'claude']:
            installed = root / cli / 'skills' / 'relay-handoff' / 'SKILL.md'
            check(installed.read_bytes() == (REPO / 'skills/relay-handoff/SKILL.md').read_bytes(), '没有自动安装内嵌 Skill')
        install()  # 原子升级和幂等重复安装
        env['RELAY_DOWNLOAD_MODE'] = 'gh'
        install()  # 私有仓库通过已登录的 gh 下载，不向 argv 传入凭据
        env['RELAY_DOWNLOAD_MODE'] = 'direct'
        if not windows:
            profile = root / '.bashrc'
            check(profile.read_text().count('# Relay') == 1, 'PATH 配置重复写入')
            probe = subprocess.run(['sh', '-c', '. "$HOME/.bashrc"; command -v relay'], env=env, capture_output=True, text=True)
            check(probe.returncode == 0 and probe.stdout.strip() == str(target),
                  '含空格/单引号的 PATH 设置无效: ' + repr(profile.read_text()) + repr(probe.stderr) + repr(probe.stdout))

        checksums.write_text('0' * 64 + '  ' + asset + '\n', encoding='ascii')
        install(False)
        check(target.read_bytes() == binary.read_bytes(), '校验失败破坏了旧版本')
        checksums.write_text((digest + '  ' + asset + '\n') * 2, encoding='ascii')
        install(False)
        checksums.write_text(digest + '  ' + asset + '\n', encoding='ascii')
        (fixtures / asset).rename(fixtures / 'missing-download')
        install(False)
        check(target.read_bytes() == binary.read_bytes(), '下载失败破坏了旧版本')
        (fixtures / 'missing-download').rename(fixtures / asset)

        skill = root / 'codex' / 'skills' / 'relay-handoff' / 'SKILL.md'
        skill.write_text('用户定制 Skill', encoding='utf-8')
        install(False)
        check(skill.read_text(encoding='utf-8') == '用户定制 Skill', '安装器覆盖了用户定制 Skill')
        check(not (root / 'relay-state').exists(), '安装意外初始化凭据或数据库')
        print('安装器验证通过：首次安装、PATH、自动 Skill、幂等升级、校验/下载失败、用户修改保护。')


if __name__ == '__main__':
    main()
