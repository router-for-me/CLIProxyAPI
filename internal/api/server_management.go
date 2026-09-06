package api

import (
	"context"
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/managementasset"
	log "github.com/sirupsen/logrus"
)

func (s *Server) registerManagementRoutes() {
	if s == nil || s.engine == nil || s.mgmt == nil {
		return
	}
	if !s.managementRoutesRegistered.CompareAndSwap(false, true) {
		return
	}

	log.Info("management routes registered after secret key configuration")

	s.engine.POST("/v0/management/oauth-callback", s.managementAvailabilityMiddleware(), s.mgmt.PostOAuthCallback)
	s.engine.GET("/v0/management/oauth-callback", s.managementAvailabilityMiddleware(), s.mgmt.GetOAuthCallback)

	mgmt := s.engine.Group("/v0/management")
	mgmt.Use(s.managementAvailabilityMiddleware(), s.mgmt.Middleware(), s.mgmt.EnforceTokenPolicy(), s.mgmt.AuditTokenCall())
	{
		mgmt.GET("/config", s.mgmt.GetConfig)
		mgmt.GET("/config.yaml", s.mgmt.GetConfigYAML)
		mgmt.PUT("/config.yaml", s.mgmt.PutConfigYAML)
		mgmt.GET("/latest-version", s.mgmt.GetLatestVersion)
		mgmt.GET("/plugins", s.mgmt.ListPlugins)
		mgmt.GET("/plugin-store", s.mgmt.ListPluginStore)
		mgmt.POST("/plugin-store/:id/install", s.mgmt.InstallPluginFromStore)
		mgmt.DELETE("/plugins/:id", s.mgmt.DeletePlugin)
		mgmt.PATCH("/plugins/:id/enabled", s.mgmt.PatchPluginEnabled)
		mgmt.GET("/plugins/:id/config", s.mgmt.GetPluginConfig)
		mgmt.PUT("/plugins/:id/config", s.mgmt.PutPluginConfig)
		mgmt.PATCH("/plugins/:id/config", s.mgmt.PatchPluginConfig)

		mgmt.GET("/debug", s.mgmt.GetDebug)
		mgmt.PUT("/debug", s.mgmt.PutDebug)
		mgmt.PATCH("/debug", s.mgmt.PutDebug)

		mgmt.GET("/logging-to-file", s.mgmt.GetLoggingToFile)
		mgmt.PUT("/logging-to-file", s.mgmt.PutLoggingToFile)
		mgmt.PATCH("/logging-to-file", s.mgmt.PutLoggingToFile)

		mgmt.GET("/logs-max-total-size-mb", s.mgmt.GetLogsMaxTotalSizeMB)
		mgmt.PUT("/logs-max-total-size-mb", s.mgmt.PutLogsMaxTotalSizeMB)
		mgmt.PATCH("/logs-max-total-size-mb", s.mgmt.PutLogsMaxTotalSizeMB)

		mgmt.GET("/error-logs-max-files", s.mgmt.GetErrorLogsMaxFiles)
		mgmt.PUT("/error-logs-max-files", s.mgmt.PutErrorLogsMaxFiles)
		mgmt.PATCH("/error-logs-max-files", s.mgmt.PutErrorLogsMaxFiles)

		mgmt.GET("/usage-statistics-enabled", s.mgmt.GetUsageStatisticsEnabled)
		mgmt.PUT("/usage-statistics-enabled", s.mgmt.PutUsageStatisticsEnabled)
		mgmt.PATCH("/usage-statistics-enabled", s.mgmt.PutUsageStatisticsEnabled)

		mgmt.GET("/proxy-url", s.mgmt.GetProxyURL)
		mgmt.PUT("/proxy-url", s.mgmt.PutProxyURL)
		mgmt.PATCH("/proxy-url", s.mgmt.PutProxyURL)
		mgmt.DELETE("/proxy-url", s.mgmt.DeleteProxyURL)

		mgmt.POST("/api-call", s.mgmt.APICall)

		mgmt.GET("/quota-exceeded/switch-project", s.mgmt.GetSwitchProject)
		mgmt.PUT("/quota-exceeded/switch-project", s.mgmt.PutSwitchProject)
		mgmt.PATCH("/quota-exceeded/switch-project", s.mgmt.PutSwitchProject)

		mgmt.GET("/quota-exceeded/switch-preview-model", s.mgmt.GetSwitchPreviewModel)
		mgmt.PUT("/quota-exceeded/switch-preview-model", s.mgmt.PutSwitchPreviewModel)
		mgmt.PATCH("/quota-exceeded/switch-preview-model", s.mgmt.PutSwitchPreviewModel)
		mgmt.POST("/reset-quota", s.mgmt.ResetQuota)

		mgmt.GET("/api-keys", s.mgmt.GetAPIKeys)
		mgmt.PUT("/api-keys", s.mgmt.PutAPIKeys)
		mgmt.PATCH("/api-keys", s.mgmt.PatchAPIKeys)
		mgmt.DELETE("/api-keys", s.mgmt.DeleteAPIKeys)
		mgmt.GET("/api-key-usage", s.mgmt.GetAPIKeyUsage)
		mgmt.GET("/usage-queue", s.mgmt.GetUsageQueue)

		mgmt.GET("/gemini-api-key", s.mgmt.GetGeminiKeys)
		mgmt.PUT("/gemini-api-key", s.mgmt.PutGeminiKeys)
		mgmt.PATCH("/gemini-api-key", s.mgmt.PatchGeminiKey)
		mgmt.DELETE("/gemini-api-key", s.mgmt.DeleteGeminiKey)

		mgmt.GET("/interactions-api-key", s.mgmt.GetInteractionsKeys)
		mgmt.PUT("/interactions-api-key", s.mgmt.PutInteractionsKeys)
		mgmt.PATCH("/interactions-api-key", s.mgmt.PatchInteractionsKey)
		mgmt.DELETE("/interactions-api-key", s.mgmt.DeleteInteractionsKey)

		mgmt.GET("/logs", s.mgmt.GetLogs)
		mgmt.DELETE("/logs", s.mgmt.DeleteLogs)
		mgmt.GET("/request-error-logs", s.mgmt.GetRequestErrorLogs)
		mgmt.GET("/request-error-logs/:name", s.mgmt.DownloadRequestErrorLog)
		mgmt.GET("/request-log-by-id/:id", s.mgmt.GetRequestLogByID)
		mgmt.GET("/request-log", s.mgmt.GetRequestLog)
		mgmt.PUT("/request-log", s.mgmt.PutRequestLog)
		mgmt.PATCH("/request-log", s.mgmt.PutRequestLog)
		mgmt.GET("/ws-auth", s.mgmt.GetWebsocketAuth)
		mgmt.PUT("/ws-auth", s.mgmt.PutWebsocketAuth)
		mgmt.PATCH("/ws-auth", s.mgmt.PutWebsocketAuth)

		mgmt.GET("/request-retry", s.mgmt.GetRequestRetry)
		mgmt.PUT("/request-retry", s.mgmt.PutRequestRetry)
		mgmt.PATCH("/request-retry", s.mgmt.PutRequestRetry)
		mgmt.GET("/max-retry-interval", s.mgmt.GetMaxRetryInterval)
		mgmt.PUT("/max-retry-interval", s.mgmt.PutMaxRetryInterval)
		mgmt.PATCH("/max-retry-interval", s.mgmt.PutMaxRetryInterval)

		mgmt.GET("/force-model-prefix", s.mgmt.GetForceModelPrefix)
		mgmt.PUT("/force-model-prefix", s.mgmt.PutForceModelPrefix)
		mgmt.PATCH("/force-model-prefix", s.mgmt.PutForceModelPrefix)

		mgmt.GET("/branding", s.mgmt.GetBranding)
		mgmt.PUT("/branding", s.mgmt.PutBranding)
		mgmt.PATCH("/branding", s.mgmt.PutBranding)

		mgmt.GET("/routing/strategy", s.mgmt.GetRoutingStrategy)
		mgmt.PUT("/routing/strategy", s.mgmt.PutRoutingStrategy)
		mgmt.PATCH("/routing/strategy", s.mgmt.PutRoutingStrategy)

		mgmt.GET("/claude-api-key", s.mgmt.GetClaudeKeys)
		mgmt.PUT("/claude-api-key", s.mgmt.PutClaudeKeys)
		mgmt.PATCH("/claude-api-key", s.mgmt.PatchClaudeKey)
		mgmt.DELETE("/claude-api-key", s.mgmt.DeleteClaudeKey)

		mgmt.GET("/codex-api-key", s.mgmt.GetCodexKeys)
		mgmt.PUT("/codex-api-key", s.mgmt.PutCodexKeys)
		mgmt.PATCH("/codex-api-key", s.mgmt.PatchCodexKey)
		mgmt.DELETE("/codex-api-key", s.mgmt.DeleteCodexKey)

		mgmt.GET("/xai-api-key", s.mgmt.GetXAIKeys)
		mgmt.PUT("/xai-api-key", s.mgmt.PutXAIKeys)
		mgmt.PATCH("/xai-api-key", s.mgmt.PatchXAIKey)
		mgmt.DELETE("/xai-api-key", s.mgmt.DeleteXAIKey)

		mgmt.GET("/openai-compatibility", s.mgmt.GetOpenAICompat)
		mgmt.PUT("/openai-compatibility", s.mgmt.PutOpenAICompat)
		mgmt.PATCH("/openai-compatibility", s.mgmt.PatchOpenAICompat)
		mgmt.DELETE("/openai-compatibility", s.mgmt.DeleteOpenAICompat)

		mgmt.GET("/vertex-api-key", s.mgmt.GetVertexCompatKeys)
		mgmt.PUT("/vertex-api-key", s.mgmt.PutVertexCompatKeys)
		mgmt.PATCH("/vertex-api-key", s.mgmt.PatchVertexCompatKey)
		mgmt.DELETE("/vertex-api-key", s.mgmt.DeleteVertexCompatKey)

		mgmt.GET("/oauth-excluded-models", s.mgmt.GetOAuthExcludedModels)
		mgmt.PUT("/oauth-excluded-models", s.mgmt.PutOAuthExcludedModels)
		mgmt.PATCH("/oauth-excluded-models", s.mgmt.PatchOAuthExcludedModels)
		mgmt.DELETE("/oauth-excluded-models", s.mgmt.DeleteOAuthExcludedModels)

		mgmt.GET("/oauth-model-alias", s.mgmt.GetOAuthModelAlias)
		mgmt.PUT("/oauth-model-alias", s.mgmt.PutOAuthModelAlias)
		mgmt.PATCH("/oauth-model-alias", s.mgmt.PatchOAuthModelAlias)
		mgmt.DELETE("/oauth-model-alias", s.mgmt.DeleteOAuthModelAlias)

		mgmt.GET("/auth-files", s.mgmt.ListAuthFiles)
		mgmt.GET("/auth-files/models", s.mgmt.GetAuthFileModels)
		mgmt.GET("/model-definitions/:channel", s.mgmt.GetStaticModelDefinitions)
		mgmt.GET("/auth-files/download", s.mgmt.DownloadAuthFile)
		mgmt.POST("/auth-files", s.mgmt.UploadAuthFile)
		mgmt.DELETE("/auth-files", s.mgmt.DeleteAuthFile)
		mgmt.PATCH("/auth-files/status", s.mgmt.PatchAuthFileStatus)
		mgmt.PATCH("/auth-files/fields", s.mgmt.PatchAuthFileFields)
		mgmt.POST("/vertex/import", s.mgmt.ImportVertexCredential)

		mgmt.GET("/anthropic-auth-url", s.mgmt.RequestAnthropicToken)
		mgmt.GET("/codex-auth-url", s.mgmt.RequestCodexToken)
		mgmt.GET("/antigravity-auth-url", s.mgmt.RequestAntigravityToken)
		mgmt.GET("/kimi-auth-url", s.mgmt.RequestKimiToken)
		mgmt.GET("/xai-auth-url", s.mgmt.RequestXAIToken)
		mgmt.GET("/get-auth-status", s.mgmt.GetAuthStatus)
		mgmt.DELETE("/oauth-session", s.mgmt.CancelAuthSession)

		// Server-side remote probe used by the dashboard's "Discover
		// models" section. Avoids CORS: the browser would otherwise
		// block cross-origin GET <upstream>/v1/models from the page.
		mgmt.POST("/remote-probe/models", s.mgmt.RemoteProbeModels)

		// PG-backed API keys + policy management. These routes return 503
		// when the PG store is not configured so callers can detect absence.
		mgmt.GET("/api-keys-pg", s.mgmt.ListPGAPIKeys)
		mgmt.POST("/api-keys-pg", s.mgmt.CreatePGAPIKey)
		mgmt.GET("/api-keys-pg/:id", s.mgmt.GetPGAPIKey)
		mgmt.PATCH("/api-keys-pg/:id", s.mgmt.PatchPGAPIKey)
		mgmt.PUT("/api-keys-pg/:id/policy", s.mgmt.PutPGAPIKeyPolicy)
		mgmt.POST("/api-keys-pg/:id/regenerate", s.mgmt.RegeneratePGAPIKey)
		mgmt.DELETE("/api-keys-pg/:id", s.mgmt.DeletePGAPIKey)
		// Import custom API-key secrets by matching key_alias (case-
		// insensitive; ambiguous aliases are skipped). Applies each secret
		// via Regenerate, preserving key ID/policy/metadata/owner. Registered
		// after the :id routes: Gin's httprouter gives static segments
		// precedence over :param regardless of order, but keeping collection-
		// level routes after the :id-suffixed ones matches convention here.
		mgmt.POST("/api-keys-pg/import", s.mgmt.ImportPGAPIKeys)

		// Internal Users (LiteLLM-style key owners with per-user
		// budget/RPM/TPM enforcement, max-p concurrent-request caps, and
		// benchmark dashboards). Return 503 when the PG store is not
		// configured. Path prefix /internal-users/* mirrors LiteLLM's
		// /user/* (kept for backward compatibility — see internal_users.go
		// file-level comment for the equivalence table).
		mgmt.GET("/internal-users", s.mgmt.ListInternalUsers)
		mgmt.POST("/internal-users", s.mgmt.CreateInternalUser)
		mgmt.GET("/internal-users/leaderboard", s.mgmt.GetInternalUsersLeaderboard)
		mgmt.POST("/internal-users/reconcile-all", s.mgmt.ReconcileAllSpend)
		mgmt.GET("/internal-users/:id", s.mgmt.GetInternalUser)
		mgmt.PATCH("/internal-users/:id", s.mgmt.PatchInternalUser)
		mgmt.DELETE("/internal-users/:id", s.mgmt.DeleteInternalUser)
		mgmt.POST("/internal-users/:id/reset-spend", s.mgmt.ResetInternalUserSpend)
		mgmt.POST("/internal-users/:id/reconcile-spend", s.mgmt.ReconcileInternalUserSpend)
		mgmt.GET("/internal-users/:id/keys", s.mgmt.ListInternalUserKeys)
		mgmt.POST("/internal-users/:id/keys/:keyId/attach", s.mgmt.AttachKeyToUser)
		mgmt.DELETE("/internal-users/:id/keys/:keyId", s.mgmt.DetachKeyFromUser)
		// Per-user benchmark endpoints (mirror /usage-stats but scoped to a user).
		mgmt.GET("/internal-users/:id/totals", s.mgmt.GetInternalUserTotals)
		mgmt.GET("/internal-users/:id/timeseries", s.mgmt.GetInternalUserTimeSeries)
		mgmt.GET("/internal-users/:id/top", s.mgmt.GetInternalUserTop)
		mgmt.GET("/internal-users/:id/events", s.mgmt.GetInternalUserEvents)
		mgmt.GET("/internal-users/:id/errors", s.mgmt.GetInternalUserErrors)
		mgmt.GET("/internal-users/:id/windows", s.mgmt.GetInternalUserWindows)
		mgmt.GET("/internal-users/:id/model-spend", s.mgmt.GetInternalUserModelSpend)
		mgmt.GET("/internal-users/:id/models", s.mgmt.GetInternalUserModels)

		// Manage LiteLLM — Internal Users and API Keys stored in the dedicated
		// litellm_* tables (complete LiteLLM-style data, management-only).
		// Return 503 when the PG store is not configured. These routes never
		// touch the runtime tables or the policy cache.
		mgmt.GET("/litellm/users", s.mgmt.ListLiteLLMUsers)
		mgmt.POST("/litellm/users", s.mgmt.CreateLiteLLMUser)
		mgmt.GET("/litellm/users/:id", s.mgmt.GetLiteLLMUser)
		mgmt.PATCH("/litellm/users/:id", s.mgmt.PatchLiteLLMUser)
		mgmt.DELETE("/litellm/users/:id", s.mgmt.DeleteLiteLLMUser)
		mgmt.POST("/litellm/users/:id/reset-spend", s.mgmt.ResetLiteLLMUserSpend)
		mgmt.GET("/litellm/users/:id/keys", s.mgmt.ListLiteLLMUserKeys)
		mgmt.GET("/litellm/keys", s.mgmt.ListLiteLLMKeys)
		mgmt.POST("/litellm/keys", s.mgmt.CreateLiteLLMKey)
		mgmt.GET("/litellm/keys/:id", s.mgmt.GetLiteLLMKey)
		mgmt.PATCH("/litellm/keys/:id", s.mgmt.PatchLiteLLMKey)
		mgmt.PUT("/litellm/keys/:id/policy", s.mgmt.PutLiteLLMKeyPolicy)
		mgmt.POST("/litellm/keys/:id/regenerate", s.mgmt.RegenerateLiteLLMKey)
		mgmt.DELETE("/litellm/keys/:id", s.mgmt.DeleteLiteLLMKey)
		// Manage LiteLLM — external sync settings + manual sync trigger.
		mgmt.GET("/litellm/settings", s.mgmt.GetLiteLLMSyncSettings)
		mgmt.PUT("/litellm/settings", s.mgmt.PutLiteLLMSyncSettings)
		mgmt.POST("/litellm/sync/run", s.mgmt.RunLiteLLMSync)
		// Manage LiteLLM — push Manage-LiteLLM Internal Users into the runtime
		// internal_users table ("sync to NixLLM"). Needs no external connection.
		mgmt.POST("/litellm/sync/nixllm", s.mgmt.RunLiteLLMSyncNixLLM)

		// Runtime-backed LiteLLM compat routes (wire-parity with LiteLLM's
		// OpenAPI). These are additive siblings of the Manage-LiteLLM routes
		// above: each is served by a *Compat handler that reads/writes the
		// runtime tables through requireLiteLLMRuntime, returning 503
		// pg_store_not_configured when the PG store is unwired. Existing routes
		// are left untouched.
		mgmt.POST("/litellm/user/new", s.mgmt.CreateLiteLLMUserCompat)
		mgmt.GET("/litellm/user/list", s.mgmt.ListLiteLLMUsersCompat)
		mgmt.GET("/litellm/user/info", s.mgmt.GetLiteLLMUserCompat)
		mgmt.POST("/litellm/user/update", s.mgmt.UpdateLiteLLMUserCompat)
		mgmt.POST("/litellm/user/delete", s.mgmt.DeleteLiteLLMUserCompat)
		mgmt.POST("/litellm/key/generate", s.mgmt.GenerateLiteLLMKeyCompat)
		mgmt.GET("/litellm/key/info", s.mgmt.GetLiteLLMKeyCompat)
		mgmt.GET("/litellm/key/list", s.mgmt.ListLiteLLMKeysCompat)
		mgmt.POST("/litellm/key/update", s.mgmt.UpdateLiteLLMKeyCompat)
		mgmt.POST("/litellm/key/regenerate", s.mgmt.RegenerateLiteLLMKeyCompat)
		mgmt.POST("/litellm/key/delete", s.mgmt.DeleteLiteLLMKeyCompat)
		mgmt.GET("/litellm/spend/logs", s.mgmt.ListLiteLLMSpendLogsCompat)
		mgmt.GET("/litellm/spend/users", s.mgmt.ListLiteLLMSpendUsersCompat)
		mgmt.GET("/litellm/global/spend", s.mgmt.GetLiteLLMGlobalSpendCompat)

		// Aggregate usage stats powered by the PG usage_events table.
		mgmt.GET("/usage-stats", s.mgmt.GetUsageStats)
		mgmt.GET("/usage-stats/summary", s.mgmt.GetUsageSummary)
		mgmt.GET("/usage-stats/totals", s.mgmt.GetUsageTotals)
		mgmt.GET("/usage-stats/timeseries", s.mgmt.GetUsageTimeSeries)
		mgmt.GET("/usage-stats/top", s.mgmt.GetUsageTop)
		mgmt.GET("/usage-stats/events", s.mgmt.GetUsageEvents)
		mgmt.GET("/usage-stats/events/:id", s.mgmt.GetUsageEvent)
		mgmt.GET("/usage-stats/errors", s.mgmt.GetUsageErrors)
		mgmt.GET("/usage-stats/errors/:id", s.mgmt.GetUsageError)
		mgmt.GET("/usage-stats/filters", s.mgmt.GetUsageFilters)
		mgmt.GET("/usage-windows/:api_key_id", s.mgmt.GetUsageWindows)

		// Live snapshot of upstream auth/model pairs currently in cooldown.
		// Read-only; the dashboard's per-row "Reset" button calls the
		// existing POST /v0/management/reset-quota route with auth_index.
		mgmt.GET("/cooldown-providers", s.mgmt.GetCooldownProviders)

		// Live view of the in-memory session→auth affinity cache. Read-only
		// snapshot plus a per-session revoke escape hatch for sessions pinned
		// to a stuck/erroring auth that the lazy failover has not yet
		// migrated. Sourced from the auth manager's active selector (per-
		// process, not persisted). Reports affinity_enabled=false when
		// routing.session-affinity is disabled.
		mgmt.GET("/session-affinity", s.mgmt.GetSessionAffinity)
		mgmt.POST("/session-affinity/revoke", s.mgmt.RevokeSessionAffinity)

		// Operator-customizable error response text, keyed by HTTP status code.
		// The preview route is POST because it accepts a candidate body to
		// render (GET would conflict with the "/:code" pattern and has no
		// query-string body semantics).
		mgmt.GET("/error-messages", s.mgmt.ListErrorMessages)
		mgmt.POST("/error-messages/preview", s.mgmt.PreviewErrorMessage)
		mgmt.GET("/error-messages/:code", s.mgmt.GetErrorMessage)
		mgmt.PUT("/error-messages/:code", s.mgmt.PutErrorMessage)
		mgmt.DELETE("/error-messages/:code", s.mgmt.DeleteErrorMessage)

		// PG-backed model catalog and pricing management.
		mgmt.GET("/models-catalog", s.mgmt.ListModelsCatalog)
		mgmt.GET("/models-catalog/count", s.mgmt.GetModelsCatalogCount)
		mgmt.GET("/models-catalog/summary", s.mgmt.GetModelsCatalogSummary)
		mgmt.GET("/models-catalog/distinct", s.mgmt.GetModelsCatalogDistinct)
		mgmt.GET("/models-catalog/providers-for-model", s.mgmt.GetModelProviders)
		mgmt.GET("/models-catalog/sync-status", s.mgmt.SyncStatus)
		mgmt.GET("/models-catalog/:id/pricing", s.mgmt.GetModelPricing)
		mgmt.PUT("/models-catalog/:id/pricing", s.mgmt.PutModelPricing)
		mgmt.POST("/models-catalog/sync-from-v1", s.mgmt.SyncModelsFromV1)
		mgmt.POST("/models-catalog/sync-pricing-preview", s.mgmt.SyncPricingPreview)
		mgmt.POST("/models-catalog/sync-pricing-apply", s.mgmt.SyncPricingApply)
		mgmt.GET("/models-catalog/entry/:id/:provider", s.mgmt.GetModelEntry)
		mgmt.PUT("/models-catalog/entry/:id/:provider", s.mgmt.PutModelEntry)
		mgmt.DELETE("/models-catalog/entry/:id/:provider", s.mgmt.DeleteModelEntry)
		// Global Model management: one operator edit fans out to every
		// catalog row sharing the same model id (across providers), plus the
		// (already global) pricing row keyed by model id.
		mgmt.GET("/models-catalog/global/:id", s.mgmt.GetGlobalModel)
		mgmt.PUT("/models-catalog/global/:id", s.mgmt.PutGlobalModel)

		// Operator-managed external pricing catalogs (LiteLLM-format JSON
		// URLs / uploaded files). Surface as suggestions in the dashboard's
		// Sync Pricing modal alongside the bundled catalog.
		mgmt.GET("/pricing-sources", s.mgmt.ListPricingSources)
		mgmt.POST("/pricing-sources", s.mgmt.CreatePricingSource)
		mgmt.PUT("/pricing-sources/:id", s.mgmt.UpdatePricingSource)
		mgmt.DELETE("/pricing-sources/:id", s.mgmt.DeletePricingSource)
		mgmt.POST("/pricing-sources/refresh-all", s.mgmt.RefreshAllPricingSources)
		mgmt.POST("/pricing-sources/:id/refresh", s.mgmt.RefreshPricingSource)
		mgmt.POST("/pricing-sources/:id/upload", s.mgmt.UploadPricingSourceFile)

		// Normalized upstream providers (source of truth for both the
		// config.yaml-based API-key providers and the OAuth/file-backed auths).
		// Returns 503 when the PG store is not configured. Every mutation
		// re-renders config.yaml + auth-dir artifacts from the table and
		// triggers a client reload.
		mgmt.GET("/upstream-providers", s.mgmt.ListUpstreamProviders)
		mgmt.POST("/upstream-providers", s.mgmt.CreateUpstreamProvider)
		mgmt.GET("/upstream-providers/:id", s.mgmt.GetUpstreamProvider)
		mgmt.PUT("/upstream-providers/:id", s.mgmt.UpdateUpstreamProvider)
		mgmt.DELETE("/upstream-providers/:id", s.mgmt.DeleteUpstreamProvider)

		// Named egress-proxy pools (9router-derived Proxy Pools workflow).
		// Returns 503 when the PG store is not configured. Binding lives on
		// upstream_providers(.proxy_pool_id) rows/entries; mutations here
		// re-render upstream artifacts so bindings resolve immediately.
		mgmt.GET("/proxy-pools", s.mgmt.ListProxyPools)
		mgmt.POST("/proxy-pools", s.mgmt.CreateProxyPool)
		mgmt.GET("/proxy-pools/:id", s.mgmt.GetProxyPool)
		mgmt.PUT("/proxy-pools/:id", s.mgmt.UpdateProxyPool)
		mgmt.DELETE("/proxy-pools/:id", s.mgmt.DeleteProxyPool)
		mgmt.POST("/proxy-pools/:id/test", s.mgmt.TestProxyPool)
		mgmt.POST("/proxy-pools/batch-import", s.mgmt.BatchImportProxyPools)
		mgmt.POST("/proxy-pools/relay-deploy", s.mgmt.DeployRelayProxyPool)

		// Upstream OAuth/auth token refresh outcomes recorded by the auth
		// manager's RefreshSink into the upstream_sync_log table. Surfaced on
		// the dashboard under Analysis → Upstream Providers. Returns 503 when
		// the PG store is not configured.
		mgmt.GET("/upstream-sync-log", s.mgmt.ListSyncLog)
		mgmt.GET("/upstream-sync-log/providers", s.mgmt.ListSyncLogProviders)
		mgmt.GET("/upstream-sync-log/:id", s.mgmt.GetSyncLog)
		mgmt.DELETE("/upstream-sync-log", s.mgmt.ClearSyncLog)

		// Model Health Check: periodically probes each live (Global) model id
		// with a tiny inference request and records status + response time +
		// tokens-per-second. Surfaced on the dashboard under Analysis → Model
		// Health; the public /v0/model-health/uptime view is mounted on the
		// root engine (server_routes.go) so it needs no management token.
		// Returns 503 when the PG store is not configured.
		mgmt.GET("/model-health", s.mgmt.GetModelHealth)
		mgmt.GET("/model-health/models", s.mgmt.ListModelHealthModels)
		mgmt.GET("/model-health/settings", s.mgmt.GetModelHealthSettings)
		mgmt.PUT("/model-health/settings", s.mgmt.PutModelHealthSettings)
		mgmt.PATCH("/model-health/settings", s.mgmt.PutModelHealthSettings)
		mgmt.POST("/model-health/run", s.mgmt.RunModelHealthCheckNow)
		// Probe a single model id synchronously (the per-row "Check now" action
		// on the Latest Status table). Ignores the excluded_models set so an
		// operator can re-check a model the scheduled sweep skips.
		mgmt.POST("/model-health/probe/:model", s.mgmt.RunModelHealthProbe)
		mgmt.GET("/model-health/log", s.mgmt.ListModelHealthLog)
		mgmt.DELETE("/model-health/log", s.mgmt.ClearModelHealthLog)
		mgmt.GET("/model-health/log/:id", s.mgmt.GetModelHealthLogEntry)

		// Alerts / Notifications feed (Analysis → Alerts). Records conditions
		// detected by the background sweep (max-spend, error-rate, provider
		// cooldown, model-health). Returns 503 when the PG store is not
		// configured.
		mgmt.GET("/alerts", s.mgmt.ListAlerts)
		mgmt.GET("/alerts/active", s.mgmt.ListActiveAlerts)
		mgmt.GET("/alerts/unread-count", s.mgmt.GetUnreadAlertCount)
		mgmt.POST("/alerts/read-all", s.mgmt.MarkAllAlertsRead)
		mgmt.POST("/alerts/:id/read", s.mgmt.MarkAlertRead)
		mgmt.POST("/alerts/:id/dismiss", s.mgmt.DismissAlert)
		mgmt.DELETE("/alerts", s.mgmt.ClearAlerts)
		mgmt.GET("/alerts/settings", s.mgmt.GetAlertSettings)
		mgmt.PUT("/alerts/settings", s.mgmt.PutAlertSettings)
		mgmt.PATCH("/alerts/settings", s.mgmt.PutAlertSettings)

		// Reusable Model Group templates (allowed-models grant lists +
		// per-model upstream routing) attachable to API-key policies and
		// internal users. Return 503 when the PG store is not configured.
		// When attached, the group becomes the source of truth for the
		// entity's allowed/blocked lists (and, for API-key policies, routes).
		mgmt.GET("/model-groups", s.mgmt.ListModelGroups)
		mgmt.POST("/model-groups", s.mgmt.CreateModelGroup)
		mgmt.GET("/model-groups/:id", s.mgmt.GetModelGroup)
		mgmt.PUT("/model-groups/:id", s.mgmt.UpdateModelGroup)
		mgmt.DELETE("/model-groups/:id", s.mgmt.DeleteModelGroup)
		mgmt.POST("/model-groups/:id/attach", s.mgmt.AttachModelGroup)
		mgmt.POST("/model-groups/:id/detach", s.mgmt.DetachModelGroup)

		// Auto Routers: the router entity (a kind of global model) that scores
		// requests across complexity dimensions and forwards them to a
		// tier-appropriate upstream model. Multiple routers with distinct
		// names/model ids are supported. Return 503 when the PG store is not
		// configured.
		mgmt.GET("/auto-routers", s.mgmt.ListAutoRouters)
		mgmt.POST("/auto-routers", s.mgmt.CreateAutoRouter)
		mgmt.GET("/auto-routers/stats", s.mgmt.GetAutoRouterStats)
		mgmt.GET("/auto-routers/:id", s.mgmt.GetAutoRouter)
		mgmt.PUT("/auto-routers/:id", s.mgmt.UpdateAutoRouter)
		mgmt.DELETE("/auto-routers/:id", s.mgmt.DeleteAutoRouter)
		// Sub-routes for the per-router scoring profile (GET/PUT) and the
		// persisted decision explainability feed (GET). PG-only, return 503
		// when not configured, mirroring the parent router endpoints.
		mgmt.GET("/auto-routers/:id/profile", s.mgmt.GetAutoRouterProfile)
		mgmt.PUT("/auto-routers/:id/profile", s.mgmt.UpdateAutoRouterProfile)
		mgmt.GET("/auto-routers/:id/decisions", s.mgmt.ListAutoRouterDecisions)

		// PG-backed management API tokens: gate access to the /v0/management
		// REST surface with per-token policy (read/write scope, per-endpoint
		// allowlist, RPM + max-parallel limits, expiry) and a full audit log
		// of every call made with a token (request/response bodies, status,
		// latency, errors). Return 503 when the PG store is not configured.
		mgmt.GET("/api-tokens", s.mgmt.ListAPITokens)
		mgmt.POST("/api-tokens", s.mgmt.CreateAPIToken)
		mgmt.GET("/api-tokens/audit-log", s.mgmt.ListAPITokenAuditLog)
		mgmt.GET("/api-tokens/audit-log/:id", s.mgmt.GetAPITokenAuditEntry)
		mgmt.GET("/api-tokens/:id", s.mgmt.GetAPIToken)
		mgmt.PATCH("/api-tokens/:id", s.mgmt.PatchAPIToken)
		mgmt.PUT("/api-tokens/:id/policy", s.mgmt.PutAPITokenPolicy)
		mgmt.POST("/api-tokens/:id/regenerate", s.mgmt.RegenerateAPIToken)
		mgmt.DELETE("/api-tokens/:id", s.mgmt.DeleteAPIToken)

		// Export / import of the PG-backed data. GET /export dumps the
		// requested resources (default all) as a portable JSON bundle; POST
		// /import restores such a bundle, wiping and replacing the selected
		// resources within a single transaction (destructive for those
		// categories). Return 503 when the PG store is not configured.
		mgmt.GET("/export/resources", s.mgmt.ListBackupResources)
		mgmt.GET("/export", s.mgmt.ExportAllData)
		mgmt.POST("/import", s.mgmt.ImportAllData)

		// Full-backup-to-S3 subsystem. GET /backup lists available snapshots,
		// POST /backup creates one, POST /backup/restore applies a snapshot,
		// and GET /backup/settings reports the current configuration. Return
		// 503 when BACKUP_S3_ENDPOINT is not configured.
		mgmt.GET("/backup", s.mgmt.ListBackups)
		mgmt.POST("/backup", s.mgmt.CreateBackup)
		mgmt.POST("/backup/restore", s.mgmt.RestoreBackup)
		mgmt.GET("/backup/settings", s.mgmt.BackupSettings)
	}
}

func (s *Server) managementAvailabilityMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !s.managementAvailable(c) {
			return
		}
		c.Next()
	}
}

func (s *Server) managementAvailable(c *gin.Context) bool {
	if s == nil || s.cfg == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return false
	}
	if s.cfg.Home.Enabled {
		c.AbortWithStatus(http.StatusNotFound)
		return false
	}
	if !s.managementRoutesEnabled.Load() {
		c.AbortWithStatus(http.StatusNotFound)
		return false
	}
	return true
}

func (s *Server) refreshPluginManagementRoutes() {
	if s == nil || s.pluginHost == nil || s.engine == nil {
		return
	}
	s.pluginHost.RegisterManagementRoutes(context.Background(), s.registeredManagementRouteKeys())
}

// RefreshPluginManagementRoutes rebuilds plugin-owned Management API routes.
func (s *Server) RefreshPluginManagementRoutes() {
	s.refreshPluginManagementRoutes()
}

func (s *Server) registeredManagementRouteKeys() map[string]struct{} {
	out := make(map[string]struct{})
	if s == nil || s.engine == nil {
		return out
	}
	for _, route := range s.engine.Routes() {
		if strings.HasPrefix(route.Path, "/v0/management/") || route.Path == "/v0/management" {
			out[strings.ToUpper(strings.TrimSpace(route.Method))+" "+route.Path] = struct{}{}
		}
	}
	return out
}

func (s *Server) pluginManagementNoRoute(c *gin.Context) {
	if s == nil || c == nil || c.Request == nil || c.Request.URL == nil {
		if c != nil {
			c.AbortWithStatus(http.StatusNotFound)
		}
		return
	}
	path := c.Request.URL.Path
	if strings.HasPrefix(path, "/v0/resource/plugins/") {
		s.pluginResourceNoRoute(c)
		return
	}
	if path != "/v0/management" && !strings.HasPrefix(path, "/v0/management/") {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if s.pluginHost == nil || s.mgmt == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if !s.managementAvailable(c) {
		return
	}
	s.mgmt.Middleware()(c)
	if c.IsAborted() {
		return
	}
	if s.mgmt.ServePluginAuthURL(c) {
		c.Abort()
		return
	}
	if s.pluginHost.ServeManagementHTTP(c.Writer, c.Request) {
		c.Abort()
		return
	}
	c.AbortWithStatus(http.StatusNotFound)
}

func (s *Server) pluginResourceNoRoute(c *gin.Context) {
	if s == nil || c == nil || c.Request == nil || c.Request.URL == nil {
		if c != nil {
			c.AbortWithStatus(http.StatusNotFound)
		}
		return
	}
	if s.cfg == nil || s.cfg.Home.Enabled || s.pluginHost == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if s.pluginHost.ServeResourceHTTP(c.Writer, c.Request) {
		c.Abort()
		return
	}
	c.AbortWithStatus(http.StatusNotFound)
}

func (s *Server) serveManagementControlPanel(c *gin.Context) {
	cfg := s.cfg
	if cfg == nil || cfg.Home.Enabled || cfg.RemoteManagement.DisableControlPanel {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	filePath := managementasset.FilePath(s.configFilePath)
	if strings.TrimSpace(filePath) == "" {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	if _, err := os.Stat(filePath); err != nil {
		if os.IsNotExist(err) {
			// Synchronously ensure management.html is available with a detached context.
			// Control panel bootstrap should not be canceled by client disconnects.
			if !managementasset.EnsureLatestManagementHTML(context.Background(), managementasset.StaticDir(s.configFilePath), cfg.ProxyURL, cfg.RemoteManagement.PanelGitHubRepository) {
				c.AbortWithStatus(http.StatusNotFound)
				return
			}
		} else {
			log.WithError(err).Error("failed to stat management control panel asset")
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
	}

	c.File(filePath)
}
