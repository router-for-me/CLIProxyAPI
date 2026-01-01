#!/usr/bin/env pwsh
# Quick start script for CLIProxyAPI server

[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
[Console]::InputEncoding = [System.Text.Encoding]::UTF8
$OutputEncoding = [System.Text.Encoding]::UTF8
$PSDefaultParameterValues['*:Encoding'] = 'utf8'
chcp 65001 | Out-Null

# Получаем порт и API ключ из конфига
$config = if (Test-Path "config.yaml") { Get-Content "config.yaml" -Raw } else { "" }
$port = if ($config -match "port:\s*(\d+)") { [int]$matches[1] } else { 11434 }
$apiKey = if ($config -match "api-keys:\s*\n\s*-\s*""([^""]+)""") { $matches[1] } else { "123456" }
$exeName = "server.exe"

# Функция для освобождения порта
function Release-Port {
    param([int]$PortNumber)
    $procIds = netstat -ano | Select-String ":$PortNumber\s" | ForEach-Object {
        ($_ -split '\s+')[-1]
    } | Where-Object { $_ -match '^\d+$' -and $_ -ne '0' } | Select-Object -Unique
    
    if ($procIds) {
        Write-Host "Releasing port $PortNumber..." -ForegroundColor Yellow
        foreach ($p in $procIds) {
            Stop-Process -Id $p -Force -ErrorAction SilentlyContinue
        }
        Start-Sleep -Milliseconds 500
    }
}

# Компиляция
Write-Host "Building server..." -ForegroundColor Cyan
$buildResult = go build -o $exeName ./cmd/server 2>&1
if ($LASTEXITCODE -ne 0) {
    Write-Host "Build failed:" -ForegroundColor Red
    Write-Host $buildResult
    exit 1
}
Write-Host "Build successful" -ForegroundColor Green

# Освобождаем порт
Release-Port -PortNumber $port

# Запускаем exe в фоне
Write-Host "Starting server..." -ForegroundColor Cyan
$process = Start-Process -FilePath ".\$exeName" -PassThru -NoNewWindow

# Ждём запуска и проверяем модели
Start-Sleep -Seconds 3
for ($i = 0; $i -lt 10; $i++) {
    try {
        $json = (Invoke-WebRequest -Uri "http://localhost:$port/v1/models" -Headers @{"Authorization"="Bearer $apiKey"}).Content | ConvertFrom-Json
        Write-Host "`nAvailable models:" -ForegroundColor Green
        $json.data.id | ForEach-Object { Write-Host $_ -ForegroundColor Yellow }
        Write-Host ""
        break
    } catch {
        if ($i -eq 9) { Write-Host "Failed to fetch models" -ForegroundColor Red }
        Start-Sleep -Seconds 1
    }
}

# Ждём завершения процесса
try {
    $process | Wait-Process
} finally {
    if (!$process.HasExited) {
        Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue
    }
    Release-Port -PortNumber $port
}
