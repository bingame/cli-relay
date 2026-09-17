# 可通过 irm <url> | iex 执行；仅下载预编译的 Windows amd64 产物。
function Install-Relay {
    $ErrorActionPreference = 'Stop'
    Set-StrictMode -Version Latest
    if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) {
        throw '请在 Linux/macOS 使用 install.sh'
    }
    $architecture = $env:PROCESSOR_ARCHITEW6432
    if (-not $architecture) { $architecture = $env:PROCESSOR_ARCHITECTURE }
    if ($architecture -ne 'AMD64') { throw '当前发行仅支持 Windows amd64' }
    [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
    $repository = $env:RELAY_REPOSITORY
    if (-not $repository) { $repository = 'bingame/cli-relay' }
    if ($repository -notmatch '^[\w.-]+/[\w.-]+$' -or $repository.Contains('..')) { throw '无效的 RELAY_REPOSITORY' }
    $mode = $env:RELAY_DOWNLOAD_MODE
    if (-not $mode) { $mode = 'direct' }
    if ($mode -notin @('direct', 'gh')) { throw 'RELAY_DOWNLOAD_MODE 只支持 direct 或 gh' }
    if ($mode -eq 'gh' -and -not (Get-Command gh -ErrorAction SilentlyContinue)) { throw '私有发行需要已登录的 GitHub CLI (gh)' }
    $version = $env:RELAY_VERSION
    if (-not $version -or $version -eq 'latest') {
        if ($mode -eq 'gh') {
            $version = & gh api "repos/$repository/releases/latest" --jq .tag_name
            if ($LASTEXITCODE -ne 0) { throw '无法读取私有发行；请确认 gh 已登录且有仓库访问权限' }
        } else {
            $release = Invoke-RestMethod -Uri "https://api.github.com/repos/$repository/releases/latest" -TimeoutSec 60
            $version = $release.tag_name
        }
    }
    if ($version -notmatch '^v[0-9][a-zA-Z0-9.+-]*$') { throw '版本必须为 v 开头的发布 tag，例如 v0.1.0' }
    $installDir = $env:RELAY_INSTALL_DIR
    if (-not $installDir) {
        if (-not $env:LOCALAPPDATA) { throw 'LOCALAPPDATA 未设置，请指定 RELAY_INSTALL_DIR' }
        $installDir = Join-Path $env:LOCALAPPDATA 'Relay\bin'
    }
    if (-not [IO.Path]::IsPathRooted($installDir)) { throw 'RELAY_INSTALL_DIR 必须为绝对路径' }
    $installDir = [IO.Path]::GetFullPath($installDir)
    $work = Join-Path ([IO.Path]::GetTempPath()) ('relay-install-' + [Guid]::NewGuid().ToString('N'))
    $stage = $null
    New-Item -ItemType Directory -Path $work | Out-Null
    try {
        $base = "https://github.com/$repository/releases/download/$version"
        $asset = 'relay-windows-amd64.exe'
        $download = Join-Path $work $asset
        $checksums = Join-Path $work 'checksums.txt'
        if ($mode -eq 'gh') {
            & gh release download $version --repo $repository --pattern checksums.txt --output $checksums
            if ($LASTEXITCODE -ne 0) { throw '下载 checksums.txt 失败' }
            & gh release download $version --repo $repository --pattern $asset --output $download
            if ($LASTEXITCODE -ne 0) { throw '下载 Relay 二进制失败' }
        } else {
            Invoke-WebRequest -UseBasicParsing -Uri "$base/checksums.txt" -OutFile $checksums -TimeoutSec 300
            Invoke-WebRequest -UseBasicParsing -Uri "$base/$asset" -OutFile $download -TimeoutSec 300
        }
        $entries = @(Get-Content -LiteralPath $checksums | Where-Object { $_ -match ('^[a-fA-F0-9]{64}  ' + [regex]::Escape($asset) + '$') })
        if ($entries.Count -ne 1) { throw '校验文件缺少唯一的 SHA-256 条目' }
        $expected = $entries[0].Substring(0, 64)
        if ((Get-FileHash -LiteralPath $download -Algorithm SHA256).Hash -ne $expected) {
            throw 'SHA-256 校验失败，未修改现有安装'
        }
        New-Item -ItemType Directory -Force -Path $installDir | Out-Null
        $target = Join-Path $installDir 'relay.exe'
        if (Test-Path -LiteralPath $target) {
            if ((Get-Item -LiteralPath $target -Force).Attributes -band [IO.FileAttributes]::ReparsePoint) { throw '目标 relay.exe 是链接，请先检查现有安装' }
        }
        $stage = Join-Path $installDir ('.relay-install-' + [Guid]::NewGuid().ToString('N') + '.exe')
        Copy-Item -LiteralPath $download -Destination $stage
        if (Test-Path -LiteralPath $target) {
            # 同卷原子替换；占用导致失败时保留旧版本。
            [IO.File]::Replace($stage, $target, [NullString]::Value)
        } else {
            [IO.File]::Move($stage, $target)
        }
        $stage = $null
        if ($env:RELAY_NO_MODIFY_PATH -ne '1') {
            $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
            $present = @($userPath -split ';' | Where-Object { [Environment]::ExpandEnvironmentVariables($_).TrimEnd('\') -ieq $installDir.TrimEnd('\') })
            if ($present.Count -eq 0) {
                $newPath = if ([string]::IsNullOrEmpty($userPath)) { $installDir } else { $userPath.TrimEnd(';') + ';' + $installDir }
                [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
            }
        }
        if (@($env:PATH -split ';' | Where-Object { $_.TrimEnd('\') -ieq $installDir.TrimEnd('\') }).Count -eq 0) {
            $env:PATH = $installDir + ';' + $env:PATH
        }
        & $target --version
        if ($LASTEXITCODE -ne 0) { throw '已下载的 Relay 无法运行' }
        if ($env:RELAY_SKIP_SKILLS -ne '1') {
            & $target skill install
            if ($LASTEXITCODE -ne 0) { throw '二进制已安装，但 Skill 安装失败；处理上述问题后运行 relay skill install' }
        }
        Write-Host "Relay 已安装到 $target。运行 relay --help 开始使用。"
    } finally {
        if ($stage -and (Test-Path -LiteralPath $stage)) { Remove-Item -LiteralPath $stage -Force }
        # work 是本函数创建的唯一临时目录，解析并验证后才递归删除。
        $resolvedWork = [IO.Path]::GetFullPath($work)
        $tempRoot = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\') + '\'
        if ($resolvedWork.StartsWith($tempRoot, [StringComparison]::OrdinalIgnoreCase) -and ([IO.Path]::GetFileName($resolvedWork) -like 'relay-install-*')) {
            Remove-Item -LiteralPath $resolvedWork -Recurse -Force
        }
    }
}
Install-Relay
