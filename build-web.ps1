$ErrorActionPreference = "Stop"

Write-Host "Building Next.js frontend..."
Set-Location web
npm run build
if ($LASTEXITCODE -ne 0) {
    Write-Host "Frontend build failed!"
    exit 1
}
Set-Location ..

Write-Host "Copying static export to embed directory..."
$dest = "internal\managementasset\web_static"
if (Test-Path $dest) {
    Get-ChildItem $dest | Remove-Item -Recurse -Force
}
if (-not (Test-Path $dest)) {
    New-Item -ItemType Directory -Path $dest -Force | Out-Null
}
Copy-Item -Path "web\out\*" -Destination $dest -Recurse -Force

Write-Host "Done! Web assets embedded."
