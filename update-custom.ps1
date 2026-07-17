param(
    [string]$PatchBranch = "local/no-ads-clean",
    [string]$BaseBranch = "main",
    [string]$UpstreamBranch = "upstream/main",
    [string]$TargetExe = "cli-proxy-api.exe",
    [string]$StartArgs = "",
    [switch]$SkipTests,
    [switch]$NoRestart
)

$ErrorActionPreference = "Stop"

function Write-Step {
    param([string]$Message)
    Write-Host ""
    Write-Host "==> $Message" -ForegroundColor Cyan
}

function Invoke-Git {
    $gitArguments = @($args)
    & git @gitArguments
    if ($LASTEXITCODE -ne 0) { throw "git $($gitArguments -join ' ') failed" }
}

function Invoke-GitAt {
    param([string]$Path)
    $gitArguments = @($args)
    & git -C $Path @gitArguments
    if ($LASTEXITCODE -ne 0) { throw "git -C $Path $($gitArguments -join ' ') failed" }
}

function Resolve-GoBinary {
    if ($env:GO_EXE -and (Test-Path -LiteralPath $env:GO_EXE)) { return $env:GO_EXE }
    $goCommand = Get-Command go -ErrorAction SilentlyContinue
    if ($goCommand) { return $goCommand.Source }
    $fallback = Join-Path $env:USERPROFILE ".cache\codex-runtimes\go1.26.4\go\bin\go.exe"
    if (Test-Path -LiteralPath $fallback) { return $fallback }
    throw "Go executable not found. Install Go or set GO_EXE to go.exe."
}

function Assert-CleanWorktree {
    $dirty = (& git status --porcelain)
    if ($dirty) { throw "Working tree is not clean. Commit or remove local changes before updating." }
}

function Assert-NoActiveGitOperation {
    foreach ($name in @("rebase-merge", "rebase-apply", "MERGE_HEAD", "CHERRY_PICK_HEAD")) {
        $path = (& git rev-parse --git-path $name).Trim()
        if ($path -and (Test-Path -LiteralPath $path)) {
            throw "A Git operation is already in progress at '$path'."
        }
    }
}

function Assert-BranchExists {
    param([string]$Branch)
    & git show-ref --verify --quiet "refs/heads/$Branch"
    if ($LASTEXITCODE -ne 0) { throw "Local branch '$Branch' does not exist." }
}

function Assert-MainCanFastForward {
    param([string]$LocalBranch, [string]$RemoteBranch)
    & git merge-base --is-ancestor $LocalBranch $RemoteBranch
    if ($LASTEXITCODE -ne 0) {
        throw "Local '$LocalBranch' has commits that are not in '$RemoteBranch'."
    }
}

function Get-TargetProcesses {
    param([string]$ExecutablePath)
    $resolved = [System.IO.Path]::GetFullPath($ExecutablePath)
    Get-CimInstance Win32_Process |
        Where-Object { $_.ExecutablePath -and ([System.IO.Path]::GetFullPath($_.ExecutablePath) -ieq $resolved) }
}

function Stop-TargetProcesses {
    param([object[]]$Processes)
    foreach ($proc in $Processes) {
        Write-Host "Stopping running process PID $($proc.ProcessId)"
        Stop-Process -Id $proc.ProcessId -Force -ErrorAction SilentlyContinue
        for ($i = 0; $i -lt 60; $i++) {
            if (-not (Get-Process -Id $proc.ProcessId -ErrorAction SilentlyContinue)) { break }
            Start-Sleep -Milliseconds 250
        }
    }
    Start-Sleep -Milliseconds 500
}

function Copy-ExecutableWithRetry {
    param([string]$Source, [string]$Destination)
    $lastError = $null
    for ($i = 0; $i -lt 60; $i++) {
        try {
            Copy-Item -LiteralPath $Source -Destination $Destination -Force
            return
        } catch {
            $lastError = $_
            Start-Sleep -Milliseconds 500
        }
    }
    throw $lastError
}

function Start-TargetService {
    param(
        [string]$RepoRoot,
        [string]$StartArgs
    )
    $launcher = Join-Path $RepoRoot "start-service.bat"
    if (-not (Test-Path -LiteralPath $launcher)) {
        throw "Service launcher is missing: $launcher"
    }
    $commandLine = "call `"$launcher`""
    if ($StartArgs.Trim()) { $commandLine += " $StartArgs" }
    return Start-Process -FilePath $env:ComSpec -ArgumentList @("/D", "/C", $commandLine) -WorkingDirectory $RepoRoot -WindowStyle Normal -PassThru
}

function Wait-TargetProcess {
    param([string]$ExecutablePath)
    for ($i = 0; $i -lt 120; $i++) {
        if (@(Get-TargetProcesses $ExecutablePath).Count -gt 0) { return }
        Start-Sleep -Milliseconds 500
    }
    throw "The executable did not become visible as a running process within 60 seconds. Its console window was kept open for diagnostics."
}

function Get-ConfigPort {
    param([string]$ConfigPath)
    if (Test-Path -LiteralPath $ConfigPath) {
        foreach ($line in Get-Content -LiteralPath $ConfigPath) {
            if ($line -match '^\s*port:\s*(\d+)\s*$') { return [int]$Matches[1] }
        }
    }
    return 8317
}

function Wait-ServerReady {
    param([int]$Port)
    $url = "http://127.0.0.1:$Port/"
    for ($i = 0; $i -lt 20; $i++) {
        try {
            $response = Invoke-WebRequest -Uri $url -UseBasicParsing -TimeoutSec 2
            if ($response.StatusCode -ge 200 -and $response.StatusCode -lt 500) {
                Write-Host "Server responded at $url"
                return
            }
        } catch { Start-Sleep -Milliseconds 500 }
    }
    throw "Server did not respond at $url within the wait window."
}

function Invoke-GoTests {
    param([string]$GoExe, [string]$WorkingDirectory)
    $failures = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::Ordinal)
    Push-Location -LiteralPath $WorkingDirectory
    $previousErrorActionPreference = $ErrorActionPreference
    try {
        # Windows PowerShell 5.1 promotes redirected native stderr to NativeCommandError.
        # Go writes normal dependency-download progress to stderr, so inspect its exit code instead.
        $ErrorActionPreference = "Continue"
        & $GoExe test -json ./... 2>&1 | ForEach-Object {
            $line = $_
            try { $event = $line.ToString() | ConvertFrom-Json -ErrorAction Stop }
            catch { Write-Host $line; return }
            if ($event.Output) { Write-Host -NoNewline $event.Output }
            if ($event.Action -eq "fail" -and $event.Package) {
                [void]$failures.Add("package:$($event.Package)")
                if ($event.Test) { [void]$failures.Add("test:$($event.Package)::$($event.Test)") }
            }
        }
        $exitCode = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previousErrorActionPreference
        Pop-Location
    }
    return [pscustomobject]@{ ExitCode = $exitCode; Failures = @($failures | Sort-Object) }
}

function Test-SameFailures {
    param([string[]]$PatchFailures, [string[]]$BaseFailures)
    if ($PatchFailures.Count -eq 0 -or $BaseFailures.Count -eq 0) { return $false }
    return @(Compare-Object -ReferenceObject $PatchFailures -DifferenceObject $BaseFailures).Count -eq 0
}

function Invoke-UpstreamBaselineTests {
    param([string]$RepoRoot, [string]$BaseBranch, [string]$GoExe)
    $path = Join-Path ([System.IO.Path]::GetTempPath()) "cliproxyapi-baseline-$PID"
    Invoke-Git worktree add --detach $path $BaseBranch | Out-Host
    try { return Invoke-GoTests -GoExe $GoExe -WorkingDirectory $path }
    finally { Invoke-Git worktree remove --force $path | Out-Host }
}

function Update-PatchBranchToHead {
    param([string]$PatchBranch, [string]$NewHead)
    Invoke-Git switch $PatchBranch
    $oldHead = (& git rev-parse HEAD).Trim()
    if ($oldHead -eq $NewHead) { return }
    Invoke-Git switch --detach $NewHead
    try {
        Invoke-Git branch -f $PatchBranch $NewHead
        Invoke-Git switch $PatchBranch
    } catch {
        try {
            Invoke-Git branch -f $PatchBranch $oldHead
            Invoke-Git switch $PatchBranch
        } catch {}
        throw
    }
}

function New-CustomizedBuild {
    param(
        [string]$RepoRoot,
        [string]$BaseBranch,
        [string]$GoExe,
        [bool]$RunTests,
        [string]$UpstreamBranch,
        [string]$CustomizationScript
    )

    $stamp = Get-Date -Format "yyyyMMdd-HHmmss"
    $safeName = $BaseBranch -replace '[^A-Za-z0-9._-]', '-'
    $tempRoot = Join-Path ([System.IO.Path]::GetTempPath()) "cliproxyapi-generated-worktrees"
    $worktree = Join-Path $tempRoot "$safeName-$stamp-$PID"
    $branch = "codex/generated-$safeName-$stamp-$PID"
    New-Item -ItemType Directory -Force -Path $tempRoot | Out-Null
    Invoke-Git worktree prune
    Write-Host "Generated source worktree: $worktree"
    Invoke-Git worktree add -b $branch $worktree $BaseBranch | Out-Host

    try {
        & $CustomizationScript -SourceRoot $RepoRoot -TargetRoot $worktree
        if ($LASTEXITCODE -ne 0) { throw "apply-customizations.ps1 failed" }

        $goFiles = @(
            "internal/api/server.go",
            "internal/api/handlers/management/config_basic.go",
            "internal/api/handlers/management/plugin_store.go",
            "internal/api/handlers/management/plugin_store_test.go",
            "internal/managementasset/sanitize.go",
            "internal/managementasset/sanitize_custom_test.go",
            "internal/managementasset/updater.go",
            "internal/misc/antigravity_version.go",
            "internal/pluginhost/support_nocgo.go",
            "internal/pluginhost/support_test.go",
            "internal/pluginhost/support_windows_nocgo.go",
            "internal/runtime/executor/helps/usage_helpers.go",
            "internal/runtime/executor/helps/usage_source_custom.go",
            "internal/runtime/executor/helps/usage_source_custom_test.go",
            "internal/registry/model_updater.go",
            "internal/registry/codex_client_models_updater.go",
            "internal/translator/openai/openai/responses/openai_openai-responses_response.go",
            "internal/translator/openai/openai/responses/openai_openai-responses_lifecycle_model_test.go"
        ) | ForEach-Object { Join-Path $worktree $_ }
        $gofmtExe = Join-Path (Split-Path -Parent $GoExe) "gofmt.exe"
        & $gofmtExe -w @goFiles
        if ($LASTEXITCODE -ne 0) { throw "gofmt failed" }

        Invoke-GitAt $worktree add -A
        Invoke-GitAt $worktree -c user.name="CLIProxyAPI Custom Updater" -c user.email="local@cliproxy.invalid" commit -m "custom: apply local no-ads policy"
        $head = (& git -C $worktree rev-parse HEAD).Trim()

        if ($RunTests) {
            Write-Step "Run tests on generated source"
            $patchTests = Invoke-GoTests -GoExe $GoExe -WorkingDirectory $worktree
            if ($patchTests.ExitCode -ne 0) {
                Write-Step "Compare failures with upstream baseline"
                $baseTests = Invoke-UpstreamBaselineTests -RepoRoot $RepoRoot -BaseBranch $BaseBranch -GoExe $GoExe
                if ($baseTests.ExitCode -eq 0 -or -not (Test-SameFailures $patchTests.Failures $baseTests.Failures)) {
                    throw "Generated source has test failures not present on upstream."
                }
                Write-Warning "Generated source only has failures already present on upstream."
            }
        }

        Write-Step "Build generated source"
        $tag = (& git -C $RepoRoot describe --tags --abbrev=0 $UpstreamBranch 2>$null)
        if (-not $tag) { $tag = "dev" }
        $version = ($tag -replace '^v', '') + "-custom"
        $buildDate = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")
        $commit = $head.Substring(0, 8)
        $ldflags = "-s -w -X main.Version=$version -X main.Commit=$commit -X main.BuildDate=$buildDate"
        $builtExe = Join-Path $worktree "cli-proxy-api.custom.exe"
        Push-Location $worktree
        try { & $GoExe build -buildvcs=false "-ldflags=$ldflags" -o $builtExe ./cmd/server }
        finally { Pop-Location }
        if ($LASTEXITCODE -ne 0) { throw "go build failed" }

        return [pscustomobject]@{ Head = $head; Worktree = $worktree; Branch = $branch; BuiltExe = $builtExe }
    } catch {
        Write-Warning "Generated source failed. The current branch, executable, and service were not changed."
        Write-Warning "Diagnostic worktree: $worktree"
        throw
    }
}

function Remove-CustomizedBuild {
    param([object]$Result)
    if (-not $Result) { return }
    try { Invoke-Git worktree remove --force $Result.Worktree | Out-Host } catch { Write-Warning $_ }
    try { Invoke-Git branch --delete --force $Result.Branch | Out-Host } catch { Write-Warning $_ }
}

$repoRoot = (& git rev-parse --show-toplevel).Trim()
if (-not $repoRoot) { throw "Not inside a Git repository." }
Set-Location -LiteralPath $repoRoot
$updaterRoot = Split-Path -Parent $PSCommandPath
$customizationScript = Join-Path $updaterRoot "apply-customizations.ps1"
if (-not (Test-Path -LiteralPath $customizationScript)) {
    throw "Customization script is missing: $customizationScript"
}
$targetPath = if ([System.IO.Path]::IsPathRooted($TargetExe)) { $TargetExe } else { Join-Path $repoRoot $TargetExe }
$targetPath = [System.IO.Path]::GetFullPath($targetPath)

Write-Step "Preflight"
Assert-NoActiveGitOperation
Assert-CleanWorktree
Assert-BranchExists $PatchBranch
Assert-BranchExists $BaseBranch
if ((git remote) -notcontains "upstream") { Invoke-Git remote add upstream https://github.com/router-for-me/CLIProxyAPI.git }

Write-Step "Fetch upstream"
Invoke-Git fetch upstream --tags
Assert-MainCanFastForward $BaseBranch $UpstreamBranch
Invoke-Git branch -f $BaseBranch $UpstreamBranch

$goExe = Resolve-GoBinary
if (-not $env:GOPROXY) { $env:GOPROXY = "https://goproxy.cn,direct" }

Write-Step "Generate custom source from latest upstream"
$result = New-CustomizedBuild -RepoRoot $repoRoot -BaseBranch $BaseBranch -GoExe $goExe -RunTests (-not $SkipTests) -UpstreamBranch $UpstreamBranch -CustomizationScript $customizationScript
$applied = $false
try {
    $running = @(Get-TargetProcesses $targetPath)
    $wasRunning = $running.Count -gt 0
    $backupPath = $null
    if (Test-Path -LiteralPath $targetPath) {
        $backupDir = Join-Path $repoRoot "dist\backups"
        New-Item -ItemType Directory -Force -Path $backupDir | Out-Null
        $backupPath = Join-Path $backupDir "cli-proxy-api.$((Get-Date).ToString('yyyyMMdd-HHmmss')).exe"
        Copy-Item -LiteralPath $targetPath -Destination $backupPath -Force
        Write-Host "Backed up existing executable to $backupPath"
    }

    $oldPatchHead = (& git rev-parse $PatchBranch).Trim()
    Update-PatchBranchToHead -PatchBranch $PatchBranch -NewHead $result.Head
    $branchUpdated = $true
    $launcherProcess = $null
    try {
        if ($wasRunning) { Stop-TargetProcesses $running }
        Copy-ExecutableWithRetry -Source $result.BuiltExe -Destination $targetPath

        if (-not $NoRestart -and $wasRunning) {
            Write-Step "Restart service"
            $launcherProcess = Start-TargetService -RepoRoot $repoRoot -StartArgs $StartArgs
            Wait-TargetProcess -ExecutablePath $targetPath
            Wait-ServerReady -Port (Get-ConfigPort (Join-Path $repoRoot "config.yaml"))
        }

        $applied = $true
    } catch {
        $deploymentError = $_
        Write-Warning "Deployment failed. Restoring the previous branch, executable, and service."
        $rollbackFailures = @()
        try {
            $newProcesses = @(Get-TargetProcesses $targetPath)
            if ($newProcesses.Count -gt 0) { Stop-TargetProcesses $newProcesses }
        } catch { $rollbackFailures += "stop updated service: $($_.Exception.Message)" }
        try {
            if ($launcherProcess -and -not $launcherProcess.HasExited) {
                Stop-Process -Id $launcherProcess.Id -Force
            }
        } catch { $rollbackFailures += "close failed service console: $($_.Exception.Message)" }
        try {
            if ($backupPath -and (Test-Path -LiteralPath $backupPath)) {
                Copy-ExecutableWithRetry -Source $backupPath -Destination $targetPath
            }
        } catch { $rollbackFailures += "restore executable: $($_.Exception.Message)" }
        try {
            if ($branchUpdated) {
                Update-PatchBranchToHead -PatchBranch $PatchBranch -NewHead $oldPatchHead
            }
        } catch { $rollbackFailures += "restore branch: $($_.Exception.Message)" }
        try {
            if ($wasRunning -and $backupPath -and (Test-Path -LiteralPath $targetPath)) {
                [void](Start-TargetService -RepoRoot $repoRoot -StartArgs $StartArgs)
                Wait-TargetProcess -ExecutablePath $targetPath
                Wait-ServerReady -Port (Get-ConfigPort (Join-Path $repoRoot "config.yaml"))
            }
        } catch { $rollbackFailures += "restart previous service: $($_.Exception.Message)" }
        if ($rollbackFailures.Count -gt 0) {
            throw "Deployment failed: $($deploymentError.Exception.Message). Rollback also failed: $($rollbackFailures -join '; ')"
        }
        throw $deploymentError
    }
} finally {
    if ($applied) { Remove-CustomizedBuild $result }
}

Write-Step "Done"
git status --short --branch
