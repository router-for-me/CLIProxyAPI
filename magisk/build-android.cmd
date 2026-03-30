@echo off
REM CLIProxyAPI Android 构建脚本 (Windows)
REM 用于交叉编译 Android 版本

setlocal EnableDelayedExpansion

set SCRIPT_DIR=%~dp0
set PROJECT_ROOT=%SCRIPT_DIR%..
set OUTPUT_DIR=%SCRIPT_DIR%bin

REM 默认值
if "%VERSION%"=="" set VERSION=dev
for /f "tokens=*" %%i in ('git rev-parse --short HEAD 2^>nul') do set COMMIT=%%i
if "%COMMIT%"=="" set COMMIT=unknown
for /f "tokens=*" %%i in ('powershell -Command "Get-Date -Format 'yyyy-MM-ddTHH:mm:ssZ'"') do set BUILD_DATE=%%i

echo.
echo [INFO] CLIProxyAPI Android Build Script
echo [INFO] Version: %VERSION%
echo [INFO] Commit: %COMMIT%
echo [INFO] Build Date: %BUILD_DATE%
echo.

REM 检查 Go 是否安装
where go >nul 2>&1
if %ERRORLEVEL% neq 0 (
    echo [ERROR] Go is not installed. Please install Go 1.21 or later.
    exit /b 1
)

for /f "tokens=*" %%i in ('go version') do echo [INFO] %%i

REM 创建输出目录
if not exist "%OUTPUT_DIR%" mkdir "%OUTPUT_DIR%"

REM 构建 Android arm64
echo.
echo [INFO] Building for android/arm64...
cd /d "%PROJECT_ROOT%"
set CGO_ENABLED=0
set GOOS=android
set GOARCH=arm64
go build -ldflags="-s -w -X 'main.Version=%VERSION%' -X 'main.Commit=%COMMIT%' -X 'main.BuildDate=%BUILD_DATE%'" -o "%OUTPUT_DIR%\cli-proxy-api-android-arm64" .\cmd\server\
if %ERRORLEVEL% equ 0 (
    echo [INFO] Successfully built: cli-proxy-api-android-arm64
) else (
    echo [ERROR] Failed to build for android/arm64
    exit /b 1
)

REM 构建 Android arm
echo.
echo [INFO] Building for android/arm...
set GOARCH=arm
go build -ldflags="-s -w -X 'main.Version=%VERSION%' -X 'main.Commit=%COMMIT%' -X 'main.BuildDate=%BUILD_DATE%'" -o "%OUTPUT_DIR%\cli-proxy-api-android-arm" .\cmd\server\
if %ERRORLEVEL% equ 0 (
    echo [INFO] Successfully built: cli-proxy-api-android-arm
) else (
    echo [ERROR] Failed to build for android/arm
    exit /b 1
)

REM 构建 Android amd64
echo.
echo [INFO] Building for android/amd64...
set GOARCH=amd64
go build -ldflags="-s -w -X 'main.Version=%VERSION%' -X 'main.Commit=%COMMIT%' -X 'main.BuildDate=%BUILD_DATE%'" -o "%OUTPUT_DIR%\cli-proxy-api-android-amd64" .\cmd\server\
if %ERRORLEVEL% equ 0 (
    echo [INFO] Successfully built: cli-proxy-api-android-amd64
) else (
    echo [ERROR] Failed to build for android/amd64
    exit /b 1
)

echo.
echo [INFO] All Android builds completed!
echo [INFO] Output directory: %OUTPUT_DIR%
dir /b "%OUTPUT_DIR%"

echo.
echo [INFO] To create Magisk modules, run: pack.cmd
echo [INFO] Or manually copy binaries to the magisk directory

endlocal
