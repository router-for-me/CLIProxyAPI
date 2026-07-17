param(
    [Parameter(Mandatory = $true)][string]$SourceRoot,
    [Parameter(Mandatory = $true)][string]$TargetRoot
)

$ErrorActionPreference = "Stop"

function Get-NormalizedText {
    param([string]$Path)
    return [System.IO.File]::ReadAllText($Path).Replace("`r`n", "`n")
}

function Set-NormalizedText {
    param([string]$Path, [string]$Text)
    [System.IO.File]::WriteAllText($Path, $Text, [System.Text.UTF8Encoding]::new($false))
}

function Replace-Once {
    param([string]$RelativePath, [string]$Old, [string]$New)
    $path = Join-Path $TargetRoot $RelativePath
    $text = Get-NormalizedText -Path $path
    $first = $text.IndexOf($Old, [System.StringComparison]::Ordinal)
    $last = $text.LastIndexOf($Old, [System.StringComparison]::Ordinal)
    if ($first -lt 0) { throw "Customization anchor was not found in '$RelativePath'. Upstream changed this integration point." }
    if ($first -ne $last) { throw "Customization anchor matched more than once in '$RelativePath'." }
    Set-NormalizedText -Path $path -Text ($text.Substring(0, $first) + $New + $text.Substring($first + $Old.Length))
}

function Insert-After-Once {
    param([string]$RelativePath, [string]$Anchor, [string]$Content)
    Replace-Once -RelativePath $RelativePath -Old $Anchor -New ($Anchor + $Content)
}

function Copy-CustomFile {
    param([string]$RelativePath)
    $source = Join-Path $SourceRoot $RelativePath
    $target = Join-Path $TargetRoot $RelativePath
    New-Item -ItemType Directory -Force -Path (Split-Path -Parent $target) | Out-Null
    Copy-Item -LiteralPath $source -Destination $target -Force
}

Replace-Once -RelativePath ".gitignore" -Old "temp/*`n" -New "temp/*`ndata/*`n"
Insert-After-Once -RelativePath "internal/managementasset/updater.go" -Anchor "func StartAutoUpdater(ctx context.Context, configFilePath string) {`n" -Content "`t_ = ctx`n`t_ = configFilePath`n`tlog.Debug(`"management asset remote updates disabled; using local management.html`")`n`treturn`n"
Insert-After-Once -RelativePath "internal/managementasset/updater.go" -Anchor "func EnsureLatestManagementHTML(ctx context.Context, staticDir string, proxyURL string, panelRepository string) bool {`n" -Content "`t_ = ctx`n`t_ = proxyURL`n`t_ = panelRepository`n`tstaticDir = strings.TrimSpace(staticDir)`n`tif staticDir == `"`" {`n`t`treturn false`n`t}`n`tif _, errLocal := os.Stat(filepath.Join(staticDir, managementAssetName)); errLocal != nil {`n`t`tlog.WithError(errLocal).Warn(`"local management.html is unavailable; remote download is disabled`")`n`t`treturn false`n`t}`n`treturn true`n"
Insert-After-Once -RelativePath "internal/misc/antigravity_version.go" -Anchor "func StartAntigravityVersionUpdater(ctx context.Context) {`n" -Content "`t_ = ctx`n`tlog.Debug(`"antigravity version remote refresh disabled; using embedded fallback`")`n`treturn`n"
Insert-After-Once -RelativePath "internal/registry/model_updater.go" -Anchor "func StartModelsUpdater(ctx context.Context) {`n" -Content "`t_ = ctx`n`tlog.Debug(`"remote models.json refresh disabled; using embedded catalog`")`n`treturn`n"
Insert-After-Once -RelativePath "internal/registry/codex_client_models_updater.go" -Anchor "func StartCodexClientModelsUpdater(ctx context.Context) {`n" -Content "`t_ = ctx`n`tlog.Debug(`"remote Codex client model refresh disabled; using embedded catalog`")`n`treturn`n"
Insert-After-Once -RelativePath "internal/api/handlers/management/config_basic.go" -Anchor "func (h *Handler) GetLatestVersion(c *gin.Context) {`n" -Content "`tc.JSON(http.StatusServiceUnavailable, gin.H{`n`t`t`"error`": `"remote_version_check_disabled`",`n`t`t`"message`": `"remote version checks are disabled in this fork`",`n`t})`n`treturn`n"
Insert-After-Once -RelativePath "internal/api/handlers/management/plugin_store.go" -Anchor "func (h *Handler) latestPluginVersions(ctx context.Context, client pluginstore.Client, plugins []pluginstore.Plugin) []string {`n" -Content "`t_ = ctx`n`t_ = client`n`treturn make([]string, len(plugins))`n"
Replace-Once -RelativePath "internal/api/handlers/management/plugin_store_test.go" -Old "func TestListPluginStoreShowsLatestReleaseVersionAndCaches(t *testing.T) {" -New "func TestListPluginStoreSkipsLatestReleaseVersionLookup(t *testing.T) {"
Replace-Once -RelativePath "internal/api/handlers/management/plugin_store_test.go" -Old "if body.Plugins[0].Version != `"0.2.0`" {`n`t`t`tt.Fatalf(`"version = %q, want 0.2.0 from latest release tag`", body.Plugins[0].Version)`n`t`t}" -New "if body.Plugins[0].Version != `"0.1.0`" {`n`t`t`tt.Fatalf(`"version = %q, want registry version 0.1.0`", body.Plugins[0].Version)`n`t`t}"
Replace-Once -RelativePath "internal/api/handlers/management/plugin_store_test.go" -Old "if releaseCalls != 1 {`n`t`tt.Fatalf(`"latest release fetched %d times, want 1 (cached)`", releaseCalls)`n`t}" -New "if releaseCalls != 0 {`n`t`tt.Fatalf(`"latest release fetched %d times, want 0`", releaseCalls)`n`t}"
Replace-Once -RelativePath "internal/api/server.go" -Old "`tc.File(filePath)`n" -New "`tdata, errRead := os.ReadFile(filePath)`n`tif errRead != nil {`n`t`tlog.WithError(errRead).Error(`"failed to read management control panel asset`")`n`t`tc.AbortWithStatus(http.StatusInternalServerError)`n`t`treturn`n`t}`n`tc.Header(`"Cache-Control`", `"no-store`")`n`tc.Data(http.StatusOK, `"text/html; charset=utf-8`", managementasset.SanitizeManagementHTML(data))`n"
Replace-Once -RelativePath "internal/pluginhost/support_nocgo.go" -Old "//go:build !cgo`n" -New "//go:build !cgo && !windows`n"
Replace-Once -RelativePath "internal/runtime/executor/helps/usage_helpers.go" -Old "func resolveUsageSource(auth *cliproxyauth.Auth, ctxAPIKey string) string {" -New "func legacyResolveUsageSource(auth *cliproxyauth.Auth, ctxAPIKey string) string {"
Insert-After-Once -RelativePath "internal/translator/openai/openai/responses/openai_openai-responses_response.go" -Anchor "`t`tst.CompletedEmitted = false`n" -Content "`t`tresponseModel := strings.TrimSpace(gjson.GetBytes(requestForNamespace, `"model`").String())`n`t`tif responseModel == `"`" {`n`t`t`tresponseModel = strings.TrimSpace(root.Get(`"model`").String())`n`t`t}`n`t`tif responseModel == `"`" {`n`t`t`tresponseModel = strings.TrimSpace(modelName)`n`t`t}`n"
Replace-Once -RelativePath "internal/translator/openai/openai/responses/openai_openai-responses_response.go" -Old '{"type":"response.in_progress","sequence_number":0,"response":{"id":"","object":"response","created_at":0,"status":"in_progress"}}' -New '{"type":"response.in_progress","sequence_number":0,"response":{"id":"","object":"response","created_at":0,"status":"in_progress","background":false,"error":null,"output":[]}}'
Insert-After-Once -RelativePath "internal/translator/openai/openai/responses/openai_openai-responses_response.go" -Anchor "`t`tcreated, _ = sjson.SetBytes(created, `"response.created_at`", st.Created)`n" -Content "`t`tif responseModel != `"`" {`n`t`t`tcreated, _ = sjson.SetBytes(created, `"response.model`", responseModel)`n`t`t}`n"
Insert-After-Once -RelativePath "internal/translator/openai/openai/responses/openai_openai-responses_response.go" -Anchor "`t`tinprog, _ = sjson.SetBytes(inprog, `"response.created_at`", st.Created)`n" -Content "`t`tif responseModel != `"`" {`n`t`t`tinprog, _ = sjson.SetBytes(inprog, `"response.model`", responseModel)`n`t`t}`n"

foreach ($file in @(
    "internal/managementasset/sanitize.go",
    "internal/managementasset/sanitize_custom_test.go",
    "internal/pluginhost/support_windows_nocgo.go",
    "internal/pluginhost/support_test.go",
    "internal/runtime/executor/helps/usage_source_custom.go",
    "internal/runtime/executor/helps/usage_source_custom_test.go",
    "internal/translator/openai/openai/responses/openai_openai-responses_lifecycle_model_test.go",
    "apply-customizations.ps1",
    "update-custom.ps1"
)) {
    Copy-CustomFile -RelativePath $file
}

foreach ($batchFile in Get-ChildItem -LiteralPath $SourceRoot -Filter "*.bat" -File) {
    Copy-Item -LiteralPath $batchFile.FullName -Destination (Join-Path $TargetRoot $batchFile.Name) -Force
}

Write-Host "Customizations applied to $TargetRoot"
