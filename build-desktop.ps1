# CLIProxyAPI Desktop 构建脚本
# management.html 由后端运行时自动从 GitHub Releases 下载，无需手动构建前端
# 用法: .\build-desktop.ps1

# 设置控制台 UTF-8 编码，避免中文乱码
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$OutputEncoding = [System.Text.Encoding]::UTF8
chcp 65001 | Out-Null

$ErrorActionPreference = "Stop"
$wails = "wails"
if (-not (Get-Command $wails -ErrorAction SilentlyContinue)) {
    $goBin = go env GOPATH
    if ($goBin) { $env:Path += ";$goBin\bin" }
}

Write-Host "=== CLIProxyAPI Desktop 构建 ===" -ForegroundColor Cyan
$root = $PSScriptRoot

function Stop-DesktopProcess {
    $running = Get-Process -Name "CLIProxyAPI" -ErrorAction SilentlyContinue
    if (-not $running) { return }
    Write-Host "  检测到运行中的 CLIProxyAPI，正在停止..." -ForegroundColor Yellow
    $running | Stop-Process -Force -ErrorAction SilentlyContinue
    Start-Sleep -Milliseconds 800
}

function Remove-BuildBinExecutables {
    param(
        [string]$binDir
    )

    if (-not (Test-Path $binDir)) { return }

    $targets = Get-ChildItem -Path $binDir -Filter "CLIProxyAPI.exe*" -File -ErrorAction SilentlyContinue
    foreach ($target in $targets) {
        try {
            Remove-Item -Path $target.FullName -Force -ErrorAction Stop
        } catch {
            throw "无法删除旧可执行文件 '$($target.FullName)'。请关闭占用该文件的程序（如杀毒/索引器）后重试。"
        }
    }
}

# 1. 同步桌面图标
Write-Host "`n[1/3] 同步桌面图标..." -ForegroundColor Yellow
$iconSource = Join-Path $root "CLIProxyAPI.png"
$appIconTarget = Join-Path $root "cmd\desktop\build\appicon.png"
$windowsIconTarget = Join-Path $root "cmd\desktop\build\windows\icon.ico"

if (-not (Test-Path $iconSource)) {
    throw "未找到图标文件: $iconSource"
}

$appIconDir = Split-Path $appIconTarget -Parent
if (-not (Test-Path $appIconDir)) {
    New-Item -ItemType Directory -Force -Path $appIconDir | Out-Null
}
Copy-Item -Path $iconSource -Destination $appIconTarget -Force

if (Test-Path $windowsIconTarget) {
    Remove-Item -Path $windowsIconTarget -Force
}

Write-Host "  图标已同步: $iconSource -> $appIconTarget" -ForegroundColor Gray
Write-Host "  已移除旧 icon.ico，wails build 将根据 appicon.png 重新生成" -ForegroundColor Gray

# 2. 获取版本信息
Write-Host "`n[2/3] 准备版本信息..." -ForegroundColor Yellow
Set-Location $root
$gitVersion = (git describe --tags --always 2>$null)
if (-not $gitVersion) { $gitVersion = "dev" }
$gitCommit = (git rev-parse --short HEAD 2>$null)
if (-not $gitCommit) { $gitCommit = "none" }
$buildDate = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")
$ldflags = "-X 'main.Version=$gitVersion' -X 'main.Commit=$gitCommit' -X 'main.BuildDate=$buildDate'"
Write-Host "  版本: $gitVersion, 提交: $gitCommit, 时间: $buildDate" -ForegroundColor Gray

# 3. 构建桌面 exe
Write-Host "`n[3/3] 构建 exe..." -ForegroundColor Yellow
Set-Location (Join-Path $root "cmd\desktop")

$binDir = Join-Path $root "cmd\desktop\build\bin"
Stop-DesktopProcess
Remove-BuildBinExecutables -binDir $binDir

& $wails generate module 2>$null
& $wails build -clean -ldflags "$ldflags"
if ($LASTEXITCODE -ne 0) { exit 1 }

$exePath = ".\build\bin\CLIProxyAPI.exe"
if (Test-Path $exePath) {
    $size = (Get-Item $exePath).Length / 1MB
    Write-Host "`n构建完成: $exePath ($([math]::Round($size, 2)) MB)" -ForegroundColor Green
}
