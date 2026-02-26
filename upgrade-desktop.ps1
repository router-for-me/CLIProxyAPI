# CLIProxyAPI Desktop 一键升级脚本
# 拉取官方后端最新代码，重新构建桌面 exe
# management.html 由后端运行时自动从 GitHub Releases 下载，无需手动构建前端
# 用法: .\upgrade-desktop.ps1
#   -Output <path> 指定 exe 输出路径（默认覆盖 build 目录）

param(
    [string]$Output = "",
    [string]$AutoLaunch = ""
)

$ErrorActionPreference = "Continue"
$root = $PSScriptRoot

function Write-Step($step, $msg) { Write-Host "`n[$step] $msg" -ForegroundColor Yellow }
function Write-Ok($msg) { Write-Host "  $msg" -ForegroundColor Green }
function Write-Info($msg) { Write-Host "  $msg" -ForegroundColor Gray }

function Exit-WithPause($code) {
    Write-Host ""
    Read-Host "按回车键关闭窗口"
    exit $code
}

try {

# ── 日志记录（方便排查问题） ──
$logFile = Join-Path $env:TEMP "cliproxyapi_upgrade.log"
Start-Transcript -Path $logFile -Force | Out-Null

# ── 环境检查 ──
Write-Host "=== CLIProxyAPI Desktop 升级 ===" -ForegroundColor Cyan
Write-Host "  项目根目录: $root" -ForegroundColor Gray

$wails = "wails"
if (-not (Get-Command $wails -ErrorAction SilentlyContinue)) {
    $gopath = (go env GOPATH 2>$null)
    if ($gopath) { $env:Path += ";$gopath\bin" }
}
foreach ($tool in @("go", "git", "wails")) {
    if (-not (Get-Command $tool -ErrorAction SilentlyContinue)) {
        Write-Host "错误: 未找到 $tool，请先安装" -ForegroundColor Red
        Exit-WithPause 1
    }
}
Write-Ok "环境检查通过 (go, git, wails)"

# ── 从配置文件读取代理 ──
$configFile = Join-Path $env:APPDATA "CLIProxyAPI-Desktop\config.yaml"
$proxyUrl = ""
if (Test-Path $configFile) {
    $configContent = Get-Content $configFile -Raw
    if ($configContent -match 'proxy-url:\s*"([^"]+)"') {
        $proxyUrl = $Matches[1]
    }
}
if ($proxyUrl) {
    Write-Info "使用代理: $proxyUrl"
    $env:http_proxy = $proxyUrl
    $env:https_proxy = $proxyUrl
} else {
    Write-Info "未检测到代理配置，直连"
}

# ── 等待旧进程退出 ──
$buildExe = Join-Path $root "cmd\desktop\build\bin\CLIProxyAPI.exe"
$waited = 0
while ($waited -lt 15) {
    $proc = Get-Process -Name "CLIProxyAPI" -ErrorAction SilentlyContinue
    if (-not $proc) { break }
    Write-Info "等待旧进程退出... ($waited 秒)"
    Start-Sleep -Seconds 1
    $waited++
}
$proc = Get-Process -Name "CLIProxyAPI" -ErrorAction SilentlyContinue
if ($proc) {
    Write-Info "强制终止旧进程"
    Stop-Process -Name "CLIProxyAPI" -Force -ErrorAction SilentlyContinue
    Start-Sleep -Seconds 2
}

# ── 步骤 1: 更新后端 ──
Write-Step "1/3" "更新后端 (CLIProxyAPI)..."
Set-Location $root

$oldBackend = git rev-parse --short HEAD 2>$null
if (-not $oldBackend) { $oldBackend = "unknown" }
Write-Info "当前版本: $oldBackend"

Write-Info "拉取官方最新代码..."
git fetch origin main --tags --progress
if ($LASTEXITCODE -ne 0) {
    Write-Host "  git fetch 失败 (exit code: $LASTEXITCODE)" -ForegroundColor Red
    Exit-WithPause 1
}

$workingTreeDirty = (git status --porcelain 2>$null)
if ($workingTreeDirty) {
    Write-Host "检测到本地未提交改动，已取消升级以避免覆盖本地文件。请先提交或暂存后再重试。" -ForegroundColor Red
    Exit-WithPause 1
}

# 强制同步到 origin/main（仅在工作区干净时）
Write-Info "同步到最新版本..."
git reset --hard origin/main
if ($LASTEXITCODE -ne 0) {
    Write-Host "  git reset 失败 (exit code: $LASTEXITCODE)" -ForegroundColor Red
    Exit-WithPause 1
}

$newBackend = git rev-parse --short HEAD
if ($oldBackend -eq $newBackend) {
    Write-Ok "后端已是最新 ($newBackend)"
} else {
    Write-Ok "后端已更新: $oldBackend -> $newBackend"
    Write-Info "更新记录:"
    git --no-pager log --oneline --no-decorate "${oldBackend}..${newBackend}"
}

# 重新添加固定版本的 wails 依赖（官方仓库没有这个依赖）
Write-Info "添加固定版本 wails 依赖..."
go get github.com/wailsapp/wails/v2@v2.11.0 2>$null
go mod tidy 2>$null

# ── 步骤 2: 构建 exe ──
Write-Step "2/3" "同步图标并构建..."

$iconSource = Join-Path $root "CLIProxyAPI.png"
$appIconTarget = Join-Path $root "cmd\desktop\build\appicon.png"
$windowsIconTarget = Join-Path $root "cmd\desktop\build\windows\icon.ico"

if (-not (Test-Path $iconSource)) {
    Write-Host "错误: 未找到图标文件 $iconSource" -ForegroundColor Red
    Exit-WithPause 1
}

$appIconDir = Split-Path $appIconTarget -Parent
if (-not (Test-Path $appIconDir)) {
    New-Item -ItemType Directory -Force -Path $appIconDir | Out-Null
}
Copy-Item -Path $iconSource -Destination $appIconTarget -Force

if (Test-Path $windowsIconTarget) {
    Remove-Item -Path $windowsIconTarget -Force
}
Write-Info "图标已同步并重置 windows/icon.ico"

$gitVersion = git describe --tags --always 2>$null
if (-not $gitVersion) { $gitVersion = "dev" }
$gitCommit = git rev-parse --short HEAD 2>$null
if (-not $gitCommit) { $gitCommit = "none" }
$buildDate = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")
$ldflags = "-X 'main.Version=$gitVersion' -X 'main.Commit=$gitCommit' -X 'main.BuildDate=$buildDate'"
Write-Info "版本: $gitVersion, 提交: $gitCommit"

Set-Location (Join-Path $root "cmd\desktop")
& $wails build -clean -ldflags "$ldflags"
if ($LASTEXITCODE -ne 0) {
    Write-Host "exe 构建失败" -ForegroundColor Red
    Exit-WithPause 1
}
Write-Ok "exe 构建完成"

# ── 步骤 3: 输出 ──
Write-Step "3/3" "完成"

$size = [math]::Round((Get-Item $buildExe).Length / 1MB, 2)

if ($Output -and $Output -ne $buildExe) {
    $outputDir = Split-Path $Output -Parent
    if ($outputDir -and -not (Test-Path $outputDir)) {
        New-Item -ItemType Directory -Force -Path $outputDir | Out-Null
    }
    Copy-Item $buildExe $Output -Force
    Write-Ok "已输出到: $Output ($size MB)"
} else {
    Write-Ok "构建产物: $buildExe ($size MB)"
}

# 版本摘要
Write-Host ""
Write-Host "=== 升级摘要 ===" -ForegroundColor Cyan
Set-Location $root
$finalBackend = git rev-parse --short HEAD
Write-Host "  后端: $oldBackend -> $finalBackend" -ForegroundColor White
Write-Host "  大小: $size MB" -ForegroundColor White
Write-Host ""

# 自动启动新 exe（由 app 内置更新调用）
if ($AutoLaunch -and (Test-Path $AutoLaunch)) {
    Write-Host "正在启动新版本..." -ForegroundColor Cyan
    Start-Process $AutoLaunch
    Start-Sleep -Seconds 2
} else {
    Write-Host "升级完成，请手动启动 CLIProxyAPI.exe" -ForegroundColor Cyan
    Exit-WithPause 0
}

} catch {
    Write-Host ""
    Write-Host "升级过程出错: $_" -ForegroundColor Red
    Write-Host $_.ScriptStackTrace -ForegroundColor DarkGray
    Stop-Transcript -ErrorAction SilentlyContinue | Out-Null
    Exit-WithPause 1
}
Stop-Transcript -ErrorAction SilentlyContinue | Out-Null
