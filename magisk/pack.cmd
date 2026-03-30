@echo off
REM CLIProxyAPI Magisk Module 打包脚本 (Windows)

setlocal EnableDelayedExpansion

set SCRIPT_DIR=%~dp0
set OUTPUT_DIR=%SCRIPT_DIR%bin

if "%VERSION%"=="" set VERSION=dev

echo.
echo [INFO] Packing Magisk modules...
echo [INFO] Version: %VERSION%
echo.

REM 检查是否已构建
if not exist "%OUTPUT_DIR%\cli-proxy-api-android-arm64" (
    echo [ERROR] Binary not found. Please run build-android.cmd first.
    exit /b 1
)

REM 打包 arm64 模块
call :pack_module arm64
if %ERRORLEVEL% neq 0 exit /b 1

REM 打包 arm 模块
call :pack_module arm
if %ERRORLEVEL% neq 0 exit /b 1

REM 打包 amd64 模块
call :pack_module amd64
if %ERRORLEVEL% neq 0 exit /b 1

echo.
echo [INFO] All Magisk modules packed!
echo [INFO] Output directory: %OUTPUT_DIR%
dir /b "%OUTPUT_DIR%\*.zip"

endlocal
exit /b 0

:pack_module
set ARCH=%1
set MODULE_NAME=cliproxyapi-%ARCH%-%VERSION%
set MODULE_DIR=%OUTPUT_DIR%\%MODULE_NAME%

echo.
echo [INFO] Creating Magisk module for %ARCH%...

REM 清理并创建目录
if exist "%MODULE_DIR%" rmdir /s /q "%MODULE_DIR%"
mkdir "%MODULE_DIR%"

REM 复制文件
copy "%OUTPUT_DIR%\cli-proxy-api-android-%ARCH%" "%MODULE_DIR%\cli-proxy-api" >nul
if %ERRORLEVEL% neq 0 exit /b 1
copy "%SCRIPT_DIR%\module.prop" "%MODULE_DIR%\" >nul
if %ERRORLEVEL% neq 0 exit /b 1
copy "%SCRIPT_DIR%\service.sh" "%MODULE_DIR%\" >nul
if %ERRORLEVEL% neq 0 exit /b 1
copy "%SCRIPT_DIR%\post-fs-data.sh" "%MODULE_DIR%\" >nul
if %ERRORLEVEL% neq 0 exit /b 1
copy "%SCRIPT_DIR%\uninstall.sh" "%MODULE_DIR%\" >nul
if %ERRORLEVEL% neq 0 exit /b 1
copy "%SCRIPT_DIR%\config.yaml" "%MODULE_DIR%\" >nul
if %ERRORLEVEL% neq 0 exit /b 1

REM 创建子目录
mkdir "%MODULE_DIR%\auths"
mkdir "%MODULE_DIR%\logs"
mkdir "%MODULE_DIR%\config_backup"
type nul > "%MODULE_DIR%\auths\.gitkeep"

REM 更新 module.prop 中的版本和版本代码
powershell -Command "$v='%VERSION%'; $n=[regex]::Matches($v, '[0-9]+') | Select-Object -First 1 -ExpandProperty Value; if(-not $n){$n='1'}; $vc=$n.PadRight(8,'0')+'000'; (Get-Content '%MODULE_DIR%\module.prop') -replace 'version=v1.0.0', 'version='+$v -replace 'versionCode=10000', ('versionCode='+$vc) | Set-Content '%MODULE_DIR%\module.prop'"

REM 打包 zip
cd /d "%OUTPUT_DIR%"
powershell -Command "Compress-Archive -Path '%MODULE_NAME%' -DestinationPath '%MODULE_NAME%.zip' -Force"

echo [INFO] Created Magisk module: %MODULE_NAME%.zip
exit /b 0
