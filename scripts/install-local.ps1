# 从当前仓库源码构建 Relay，并覆盖本机已安装的二进制。
#
# 与 install.ps1 的区别：install.ps1 下载 Release 上的预编译产物，不含本地改动；
# 本脚本构建当前工作区代码，供改完代码后立即装到本机测试时使用。
#
# 用法：
#   powershell -ExecutionPolicy Bypass -File scripts/install-local.ps1
#   powershell -ExecutionPolicy Bypass -File scripts/install-local.ps1 -Test
#   powershell -ExecutionPolicy Bypass -File scripts/install-local.ps1 -SkipSkill
#
# 常用环境变量（与 install.ps1 一致）：
#   RELAY_INSTALL_DIR  目标安装目录，默认 %LOCALAPPDATA%\Relay\bin
[CmdletBinding()]
param(
    # 目标安装目录；默认为 RELAY_INSTALL_DIR，再默认为 %LOCALAPPDATA%\Relay\bin
    [string]$InstallDir,

    # 跳过 relay skill install。Skill 内嵌在二进制中，改过 skills/ 后不要跳过
    [switch]$SkipSkill,

    # 构建前先运行 go test ./...，失败则不安装
    [switch]$Test,

    # 不保留旧二进制备份（默认备份为 relay.exe.bak，便于回滚）
    [switch]$NoBackup
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$LASTEXITCODE = 0
$script:RelayCompletionScript = $null

# Windows PowerShell 5.1 的控制台输出默认走系统代码页，中文提示会显示成乱码。
# 只在 5.1 下调整；本脚本通常以独立进程运行，不影响调用者的会话。
if ($PSVersionTable.PSVersion.Major -lt 6) {
    [Console]::OutputEncoding = [Text.Encoding]::UTF8
}

function Install-RelayCompletion {
    param([Parameter(Mandatory = $true)][string]$RelayExe)
    if ($env:RELAY_NO_COMPLETION -eq '1') { return $false }
    if (-not (Test-Path -LiteralPath $RelayExe)) { throw "Relay executable not found: $RelayExe" }
    # Generate and validate the script before touching any profile so a broken
    # completion command cannot leave a half-configured shell behind.
    $generated = & $RelayExe completion powershell | Out-String
    if ($LASTEXITCODE -ne 0 -or -not $generated) { throw 'Unable to generate the PowerShell completion script' }
    $script:RelayCompletionScript = $generated
    $documents = $env:RELAY_SHELL_CONFIG_DIR
    if ($documents) {
        if (-not [IO.Path]::IsPathRooted($documents)) { throw 'RELAY_SHELL_CONFIG_DIR must be an absolute path' }
        $documents = [IO.Path]::GetFullPath($documents)
    } else {
        $documents = [Environment]::GetFolderPath('MyDocuments')
        if (-not $documents) {
            if (-not $env:USERPROFILE) { throw 'Cannot locate the PowerShell profile directory' }
            $documents = Join-Path $env:USERPROFILE 'Documents'
        }
    }
    $start = '# RELAY:START - Relay completion (managed block, do not edit manually)'
    $end = '# RELAY:END'
    $quoted = "'" + $RelayExe.Replace("'", "''") + "'"
    $command = "& $quoted completion powershell | Out-String | Invoke-Expression"
    $pattern = '(?s)' + [regex]::Escape($start) + '.*?' + [regex]::Escape($end) + '[ \t]*\r?\n?'
    $profilePaths = @(
        (Join-Path $documents 'PowerShell\Microsoft.PowerShell_profile.ps1'),
        (Join-Path $documents 'WindowsPowerShell\Microsoft.PowerShell_profile.ps1')
    )
    # Refuse to touch either profile if any of them is a reparse point, so the
    # two hosts never diverge (one updated, one still a link).
    foreach ($profilePath in $profilePaths) {
        if (Test-Path -LiteralPath $profilePath) {
            $item = Get-Item -LiteralPath $profilePath -Force
            if ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) {
                throw "The PowerShell profile is a link and was not modified: $profilePath"
            }
        }
    }
    foreach ($profilePath in $profilePaths) {
        $parent = Split-Path -Parent $profilePath
        if (-not (Test-Path -LiteralPath $parent)) {
            New-Item -ItemType Directory -Force -Path $parent | Out-Null
        }
        $encoding = New-Object System.Text.UTF8Encoding $true
        $text = ''
        if (Test-Path -LiteralPath $profilePath) {
            $bytes = [IO.File]::ReadAllBytes($profilePath)
            $offset = 0
            if ($bytes.Length -ge 3 -and $bytes[0] -eq 239 -and $bytes[1] -eq 187 -and $bytes[2] -eq 191) {
                $encoding = New-Object System.Text.UTF8Encoding $true
                $offset = 3
            } elseif ($bytes.Length -ge 2 -and $bytes[0] -eq 255 -and $bytes[1] -eq 254) {
                $encoding = New-Object System.Text.UnicodeEncoding $false, $true
                $offset = 2
            } elseif ($bytes.Length -ge 2 -and $bytes[0] -eq 254 -and $bytes[1] -eq 255) {
                $encoding = New-Object System.Text.UnicodeEncoding $true, $true
                $offset = 2
            } else {
                $strictUtf8 = New-Object System.Text.UTF8Encoding $false, $true
                try {
                    [void]$strictUtf8.GetString($bytes)
                    $encoding = New-Object System.Text.UTF8Encoding $false
                } catch {
                    $encoding = [Text.Encoding]::Default
                }
            }
            if ($bytes.Length -gt $offset) { $text = $encoding.GetString($bytes, $offset, $bytes.Length - $offset) }
        }
        $newline = "`r`n"
        if ($text -and $text.Contains([string][char]10) -and -not $text.Contains([string]([char]13) + [char]10)) { $newline = [string][char]10 }
        $block = $start + $newline + $command + $newline + $end
        $stripped = [regex]::Replace($text, $pattern, '')
        $stripped = $stripped.TrimEnd([char]13, [char]10)
        if ($stripped.Length -gt 0) {
            $updated = $stripped + $newline + $newline + $block + $newline
        } else {
            $updated = $block + $newline
        }
        if ($updated -ne $text) { [IO.File]::WriteAllText($profilePath, $updated, $encoding) }
    }
    return $true
}

$repoRoot = Split-Path -Parent $PSScriptRoot
if (-not (Test-Path -LiteralPath (Join-Path $repoRoot 'go.mod'))) {
    throw "未在 $repoRoot 找到 go.mod；请从仓库内的 scripts/ 目录运行本脚本"
}
if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    throw '未找到 go 命令；Relay 需要 Go 1.26 或更新版本'
}

# 解析安装目录，规则与 install.ps1 保持一致。
if (-not $InstallDir) { $InstallDir = $env:RELAY_INSTALL_DIR }
if (-not $InstallDir) {
    if (-not $env:LOCALAPPDATA) { throw 'LOCALAPPDATA 未设置，请用 -InstallDir 指定绝对路径' }
    $InstallDir = Join-Path $env:LOCALAPPDATA 'Relay\bin'
}
if (-not [IO.Path]::IsPathRooted($InstallDir)) { throw '-InstallDir 必须是绝对路径' }
$InstallDir = [IO.Path]::GetFullPath($InstallDir)
$target = Join-Path $InstallDir 'relay.exe'

# relay 正在运行时 Windows 无法替换该文件，先明确报错而不是留下半截安装。
$running = @(Get-Process -Name relay -ErrorAction SilentlyContinue)
if ($running.Count -gt 0) {
    throw "检测到 $($running.Count) 个 relay 进程仍在运行（PID: $(($running | ForEach-Object { $_.Id }) -join ', ')）；请先退出它们再重新运行本脚本"
}

# 版本串只用于 relay --version 显示；GoReleaser 才会注入正式 tag。
$revision = (& git -C $repoRoot rev-parse --short HEAD 2>$null)
if (-not $revision) { $revision = 'unknown' }
$dirty = @(& git -C $repoRoot status --porcelain).Count -gt 0
$version = "v0.0.0-local+$revision"
if ($dirty) { $version += '.dirty' }

$work = Join-Path ([IO.Path]::GetTempPath()) ('relay-local-' + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $work | Out-Null
$built = Join-Path $work 'relay.exe'
$stage = $null
$previousCgo = $env:CGO_ENABLED

try {
    if ($Test) {
        Write-Host 'go test ./...'
        & go -C $repoRoot test ./...
        if ($LASTEXITCODE -ne 0) { throw 'go test 失败，已中止安装' }
    }

    Write-Host "go build ($version)"
    $env:CGO_ENABLED = '0'
    $ldflags = "-X github.com/bingame/cli-relay/internal/version.Version=$version"
    & go -C $repoRoot build -trimpath -ldflags $ldflags -o $built ./cmd/relay
    if ($LASTEXITCODE -ne 0) { throw 'go build 失败' }

    # 先拷到安装目录内的暂存名，再同卷原子替换，失败时旧二进制不受影响。
    New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
    if (Test-Path -LiteralPath $target) {
        if ((Get-Item -LiteralPath $target -Force).Attributes -band [IO.FileAttributes]::ReparsePoint) {
            throw "$target 是链接；请先确认现有安装方式，本脚本不覆盖链接"
        }
    }
    $stage = Join-Path $InstallDir ('.relay-install-' + [Guid]::NewGuid().ToString('N') + '.exe')
    Copy-Item -LiteralPath $built -Destination $stage
    if (Test-Path -LiteralPath $target) {
        $backup = if ($NoBackup) { [NullString]::Value } else { "$target.bak" }
        [IO.File]::Replace($stage, $target, $backup)
    } else {
        [IO.File]::Move($stage, $target)
    }
    $stage = $null

    & $target --version
    if ($LASTEXITCODE -ne 0) { throw "$target 安装后无法运行；旧二进制备份在 $target.bak" }

    if (-not $SkipSkill) {
        & $target skill install
        if ($LASTEXITCODE -ne 0) { throw "二进制已安装，但 Skill 安装失败；解决错误后执行 relay skill install" }
    }

    $completionConfigured = $false
    try {
        $completionConfigured = Install-RelayCompletion -RelayExe $target
    } catch {
        throw "二进制已安装，但 Shell 补全安装失败：$($_.Exception.Message)"
    }

    Write-Host "已安装 $version 到 $target"
    if ($completionConfigured) {
        Write-Host "新 PowerShell 窗口会自动加载 relay 命令补全。"
    }
    if (-not $NoBackup -and (Test-Path -LiteralPath "$target.bak")) {
        Write-Host "回滚：Copy-Item -LiteralPath '$target.bak' -Destination '$target' -Force"
    }
} finally {
    $env:CGO_ENABLED = $previousCgo
    if ($stage -and (Test-Path -LiteralPath $stage)) { Remove-Item -LiteralPath $stage -Force }
    $resolvedWork = [IO.Path]::GetFullPath($work)
    $tempRoot = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\') + '\'
    if ($resolvedWork.StartsWith($tempRoot, [StringComparison]::OrdinalIgnoreCase) -and ([IO.Path]::GetFileName($resolvedWork) -like 'relay-local-*')) {
        Remove-Item -LiteralPath $resolvedWork -Recurse -Force
    }
}
if ($script:RelayCompletionScript) { Invoke-Expression $script:RelayCompletionScript | Out-Null }
