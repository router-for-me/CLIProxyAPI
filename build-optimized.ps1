$VERSION = git describe --tags --always
$COMMIT = git rev-parse --short HEAD
$BUILDDATE = [DateTime]::UtcNow.ToString('yyyy-MM-ddTHH:mm:ssZ')

$env:CGO_ENABLED = 0
go build -trimpath -ldflags="-s -w -buildid= -X 'main.Version=$VERSION' -X 'main.Commit=$COMMIT' -X 'main.BuildDate=$BUILDDATE'" -o cli-proxy.exe ./cmd/server/

if ($LASTEXITCODE -eq 0) {
    $size = (Get-Item cli-proxy.exe).Length / 1MB
    Write-Host "Build successful: cli-proxy.exe ($( [math]::Round($size, 2) ) MB)" -ForegroundColor Green
} else {
    Write-Host "Build failed with exit code: $LASTEXITCODE" -ForegroundColor Red
    exit $LASTEXITCODE
}
