@echo off
cd /d "%~dp0"
echo ========================================================
echo   CLIProxyAPI Service Starting...
echo   Address:         http://127.0.0.1:8317
echo   Management UI:   http://127.0.0.1:8317/management.html
echo   Management Key:  admin
echo   Client API Key:  sk-cliproxy-123456
echo ========================================================
echo.
start "" powershell -WindowStyle Hidden -NoProfile -Command "Start-Sleep -Seconds 2; Start-Process 'http://127.0.0.1:8317/management.html'"
cli-proxy-api.exe %*
if %ERRORLEVEL% NEQ 0 (
    echo.
    echo Service stopped or exited with error. Press any key to exit...
    pause >nul
)
