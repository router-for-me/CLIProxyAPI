@echo off
REM Double-click to start the CLIProxyAPI on http://127.0.0.1:8317
REM Close the window (or Ctrl+C) to stop it.

cd /d "%~dp0"
title CLIProxyAPI - local AI proxy

echo ================================================================
echo  CLIProxyAPI starting
echo ================================================================
echo  URL:     http://127.0.0.1:8317
echo  Key:     see api-keys in config.yaml
echo  Stop:    close this window or press Ctrl+C
echo ================================================================
echo.

cli-proxy-api.exe --config config.yaml

echo.
echo ================================================================
echo  Proxy stopped. Press any key to close.
echo ================================================================
pause >nul
