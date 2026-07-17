@echo off
setlocal EnableExtensions

cd /d "%~dp0"

set "REPO_ROOT=%CD%"
set "POWERSHELL_EXE=%SystemRoot%\System32\WindowsPowerShell\v1.0\powershell.exe"
set "TEMP_DIR=%TEMP%\cliproxyapi-update"
set "SOURCE_PS1=%REPO_ROOT%\update-custom.ps1"
set "TEMP_PS1=%TEMP_DIR%\update-custom.ps1"
set "SOURCE_APPLY_PS1=%REPO_ROOT%\apply-customizations.ps1"
set "TEMP_APPLY_PS1=%TEMP_DIR%\apply-customizations.ps1"
set "TEMP_RUNNER=%TEMP_DIR%\run-update.bat"

if not exist "%TEMP_DIR%" mkdir "%TEMP_DIR%" >nul 2>nul
if errorlevel 1 (
    echo Failed to create temp updater directory: "%TEMP_DIR%"
    pause
    exit /b 1
)

if exist "%SOURCE_PS1%" (
    copy /Y "%SOURCE_PS1%" "%TEMP_PS1%" >nul
)
if exist "%SOURCE_APPLY_PS1%" (
    copy /Y "%SOURCE_APPLY_PS1%" "%TEMP_APPLY_PS1%" >nul
)

if not exist "%TEMP_PS1%" (
    echo update-custom.ps1 is missing and no cached updater exists.
    echo Keep this window open and ask Codex to repair the updater files.
    pause
    exit /b 1
)

if not exist "%TEMP_APPLY_PS1%" (
    echo apply-customizations.ps1 is missing and no cached customization script exists.
    echo Keep this window open and ask Codex to repair the updater files.
    pause
    exit /b 1
)

(
    echo @echo off
    echo setlocal EnableExtensions
    echo cd /d "%REPO_ROOT%"
    echo "%POWERSHELL_EXE%" -NoProfile -ExecutionPolicy Bypass -File "%TEMP_PS1%" %%*
    echo set "EXIT_CODE=%%ERRORLEVEL%%"
    echo echo.
    echo if "%%EXIT_CODE%%"=="0" ^(
    echo     echo Update completed.
    echo ^) else ^(
    echo     echo Update failed with exit code %%EXIT_CODE%%.
    echo     echo The existing branch, executable, and running service are preserved on generation failure.
    echo     echo Keep this window open and ask Codex to inspect the printed diagnostic worktree.
    echo ^)
    echo if not defined CLIPROXYAPI_NO_PAUSE pause
    echo exit /b %%EXIT_CODE%%
) > "%TEMP_RUNNER%"

if /I "%~1"=="--inline" goto run_inline

start "CLIProxyAPI update" /D "%REPO_ROOT%" "%ComSpec%" /C ""%TEMP_RUNNER%" %*"
exit /b 0

:run_inline
shift
call "%TEMP_RUNNER%" %*
exit /b %ERRORLEVEL%
