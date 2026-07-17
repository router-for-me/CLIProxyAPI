@echo off
setlocal EnableExtensions

cd /d "%~dp0"
set "TARGET_EXE=%CD%\cli-proxy-api.exe"
set "RUNNING_PID="

for /f "tokens=5" %%P in ('netstat -ano ^| findstr /R /C:":8317 .*LISTENING"') do if not defined RUNNING_PID set "RUNNING_PID=%%P"

if defined RUNNING_PID (
    echo CLIProxyAPI is already running. PID: %RUNNING_PID%
    echo Address: http://127.0.0.1:8317/
    echo.
    pause
    exit /b 0
)

if not exist "%TARGET_EXE%" (
    echo Missing executable: "%TARGET_EXE%"
    echo.
    pause
    exit /b 1
)

echo Starting CLIProxyAPI...
echo Address: http://127.0.0.1:8317/
echo.
"%TARGET_EXE%" %*
set "EXIT_CODE=%ERRORLEVEL%"

echo.
echo CLIProxyAPI exited with code %EXIT_CODE%.
pause
exit /b %EXIT_CODE%
