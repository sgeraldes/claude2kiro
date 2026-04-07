# claude2kiro installer for Windows
# Usage:
#   irm https://raw.githubusercontent.com/sgeraldes/claude2kiro/main/install.ps1 | iex
#
# Installs:
#   $HOME\.local\bin\claude2kiro.exe              <- Lightweight launcher (added to user PATH if needed)
#   $HOME\.claude2kiro\bin\claude2kiro-X.Y.Z.exe  <- Versioned app binary (auto-managed)

$ErrorActionPreference = 'Stop'

$Repo = 'sgeraldes/claude2kiro'
$LauncherDir = Join-Path $HOME '.local\bin'
$AppDir = Join-Path $HOME '.claude2kiro\bin'

function Get-Arch {
    switch ($env:PROCESSOR_ARCHITECTURE) {
        'AMD64' { return 'amd64' }
        'ARM64' { return 'arm64' }
        default {
            if ([Environment]::Is64BitOperatingSystem) { return 'amd64' }
            throw "Unsupported architecture: $env:PROCESSOR_ARCHITECTURE"
        }
    }
}

function Add-ToUserPath {
    param([Parameter(Mandatory = $true)][string]$Dir)

    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    if ([string]::IsNullOrWhiteSpace($userPath)) {
        $userPath = ''
    }

    $parts = $userPath -split ';' | Where-Object { -not [string]::IsNullOrWhiteSpace($_) }
    $exists = $false
    foreach ($part in $parts) {
        if ($part.Trim().ToLowerInvariant() -eq $Dir.Trim().ToLowerInvariant()) {
            $exists = $true
            break
        }
    }

    if (-not $exists) {
        $newPath = if ([string]::IsNullOrWhiteSpace($userPath)) { $Dir } else { "$userPath;$Dir" }
        [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
        if (-not (($env:Path -split ';') | Where-Object { $_.Trim().ToLowerInvariant() -eq $Dir.Trim().ToLowerInvariant() })) {
            $env:Path = "$env:Path;$Dir"
        }
        return $true
    }

    if (-not (($env:Path -split ';') | Where-Object { $_.Trim().ToLowerInvariant() -eq $Dir.Trim().ToLowerInvariant() })) {
        $env:Path = "$env:Path;$Dir"
    }
    return $false
}

$Arch = Get-Arch
$LauncherAsset = "claude2kiro-launcher-windows-$Arch.exe"
$AppAsset = "claude2kiro-windows-$Arch.exe"

Write-Host 'claude2kiro installer'
Write-Host '====================='
Write-Host "Platform: windows/$Arch"
Write-Host ''
Write-Host 'Fetching latest release...'

$Release = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases/latest"
$Version = $Release.tag_name.TrimStart('v')

$LauncherUrl = ($Release.assets | Where-Object { $_.name -eq $LauncherAsset } | Select-Object -First 1).browser_download_url
$AppUrl = ($Release.assets | Where-Object { $_.name -eq $AppAsset } | Select-Object -First 1).browser_download_url

if (-not $LauncherUrl -or -not $AppUrl) {
    throw "Release assets not found for windows/$Arch. Check https://github.com/$Repo/releases"
}

Write-Host "Version: v$Version"

New-Item -ItemType Directory -Force -Path $LauncherDir, $AppDir | Out-Null

$LauncherPath = Join-Path $LauncherDir 'claude2kiro.exe'
$AppPath = Join-Path $AppDir "claude2kiro-$Version.exe"
$CurrentPath = Join-Path $AppDir 'current.txt'

Write-Host 'Downloading launcher...'
Invoke-WebRequest -Uri $LauncherUrl -OutFile $LauncherPath

Write-Host "Downloading claude2kiro v$Version..."
Invoke-WebRequest -Uri $AppUrl -OutFile $AppPath

Set-Content -Path $CurrentPath -Value $Version -NoNewline

$pathAdded = Add-ToUserPath -Dir $LauncherDir

Write-Host ''
Write-Host 'Installed:'
Write-Host "  Launcher: $LauncherPath"
Write-Host "  Binary:   $AppPath"
if ($pathAdded) {
    Write-Host "  PATH:     added $LauncherDir to your user PATH"
} else {
    Write-Host "  PATH:     already contains $LauncherDir"
}

Write-Host ''
Write-Host 'Open a new terminal if needed, then get started:'
Write-Host '  claude2kiro login    # Authenticate with Kiro'
Write-Host '  claude2kiro run      # Start Claude Code via Kiro'
Write-Host '  claude2kiro update   # Auto-update without replacing the launcher'
