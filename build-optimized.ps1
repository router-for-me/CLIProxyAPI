$ErrorActionPreference = "Stop"

# --- Pre-build: ensure Go toolchain is functional ---
# The project requires go1.26.4 (see go.mod), but the auto-downloaded
# toolchain cache at $GOPATH/pkg/mod/golang.org/toolchain@... may be
# incomplete (missing src/). Force GOTOOLCHAIN=local to use the
# system-installed Go even when the version doesn't match exactly.
$env:GOTOOLCHAIN = "local"

$goroot = go env GOROOT
if (-not (Test-Path "$goroot\src\net\http")) {
    Write-Host "ERROR: GOROOT ($goroot) is missing standard library sources." -ForegroundColor Red
    Write-Host "This usually means the auto-downloaded toolchain cache is incomplete." -ForegroundColor Yellow
    Write-Host "Fix: Delete the broken cache and let the build use the system Go:" -ForegroundColor Yellow
    $fixCmd = 'Remove-Item -Recurse -Force "$env:GOPATH\pkg\mod\golang.org\toolchain@*"'
    Write-Host "  $fixCmd" -ForegroundColor White
    Write-Host "Or ensure GOTOOLCHAIN=local is set in the environment." -ForegroundColor Yellow
    exit 1
}

# --- Build ---
$VERSION = git describe --tags --always
$COMMIT = git rev-parse --short HEAD
$BUILDDATE = [DateTime]::UtcNow.ToString('yyyy-MM-ddTHH:mm:ssZ')

$env:CGO_ENABLED = 0
go build -trimpath -ldflags="-s -w -buildid= -X 'main.Version=$VERSION' -X 'main.Commit=$COMMIT' -X 'main.BuildDate=$BUILDDATE'" -o cli-proxy.exe ./cmd/server/

if ($LASTEXITCODE -ne 0) {
    Write-Host "Build failed with exit code: $LASTEXITCODE" -ForegroundColor Red
    exit $LASTEXITCODE
}

$sizeBefore = (Get-Item cli-proxy.exe).Length / 1MB

Write-Host "Compressing with UPX..." -ForegroundColor Yellow
upx --best --lzma cli-proxy.exe

if ($LASTEXITCODE -eq 0) {
    $sizeAfter = (Get-Item cli-proxy.exe).Length / 1MB
    $saved = $sizeBefore - $sizeAfter
    Write-Host "Build successful: cli-proxy.exe" -ForegroundColor Green
    Write-Host "  Before: $([math]::Round($sizeBefore, 2)) MB" -ForegroundColor Gray
    Write-Host "  After:  $([math]::Round($sizeAfter, 2)) MB (saved $([math]::Round($saved, 2)) MB)" -ForegroundColor Gray
} else {
    $sizeRounded = [math]::Round($sizeBefore, 2)
    Write-Host "UPX compression failed, but executable is ready: cli-proxy.exe ($sizeRounded MB)" -ForegroundColor Yellow
}
