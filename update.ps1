# CLIProxyAPI - Windows Auto Updater
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $MyInvocation.MyCommand.Path
Set-Location $root

# Enable TLS 1.2 and TLS 1.3 security protocols
[System.Net.ServicePointManager]::SecurityProtocol = [System.Net.SecurityProtocolType]::Tls12 -bor [System.Net.SecurityProtocolType]::Tls13

Write-Host "==================================================" -ForegroundColor Cyan
Write-Host "       CLIProxyAPI Windows Auto Updater           " -ForegroundColor Cyan
Write-Host "==================================================" -ForegroundColor Cyan

# ── 1. Discover user download folders accurately ────────────
function Get-SmartDownloadDirectories {
    $dirs = [System.Collections.Generic.List[string]]::new()

    # Detect system download folder from Windows Registry (handles folder redirection)
    try {
        $regKey = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Explorer\User Shell Folders'
        $regVal = (Get-ItemProperty -Path $regKey -ErrorAction SilentlyContinue).'{374DE290-123F-4565-9164-39C4925E467B}'
        if ($regVal) {
            $realDownloads = [System.Environment]::ExpandEnvironmentVariables($regVal)
            if (Test-Path $realDownloads) {
                if (-not $dirs.Contains($realDownloads)) { $dirs.Add($realDownloads) }
                $compSub = Join-Path $realDownloads 'Compressed'
                if ((Test-Path $compSub) -and (-not $dirs.Contains($compSub))) { $dirs.Add($compSub) }
            }
        }
    } catch {}

    # Detect Internet Download Manager (IDM) download folder if present
    try {
        $idmKey = 'HKCU:\Software\DownloadManager'
        $idmProps = Get-ItemProperty -Path $idmKey -ErrorAction SilentlyContinue
        if ($idmProps) {
            if ($idmProps.SavePathCompressed -and (Test-Path $idmProps.SavePathCompressed) -and (-not $dirs.Contains($idmProps.SavePathCompressed))) {
                $dirs.Add($idmProps.SavePathCompressed)
            }
            if ($idmProps.SavePath -and (Test-Path $idmProps.SavePath) -and (-not $dirs.Contains($idmProps.SavePath))) {
                $dirs.Add($idmProps.SavePath)
            }
        }
    } catch {}

    # Default UserProfile Downloads
    $defaultDownloads = Join-Path ([Environment]::GetFolderPath('UserProfile')) 'Downloads'
    if (Test-Path $defaultDownloads) {
        if (-not $dirs.Contains($defaultDownloads)) { $dirs.Add($defaultDownloads) }
        $compDef = Join-Path $defaultDownloads 'Compressed'
        if ((Test-Path $compDef) -and (-not $dirs.Contains($compDef))) { $dirs.Add($compDef) }
    }

    # Current working directory
    if (-not $dirs.Contains($root)) { $dirs.Add($root) }

    return $dirs
}

function Find-LocalPackage {
    param(
        [string[]]$searchDirectories,
        [string]$targetVersion
    )
    foreach ($d in $searchDirectories) {
        if (-not (Test-Path $d)) { continue }
        $candidates = Get-ChildItem -Path $d -Filter "CLIProxyAPI*.zip" -Recurse -Depth 2 -ErrorAction SilentlyContinue | `
            Where-Object {
                $_.Length -gt 5000000 -and `
                -not (Test-Path "$($_.FullName).crdownload") -and `
                -not (Test-Path "$($_.FullName).tmp")
            } | Sort-Object LastWriteTime -Descending
        
        if ($candidates) {
            if ($targetVersion) {
                $cleanVer = $targetVersion -replace '^v',''
                $matched = $candidates | Where-Object { $_.Name -match [regex]::Escape($cleanVer) -or $_.Name -match [regex]::Escape($targetVersion) } | Select-Object -First 1
                if ($matched) { return $matched }
            } else {
                return ($candidates | Select-Object -First 1)
            }
        }
    }
    return $null
}

# ── 2. Check latest GitHub release without API rate limits ─
function Get-LatestGitHubTag {
    $proxyList = [System.Collections.Generic.List[string]]::new()
    if ($env:HTTPS_PROXY) { $proxyList.Add($env:HTTPS_PROXY) }
    if ($env:ALL_PROXY -and -not $proxyList.Contains($env:ALL_PROXY)) { $proxyList.Add($env:ALL_PROXY) }
    foreach ($dp in @('http://127.0.0.1:7897', 'http://127.0.0.1:7890', 'http://127.0.0.1:10809')) {
        if (-not $proxyList.Contains($dp)) { $proxyList.Add($dp) }
    }
    $proxyList.Add('')

    $curlExe = (Get-Command curl.exe -ErrorAction SilentlyContinue).Source

    # Method 1: HEAD request to /releases/latest redirect location (avoids 60 req/hr rate limit)
    if ($curlExe) {
        foreach ($p in $proxyList) {
            try {
                $cArgs = @('-s', '-I', '--connect-timeout', '4')
                if ($p) { $cArgs += @('-x', $p) }
                $cArgs += 'https://github.com/router-for-me/CLIProxyAPI/releases/latest'
                $lines = & $curlExe $cArgs
                foreach ($line in $lines) {
                    if ($line -match 'location:\s*.*?/releases/tag/([^\r\n/?#]+)') {
                        $foundTag = $Matches[1].Trim()
                        if ($foundTag) {
                            return @{ Tag = $foundTag; Proxy = $p }
                        }
                    }
                }
            } catch {}
        }
    }

    # Method 2: Git ls-remote tags query sorted by semver
    try {
        $tags = git ls-remote --tags origin
        if ($tags) {
            $parsedList = @()
            foreach ($line in $tags) {
                if ($line -match 'refs/tags/(v?(\d+\.\d+\.\d+[\w\.\-]*))$') {
                    $rawTag = $Matches[1]
                    $coreVer = $Matches[2].Split('-')[0]
                    try {
                        $parsedList += [PSCustomObject]@{
                            Tag = $rawTag
                            SemVer = [version]$coreVer
                        }
                    } catch {}
                }
            }
            if ($parsedList.Count -gt 0) {
                $best = $parsedList | Sort-Object SemVer -Descending | Select-Object -First 1
                return @{ Tag = $best.Tag; Proxy = '' }
            }
        }
    } catch {}

    # Method 3: GitHub REST API fallback
    foreach ($p in $proxyList) {
        try {
            $apiParams = @{
                Uri = "https://api.github.com/repos/router-for-me/CLIProxyAPI/releases/latest"
                Headers = @{"User-Agent"="PowerShell"}
                TimeoutSec = 4
            }
            if ($p) { $apiParams['Proxy'] = $p }
            $res = Invoke-RestMethod @apiParams
            if ($res.tag_name) { return @{ Tag = $res.tag_name; Proxy = $p } }
        } catch {}
    }

    return $null
}

# ── 3. Compare local and remote versions ────────────────────
$currentVer = "unknown"
$verFile = Join-Path $root '.version'
if (Test-Path $verFile) {
    $currentVer = (Get-Content $verFile -Raw).Trim()
} elseif (Test-Path (Join-Path $root 'cli-proxy-api.exe')) {
    try {
        $out = & (Join-Path $root 'cli-proxy-api.exe') --help 2>&1
        if ($out -match 'CLIProxyAPI Version:\s*([^\s,]+)') {
            $currentVer = "v" + $Matches[1]
        }
    } catch {}
}

Write-Host "[INFO] Current installed version: $currentVer" -ForegroundColor White
Write-Host "[INFO] Checking latest version from GitHub..." -ForegroundColor Cyan

$tagInfo = Get-LatestGitHubTag
$latestVer = if ($tagInfo) { $tagInfo.Tag } else { $null }
$detectedProxy = if ($tagInfo) { $tagInfo.Proxy } else { if ($env:HTTPS_PROXY) { $env:HTTPS_PROXY } else { 'http://127.0.0.1:7897' } }

$searchDirs = Get-SmartDownloadDirectories
$existingZip = Find-LocalPackage -searchDirectories $searchDirs -targetVersion $null

# If offline but local package found, infer version from file name
if (-not $latestVer -and $existingZip) {
    if ($existingZip.Name -match 'CLIProxyAPI[_-](v?\d+\.\d+\.\d+)') {
        $latestVer = $Matches[1]
        if (-not $latestVer.StartsWith('v')) { $latestVer = "v" + $latestVer }
        Write-Host "[INFO] Identified version from local package: $latestVer" -ForegroundColor Yellow
    } else {
        $latestVer = "local package"
    }
}

if (-not $latestVer -and -not $existingZip) {
    Write-Host "[WARN] Unable to reach GitHub and no offline package found." -ForegroundColor Yellow
    Write-Host "[INFO] Searched directories: $($searchDirs -join ' | ')" -ForegroundColor DarkGray
    $openBrowser = Read-Host "Open GitHub Releases page in browser? (Y/n)"
    if ($openBrowser -ne 'n' -and $openBrowser -ne 'N') {
        Start-Process "https://github.com/router-for-me/CLIProxyAPI/releases"
        Write-Host "[WAIT] Monitoring download folder for new packages..." -ForegroundColor Cyan
        
        $startTime = [DateTime]::Now
        while ($true) {
            Start-Sleep -Seconds 2
            $detected = Find-LocalPackage -searchDirectories $searchDirs -targetVersion $null
            if ($detected -and ($detected.LastWriteTime -gt $startTime.AddMinutes(-5))) {
                $existingZip = $detected
                $latestVer = "offline package"
                break
            }
            if (([DateTime]::Now - $startTime).TotalSeconds -gt 300) {
                Write-Host "[ERROR] Timed out waiting for package download." -ForegroundColor Red
                exit 1
            }
        }
    } else {
        exit 1
    }
}

Write-Host "[INFO] Latest release version: $latestVer" -ForegroundColor Green

if ($currentVer -eq $latestVer) {
    Write-Host "[INFO] You already have the latest version ($currentVer)." -ForegroundColor Green
    $reinstall = Read-Host "Force reinstall/update? (y/N)"
    if ($reinstall -ne 'y' -and $reinstall -ne 'Y') {
        Write-Host "[INFO] Exiting safely."
        exit 0
    }
}

# ── 4. Retrieve package (local or remote) ───────────────────
$tempZip = Join-Path $root "update_temp.zip"
if (Test-Path $tempZip) { Remove-Item $tempZip -Force }

$targetZipFile = Find-LocalPackage -searchDirectories $searchDirs -targetVersion $latestVer

if ($targetZipFile) {
    Write-Host "[INFO] Found matching local package: $($targetZipFile.FullName)" -ForegroundColor Green
    Write-Host "[INFO] Copying package to workspace..." -ForegroundColor Cyan
    Copy-Item -Path $targetZipFile.FullName -Destination $tempZip -Force
} else {
    $cleanVer = $latestVer -replace '^v',''
    $downloadUrl = "https://github.com/router-for-me/CLIProxyAPI/releases/download/$latestVer/CLIProxyAPI_${cleanVer}_windows_amd64.zip"
    $curlExe = (Get-Command curl.exe -ErrorAction SilentlyContinue).Source
    $downloadSuccess = $false

    # Try downloading with curl.exe
    if ($curlExe) {
        Write-Host "[INFO] Downloading release package: $downloadUrl" -ForegroundColor Cyan
        $curlArgs = @('-L', '--fail', '--connect-timeout', '10', '-o', $tempZip)
        if ($detectedProxy) { $curlArgs += @('-x', $detectedProxy) }
        $curlArgs += $downloadUrl

        & $curlExe $curlArgs
        if ($LASTEXITCODE -eq 0 -and (Test-Path $tempZip) -and ((Get-Item $tempZip).Length -gt 5000000)) {
            $downloadSuccess = $true
            Write-Host "[SUCCESS] Package downloaded successfully." -ForegroundColor Green
        }
    }

    # Fallback: open browser and monitor download folder
    if (-not $downloadSuccess) {
        Write-Host "[WARN] Command-line download failed; opening browser to download link..." -ForegroundColor Yellow
        Write-Host "[INFO] Download URL: $downloadUrl" -ForegroundColor Cyan
        Start-Process $downloadUrl

        Write-Host "[WAIT] Monitoring download folder for completed package..." -ForegroundColor Cyan
        $watchTimeoutSeconds = 300
        $startTime = [DateTime]::Now

        while (-not (Test-Path $tempZip)) {
            Start-Sleep -Seconds 2
            if (([DateTime]::Now - $startTime).TotalSeconds -gt $watchTimeoutSeconds) {
                Write-Host "[ERROR] Download timeout. Update cancelled." -ForegroundColor Red
                exit 1
            }

            $detected = Find-LocalPackage -searchDirectories $searchDirs -targetVersion $latestVer
            if ($detected -and ($detected.LastWriteTime -gt $startTime.AddMinutes(-5))) {
                try {
                    $stream = [System.IO.File]::Open($detected.FullName, [System.IO.FileMode]::Open, [System.IO.FileAccess]::Read, [System.IO.FileShare]::None)
                    $stream.Dispose()
                    Write-Host "[SUCCESS] Detected downloaded package: $($detected.FullName)" -ForegroundColor Green
                    Copy-Item -Path $detected.FullName -Destination $tempZip -Force
                    break
                } catch {}
            }
        }
    }
}

# ── 5. Gracefully stop running proxy instances ──────────────
Write-Host "[INFO] Stopping running CLIProxyAPI instances..." -ForegroundColor Yellow
$procs = Get-Process -Name "cli-proxy-api" -ErrorAction SilentlyContinue
if ($procs) {
    $procs | Stop-Process -Force
    Start-Sleep -Seconds 1
}

# Check if port 8317 is occupied and stop owning process
try {
    $conns = Get-NetTCPConnection -LocalPort 8317 -ErrorAction SilentlyContinue
    if ($conns) {
        foreach ($c in $conns) {
            if ($c.OwningProcess -gt 0) {
                Stop-Process -Id $c.OwningProcess -Force -ErrorAction SilentlyContinue
            }
        }
    }
} catch {}

# ── 6. Extract and update while protecting user config ──────
Write-Host "[INFO] Extracting and updating components..." -ForegroundColor Cyan

Add-Type -AssemblyName System.IO.Compression.FileSystem
$zip = [System.IO.Compression.ZipFile]::OpenRead($tempZip)

foreach ($entry in $zip.Entries) {
    $name = $entry.Name
    if (-not $name) { continue }

    # Protected user files: never overwrite config, auths, or user scripts
    if ($name -eq 'config.yaml' -or `
        $name -eq '.env' -or `
        $name -eq 'start.bat' -or `
        $name -eq 'update.cmd' -or `
        $name -eq 'update.ps1') {
        continue
    }

    $destPath = Join-Path $root $name
    if ($name -eq 'cli-proxy-api.exe' -or $name -eq 'config.example.yaml') {
        [System.IO.Compression.ZipFileExtensions]::ExtractToFile($entry, $destPath, $true)
        Write-Host "  -> Updated: $name" -ForegroundColor DarkCyan
    }
}
$zip.Dispose()

# Record updated version
Set-Content -Path (Join-Path $root '.version') -Value $latestVer -Encoding UTF8

# Clean up temp file
Remove-Item $tempZip -Force -ErrorAction SilentlyContinue
Write-Host "[INFO] Temporary package cleaned up." -ForegroundColor DarkGray

Write-Host "==================================================" -ForegroundColor Green
Write-Host "  [SUCCESS] CLIProxyAPI updated to $latestVer     " -ForegroundColor Green
Write-Host "==================================================" -ForegroundColor Green
Write-Host "[INFO] User configurations (config.yaml, auths) preserved." -ForegroundColor White

# ── 7. Prompt to restart service ────────────────────────────
$startNow = Read-Host "Start service now? (Y/n)"
if ($startNow -ne 'n' -and $startNow -ne 'N') {
    Write-Host "[INFO] Launching service..." -ForegroundColor Cyan
    $startCmd = Join-Path $root 'start.bat'
    Start-Process -FilePath "cmd.exe" -ArgumentList "/c", "`"$startCmd`""
}