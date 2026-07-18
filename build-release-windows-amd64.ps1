[CmdletBinding()]
param(
    [string]$RepositoryRoot = $PSScriptRoot,
    [string]$Remote = "origin",
    [string]$Branch = "",
    [string]$Version = "",
    [string]$OutputRoot = "dist",
    [string]$GoCommand = "go",
    [string]$GoProxy = "https://goproxy.cn,direct",
    [string]$GoSumDB = "sum.golang.google.cn",
    [switch]$SkipPull,
    [switch]$AllowDirty,
    [switch]$SkipDependencyDownload,
    [switch]$RefreshModelsCatalog
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function Write-Step {
    param([string]$Message)

    Write-Host ""
    Write-Host "==> $Message" -ForegroundColor Cyan
}

function Invoke-ExternalCommand {
    param(
        [Parameter(Mandatory = $true)][string]$FilePath,
        [Parameter(Mandatory = $true)][string[]]$Arguments,
        [string]$WorkingDirectory = $script:RepoRoot
    )

    Write-Host "> $FilePath $($Arguments -join ' ')"
    Push-Location -LiteralPath $WorkingDirectory
    try {
        & $FilePath @Arguments
        if ($LASTEXITCODE -ne 0) {
            throw "$FilePath failed with exit code $LASTEXITCODE"
        }
    } finally {
        Pop-Location
    }
}

function Get-ExternalCommandOutput {
    param(
        [Parameter(Mandatory = $true)][string]$FilePath,
        [Parameter(Mandatory = $true)][string[]]$Arguments,
        [string]$WorkingDirectory = $script:RepoRoot
    )

    Push-Location -LiteralPath $WorkingDirectory
    try {
        $output = & $FilePath @Arguments 2>&1
        if ($LASTEXITCODE -ne 0) {
            $message = ($output | Out-String).Trim()
            if ([string]::IsNullOrWhiteSpace($message)) {
                $message = "$FilePath failed with exit code $LASTEXITCODE"
            }
            throw $message
        }
        return ($output | Out-String).Trim()
    } finally {
        Pop-Location
    }
}

function Assert-PathInside {
    param(
        [Parameter(Mandatory = $true)][string]$ChildPath,
        [Parameter(Mandatory = $true)][string]$ParentPath
    )

    $childFull = [System.IO.Path]::GetFullPath($ChildPath).TrimEnd('\', '/')
    $parentFull = [System.IO.Path]::GetFullPath($ParentPath).TrimEnd('\', '/')
    $comparison = [System.StringComparison]::OrdinalIgnoreCase

    if ($childFull.Equals($parentFull, $comparison)) {
        return
    }

    $parentWithSeparator = $parentFull + [System.IO.Path]::DirectorySeparatorChar
    if (-not $childFull.StartsWith($parentWithSeparator, $comparison)) {
        throw "Refusing to operate outside repository: $childFull"
    }
}

function Invoke-WithRetry {
    param(
        [Parameter(Mandatory = $true)][scriptblock]$Operation,
        [string]$Description = "operation",
        [int]$Attempts = 12,
        [int]$DelayMilliseconds = 1000
    )

    $lastError = $null
    for ($attempt = 1; $attempt -le $Attempts; $attempt++) {
        try {
            & $Operation
            return
        } catch {
            $lastError = $_
            if ($attempt -ge $Attempts) {
                break
            }
            Start-Sleep -Milliseconds $DelayMilliseconds
        }
    }

    throw "$Description failed after $Attempts attempts: $($lastError.Exception.Message)"
}

function Remove-PathWithRetry {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [switch]$Recurse
    )

    if (-not (Test-Path -LiteralPath $Path)) {
        return
    }

    Invoke-WithRetry -Description "Remove '$Path'" -Operation {
        if ($Recurse) {
            Remove-Item -LiteralPath $Path -Recurse -Force
        } else {
            Remove-Item -LiteralPath $Path -Force
        }
    }
}

function Copy-FileWithRetry {
    param(
        [Parameter(Mandatory = $true)][string]$Source,
        [Parameter(Mandatory = $true)][string]$Destination
    )

    Invoke-WithRetry -Description "Copy '$Source' to '$Destination'" -Operation {
        Copy-Item -LiteralPath $Source -Destination $Destination -Force
    }
}

function Copy-DirectoryContentsWithRetry {
    param(
        [Parameter(Mandatory = $true)][string]$SourceDirectory,
        [Parameter(Mandatory = $true)][string]$DestinationDirectory
    )

    Invoke-WithRetry -Description "Copy contents from '$SourceDirectory' to '$DestinationDirectory'" -Operation {
        Copy-Item -Path (Join-Path $SourceDirectory "*") -Destination $DestinationDirectory -Recurse -Force
    }
}

function Get-RelativePathInRepo {
    param(
        [Parameter(Mandatory = $true)][string]$FullPath,
        [Parameter(Mandatory = $true)][string]$RootPath
    )

    $full = [System.IO.Path]::GetFullPath($FullPath)
    $root = [System.IO.Path]::GetFullPath($RootPath).TrimEnd('\', '/') + [System.IO.Path]::DirectorySeparatorChar
    if (-not $full.StartsWith($root, [System.StringComparison]::OrdinalIgnoreCase)) {
        return ""
    }
    return $full.Substring($root.Length).Replace('\', '/')
}

function Test-IgnoredDirtyLine {
    param(
        [Parameter(Mandatory = $true)][string]$Line,
        [string]$ScriptRelativePath,
        [string]$OutputRootRelativePath
    )

    if (-not $Line.StartsWith("?? ")) {
        return $false
    }

    $path = $Line.Substring(3).Trim().Replace('\', '/')
    if ($path -eq $ScriptRelativePath) {
        return $true
    }

    if (-not [string]::IsNullOrWhiteSpace($OutputRootRelativePath)) {
        $prefix = $OutputRootRelativePath.TrimEnd('/') + '/'
        if ($path -eq $OutputRootRelativePath -or $path.StartsWith($prefix, [System.StringComparison]::OrdinalIgnoreCase)) {
            return $true
        }
    }

    return $false
}

function Get-RemoteDefaultBranch {
    param([string]$RemoteName)

    try {
        $symbolic = Get-ExternalCommandOutput -FilePath "git" -Arguments @("symbolic-ref", "--short", "refs/remotes/$RemoteName/HEAD")
        if (-not [string]::IsNullOrWhiteSpace($symbolic)) {
            return ($symbolic -replace "^$([regex]::Escape($RemoteName))/", "")
        }
    } catch {
        # Fall through to common branch names when origin/HEAD is unavailable.
    }

    $mainBranch = Get-ExternalCommandOutput -FilePath "git" -Arguments @("branch", "-r", "--list", "$RemoteName/main")
    if (-not [string]::IsNullOrWhiteSpace($mainBranch)) {
        return "main"
    }

    $masterBranch = Get-ExternalCommandOutput -FilePath "git" -Arguments @("branch", "-r", "--list", "$RemoteName/master")
    if (-not [string]::IsNullOrWhiteSpace($masterBranch)) {
        return "master"
    }

    throw "Cannot determine default branch for remote '$RemoteName'. Pass -Branch explicitly."
}

function Resolve-GoCommand {
    param([string]$CommandName)

    try {
        $command = Get-Command $CommandName -ErrorAction Stop
        if (-not [string]::IsNullOrWhiteSpace($command.Source)) {
            return $command.Source
        }
    } catch {
        # Fall through to common Windows installation paths.
    }

    if ([System.IO.Path]::IsPathRooted($CommandName) -and (Test-Path -LiteralPath $CommandName)) {
        return [System.IO.Path]::GetFullPath($CommandName)
    }

    $candidatePaths = New-Object System.Collections.Generic.List[string]
    if (-not [string]::IsNullOrWhiteSpace($env:ProgramFiles)) {
        $candidatePaths.Add((Join-Path $env:ProgramFiles "Go\bin\go.exe"))
    }
    $programFilesX86 = [Environment]::GetEnvironmentVariable("ProgramFiles(x86)")
    if (-not [string]::IsNullOrWhiteSpace($programFilesX86)) {
        $candidatePaths.Add((Join-Path $programFilesX86 "Go\bin\go.exe"))
    }
    if (-not [string]::IsNullOrWhiteSpace($env:LOCALAPPDATA)) {
        $candidatePaths.Add((Join-Path $env:LOCALAPPDATA "Programs\Go\bin\go.exe"))
    }

    foreach ($candidatePath in $candidatePaths) {
        if (Test-Path -LiteralPath $candidatePath) {
            return $candidatePath
        }
    }

    throw "Cannot find Go command '$CommandName'. Install Go, add it to PATH, or pass -GoCommand with the full go.exe path."
}

function Switch-ToBranch {
    param(
        [Parameter(Mandatory = $true)][string]$RemoteName,
        [Parameter(Mandatory = $true)][string]$BranchName
    )

    $localBranch = Get-ExternalCommandOutput -FilePath "git" -Arguments @("branch", "--list", $BranchName)
    if (-not [string]::IsNullOrWhiteSpace($localBranch)) {
        Invoke-ExternalCommand -FilePath "git" -Arguments @("switch", $BranchName)
        return
    }

    $remoteBranch = Get-ExternalCommandOutput -FilePath "git" -Arguments @("branch", "-r", "--list", "$RemoteName/$BranchName")
    if ([string]::IsNullOrWhiteSpace($remoteBranch)) {
        throw "Remote branch '$RemoteName/$BranchName' does not exist."
    }

    Invoke-ExternalCommand -FilePath "git" -Arguments @("switch", "--track", "$RemoteName/$BranchName")
}

function Update-Repository {
    param(
        [string]$RemoteName,
        [string]$RequestedBranch
    )

    Write-Step "Fetching latest code and tags from $RemoteName"
    Invoke-ExternalCommand -FilePath "git" -Arguments @("fetch", "--force", "--tags", "--prune", $RemoteName)

    $currentBranch = Get-ExternalCommandOutput -FilePath "git" -Arguments @("rev-parse", "--abbrev-ref", "HEAD")
    $targetBranch = $RequestedBranch.Trim()

    if ([string]::IsNullOrWhiteSpace($targetBranch)) {
        if ($currentBranch -eq "HEAD") {
            $targetBranch = Get-RemoteDefaultBranch -RemoteName $RemoteName
            Write-Host "Detached HEAD detected; switching to $targetBranch."
            Switch-ToBranch -RemoteName $RemoteName -BranchName $targetBranch
        } else {
            $targetBranch = $currentBranch
        }
    } elseif ($currentBranch -ne $targetBranch) {
        Switch-ToBranch -RemoteName $RemoteName -BranchName $targetBranch
    }

    Write-Step "Fast-forwarding $targetBranch"
    Invoke-ExternalCommand -FilePath "git" -Arguments @("pull", "--ff-only", $RemoteName, $targetBranch)
}

function Resolve-BuildVersion {
    param([string]$RequestedVersion)

    if (-not [string]::IsNullOrWhiteSpace($RequestedVersion)) {
        return ($RequestedVersion.Trim() -replace '^v', '')
    }

    try {
        $exactTag = Get-ExternalCommandOutput -FilePath "git" -Arguments @("describe", "--tags", "--exact-match", "HEAD")
        if (-not [string]::IsNullOrWhiteSpace($exactTag)) {
            return ($exactTag -replace '^v', '')
        }
    } catch {
        # HEAD is not exactly on a tag; use git describe for traceability.
    }

    try {
        $described = Get-ExternalCommandOutput -FilePath "git" -Arguments @("describe", "--tags", "--always", "--dirty")
        if (-not [string]::IsNullOrWhiteSpace($described)) {
            return ($described -replace '^v', '')
        }
    } catch {
        # Fall through to a deterministic development version.
    }

    return "dev"
}

function Write-Utf8NoBomFile {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][string]$Content
    )

    $encoding = New-Object System.Text.UTF8Encoding($false)
    [System.IO.File]::WriteAllText($Path, $Content, $encoding)
}

$script:RepoRoot = [System.IO.Path]::GetFullPath((Resolve-Path -LiteralPath $RepositoryRoot).Path)
Assert-PathInside -ChildPath $script:RepoRoot -ParentPath $script:RepoRoot
Set-Location -LiteralPath $script:RepoRoot

$outputRootPath = if ([System.IO.Path]::IsPathRooted($OutputRoot)) {
    [System.IO.Path]::GetFullPath($OutputRoot)
} else {
    [System.IO.Path]::GetFullPath((Join-Path $script:RepoRoot $OutputRoot))
}
Assert-PathInside -ChildPath $outputRootPath -ParentPath $script:RepoRoot
$outputRootRelativePath = Get-RelativePathInRepo -FullPath $outputRootPath -RootPath $script:RepoRoot

Write-Step "Checking tools"
Get-Command git -ErrorAction Stop | Out-Null
$script:GoExe = Resolve-GoCommand -CommandName $GoCommand
Write-Host "Go command: $script:GoExe"
Invoke-ExternalCommand -FilePath "git" -Arguments @("rev-parse", "--is-inside-work-tree")
Invoke-ExternalCommand -FilePath $script:GoExe -Arguments @("version")

$scriptRelativePath = Get-RelativePathInRepo -FullPath $MyInvocation.MyCommand.Path -RootPath $script:RepoRoot
$statusText = Get-ExternalCommandOutput -FilePath "git" -Arguments @("status", "--porcelain") -ErrorAction Stop -WarningAction SilentlyContinue
$statusLines = @()
if (-not [string]::IsNullOrWhiteSpace($statusText)) {
    $statusLines = @($statusText -split "`r?`n")
}
if ($statusLines.Count -eq 1 -and [string]::IsNullOrWhiteSpace($statusLines[0])) {
    $statusLines = @()
}

$blockingStatus = @($statusLines | Where-Object {
        -not (Test-IgnoredDirtyLine -Line $_ -ScriptRelativePath $scriptRelativePath -OutputRootRelativePath $outputRootRelativePath)
    })
if (-not $AllowDirty -and $blockingStatus.Count -gt 0) {
    Write-Host "Working tree has local changes:" -ForegroundColor Yellow
    $blockingStatus | ForEach-Object { Write-Host "  $_" -ForegroundColor Yellow }
    throw "Commit/stash local changes first, or rerun with -AllowDirty."
}

if (-not $SkipPull) {
    Update-Repository -RemoteName $Remote -RequestedBranch $Branch
} else {
    Write-Step "Skipping git pull because -SkipPull was specified"
}

if (-not [string]::IsNullOrWhiteSpace($GoProxy)) {
    $env:GOPROXY = $GoProxy
}
if (-not [string]::IsNullOrWhiteSpace($GoSumDB)) {
    $env:GOSUMDB = $GoSumDB
}

Write-Step "Resolving build metadata"
$buildVersion = Resolve-BuildVersion -RequestedVersion $Version
$safeVersion = $buildVersion -replace '[^0-9A-Za-z._+-]', '-'
$commit = Get-ExternalCommandOutput -FilePath "git" -Arguments @("rev-parse", "--short", "HEAD")
$buildDate = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")
$outputStamp = (Get-Date).ToString("yyyyMMddHHmmss")
Write-Host "Version   : $buildVersion"
Write-Host "Commit    : $commit"
Write-Host "BuildDate : $buildDate"
Write-Host "GOPROXY   : $env:GOPROXY"
Write-Host "GOSUMDB   : $env:GOSUMDB"

$target = "windows-amd64"
$targetRoot = Join-Path $outputRootPath $target
$archiveDir = Join-Path $targetRoot "archive"
$stagingRoot = Join-Path $targetRoot ("staging-" + [System.Guid]::NewGuid().ToString("N"))
$stagingArchiveDir = Join-Path $stagingRoot "archive"
$exePath = Join-Path $archiveDir "cli-proxy-api.exe"
$stagingExePath = Join-Path $stagingArchiveDir "cli-proxy-api.exe"
$standaloneExePath = Join-Path $outputRootPath "cli-proxy-api_${safeVersion}_${commit}_${outputStamp}_windows_amd64.exe"
$zipPath = Join-Path $outputRootPath "CLIProxyAPI_${safeVersion}_windows_amd64.zip"
$stagingZipPath = Join-Path $stagingRoot "CLIProxyAPI_${safeVersion}_windows_amd64.zip"

Write-Step "Preparing output directory"
$finalZipPath = $zipPath
if (Test-Path -LiteralPath $zipPath) {
    Assert-PathInside -ChildPath $zipPath -ParentPath $outputRootPath
    try {
        Remove-PathWithRetry -Path $zipPath
    } catch {
        $fallbackZipPath = Join-Path $outputRootPath "CLIProxyAPI_${safeVersion}_${commit}_${outputStamp}_windows_amd64.zip"
        Write-Warning "Existing ZIP is locked; writing fallback ZIP instead: $fallbackZipPath"
        $finalZipPath = $fallbackZipPath
        if (Test-Path -LiteralPath $finalZipPath) {
            Remove-PathWithRetry -Path $finalZipPath
        }
    }
}
New-Item -ItemType Directory -Force -Path $stagingArchiveDir | Out-Null

$modelsPath = Join-Path $script:RepoRoot "internal\registry\models\models.json"
$modelsOriginalContent = $null
$modelsShouldRestore = $false
$syncArchiveWarning = $null
$standaloneExeWarning = $null

try {
    if ($RefreshModelsCatalog) {
        Write-Step "Refreshing models catalog from router-for-me/models"
        if (Test-Path -LiteralPath $modelsPath) {
            $modelsOriginalContent = [System.IO.File]::ReadAllText($modelsPath)
            $modelsShouldRestore = $true
        }
        Invoke-ExternalCommand -FilePath "git" -Arguments @("fetch", "--depth", "1", "https://github.com/router-for-me/models.git", "main")
        $modelsJson = Get-ExternalCommandOutput -FilePath "git" -Arguments @("show", "FETCH_HEAD:models.json")
        Write-Utf8NoBomFile -Path $modelsPath -Content ($modelsJson + [Environment]::NewLine)
    }

    if (-not $SkipDependencyDownload) {
        Write-Step "Downloading Go modules"
        Invoke-ExternalCommand -FilePath $script:GoExe -Arguments @("mod", "download")
    }

    Write-Step "Building cli-proxy-api.exe"
    $env:CGO_ENABLED = "1"
    $env:GOOS = "windows"
    $env:GOARCH = "amd64"
    $ldflags = "-s -w -X main.Version=$buildVersion -X main.Commit=$commit -X main.BuildDate=$buildDate"
    Invoke-ExternalCommand -FilePath $script:GoExe -Arguments @("build", "-ldflags=$ldflags", "-o", $stagingExePath, ".\cmd\server\")

    Write-Step "Packaging release archive"
    Copy-Item -LiteralPath (Join-Path $script:RepoRoot "LICENSE") -Destination $stagingArchiveDir -Force
    Copy-Item -LiteralPath (Join-Path $script:RepoRoot "README.md") -Destination $stagingArchiveDir -Force
    Copy-Item -LiteralPath (Join-Path $script:RepoRoot "README_CN.md") -Destination $stagingArchiveDir -Force
    Copy-Item -LiteralPath (Join-Path $script:RepoRoot "config.example.yaml") -Destination $stagingArchiveDir -Force
    Compress-Archive -Path (Join-Path $stagingArchiveDir "*") -DestinationPath $stagingZipPath -Force
    try {
        Copy-FileWithRetry -Source $stagingExePath -Destination $standaloneExePath
    } catch {
        $standaloneExeWarning = $_.Exception.Message
        Write-Warning "ZIP has been created, but standalone EXE was not copied: $standaloneExeWarning"
    }
    Copy-FileWithRetry -Source $stagingZipPath -Destination $finalZipPath

    Write-Step "Syncing latest archive directory"
    try {
        if (Test-Path -LiteralPath $archiveDir) {
            Assert-PathInside -ChildPath $archiveDir -ParentPath $targetRoot
            Remove-PathWithRetry -Path $archiveDir -Recurse
        }
        New-Item -ItemType Directory -Force -Path $archiveDir | Out-Null
        Copy-DirectoryContentsWithRetry -SourceDirectory $stagingArchiveDir -DestinationDirectory $archiveDir
    } catch {
        $syncArchiveWarning = $_.Exception.Message
        Write-Warning "ZIP has been created, but latest archive directory was not updated: $syncArchiveWarning"
    }
} finally {
    if ($modelsShouldRestore) {
        Write-Utf8NoBomFile -Path $modelsPath -Content $modelsOriginalContent
    }
    if (Test-Path -LiteralPath $stagingRoot) {
        try {
            Remove-PathWithRetry -Path $stagingRoot -Recurse
        } catch {
            Write-Warning "Failed to remove temporary staging directory '$stagingRoot': $($_.Exception.Message)"
        }
    }
}

Write-Step "Build result"
$exeResultPath = if (Test-Path -LiteralPath $standaloneExePath) { $standaloneExePath } elseif (Test-Path -LiteralPath $exePath) { $exePath } else { $stagingExePath }
$exeInfo = if (Test-Path -LiteralPath $exeResultPath) { Get-Item -LiteralPath $exeResultPath } else { $null }
$zipInfo = Get-Item -LiteralPath $finalZipPath
$zipHash = Get-FileHash -LiteralPath $finalZipPath -Algorithm SHA256

if ($null -ne $exeInfo) {
    Write-Host "EXE : $($exeInfo.FullName)"
} elseif ($standaloneExeWarning) {
    Write-Host "EXE : standalone copy failed; use the ZIP output." -ForegroundColor Yellow
} elseif ($syncArchiveWarning) {
    Write-Host "EXE : archive directory not updated because an old file is locked. Use the ZIP output." -ForegroundColor Yellow
}
Write-Host "ZIP : $($zipInfo.FullName)"
Write-Host "SHA256: $($zipHash.Hash)"
