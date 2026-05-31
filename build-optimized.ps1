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
    Write-Host "UPX compression failed, but executable is ready: cli-proxy.exe ($( [math]::Round($sizeBefore, 2) ) MB)" -ForegroundColor Yellow
}
