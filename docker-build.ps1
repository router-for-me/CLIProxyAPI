# build.ps1 - Windows PowerShell Build Script
#
# This script automates the process of building and running the Docker container
# with version information dynamically injected at build time.

# Stop script execution on any error
$ErrorActionPreference = "Stop"

function Resolve-RepositoryUrl {
    $remoteUrl = ""
    try {
        $remoteUrl = (git remote get-url origin).Trim()
    } catch {
        $remoteUrl = ""
    }
    if ([string]::IsNullOrWhiteSpace($remoteUrl)) {
        try {
            $remoteUrl = (git config --get remote.origin.url).Trim()
        } catch {
            $remoteUrl = ""
        }
    }
    if ([string]::IsNullOrWhiteSpace($remoteUrl)) {
        return "unknown"
    }

    if ($remoteUrl -like "git@github.com:*") {
        $remoteUrl = "https://github.com/" + $remoteUrl.Substring("git@github.com:".Length)
    } elseif ($remoteUrl -like "ssh://git@github.com/*") {
        $remoteUrl = "https://github.com/" + $remoteUrl.Substring("ssh://git@github.com/".Length)
    } elseif ($remoteUrl -like "http://github.com/*") {
        $remoteUrl = "https://" + $remoteUrl.Substring("http://".Length)
    }

    if ($remoteUrl.EndsWith(".git")) {
        $remoteUrl = $remoteUrl.Substring(0, $remoteUrl.Length - 4)
    }
    return $remoteUrl
}

$defaultRemoteImage = "ghcr.io/quqi1599/cliproxyapi:latest"
$selectedRemoteImage = if ([string]::IsNullOrWhiteSpace($env:CLI_PROXY_IMAGE)) {
    $defaultRemoteImage
} else {
    $env:CLI_PROXY_IMAGE
}

# --- Step 1: Choose Environment ---
Write-Host "Please select an option:"
Write-Host "1) Run using Pre-built Image (Recommended)"
Write-Host "2) Build from Source and Run (For Developers)"
$choice = Read-Host -Prompt "Enter choice [1-2]"

# --- Step 2: Execute based on choice ---
switch ($choice) {
    "1" {
        Write-Host "--- Running with Pre-built Image ---"
        Write-Host "Using remote image: $selectedRemoteImage"
        Write-Host "Tip: set CLI_PROXY_IMAGE=ghcr.io/quqi1599/cliproxyapi:fork-v7.10.43 to pin a release."
        docker compose pull
        docker compose up -d --remove-orphans --no-build
        Write-Host "Services are starting from remote image."
        Write-Host "Run 'docker compose logs -f' to see the logs."
    }
    "2" {
        Write-Host "--- Building from Source and Running ---"

        # Get Version Information
        $VERSION = (git describe --tags --always --dirty)
        $COMMIT  = (git rev-parse --short HEAD)
        $BUILD_DATE = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")
        $REPOSITORY_URL = Resolve-RepositoryUrl

        Write-Host "Building with the following info:"
        Write-Host "  Version: $VERSION"
        Write-Host "  Commit: $COMMIT"
        Write-Host "  Build Date: $BUILD_DATE"
        Write-Host "  Repository URL: $REPOSITORY_URL"
        Write-Host "----------------------------------------"

        # Build and start the services with a local-only image tag
        $env:CLI_PROXY_IMAGE = "cli-proxy-api:local"
        
        Write-Host "Building the Docker image..."
        docker compose build --build-arg VERSION=$VERSION --build-arg COMMIT=$COMMIT --build-arg BUILD_DATE=$BUILD_DATE --build-arg REPOSITORY_URL=$REPOSITORY_URL

        Write-Host "Starting the services..."
        docker compose up -d --remove-orphans --pull never

        Write-Host "Build complete. Services are starting."
        Write-Host "Run 'docker compose logs -f' to see the logs."
    }
    default {
        Write-Host "Invalid choice. Please enter 1 or 2."
        exit 1
    }
}
