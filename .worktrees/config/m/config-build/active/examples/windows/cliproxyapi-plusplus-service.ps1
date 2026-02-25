param(
    [Parameter(Mandatory = $true)]
    [ValidateSet("install","uninstall","start","stop","status")]
    [string]$Action,

    [string]$BinaryPath = "C:\Program Files\cliproxyapi-plusplus\cliproxyapi++.exe",
    [string]$ConfigPath = "C:\ProgramData\cliproxyapi-plusplus\config.yaml",
    [string]$ServiceName = "cliproxyapi-plusplus"
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function Get-ServiceState {
    if (-not (Get-Service -Name $ServiceName -ErrorAction SilentlyContinue)) {
        return "NotInstalled"
    }
    return (Get-Service -Name $ServiceName).Status
}

if ($Action -eq "install") {
    if (-not (Test-Path -Path $BinaryPath)) {
        throw "Binary not found at $BinaryPath. Update -BinaryPath to your installed cliproxyapi++ executable."
    }
    if (-not (Test-Path -Path (Split-Path $ConfigPath))) {
        New-Item -ItemType Directory -Force -Path (Split-Path $ConfigPath) | Out-Null
    }
    if (-not (Test-Path -Path $ConfigPath)) {
        throw "Config file not found at $ConfigPath"
    }
    $existing = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
    if ($null -ne $existing) {
        Stop-Service -Name $ServiceName -ErrorAction SilentlyContinue
        Start-Sleep -Seconds 1
        Remove-Service -Name $ServiceName
    }
    $binaryArgv = "`"$BinaryPath`" --config `"$ConfigPath`""
    New-Service `
        -Name $ServiceName `
        -BinaryPathName $binaryArgv `
        -DisplayName "cliproxyapi++ Service" `
        -StartupType Automatic `
        -Description "cliproxyapi++ local proxy API"
    Write-Host "Installed service '$ServiceName'. Start with: .\$(Split-Path -Leaf $PSCommandPath) -Action start"
    return
}

if ($Action -eq "uninstall") {
    if (Get-ServiceState -ne "NotInstalled") {
        Stop-Service -Name $ServiceName -ErrorAction SilentlyContinue
        Remove-Service -Name $ServiceName
        Write-Host "Removed service '$ServiceName'."
    } else {
        Write-Host "Service '$ServiceName' is not installed."
    }
    return
}

if ($Action -eq "start") {
    Start-Service -Name $ServiceName
    Write-Host "Service '$ServiceName' started."
    return
}

if ($Action -eq "stop") {
    Stop-Service -Name $ServiceName
    Write-Host "Service '$ServiceName' stopped."
    return
}

if ($Action -eq "status") {
    Write-Host "Service '$ServiceName' state: $(Get-ServiceState)"
}
