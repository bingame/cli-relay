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


def marker_count(data, marker=b'# RELAY:START - Relay completion'):
    # 补全块会按 profile 原有编码写回；UTF-16 文件需先解码再统计，否则字节不匹配。
    if data[:2] in (b'\xff\xfe', b'\xfe\xff'):
        encoding = 'utf-16-le' if data[:2] == b'\xff\xfe' else 'utf-16-be'
        return data[2:].decode(encoding, errors='replace').count(marker.decode())
    return data.count(marker)


def long_path(path):
    """展开 8.3 短名并统一分隔符，保证与安装器写入的规范化绝对路径一致。

    CI 等环境的 TEMP 可能是 C:/Users/RUNNER~1/... 这类短路径形态，而
    install.ps1 经 .NET GetFullPath 展开为长路径后再写入 profile。
    """
    if os.name != 'nt':
        return str(path)
    import ctypes
    buf = ctypes.create_unicode_buffer(32768)
    length = ctypes.windll.kernel32.GetLongPathNameW(str(path), buf, len(buf))
    result = buf.value if 0 < length < len(buf) else str(path)
    return result.replace('/', '\\')


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
                   RELAY_CODEX_BIN=str(binary), RELAY_CLAUDE_BIN=str(binary), SHELL='/bin/bash', RELAY_DOWNLOAD_MODE='direct')
        if windows:
            env['RELAY_NO_MODIFY_PATH'] = '1'
            env['RELAY_SHELL_CONFIG_DIR'] = str(root)
            asset = 'relay-windows-amd64.exe'
            shutil.copyfile(binary, fixtures / asset)
            target = root / 'Relay' / 'bin' / 'relay.exe'
            # ReadAllLines 先关闭文件，再模拟 gh 原生管道的逐行字符串输出；Out-String 必须聚合完整脚本。
            wrapper = root / 'test.ps1'
            wrapper.write_text('''$ErrorActionPreference = 'Stop'
function Invoke-RestMethod { throw 'direct installer must not call Invoke-RestMethod' }
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
    [IO.File]::ReadAllLines($env:RELAY_TEST_INSTALLER, [Text.Encoding]::ASCII) | Out-String | Invoke-Expression
} catch { Write-Error $_; exit 1 }
''', encoding='utf-8')
            env['RELAY_TEST_INSTALLER'] = str(REPO / 'install.ps1')
            # 补全注册探测依赖 pwsh 的 CommandCompletion 原生参数补全；Windows PowerShell 5.1
            # 对原生命令的 CompleteInput 不触发注册的 ArgumentCompleter，因此优先使用 pwsh。
            shell = args.powershell or shutil.which('pwsh') or shutil.which('powershell')
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

        def install(success=True, overrides=None):
            run_env = dict(env)
            if overrides:
                run_env.update(overrides)
            result = subprocess.run(command, cwd=root, env=run_env, capture_output=True, timeout=120)
            check((result.returncode == 0) == success,
                  f'安装器退出码异常 {result.returncode}: ' + result.stdout.decode('utf-8', 'replace') + result.stderr.decode('utf-8', 'replace'))
            return result

        # 首次安装：验证二进制、自动 Skill，以及补全配置文件被创建/写入。
        install()
        check(target.read_bytes() == binary.read_bytes(), '安装后的二进制不一致')
        for cli in ['codex', 'claude']:
            installed = root / cli / 'skills' / 'relay-handoff' / 'SKILL.md'
            check(installed.read_bytes() == (REPO / 'skills/relay-handoff/SKILL.md').read_bytes(), '没有自动安装内嵌 Skill')

        if windows:
            profiles = {
                'pwsh': root / 'PowerShell' / 'Microsoft.PowerShell_profile.ps1',
                'ps': root / 'WindowsPowerShell' / 'Microsoft.PowerShell_profile.ps1',
            }
            for name, profile in profiles.items():
                check(profile.exists(), f'安装器未创建 {name} profile')
                check(marker_count(profile.read_bytes()) == 1, f'{name} profile 补全块数量异常')
                expected = long_path(target).replace("'", "''").encode()
                if expected not in profile.read_bytes():
                    print(f'诊断 {name}: target={target!r}', file=sys.stderr)
                    print(f'诊断 {name}: profile 内容={profile.read_bytes()!r}', file=sys.stderr)
                check(expected in profile.read_bytes(), f'{name} profile 未写入绝对补全路径')

            # 预置带不同编码的 profile，验证安装器保留原内容、编码和换行。
            profiles['pwsh'].write_bytes(b'\xef\xbb\xbf' + b"Write-Host 'pwsh-profile'\n")
            profiles['ps'].write_bytes(b'\xff\xfe' + "Write-Host 'ps-profile'\n".encode('utf-16-le'))
            install()  # 第二次安装，验证内容/编码保留
            pwsh_bytes = profiles['pwsh'].read_bytes()
            ps_bytes = profiles['ps'].read_bytes()
            check(pwsh_bytes.startswith(b'\xef\xbb\xbf'), 'UTF-8 BOM 未保留')
            check(ps_bytes.startswith(b'\xff\xfe'), 'UTF-16 LE BOM 未保留')
            check(b'Write-Host \'pwsh-profile\'' in pwsh_bytes, 'pwsh profile 原有内容丢失')
            check("Write-Host 'ps-profile'".encode('utf-16-le') in ps_bytes, 'ps profile 原有内容丢失')
            check(marker_count(pwsh_bytes) == 1 and marker_count(ps_bytes) == 1, '编码保留安装后补全块数量异常')
            # 重复安装保持幂等（仍只有一个补全块）。
            install()
            check(marker_count(profiles['pwsh'].read_bytes()) == 1, '重复安装使 pwsh 补全块重复')
            check(marker_count(profiles['ps'].read_bytes()) == 1, '重复安装使 ps 补全块重复')

            # 私有仓库路径也保持幂等。
            install(overrides={'RELAY_DOWNLOAD_MODE': 'gh'})
            check(marker_count(profiles['pwsh'].read_bytes()) == 1, 'gh 安装使 pwsh 补全块重复')
            check(marker_count(profiles['ps'].read_bytes()) == 1, 'gh 安装使 ps 补全块重复')

            # 补全注册探测：用一个新进程加载 profile，确认 Tab 补全真的被注册。
            probe = root / 'probe.ps1'
            relay_dir_ps = str(root / 'Relay' / 'bin').replace("'", "''")
            config_ps = str(root).replace("'", "''")
            probe.write_text(r'''$ErrorActionPreference = 'Stop'
$relayDir = '@@RELAY_DIR@@'
$env:PATH = $relayDir + [IO.Path]::PathSeparator + $env:PATH
$config = '@@CONFIG@@'
$profiles = @(
  (Join-Path $config 'PowerShell\\Microsoft.PowerShell_profile.ps1'),
  (Join-Path $config 'WindowsPowerShell\\Microsoft.PowerShell_profile.ps1')
)
foreach ($p in $profiles) { if (Test-Path -LiteralPath $p) { . $p } }
$result = [System.Management.Automation.CommandCompletion]::CompleteInput('relay ', 6, $null)
$names = @($result.CompletionMatches | ForEach-Object { $_.CompletionText })
if ($names -notcontains 'completion') { throw ('completion not registered: ' + ($names -join ',')) }
'OK'
'''.replace('@@RELAY_DIR@@', relay_dir_ps).replace('@@CONFIG@@', config_ps), encoding='utf-8')
            probe_env = dict(env)
            probe_env['PATH'] = str(root / 'Relay' / 'bin') + os.pathsep + probe_env['PATH']
            probe_result = subprocess.run([shell, '-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass', '-File', str(probe)],
                                          cwd=root, env=probe_env, capture_output=True, timeout=90)
            check(probe_result.returncode == 0 and b'OK' in probe_result.stdout,
                  '补全未真正注册: ' + probe_result.stdout.decode('utf-8', 'replace') + probe_result.stderr.decode('utf-8', 'replace'))
            check(not (root / 'relay-state').exists(), '补全探测意外初始化凭据或数据库')

            # RELAY_NO_COMPLETION=1 跳过补全安装。
            profiles['pwsh'].write_bytes(b'\xef\xbb\xbf' + b"Write-Host 'pwsh-profile'\n")
            profiles['ps'].write_bytes(b'\xff\xfe' + "Write-Host 'ps-profile'\n".encode('utf-16-le'))
            install(overrides={'RELAY_NO_COMPLETION': '1'})
            check(marker_count(profiles['pwsh'].read_bytes()) == 0, '跳过安装仍在 pwsh profile 写入补全')
            check(marker_count(profiles['ps'].read_bytes()) == 0, '跳过安装仍在 ps profile 写入补全')

            # 任一 profile 是链接时，两个 profile 都不被修改，安装失败。
            profiles['pwsh'].write_bytes(b'\xef\xbb\xbf' + b"Write-Host 'pwsh-profile'\n")
            profiles['ps'].write_bytes(b'\xff\xfe' + "Write-Host 'ps-profile'\n".encode('utf-16-le'))
            link_target = root / 'pwsh-profile-target.ps1'
            link_target.write_bytes(b"Write-Host 'target'\n")
            profiles['pwsh'].unlink()
            symlink_out = subprocess.run(
                [shell, '-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass', '-Command',
                 f"New-Item -ItemType SymbolicLink -Path '{str(profiles['pwsh']).replace(chr(39), chr(39)+chr(39))}' -Target '{str(link_target).replace(chr(39), chr(39)+chr(39))}'"],
                cwd=root, env=env, capture_output=True, timeout=30)
            if symlink_out.returncode == 0:
                install(False)
                check(marker_count(link_target.read_bytes()) == 0, '链接目标被写入补全')
                check(marker_count(profiles['ps'].read_bytes()) == 0, '任一 profile 为链接时另一个仍被修改')
            else:
                print('跳过符号链接保护测试：当前进程无创建符号链接权限', file=sys.stderr)
                profiles['pwsh'].write_bytes(b'\xef\xbb\xbf' + b"Write-Host 'pwsh-profile'\n")
        else:
            bashrc = root / '.bashrc'
            posix_marker = '# RELAY:START completion'
            bashrc_text = bashrc.read_text(encoding='utf-8')
            check(bashrc_text.count('# Relay') == 1, 'PATH 配置重复写入')
            check(bashrc_text.count(posix_marker) == 1, 'bash 补全块数量异常')
            quoted_target = str(target).replace("'", "'\\''")
            check(quoted_target in bashrc_text, 'bash 补全未写入绝对路径')
            probe = subprocess.run(['bash', '-c', '. "$HOME/.bashrc"; command -v relay'], env=env, capture_output=True, text=True)
            check(probe.returncode == 0 and probe.stdout.strip() == str(target),
                  '含空格/单引号的 PATH 设置无效: ' + repr(bashrc_text) + repr(probe.stderr) + repr(probe.stdout))

            # 幂等升级与私有仓库下载。
            install()
            check(bashrc.read_text(encoding='utf-8').count(posix_marker) == 1, '重复安装使 bash 补全块重复')
            install(overrides={'RELAY_DOWNLOAD_MODE': 'gh'})
            check(bashrc.read_text(encoding='utf-8').count(posix_marker) == 1, 'gh 安装使 bash 补全块重复')

            # zsh 配置：写入 .zshrc，并只在没有 compdef 时初始化 compinit。
            zshrc = root / '.zshrc'
            install(overrides={'SHELL': '/bin/zsh', 'RELAY_SKIP_SKILLS': '1'})
            zsh_text = zshrc.read_text(encoding='utf-8')
            check(zsh_text.count(posix_marker) == 1, 'zsh 补全块数量异常')
            check('compinit -C' in zsh_text, 'zsh 补全缺少 compinit 初始化')
            check('completion zsh' in zsh_text, 'zsh 补全 eval 行缺失')

            # fish 配置：写入 fish 补全文件，重复安装保持幂等。
            fish_file = root / '.config' / 'fish' / 'completions' / 'relay.fish'
            install(overrides={'SHELL': '/usr/bin/fish', 'RELAY_SKIP_SKILLS': '1'})
            fish_text = fish_file.read_text(encoding='utf-8')
            check(fish_text.count(posix_marker) == 1, 'fish 补全块数量异常')
            check('complete -c relay' in fish_text, 'fish 补全文件缺少 complete 命令')
            install(overrides={'SHELL': '/usr/bin/fish', 'RELAY_SKIP_SKILLS': '1'})
            check(fish_file.read_text(encoding='utf-8').count(posix_marker) == 1, '重复安装使 fish 补全块重复')

            # 已存在且无标记的 fish 文件：保留用户内容并告警，安装仍成功。
            fish_file.parent.mkdir(parents=True, exist_ok=True)
            fish_file.write_text('complete -c relay -l myflag\n', encoding='utf-8')
            install(overrides={'SHELL': '/usr/bin/fish', 'RELAY_SKIP_SKILLS': '1'})
            check(fish_file.read_text(encoding='utf-8') == 'complete -c relay -l myflag\n', '用户定制 fish 补全被覆盖')

            # fish 补全文件是符号链接时不跟随写入（保留目标、安装成功）。
            fish_link_target = root / 'fish-target.fish'
            fish_link_target.write_text('complete -c relay -l custom\n', encoding='utf-8')
            fish_file.unlink()
            try:
                fish_file.symlink_to(fish_link_target)
                install(overrides={'SHELL': '/usr/bin/fish', 'RELAY_SKIP_SKILLS': '1'})
                check(fish_link_target.read_text(encoding='utf-8').count(posix_marker) == 0, 'fish 符号链接目标被写入补全')
                fish_file.unlink()
                fish_file.write_text('complete -c relay -l custom\n', encoding='utf-8')
            except OSError:
                print('跳过 fish 符号链接测试：无法创建符号链接', file=sys.stderr)
                fish_file.write_text('complete -c relay -l custom\n', encoding='utf-8')

            # RELAY_NO_COMPLETION=1 跳过补全，但 PATH 持久化仍进行。
            skip_home = root / 'skip-home'
            skip_home.mkdir(parents=True, exist_ok=True)
            install(overrides={'SHELL': '/bin/bash', 'HOME': str(skip_home),
                               'RELAY_INSTALL_DIR': str(root / 'skipbin'), 'RELAY_NO_COMPLETION': '1',
                               'RELAY_SKIP_SKILLS': '1'})
            skip_rc = skip_home / '.bashrc'
            skip_text = skip_rc.read_text(encoding='utf-8')
            check(skip_text.count('# Relay') == 1, '跳过补全时 PATH 未持久化')
            check(skip_text.count(posix_marker) == 0, '跳过补全仍写入 bash 补全块')

        # 校验失败、下载失败与用户修改保护（原始逻辑保持）。
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
        print('安装器验证通过：安装、PATH、补全写入/幂等/跳过/链接保护、自动 Skill、校验/下载失败、用户修改保护。')


if __name__ == '__main__':
    main()
