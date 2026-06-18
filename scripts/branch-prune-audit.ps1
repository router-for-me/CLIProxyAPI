# Branch prune audit — cliproxyapi-plusplus
param(
    [string]$Repo = "KooshaPari/cliproxyapi-plusplus",
    [string]$Base = "main",
    [switch]$Delete
)

$ErrorActionPreference = "Stop"
$branches = gh api "repos/$Repo/branches" --paginate --jq '.[].name' | Where-Object { $_ -ne $Base }

$deleteCandidates = @()
$notFound = @()
foreach ($b in $branches) {
    $enc = [uri]::EscapeDataString($b)
    $json = gh api "repos/$Repo/compare/${Base}...$enc" --jq '{ahead:.ahead_by,behind:.behind_by,status:.status}' 2>$null
    if ($LASTEXITCODE -ne 0) {
        $notFound += $b
        Write-Host "404 $b"
        continue
    }
    $cmp = $json | ConvertFrom-Json
    $ahead = [int]$cmp.ahead
    if ($ahead -eq 0) {
        $deleteCandidates += $b
        Write-Host "DELETE $b (ahead=0 behind=$($cmp.behind))"
    } else {
        Write-Host "KEEP   $b (ahead=$ahead)"
    }
}

Write-Host "`nSummary: $($deleteCandidates.Count) delete candidates, $($notFound.Count) 404"

if ($Delete -and $deleteCandidates.Count -gt 0) {
    foreach ($b in $deleteCandidates) {
        gh api -X DELETE "repos/$Repo/git/refs/heads/$([uri]::EscapeDataString($b))"
        Write-Host "deleted $b"
    }
}
