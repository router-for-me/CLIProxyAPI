// Package management provides the management API handlers and middleware
// for configuring the server and managing auth files.
package management

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/buildinfo"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/configstore"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/errormessages"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/events"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/pluginstore"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/policy"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/pricingsource"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store/runtimeconfig"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/upstreamsync"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	"golang.org/x/crypto/bcrypt"
)

type attemptInfo struct {
	count        int
	blockedUntil time.Time
	lastActivity time.Time // track last activity for cleanup
}

// attemptCleanupInterval controls how often stale IP entries are purged
const attemptCleanupInterval = 1 * time.Hour

// attemptMaxIdleTime controls how long an IP can be idle before cleanup
const attemptMaxIdleTime = 2 * time.Hour

// Handler aggregates config reference, persistence path and helpers.
type Handler struct {
	cfg                     *config.Config
	configFilePath          string
	mu                      sync.Mutex
	reloadMu                sync.Mutex
	reloadGeneration        uint64
	appliedReloadGeneration uint64
	attemptsMu              sync.Mutex
	failedAttempts          map[string]*attemptInfo // keyed by client IP
	authManager             *coreauth.Manager
	tokenStore              coreauth.Store
	localPassword           string
	allowRemoteOverride     bool
	envSecret               string
	logDir                  string
	postAuthHook            coreauth.PostAuthHook
	postAuthPersistHook     coreauth.PostAuthHook
	pluginHost              *pluginhost.Host
	configReloadHook        func(context.Context, *config.Config)
	pluginStoreRegistryURL  string
	pluginStoreHTTPClient   pluginstore.HTTPDoer
	pluginReleaseCacheMu    sync.Mutex
	pluginReleaseCache      map[string]pluginReleaseCacheEntry

	// PG-backed persistence handles. Empty when the PGSTORE_DSN backend is
	// not configured; the management routes for these resources return 503
	// in that case so callers can detect the absence cleanly.
	pgAPIKeys *store.APIKeyStore
	pgUsage   *store.UsageStore
	// pgFlusher is the asynchronous PG usage flusher. The alerts sweep reads its
	// cumulative Drops() count to flag backpressure (full flush queue). nil when
	// PG is not configured.
	pgFlusher *store.UsageFlusher
	pgModels  *store.ModelsStore
	pgUsers   *store.UserStore
	pgSync    *registry.PGSync
	policySvc policy.PolicyService
	// modelsCatalogResolver resolves an internal provider key (e.g. "claude",
	// "openai-compatible-opencode") to the official_provider label persisted in
	// the models catalog (e.g. "anthropic", "opencode"). Used to surface
	// "Provider Official" on Usage Stats / Errors rows. nil when no PG backend
	// is configured — FillOfficialProvider then no-ops and rows keep an empty
	// official_provider, which the dashboard falls back to the raw provider.
	modelsCatalogResolver *store.ModelsCatalogResolverImpl

	// pgMgmtTokens stores management API tokens that gate access to the
	// /v0/management REST surface (per-token policy + audit log). nil when PG
	// is not configured — the /api-tokens routes return 503 in that case.
	pgMgmtTokens *store.ManagementTokenStore

	// pgUpstreamProviders stores the normalized upstream_providers rows that
	// are the source of truth for both the config.yaml-based API-key providers
	// and the OAuth/file-backed auths. nil when PG is not configured — the
	// /upstream-providers routes return 503 in that case.
	pgUpstreamProviders store.UpstreamProviderStore

	// pgProxyPools stores the named egress-proxy pools backing the
	// /v0/management/proxy-pools routes. Also feeds the render-time pool
	// resolution in applyUpstreamProviders. nil when PG is not configured.
	pgProxyPools store.ProxyPoolStore

	// pgSyncLog stores the upstream OAuth/auth token refresh outcomes recorded
	// by the auth manager's RefreshSink. nil when PG is not configured — the
	// /upstream-sync-log routes return 503 in that case.
	pgSyncLog *store.SyncLogStore

	// pgModelGroups stores reusable Model Group templates (allowed-models
	// grant lists + per-model upstream routing) attachable to API-key policies
	// and internal users. nil when PG is not configured — the /model-groups
	// routes return 503 in that case.
	pgModelGroups *store.ModelGroupStore

	// pgAutoRouters stores Auto Router definitions (the router entity that
	// scores requests and forwards them to a tier-appropriate model). nil when
	// PG is not configured — the /auto-routers routes return 503 in that case.
	pgAutoRouters *store.AutoRouterStore

	// pgAutoRouterProfiles stores the per-router scoring profile (thresholds,
	// weights, keyword rules) plus the active version. nil when PG is not
	// configured — the /auto-routers/:id/profile and /decisions endpoints
	// return 503 in that case.
	pgAutoRouterProfiles *store.AutoRouterProfileStore

	// pgModelHealth stores the model health-check snapshots + history +
	// operator settings (Analysis → Model Health page + the public
	// /v0/model-health/uptime endpoint). nil when PG is not configured — the
	// /model-health routes return 503 in that case (the public uptime
	// endpoint degrades gracefully to an empty response instead).
	pgModelHealth *store.ModelHealthStore

	// pgAlerts stores the notification feed produced by the alert detectors
	// (Analysis → Alerts). nil when PG is not configured — the /alerts routes
	// return 503 in that case.
	pgAlerts           *store.AlertStore
	pgFlusherLastDrops int64

	// pgJev stores the singleton Jev AI classifier configuration (master
	// toggle + sealed API key + pinned model + API root). nil when PG is not
	// configured — the /jev/settings routes return 503 in that case.
	pgJev jevSettingsStore

	// jevRotator receives the classifier endpoint settings whenever an operator
	// saves them, so the live client picks them up without a restart. nil
	// disables live rotation, in which case a change applies from the next
	// server start.
	jevRotator JevConfigRotator

	// v1ModelsHandler is the http.Handler that serves GET /v1/models. It is
	// wired by api.Server after route setup so the management handler can
	// trigger an in-process sync into models_catalog without a network
	// round-trip. nil when no PG backend is configured (sync endpoint then
	// returns 503 from requirePG before reaching this handler).
	v1ModelsHandler http.Handler

	// modelsSyncMeta tracks the outcome of the most recent /v1/models →
	// models_catalog sync, surfaced via the GET
	// /v0/management/models-catalog/sync-status endpoint so the dashboard can
	// show "last synced N ago · key sk-abcd…" and actionable error hints.
	// Guarded by modelsSyncMu (RLock for reads, Lock for writes).
	modelsSyncMu            sync.RWMutex
	modelsSyncAt            time.Time
	modelsSyncCount         int
	modelsSyncErr           string
	modelsSyncErrType       string
	modelsSyncCallerKeyPref string

	// pgErrorMessages stores operator-customized error response text,
	// keyed by HTTP status code. nil when PG is not configured — handlers
	// under error_messages.go return 503 in that case.
	pgErrorMessages errormessages.Store

	// pgPricingSources stores operator-managed external pricing catalogs
	// (LiteLLM JSON URLs / uploaded files). nil when PG is not configured —
	// handlers under pricing_sources.go return 503 in that case.
	pgPricingSources store.PricingSourceStore
	// pgPricingSourcesDir is the local directory backing uploaded catalog
	// files for source_type=file pricing sources.
	pgPricingSourcesDir string

	// pgBackup is the underlying Postgres store used for the /export and
	// /import routes (dump/restore of the PG tables). nil when PG is not
	// configured — those routes return 503.
	pgBackup *store.PostgresStore

	// backupS3Runner / backupS3Restorer wire the full-backup-to-S3 subsystem
	// into the /backup management routes. Both are nil when BACKUP_S3_ENDPOINT
	// is not configured — those routes return 503. *backup.Runner satisfies
	// both interfaces (RestoreFromS3 lives on the Runner, delegating bundle
	// application to its attached *Restorer).
	backupS3Runner   backupRunner
	backupS3Restorer backupRestorer
	backupS3Settings backupSettings

	// litellmUsers / litellmKeys are the Manage-LiteLLM stores backed by the
	// dedicated litellm_internal_users / litellm_api_keys / litellm_key_policies
	// tables. nil when PG is not configured — the /v0/management/litellm/*
	// routes return 503 in that case.
	litellmUsers *store.LiteLLMUserStore
	litellmKeys  *store.LiteLLMKeyStore

	// litellmSync is the Manage-LiteLLM external-sync settings store (base URL +
	// sealed master API key + last-sync outcome). nil when PG is not configured
	// — the /litellm/settings and /litellm/sync/run routes return 503.
	litellmSync *store.LiteLLMSyncStore

	// pgControl is the raw PostgresStore handle used by the runtime-config
	// management routes (GET/POST /runtime-config, /config-revisions,
	// /config-imports). nil when PG is not configured — those routes return
	// 503 in that case.
	pgControl *store.PostgresStore

	// configRepo is the configstore.Repository facade over pgControl. It is
	// derived from pgControl by SetPGControl so every management handler
	// (not just the runtime-config routes) can commit a snapshot under
	// expected_revision when PG is the source of truth.
	configRepo configstore.Repository

	// reloadCoordinator, when non-nil, re-renders the ephemeral bridge file
	// after a successful runtime-config commit. nil when the server booted
	// in legacy file mode (no bridge is active, so nothing to reload).
	reloadCoordinator ReloadLatestFn

	// quotaRepoIface / quotaRepoEnabled gate the round-2 quota-share endpoints
	// (/v0/management/auths/:id/quota, /v0/management/pools/:key/quota). The
	// repo interface is satisfied by the production *store.PostgresStore in
	// cmd/server wiring and by an in-process fake in tests. nil/disabled
	// returns 503 from those handlers, mirroring the rest of the PG-first
	// management-route contract.
	quotaRepoIface   quotaRepo
	quotaRepoEnabled bool

	// eventsRingRecorder / eventsEnabled gate the round-2 live-events
	// endpoints (/v0/management/events, /v0/management/events/stats).
	// eventsRingRecorder is the same *events.Ring that events.SetGlobal
	// exposes to runtime emission sites (one ring backs both — see
	// SetEventsRing). eventsEnabled flips to true when SetEventsRing is
	// called with a non-nil ring (production path) or via the test-only
	// setEventsEnabledForTest seam. nil/disabled returns 503 from the
	// handlers, mirroring the rest of the management-route gate so the
	// dashboard can rely on "no PG → no /v0/management/* data routes".
	eventsRingRecorder *events.Ring
	eventsEnabled      bool
}

type configReloadSnapshot struct {
	cfg        *config.Config
	generation uint64
}

// NewHandler creates a new management handler instance.
func NewHandler(cfg *config.Config, configFilePath string, manager *coreauth.Manager) *Handler {
	// MANAGEMENT_PASSWORD is the canonical env var for the management API
	// secret. NIXLLM_DASHBOARD_PASSWORD is an optional alias surfaced for
	// operators who want the dashboard to use a dedicated secret that does
	// not unlock the broader management API (the dashboard UI scopes itself
	// to PG-backed routes only). When both are set, MANAGEMENT_PASSWORD
	// wins so existing deployments are unaffected.
	envSecret, _ := os.LookupEnv("MANAGEMENT_PASSWORD")
	envSecret = strings.TrimSpace(envSecret)
	if envSecret == "" {
		if dashSecret, _ := os.LookupEnv("NIXLLM_DASHBOARD_PASSWORD"); strings.TrimSpace(dashSecret) != "" {
			envSecret = strings.TrimSpace(dashSecret)
		}
	}

	h := &Handler{
		cfg:                 cfg,
		configFilePath:      configFilePath,
		failedAttempts:      make(map[string]*attemptInfo),
		authManager:         manager,
		tokenStore:          sdkAuth.GetTokenStore(),
		allowRemoteOverride: envSecret != "",
		envSecret:           envSecret,
	}
	h.startAttemptCleanup()
	StartMgmtAuditSweep()
	return h
}

// startAttemptCleanup launches a background goroutine that periodically
// removes stale IP entries from failedAttempts to prevent memory leaks.
func (h *Handler) startAttemptCleanup() {
	go func() {
		ticker := time.NewTicker(attemptCleanupInterval)
		defer ticker.Stop()
		for range ticker.C {
			h.purgeStaleAttempts()
		}
	}()
}

// purgeStaleAttempts removes IP entries that have been idle beyond attemptMaxIdleTime
// and whose ban (if any) has expired.
func (h *Handler) purgeStaleAttempts() {
	now := time.Now()
	h.attemptsMu.Lock()
	defer h.attemptsMu.Unlock()
	for ip, ai := range h.failedAttempts {
		// Skip if still banned
		if !ai.blockedUntil.IsZero() && now.Before(ai.blockedUntil) {
			continue
		}
		// Remove if idle too long
		if now.Sub(ai.lastActivity) > attemptMaxIdleTime {
			delete(h.failedAttempts, ip)
		}
	}
}

// NewHandler creates a new management handler instance.
func NewHandlerWithoutConfigFilePath(cfg *config.Config, manager *coreauth.Manager) *Handler {
	return NewHandler(cfg, "", manager)
}

// SetPostgresStores wires the optional PG-backed stores and services. All
// parameters may be nil when the PGSTORE_DSN backend is inactive; the
// management routes for PG resources will return 503 in that case.
func (h *Handler) SetPostgresStores(
	apiKeys *store.APIKeyStore,
	usage *store.UsageStore,
	models *store.ModelsStore,
	pgSync *registry.PGSync,
	policySvc policy.PolicyService,
) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pgAPIKeys = apiKeys
	h.pgUsage = usage
	h.pgModels = models
	h.pgSync = pgSync
	h.policySvc = policySvc
}

// SetUserStore wires the optional PG-backed internal users store. When nil,
// the /v0/management/internal-users routes return 503. User-level budget/RPM
// enforcement depends on this store AND on the policy service having a
// UserStore attached (see policy.Service.SetUserStore).
func (h *Handler) SetUserStore(users *store.UserStore) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pgUsers = users
}

// SetLiteLLMStores wires the optional PG-backed Manage-LiteLLM stores (the
// dedicated litellm_internal_users / litellm_api_keys / litellm_key_policies
// tables). When nil, the /v0/management/litellm/* routes return 503. The
// Manage-LiteLLM stores are management-only: the runtime request path never
// reads them, so no policy-cache invalidation is needed on mutations.
func (h *Handler) SetLiteLLMStores(users *store.LiteLLMUserStore, keys *store.LiteLLMKeyStore) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.litellmUsers = users
	h.litellmKeys = keys
}

// SetLiteLLMSyncStore wires the PG-backed Manage-LiteLLM external-sync settings
// store. When nil, the /v0/management/litellm/settings and /litellm/sync/run
// routes return 503 and the background auto-sync sweep is a no-op.
func (h *Handler) SetLiteLLMSyncStore(s *store.LiteLLMSyncStore) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.litellmSync = s
}

// SetV1ModelsHandler wires the http.Handler that serves GET /v1/models on
// the main API server. The management handler uses it to run in-process sync
// probes against the live caller-facing model list (rather than issuing a
// network loopback call). Set to nil to disable the /models-catalog/sync
// route.
func (h *Handler) SetV1ModelsHandler(handler http.Handler) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.v1ModelsHandler = handler
}

// SetManagementTokenStore wires the optional PG-backed management API token
// store (tokens + policy + audit log). When nil, the /v0/management/api-tokens
// routes return 503 and token-based authentication is disabled (callers fall
// back to the static management secret / local password).
func (h *Handler) SetManagementTokenStore(tokens *store.ManagementTokenStore) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pgMgmtTokens = tokens
}

// SetUpstreamProvidersStore wires the PG-backed store for the normalized
// upstream_providers table. When nil, the /v0/management/upstream-providers
// routes return 503.
func (h *Handler) SetUpstreamProvidersStore(upstream store.UpstreamProviderStore) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pgUpstreamProviders = upstream
}

// SetProxyPoolStore wires the PG-backed store for the proxy_pools table.
// When nil, the /v0/management/proxy-pools routes return 503 and the
// upstream-provider render skips pool resolution.
func (h *Handler) SetProxyPoolStore(pools store.ProxyPoolStore) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pgProxyPools = pools
}

// SetSyncLogStore wires the PG-backed store for the upstream_sync_log table
// (OAuth/auth token refresh outcomes). When nil, the
// /v0/management/upstream-sync-log routes return 503. Launching the 30-day
// retention sweep is idempotent; it is started here (once, at wiring time) so
// the goroutine only runs on PG-configured deployments.
func (h *Handler) SetSyncLogStore(s *store.SyncLogStore) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.pgSyncLog = s
	h.mu.Unlock()
	store.StartSyncLogSweep(s) // nil-safe
}

// SyncLogSink returns an auth.RefreshSink that persists refresh outcomes to the
// upstream_sync_log table. Returns nil when the PG-backed sync log store is not
// configured, so the auth manager's SetRefreshSink is a no-op (refreshes run
// uninstrumented). The sink itself inserts via a fire-and-forget goroutine so it
// never blocks the refresh pipeline.
func (h *Handler) SyncLogSink() coreauth.RefreshSink {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	syncStore := h.pgSyncLog
	h.mu.Unlock()
	if syncStore == nil {
		return nil
	}
	return func(ctx context.Context, o coreauth.RefreshOutcome) {
		// Use a bounded-time context so a slow DB never pins a goroutine.
		insertCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := syncStore.InsertSyncLog(insertCtx, store.SyncLogEvent{
			AuthID:       o.AuthID,
			Provider:     o.Provider,
			Trigger:      o.Trigger,
			Success:      o.Success,
			ErrorMessage: o.Error,
			DurationMs:   o.Duration.Milliseconds(),
			OccurredAt:   o.OccurredAt,
		}); err != nil {
			log.WithError(err).WithField("auth_id", o.AuthID).Debug("upstream-sync-log: insert refresh outcome failed")
		}
	}
}

// SetModelGroupStore wires the PG-backed store for reusable Model Group
// templates (allowed-models grant lists + per-model upstream routing). When
// nil, the /v0/management/model-groups routes return 503. The policy service
// must be wired separately (see policy.Service.SetModelGroupStore) so the
// enforcement path can resolve attached groups into API-key snapshots.
func (h *Handler) SetModelGroupStore(groups *store.ModelGroupStore) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pgModelGroups = groups
}

// SetAutoRouterStore wires the PG-backed store for Auto Router definitions
// (the router entity that scores requests and forwards them to a
// tier-appropriate model). When nil, the /v0/management/auto-routers routes
// return 503.
func (h *Handler) SetAutoRouterStore(routers *store.AutoRouterStore) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pgAutoRouters = routers
}

// SetAutoRouterProfileStore wires the PG-backed store for Auto Router scoring
// profiles (thresholds, weights, keyword rules). When nil, the
// /v0/management/auto-routers/:id/profile endpoint returns 503.
func (h *Handler) SetAutoRouterProfileStore(profiles *store.AutoRouterProfileStore) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pgAutoRouterProfiles = profiles
}

// SetModelHealthStore wires the PG-backed store for model health-check
// snapshots, history, and operator settings (Analysis → Model Health page +
// the public /v0/model-health/uptime endpoint). When nil, the
// /v0/management/model-health routes return 503 and the public uptime
// endpoint degrades to an empty response. The probe sweep is started
// separately via StartModelHealthSweep (called from server.go wiring) so it
// only runs on PG-configured deployments; retention sweep for the history
// table is launched here since it has no Handler dependency.
func (h *Handler) SetModelHealthStore(s *store.ModelHealthStore) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.pgModelHealth = s
	h.mu.Unlock()
	store.StartModelHealthRetentionSweep(s) // nil-safe
}

// SetAlertsStore wires the PG-backed store for the alerts feed + settings
// (Analysis → Alerts). When nil, the /v0/management/alerts routes return 503.
// The retention sweep for the feed is launched here (no Handler dependency);
// the detection sweep is started separately via StartAlertSweep so it only
// runs on PG-configured deployments after the auth manager is attached.
func (h *Handler) SetAlertsStore(s *store.AlertStore) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.pgAlerts = s
	h.mu.Unlock()
	store.StartAlertRetentionSweep(s) // nil-safe
}

// SetJevStore wires the PG-backed store for the Jev AI classifier settings
// (master toggle + sealed API key + pinned model). When nil, the
// /v0/management/jev/settings routes return 503.
func (h *Handler) SetJevStore(s *store.JevStore) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.pgJev = s
	h.mu.Unlock()
}

// JevConfigRotator receives the classifier endpoint settings whenever an
// operator changes them, so a running server can adopt them without a restart.
// The implementation is the classifier client; keeping it an interface avoids
// an api → autorouter dependency in the management package.
//
// Both methods are called on every settings write, including a write that
// changed neither — an empty key (or an empty base URL, which resets to the
// public endpoint) is a legitimate value here, so there is no "unchanged"
// sentinel to skip on.
type JevConfigRotator interface {
	SetAPIKey(key string)
	SetBaseURL(baseURL string)
}

// SetJevConfigRotator wires the sink for classifier endpoint changes. Called
// during server construction, after the gate's client exists.
func (h *Handler) SetJevConfigRotator(r JevConfigRotator) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.jevRotator = r
	h.mu.Unlock()
}

// SetUsageFlusher wires the PG usage flusher so the alerts sweep can flag drops
// (full flush queue = backpressure). nil when PG is not configured.
func (h *Handler) SetUsageFlusher(f *store.UsageFlusher) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.pgFlusher = f
	h.mu.Unlock()
}

// SetModelsCatalogResolver wires the resolver that translates an internal
// provider key into the official_provider label persisted in the models
// catalog. nil (file-only deployments) disables the best-effort
// FillOfficialProvider pass on Usage Stats / Errors rows; the dashboard then
// falls back to the raw provider value.
func (h *Handler) SetModelsCatalogResolver(r *store.ModelsCatalogResolverImpl) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.modelsCatalogResolver = r
}

// SetErrorMessagesStore wires the PG-backed store for operator-customized
// error responses. nil disables the /v0/management/error-messages routes
// (they return 503 in that case).
func (h *Handler) SetErrorMessagesStore(store errormessages.Store) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pgErrorMessages = store
	// Mirror into the registry so Respond() can consult the same store.
	errormessages.SetStore(store)
}

// SetPricingSourcesStore wires the PG-backed store for operator-managed
// external pricing catalogs plus the directory backing uploaded catalog
// files. nil disables the /v0/management/pricing-sources routes (they
// return 503 in that case).
func (h *Handler) SetPricingSourcesStore(store store.PricingSourceStore, uploadDir string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pgPricingSources = store
	h.pgPricingSourcesDir = uploadDir
	// Bootstrap the pricingsource registry from the persisted rows so
	// MatchAll can surface external suggestions without waiting for the
	// operator to click "Refresh" after a server restart.
	if store != nil {
		rows, err := store.List(context.Background())
		if err == nil {
			list := make([]pricingsource.ExternalSource, 0, len(rows))
			for _, r := range rows {
				list = append(list, toExternalSource(r))
			}
			pricingsource.SetExternalSources(list)
		}
	}
}

// SetBackupStore wires the underlying Postgres store used by the /export and
// /import routes. nil when PG is not configured — those routes return 503.
func (h *Handler) SetBackupStore(pg *store.PostgresStore) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pgBackup = pg
}

// SetBackupS3 wires the full-backup-to-S3 subsystem into the /backup routes.
// runner/restorer are nil when S3 backup is not configured — those routes
// return 503 via backupNotConfigured. interval/retention are surfaced by the
// GET /backup/settings endpoint for operator status display.
func (h *Handler) SetBackupS3(runner backupRunner, restorer backupRestorer, interval time.Duration, retention int) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.backupS3Runner = runner
	h.backupS3Restorer = restorer
	h.backupS3Settings.S3Configured = runner != nil
	h.backupS3Settings.Interval = interval.String()
	h.backupS3Settings.Retention = retention
}

func (h *Handler) backupRunner() backupRunner {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.backupS3Runner
}

func (h *Handler) backupRestorer() backupRestorer {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.backupS3Restorer
}

func (h *Handler) backupSettings() backupSettings {
	if h == nil {
		return backupSettings{}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.backupS3Settings
}

// SetConfig updates the in-memory config reference when the server hot-reloads.
func (h *Handler) SetConfig(cfg *config.Config) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.cfg = cfg
	h.mu.Unlock()
}

// SetAuthManager updates the auth manager reference used by management endpoints.
func (h *Handler) SetAuthManager(manager *coreauth.Manager) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.authManager = manager
	h.mu.Unlock()
}

// SetPluginHost updates the plugin host used by plugin-backed management endpoints.
func (h *Handler) SetPluginHost(host *pluginhost.Host) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.pluginHost = host
	h.mu.Unlock()
}

// SetConfigReloadHook updates the callback used after management saves config changes.
func (h *Handler) SetConfigReloadHook(hook func(context.Context, *config.Config)) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.configReloadHook = hook
	h.mu.Unlock()
}

// reloadSnapshotConfigLocked clones the runtime config and assigns a reload generation.
// Callers must hold h.mu.
func (h *Handler) reloadSnapshotConfigLocked() configReloadSnapshot {
	if h == nil || h.cfg == nil {
		return configReloadSnapshot{}
	}
	h.reloadGeneration++
	return configReloadSnapshot{
		cfg:        h.cfg.CloneForRuntime(),
		generation: h.reloadGeneration,
	}
}

// saveConfigAndSnapshotLocked saves h.cfg and returns a full runtime config snapshot.
// Callers must hold h.mu.
func (h *Handler) saveConfigAndSnapshotLocked(c *gin.Context) (configReloadSnapshot, bool) {
	if h.configRepo != nil {
		if errSave := h.saveViaRepositoryLocked(c); errSave != nil {
			return configReloadSnapshot{}, false
		}
		return h.reloadSnapshotConfigLocked(), true
	}
	if errSave := config.SaveConfigPreserveComments(h.configFilePath, h.cfg); errSave != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to save config: %v", errSave)})
		return configReloadSnapshot{}, false
	}
	return h.reloadSnapshotConfigLocked(), true
}

// saveViaRepositoryLocked commits h.cfg through the configstore.Repository
// facade so PG-backed deployments stop writing the legacy config.yaml.
// Callers must hold h.mu.
func (h *Handler) saveViaRepositoryLocked(c *gin.Context) error {
	snap, err := snapshotFromInMemoryCfg(h.cfg)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("snapshot build failed: %v", err)})
		return err
	}
	// Read the active revision under a brief lock release; using the
	// repository's optimistic-concurrency contract (expected=0 + read-back
	// conflict) is overkill for a save helper, so we use expected=0 (the
	// bootstrap path only succeeds when no row exists, which will fail
	// fast with a "revision already exists" error the first time). For
	// production we read the active revision and pass it as the expected
	// revision so concurrent saves still surface a 409.
	active, errRead := h.activeRevisionForRepoLocked()
	if errRead != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("read active revision: %v", errRead)})
		return errRead
	}
	actor := strings.TrimSpace(c.GetHeader("X-Management-User"))
	if actor == "" {
		actor = "dashboard"
	}
	audit := configstore.SaveAudit{
		Actor:  actor,
		Reason: "management save via " + safePath(c.Request.URL.Path),
		Source: "dashboard",
	}
	if _, err := h.configRepo.Save(context.Background(), active, &snap, audit); err != nil {
		var conflict *configstore.RevisionConflictError
		if errorsAs(err, &conflict) {
			c.JSON(http.StatusConflict, gin.H{
				"error":           "revision_conflict",
				"expected":        active,
				"active_revision": conflict.Current,
			})
			return err
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("save failed: %v", err)})
		return err
	}
	return nil
}

// activeRevisionForRepoLocked reads the active revision number via the
// runtime-config store handle so callers can pass it as expected_revision
// when committing through configRepo. Callers must hold h.mu.
func (h *Handler) activeRevisionForRepoLocked() (int64, error) {
	if h.pgControl == nil {
		return 0, fmt.Errorf("management: pgControl not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rc := runtimeconfig.New(h.pgControl)
	return rc.ActiveRevision(ctx)
}

// safePath returns the request path with a fallback so a nil request does
// not panic during a save helper that runs before the HTTP context is
// guaranteed live.
func safePath(p string) string {
	return strings.TrimSpace(p)
}

// reloadConfigAfterManagementSave reloads from an independent config snapshot.
// Callers must pass a full Config clone captured immediately after a successful save.
func (h *Handler) reloadConfigAfterManagementSave(ctx context.Context, snapshot configReloadSnapshot) {
	if h == nil || snapshot.cfg == nil || snapshot.generation == 0 {
		return
	}
	h.reloadMu.Lock()
	defer h.reloadMu.Unlock()

	h.mu.Lock()
	if snapshot.generation < h.appliedReloadGeneration {
		h.mu.Unlock()
		return
	}
	hook := h.configReloadHook
	host := h.pluginHost
	h.mu.Unlock()
	if hook != nil {
		hook(ctx, snapshot.cfg)
	} else if host != nil {
		host.ApplyConfig(ctx, snapshot.cfg)
	}

	h.mu.Lock()
	if snapshot.generation > h.appliedReloadGeneration {
		h.appliedReloadGeneration = snapshot.generation
	}
	h.mu.Unlock()
}

// reloadConfigAfterManagementSaveAsync reloads from an independent config snapshot.
// Callers must pass a full Config clone captured immediately after a successful save.
func (h *Handler) reloadConfigAfterManagementSaveAsync(ctx context.Context, snapshot configReloadSnapshot) {
	if h == nil || snapshot.cfg == nil || snapshot.generation == 0 {
		return
	}
	reloadCtx := context.Background()
	if ctx != nil {
		reloadCtx = context.WithoutCancel(ctx)
	}
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				log.WithField("panic", recovered).Error("management: async config reload panicked")
			}
		}()
		h.reloadConfigAfterManagementSave(reloadCtx, snapshot)
	}()
}

// SetLocalPassword configures the runtime-local password accepted for localhost requests.
func (h *Handler) SetLocalPassword(password string) { h.localPassword = password }

// SetLogDirectory updates the directory where main.log should be looked up.
func (h *Handler) SetLogDirectory(dir string) {
	if dir == "" {
		return
	}
	if !filepath.IsAbs(dir) {
		if abs, err := filepath.Abs(dir); err == nil {
			dir = abs
		}
	}
	h.logDir = dir
}

// SetPostAuthHook registers a hook to be called after auth record creation but before persistence.
func (h *Handler) SetPostAuthHook(hook coreauth.PostAuthHook) {
	h.postAuthHook = hook
}

// SetPostAuthPersistHook registers a hook to be called after auth persistence.
func (h *Handler) SetPostAuthPersistHook(hook coreauth.PostAuthHook) {
	h.postAuthPersistHook = hook
}

// Middleware enforces access control for management endpoints.
// All requests (local and remote) require a valid management key.
// Additionally, remote access requires allow-remote-management=true.
func (h *Handler) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("X-CPA-VERSION", buildinfo.Version)
		c.Header("X-CPA-COMMIT", buildinfo.Commit)
		c.Header("X-CPA-BUILD-DATE", buildinfo.BuildDate)
		c.Header("X-CPA-SUPPORT-PLUGIN", pluginhost.SupportPluginHeaderValue())

		clientIP := c.ClientIP()
		localClient := clientIP == "127.0.0.1" || clientIP == "::1"

		// Accept either Authorization: Bearer <key> or X-Management-Key
		var provided string
		if ah := c.GetHeader("Authorization"); ah != "" {
			parts := strings.SplitN(ah, " ", 2)
			if len(parts) == 2 && strings.ToLower(parts[0]) == "bearer" {
				provided = parts[1]
			} else {
				provided = ah
			}
		}
		if provided == "" {
			provided = c.GetHeader("X-Management-Key")
		}

		allowed, statusCode, errMsg := h.AuthenticateManagementKey(clientIP, localClient, provided)
		if !allowed {
			// Static-secret auth failed. Before rejecting, attempt to
			// authenticate the credential as a management API token
			// (per-token policy + audit log). A resolved token is stashed
			// in the context so the EnforceTokenPolicy + AuditTokenCall
			// middlewares can act on it. The IP-ban logic above already ran
			// for the static-secret miss; token failures are not added to
			// the ban counter to avoid locking out legit token users.
			if provided != "" {
				if tok, pol := h.authenticateManagementToken(c.Request.Context(), provided); tok != nil {
					// Token auth succeeded. Stash the token + policy and
					// proceed; the per-token enforcement middleware will
					// gate scope/endpoints/rate downstream.
					c.Set(ctxMgmtToken, tok)
					c.Set(ctxMgmtPolicy, pol)
					c.Set(ctxMgmtProvided, provided)
					c.Next()
					return
				}
			}
			c.AbortWithStatusJSON(statusCode, gin.H{"error": errMsg})
			return
		}
		c.Next()
	}
}

// AuthenticateManagementKey verifies the provided management key for the given client.
// It mirrors the behaviour of Middleware() so non-HTTP callers can reuse the same logic.
func (h *Handler) AuthenticateManagementKey(clientIP string, localClient bool, provided string) (bool, int, string) {
	const maxFailures = 5
	const banDuration = 30 * time.Minute

	if h == nil {
		return false, http.StatusForbidden, "remote management disabled"
	}

	cfg := h.cfg
	var (
		allowRemote bool
		secretHash  string
	)
	if cfg != nil {
		allowRemote = cfg.RemoteManagement.AllowRemote
		secretHash = cfg.RemoteManagement.SecretKey
	}
	if h.allowRemoteOverride {
		allowRemote = true
	}
	envSecret := h.envSecret

	now := time.Now()
	h.attemptsMu.Lock()
	ai := h.failedAttempts[clientIP]
	if ai != nil && !ai.blockedUntil.IsZero() {
		if now.Before(ai.blockedUntil) {
			remaining := ai.blockedUntil.Sub(now).Round(time.Second)
			h.attemptsMu.Unlock()
			return false, http.StatusForbidden, fmt.Sprintf("IP banned due to too many failed attempts. Try again in %s", remaining)
		}
		// Ban expired, reset state
		ai.blockedUntil = time.Time{}
		ai.count = 0
	}
	h.attemptsMu.Unlock()

	if !localClient && !allowRemote {
		return false, http.StatusForbidden, "remote management disabled"
	}

	fail := func() {
		h.attemptsMu.Lock()
		aip := h.failedAttempts[clientIP]
		if aip == nil {
			aip = &attemptInfo{}
			h.failedAttempts[clientIP] = aip
		}
		aip.count++
		aip.lastActivity = time.Now()
		if aip.count >= maxFailures {
			aip.blockedUntil = time.Now().Add(banDuration)
			aip.count = 0
		}
		h.attemptsMu.Unlock()
	}

	reset := func() {
		h.attemptsMu.Lock()
		if ai := h.failedAttempts[clientIP]; ai != nil {
			ai.count = 0
			ai.blockedUntil = time.Time{}
		}
		h.attemptsMu.Unlock()
	}

	if secretHash == "" && envSecret == "" {
		return false, http.StatusForbidden, "remote management key not set"
	}

	if provided == "" {
		fail()
		return false, http.StatusUnauthorized, "missing management key"
	}

	if localClient {
		if lp := h.localPassword; lp != "" {
			if subtle.ConstantTimeCompare([]byte(provided), []byte(lp)) == 1 {
				reset()
				return true, 0, ""
			}
		}
	}

	if envSecret != "" && subtle.ConstantTimeCompare([]byte(provided), []byte(envSecret)) == 1 {
		reset()
		return true, 0, ""
	}

	if secretHash == "" || bcrypt.CompareHashAndPassword([]byte(secretHash), []byte(provided)) != nil {
		fail()
		return false, http.StatusUnauthorized, "invalid management key"
	}

	reset()

	return true, 0, ""
}

// persist saves the current in-memory config to disk.
func (h *Handler) persist(c *gin.Context) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.persistLocked(c)
}

// persistLocked saves the current in-memory config to disk.
// It expects the caller to hold h.mu.
func (h *Handler) persistLocked(c *gin.Context) bool {
	if h.configRepo != nil {
		if errSave := h.saveViaRepositoryLocked(c); errSave != nil {
			return false
		}
	} else {
		if err := config.SaveConfigPreserveComments(h.configFilePath, h.cfg); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to save config: %v", err)})
			return false
		}
	}
	snapshot := h.reloadSnapshotConfigLocked()
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
	var reqCtx context.Context
	if c != nil && c.Request != nil {
		reqCtx = c.Request.Context()
	}
	h.reloadConfigAfterManagementSaveAsync(reqCtx, snapshot)
	return true
}

// applyUpstreamProviders re-renders the config.yaml provider sections and
// auth-dir JSON files from the upstream_providers table, then triggers the
// same config-reload path used by the file-based management mutations so
// BuildAPIKeyClients + loadFileClients re-run with the new state. It is the
// bridge that makes the normalized table the source of truth while reusing
// the existing routing pipeline. Caller must NOT hold h.mu.
func (h *Handler) applyUpstreamProviders(ctx context.Context) {
	if h == nil || h.pgUpstreamProviders == nil {
		return
	}
	h.mu.Lock()
	cfg := h.cfg
	configPath := h.configFilePath
	authDir := ""
	poolSource := h.pgProxyPools
	if cfg != nil {
		authDir = cfg.AuthDir
	}
	h.mu.Unlock()
	if cfg == nil {
		return
	}
	merged, err := upstreamsync.ApplyArtifacts(ctx, h.pgUpstreamProviders, cfg, configPath, authDir, poolSource)
	if err != nil {
		log.WithError(err).Warn("management: apply upstream providers failed")
		return
	}
	// Swap the rendered provider lists into the live config and snapshot for
	// the reload hook.
	h.mu.Lock()
	if h.cfg != nil {
		h.cfg.GeminiKey = merged.GeminiKey
		h.cfg.InteractionsKey = merged.InteractionsKey
		h.cfg.CodexKey = merged.CodexKey
		h.cfg.XAIKey = merged.XAIKey
		h.cfg.ClaudeKey = merged.ClaudeKey
		h.cfg.OpenAICompatibility = merged.OpenAICompatibility
		h.cfg.OpenCodeGo = merged.OpenCodeGo
		h.cfg.VertexCompatAPIKey = merged.VertexCompatAPIKey
		h.cfg.NeuralwattKey = merged.NeuralwattKey
	}
	snapshot := h.reloadSnapshotConfigLocked()
	h.mu.Unlock()
	h.reloadConfigAfterManagementSaveAsync(ctx, snapshot)
}

// Helper methods for simple types
func (h *Handler) updateBoolField(c *gin.Context, set func(bool)) {
	var body struct {
		Value *bool `json:"value"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Value == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	set(*body.Value)
	h.persist(c)
}

func (h *Handler) updateIntField(c *gin.Context, set func(int)) {
	var body struct {
		Value *int `json:"value"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Value == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	set(*body.Value)
	h.persist(c)
}

func (h *Handler) updateStringField(c *gin.Context, set func(string)) {
	var body struct {
		Value *string `json:"value"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Value == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	set(*body.Value)
	h.persist(c)
}
