# Supports irm <url> | iex and removes only Relay-managed files.
function Read-RelayText {
    param([Parameter(Mandatory = $true)][string]$Path)
    $bytes = [IO.File]::ReadAllBytes($Path)
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
    $text = if ($bytes.Length -gt $offset) { $encoding.GetString($bytes, $offset, $bytes.Length - $offset) } else { '' }
    return @{ Text = $text; Encoding = $encoding }
}

function Remove-RelayBlock {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][string]$Start,
        [Parameter(Mandatory = $true)][string]$End
    )
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) { return }
    $item = Get-Item -LiteralPath $Path -Force
    if ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) {
        Write-Warning "Leaving symlink or reparse point unchanged: $Path"
        return
    }
    $loaded = Read-RelayText -Path $Path
    $pattern = '(?s)' + [regex]::Escape($Start) + '.*?' + [regex]::Escape($End) + '[ \t]*\r?\n?'
    $updated = [regex]::Replace($loaded.Text, $pattern, '')
    if ($updated -ne $loaded.Text) {
        [IO.File]::WriteAllText($Path, $updated, $loaded.Encoding)
    }
}

function Remove-RelaySkill {
    param([Parameter(Mandatory = $true)][string]$Path)
    if (-not (Test-Path -LiteralPath $Path -PathType Container)) { return }
    $item = Get-Item -LiteralPath $Path -Force
    if ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) {
        Write-Warning "Leaving symlink or reparse point unchanged: $Path"
        return
    }
    $skill = Join-Path $Path 'SKILL.md'
    $stamp = Join-Path $Path '.relay-sha256'
    if (-not (Test-Path -LiteralPath $skill -PathType Leaf) -or -not (Test-Path -LiteralPath $stamp -PathType Leaf)) { return }
    $actual = (Get-FileHash -LiteralPath $skill -Algorithm SHA256).Hash
    $expected = ([IO.File]::ReadAllText($stamp)).Trim()
    if ($actual -ine $expected) {
        Write-Warning "Kept modified Skill: $Path"
        return
    }
    Remove-Item -LiteralPath $skill, $stamp, (Join-Path $Path '.relay-install.lock') -Force -ErrorAction SilentlyContinue
    $remaining = @(Get-ChildItem -LiteralPath $Path -Force -ErrorAction SilentlyContinue)
    if ($remaining.Count -eq 0) {
        Remove-Item -LiteralPath $Path -Force
    } else {
        Write-Warning "Kept $Path because it contains other files"
    }
}

function Uninstall-Relay {
    $ErrorActionPreference = 'Stop'
    Set-StrictMode -Version Latest
    if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) { throw 'Use uninstall.sh on Linux or macOS' }
    $installDir = $env:RELAY_INSTALL_DIR
    if (-not $installDir) {
        if (-not $env:LOCALAPPDATA) { throw 'LOCALAPPDATA is not set; specify RELAY_INSTALL_DIR' }
        $installDir = Join-Path $env:LOCALAPPDATA 'Relay\bin'
    }
    if (-not [IO.Path]::IsPathRooted($installDir)) { throw 'RELAY_INSTALL_DIR must be an absolute path' }
    $installDir = [IO.Path]::GetFullPath($installDir)
    $target = Join-Path $installDir 'relay.exe'
    if (Test-Path -LiteralPath $target) {
        $item = Get-Item -LiteralPath $target -Force
        if ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw "Refusing to remove symlink or reparse point: $target" }
        Remove-Item -LiteralPath $target -Force
    }

    if ($env:RELAY_NO_MODIFY_PATH -ne '1') {
        $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
        if ($null -ne $userPath) {
            $entries = @($userPath -split ';' | Where-Object { $_ -and ([Environment]::ExpandEnvironmentVariables($_).TrimEnd('\') -ine $installDir.TrimEnd('\')) })
            [Environment]::SetEnvironmentVariable('Path', ($entries -join ';'), 'User')
        }
    }
    $env:PATH = (($env:PATH -split ';' | Where-Object { $_ -and $_.TrimEnd('\') -ine $installDir.TrimEnd('\') }) -join ';')

    $documents = $env:RELAY_SHELL_CONFIG_DIR
    if (-not $documents) { $documents = [Environment]::GetFolderPath('MyDocuments') }
    if ($documents) {
        $start = '# RELAY:START - Relay completion (managed block, do not edit manually)'
        $end = '# RELAY:END'
        Remove-RelayBlock (Join-Path $documents 'PowerShell\Microsoft.PowerShell_profile.ps1') $start $end
        Remove-RelayBlock (Join-Path $documents 'WindowsPowerShell\Microsoft.PowerShell_profile.ps1') $start $end
    }

    $userDir = $env:USERPROFILE
    if (-not $userDir) { $userDir = [Environment]::GetFolderPath('UserProfile') }
    $claudeHome = if ($env:CLAUDE_CONFIG_DIR) { $env:CLAUDE_CONFIG_DIR } else { Join-Path $userDir '.claude' }
    $codexHome = if ($env:CODEX_HOME) { $env:CODEX_HOME } else { Join-Path $userDir '.codex' }
    Remove-RelaySkill (Join-Path $claudeHome 'skills\relay-handoff')
    Remove-RelaySkill (Join-Path $codexHome 'skills\relay-handoff')

    Write-Host 'Relay binary, managed PowerShell completion, and managed Handoff Skill were removed.'
    Write-Host 'Relay data and encrypted credentials were kept. Remove RELAY_HOME manually only if you intend to delete them.'
}

Uninstall-Relay
