# Supports irm <url> | iex and installs the prebuilt Windows amd64 binary.
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

function Install-Relay {
    $ErrorActionPreference = 'Stop'
    Set-StrictMode -Version Latest
    if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) {
        throw 'Use install.sh on Linux or macOS'
    }
    $architecture = $env:PROCESSOR_ARCHITEW6432
    if (-not $architecture) { $architecture = $env:PROCESSOR_ARCHITECTURE }
    if ($architecture -ne 'AMD64') { throw 'This release supports Windows amd64 only' }
    [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
    $repository = $env:RELAY_REPOSITORY
    if (-not $repository) { $repository = 'bingame/cli-relay' }
    if ($repository -notmatch '^[\w.-]+/[\w.-]+$' -or $repository.Contains('..')) { throw 'Invalid RELAY_REPOSITORY' }
    $mode = $env:RELAY_DOWNLOAD_MODE
    if (-not $mode) { $mode = 'direct' }
    if ($mode -notin @('direct', 'gh')) { throw 'RELAY_DOWNLOAD_MODE must be direct or gh' }
    if ($mode -eq 'gh' -and -not (Get-Command gh -ErrorAction SilentlyContinue)) { throw 'Private releases require an authenticated GitHub CLI (gh)' }
    $version = $env:RELAY_VERSION
    $latest = $false
    if (-not $version -or $version -eq 'latest') {
        if ($mode -eq 'gh') {
            $version = & gh api "repos/$repository/releases/latest" --jq .tag_name
            if ($LASTEXITCODE -ne 0) { throw 'Cannot read the private release; check gh authentication and repository access' }
        } else {
            # GitHub API has a low anonymous per-IP rate limit. Public releases expose
            # the same files through a CDN redirect, so avoid api.github.com entirely.
            $latest = $true
        }
    }
    if (-not $latest -and $version -notmatch '^v[0-9][a-zA-Z0-9.+-]*$') { throw 'Version must be a release tag starting with v, for example v0.1.0' }
    $installDir = $env:RELAY_INSTALL_DIR
    if (-not $installDir) {
        if (-not $env:LOCALAPPDATA) { throw 'LOCALAPPDATA is not set; specify RELAY_INSTALL_DIR' }
        $installDir = Join-Path $env:LOCALAPPDATA 'Relay\bin'
    }
    if (-not [IO.Path]::IsPathRooted($installDir)) { throw 'RELAY_INSTALL_DIR must be an absolute path' }
    $installDir = [IO.Path]::GetFullPath($installDir)
    $work = Join-Path ([IO.Path]::GetTempPath()) ('relay-install-' + [Guid]::NewGuid().ToString('N'))
    $stage = $null
    New-Item -ItemType Directory -Path $work | Out-Null
    try {
        $base = if ($latest) {
            "https://github.com/$repository/releases/latest/download"
        } else {
            "https://github.com/$repository/releases/download/$version"
        }
        $asset = 'relay-windows-amd64.exe'
        $download = Join-Path $work $asset
        $checksums = Join-Path $work 'checksums.txt'
        if ($mode -eq 'gh') {
            & gh release download $version --repo $repository --pattern checksums.txt --output $checksums
            if ($LASTEXITCODE -ne 0) { throw 'Failed to download checksums.txt' }
            & gh release download $version --repo $repository --pattern $asset --output $download
            if ($LASTEXITCODE -ne 0) { throw 'Failed to download the Relay binary' }
        } else {
            Invoke-WebRequest -UseBasicParsing -Uri "$base/checksums.txt" -OutFile $checksums -TimeoutSec 300
            Invoke-WebRequest -UseBasicParsing -Uri "$base/$asset" -OutFile $download -TimeoutSec 300
        }
        $entries = @(Get-Content -LiteralPath $checksums | Where-Object { $_ -match ('^[a-fA-F0-9]{64}  ' + [regex]::Escape($asset) + '$') })
        if ($entries.Count -ne 1) { throw 'The checksum file must contain exactly one SHA-256 entry for the asset' }
        $expected = $entries[0].Substring(0, 64)
        if ((Get-FileHash -LiteralPath $download -Algorithm SHA256).Hash -ne $expected) {
            throw 'SHA-256 verification failed; the existing installation was not changed'
        }
        New-Item -ItemType Directory -Force -Path $installDir | Out-Null
        $target = Join-Path $installDir 'relay.exe'
        if (Test-Path -LiteralPath $target) {
            if ((Get-Item -LiteralPath $target -Force).Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'The target relay.exe is a link; inspect the existing installation first' }
        }
        $stage = Join-Path $installDir ('.relay-install-' + [Guid]::NewGuid().ToString('N') + '.exe')
        Copy-Item -LiteralPath $download -Destination $stage
        if (Test-Path -LiteralPath $target) {
            # Atomic replacement on the same volume preserves the old binary on failure.
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
        if ($LASTEXITCODE -ne 0) { throw 'The downloaded Relay binary could not run' }
        if ($env:RELAY_SKIP_SKILLS -ne '1') {
            & $target skill install
            if ($LASTEXITCODE -ne 0) { throw 'The binary is installed, but Skill installation failed; resolve the error and run relay skill install' }
        }
        $completionConfigured = $false
        try {
            $completionConfigured = Install-RelayCompletion -RelayExe $target
        } catch {
            throw "The binary is installed, but shell completion setup failed: $($_.Exception.Message)"
        }
        if ($completionConfigured) {
            Write-Host "Relay installed at $target. New PowerShell windows load command completion. Run relay --help to get started."
        } else {
            Write-Host "Relay installed at $target. Run relay --help to get started."
        }
    } finally {
        if ($stage -and (Test-Path -LiteralPath $stage)) { Remove-Item -LiteralPath $stage -Force }
        # Recursively remove only the unique temporary directory created above.
        $resolvedWork = [IO.Path]::GetFullPath($work)
        $tempRoot = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\') + '\'
        if ($resolvedWork.StartsWith($tempRoot, [StringComparison]::OrdinalIgnoreCase) -and ([IO.Path]::GetFileName($resolvedWork) -like 'relay-install-*')) {
            Remove-Item -LiteralPath $resolvedWork -Recurse -Force
        }
    }
}
Install-Relay
if ($script:RelayCompletionScript) { Invoke-Expression $script:RelayCompletionScript | Out-Null }
