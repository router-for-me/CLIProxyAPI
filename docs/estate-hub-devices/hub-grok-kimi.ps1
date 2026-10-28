# Point Grok CLI and Kimi Code CLI at the estate hub (m6). Idempotent. Windows twin of hub-grok-kimi.py.
# Key source: ~\.claude\settings.json env.ANTHROPIC_AUTH_TOKEN. Never prints the key.
$ErrorActionPreference = 'Stop'
$url = 'https://waseems-mac-mini-m6.taild6c90e.ts.net:8317/v1'
$key = (Get-Content -Raw "$env:USERPROFILE\.claude\settings.json" | ConvertFrom-Json).env.ANTHROPIC_AUTH_TOKEN
if (-not $key) { throw 'no hub key in Claude settings' }
$utf8 = New-Object System.Text.UTF8Encoding($false)
function Read-Text($p) { if (Test-Path $p) { [IO.File]::ReadAllText($p) } else { '' } }
function Write-Text($p, $t) { New-Item -ItemType Directory -Force (Split-Path $p) | Out-Null; [IO.File]::WriteAllText($p, $t.Replace("`r`n", "`n"), $utf8) }

$g = "$env:USERPROFILE\.grok\config.toml"
$text = Read-Text $g
if ($text -match '\[model\.hub-grok-build\]') { 'grok: already configured' }
elseif ($text -match '(?m)^\[models\]') { throw 'grok: [models] table exists; merge by hand' }
else {
  # A signed-in Grok keeps its own default; the hub models are added beside it.
  $b = if (Test-Path "$env:USERPROFILE\.grok\auth.json") { '' } else { "`n[models]`ndefault = `"hub-grok-build`"`n" }
  foreach ($m in @(@('hub-grok-build','grok-build-0.1','Grok Build 0.1 (hub)'), @('hub-grok-4-7','grok-4.7','Grok 4.7 (hub)'))) {
    $b += "`n[model.$($m[0])]`nmodel = `"$($m[1])`"`nbase_url = `"$url`"`nname = `"$($m[2])`"`napi_key = `"$key`"`ncontext_window = 256000`n"
  }
  Write-Text $g ($text.TrimEnd() + "`n" + $b); 'grok: configured'
}

$k = "$env:USERPROFILE\.kimi-code\config.toml"
$text = Read-Text $k
if ($text -match '\[providers\.hub\]') { 'kimi: already configured' }
else {
  # An existing default_model (a signed-in Kimi) is kept.
  $hasDefault = $text -match '(?m)^default_model'
  $lines = @($text -split "`r?`n")
  $i = 0; while ($i -lt $lines.Count -and -not $lines[$i].StartsWith('[')) { $i++ }
  $head = (($lines[0..([Math]::Max($i - 1, 0))] | Where-Object { $i -gt 0 }) -join "`n").TrimEnd()
  $rest = if ($i -lt $lines.Count) { $lines[$i..($lines.Count - 1)] -join "`n" } else { '' }
  $t = "`n[providers.hub]`ntype = `"openai`"`nbase_url = `"$url`"`napi_key = `"$key`"`n"
  foreach ($m in @(@('hub/kimi-k3','kimi-k3','Kimi K3 (hub)'), @('hub/kimi-k3.1-pro','kimi-k3.1-pro','Kimi K3.1 Pro (hub)'))) {
    $t += "`n[models.`"$($m[0])`"]`nprovider = `"hub`"`nmodel = `"$($m[1])`"`ndisplay_name = `"$($m[2])`"`nmax_context_size = 256000`ncapabilities = [`"thinking`", `"tool_use`"]`n"
  }
  $out = $(if ($head) { "$head`n" } else { '' }) + $(if ($hasDefault) { '' } else { "default_model = `"hub/kimi-k3`"`n" }) + $rest + $t
  Write-Text $k $out; 'kimi: configured'
}
