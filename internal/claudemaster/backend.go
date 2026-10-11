package claudemaster

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	cliproxysession "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/session"
	"github.com/tidwall/sjson"
)

var backendRuntimeSequence atomic.Uint64

const backendAuthSelectionModel = "claude-master-auth-selection"

// BackendOptions selects one independently authenticated inference account.
// AuthDir must be a private directory containing exactly the named credential.
// Probe callers set Model. Native Claude launchers set UseRequestModel so the
// model carried by each Claude Code request is preserved.
type BackendOptions struct {
	AuthDir         string
	Provider        string
	AuthID          string
	Model           string
	UseRequestModel bool
	ModelMap        map[string]string
	Name            string // the profile's own name, for logs and metrics
}

// BackendCredential identifies one independently authenticated profile in an
// inference pool. Credential values remain in AuthDir.
type BackendCredential struct {
	AuthDir  string
	Provider string
	AuthID   string
	Name     string // the profile's own name, for logs and metrics
}

// BackendSeriesOptions groups Claude subscriptions for quota-aware selection.
// The native request chooses the model. Credentials remain ordered as the
// deterministic fallback when weekly usage is unavailable.
type BackendSeriesOptions struct {
	Credentials  []BackendCredential
	QuotaRequest ClaudeQuotaRequestFunc
	BackupAPIKey string
	ModelMap     map[string]string
	// SnapshotInterval is how often the quota snapshot is logged (default 5 minutes, negative: never).
	SnapshotInterval time.Duration
	// CheckBackupCredit turns on the periodic check that the API-key backup can pay (a token count with its key, at
	// start and every 5 minutes; backend_backup_health.go). Off, a backup refused for credit is still marked
	// unavailable by the passive check. The launcher turns it on; a backend built in a test makes no such call.
	CheckBackupCredit bool
}

type backendRoutingOptions struct {
	backupAPIKey      string
	modelMap          map[string]string
	snapshotInterval  time.Duration
	checkBackupCredit bool
}

// Backend embeds inference, credential refresh, and subscription usage polling. It has no listener,
// management API, config watcher, global token store, or update/download service.
// The caller must hold the profile's exclusive lock until Close completes.
// Stop accepting requests and join HTTP handlers before calling Close; the
// launcher does this by closing and joining its proxy first.
type Backend struct {
	handler        http.Handler
	manager        *coreauth.Manager
	store          backendCredentialStore
	authIDs        []string
	seriesSelector *backendSeriesSelector
	quotaPollDone  <-chan struct{}
	backupDone     <-chan struct{} // the API-key backup's periodic credit check
	observeDone    <-chan struct{}
	stores         []*backendStore
	cancel         context.CancelFunc
	once           sync.Once
}

// NewBackend loads one profile, then starts its account-local refresh loop.
func NewBackend(ctx context.Context, opts BackendOptions) (*Backend, error) {
	pinSingle := !(opts.Provider == "claude" && opts.UseRequestModel)
	return newBackend(ctx, []BackendCredential{{AuthDir: opts.AuthDir, Provider: opts.Provider, AuthID: opts.AuthID, Name: opts.Name}}, opts.Model, opts.UseRequestModel, pinSingle, nil, backendRoutingOptions{modelMap: opts.ModelMap})
}

// NewBackendSeries loads a quota-aware set of profiles. The caller must hold
// every profile lock until Close completes.
func NewBackendSeries(ctx context.Context, opts BackendSeriesOptions) (*Backend, error) {
	if len(opts.Credentials) == 0 || len(opts.Credentials) < 2 && strings.TrimSpace(opts.BackupAPIKey) == "" {
		return nil, errors.New("ordered inference requires two profiles or one profile with an API-key backup")
	}
	if opts.Credentials[0].Provider != "claude" {
		return nil, errors.New("ordered inference currently supports Claude profiles only")
	}
	return newBackend(ctx, opts.Credentials, "", true, false, opts.QuotaRequest, backendRoutingOptions{backupAPIKey: opts.BackupAPIKey, checkBackupCredit: opts.CheckBackupCredit, modelMap: opts.ModelMap, snapshotInterval: opts.SnapshotInterval})
}

func newBackend(ctx context.Context, credentials []BackendCredential, modelName string, useRequestModel, pinSingle bool, quotaRequest ClaudeQuotaRequestFunc, routing backendRoutingOptions) (*Backend, error) {
	if ctx == nil {
		return nil, errors.New("backend requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(credentials) == 0 {
		return nil, errors.New("backend requires an inference profile")
	}
	provider := credentials[0].Provider
	backupAPIKey := strings.TrimSpace(routing.backupAPIKey)
	for _, char := range backupAPIKey {
		if char <= ' ' || char >= 127 {
			return nil, errors.New("API-key backup must be a nonempty HTTP credential")
		}
	}
	modelMap := make(map[string]string, len(routing.modelMap))
	for incoming, target := range routing.modelMap {
		if incoming == "" || target == "" || incoming != strings.TrimSpace(incoming) || target != strings.TrimSpace(target) {
			return nil, errors.New("model mappings require nonempty model names without surrounding whitespace")
		}
		modelMap[incoming] = target
	}
	models, err := backendModels(BackendOptions{Provider: provider, Model: modelName, UseRequestModel: useRequestModel})
	if err != nil {
		return nil, err
	}

	instanceID := backendRuntimeSequence.Add(1)
	stores := make([]*backendStore, 0, len(credentials))
	loaded := make([]*coreauth.Auth, 0, len(credentials))
	authIDs := make([]string, 0, len(credentials))
	credentialLocations := make(map[string]struct{}, len(credentials))
	subscriptionIDs := make(map[string]struct{}, len(credentials))
	profileNames := make(map[string]string, len(credentials)+1)
	for i, credentialOpts := range credentials {
		if credentialOpts.Provider != provider {
			return nil, errors.New("ordered inference profiles must use one provider")
		}
		opts := BackendOptions{AuthDir: credentialOpts.AuthDir, Provider: credentialOpts.Provider, AuthID: credentialOpts.AuthID, Model: modelName, UseRequestModel: useRequestModel, Name: profileDisplayName(credentialOpts)}
		location := filepath.Clean(opts.AuthDir) + "\x00" + opts.AuthID
		if _, exists := credentialLocations[location]; exists {
			return nil, errors.New("ordered inference profiles must be distinct")
		}
		credentialLocations[location] = struct{}{}
		runtimeID := opts.AuthID
		if !pinSingle {
			runtimeID = fmt.Sprintf("claude-master-%d-%d", instanceID, i+1)
		}
		store, credential, errLoad := loadBackendCredentialAs(ctx, opts, runtimeID)
		if errLoad != nil {
			return nil, errLoad
		}
		if provider == "claude" {
			accountUUID := helps.ClaudeCredentialAccountUUID(credential)
			if accountUUID != "" {
				if _, exists := subscriptionIDs[accountUUID]; exists {
					return nil, errors.New("ordered inference profiles must use distinct Claude accounts")
				}
				subscriptionIDs[accountUUID] = struct{}{}
			}
		}
		stores = append(stores, store)
		loaded = append(loaded, credential)
		authIDs = append(authIDs, runtimeID)
		profileNames[runtimeID] = profileDisplayName(credentialOpts)
	}

	subscriptionAuthIDs := append([]string(nil), authIDs...)
	backupAuthID := ""
	if backupAPIKey != "" {
		backupAuthID = fmt.Sprintf("claude-master-%d-api-backup", instanceID)
		loaded = append(loaded, &coreauth.Auth{
			ID: backupAuthID, Provider: provider, Status: coreauth.StatusActive,
			Attributes: map[string]string{
				coreauth.AttributeAuthKind:    coreauth.AuthKindAPIKey,
				coreauth.AttributeAPIKey:      backupAPIKey,
				coreauth.AttributeRuntimeOnly: "true",
			},
			Metadata: map[string]any{"request_retry": 0, "disable_cooling": false, "cloak_mode": "never"},
		})
		authIDs = append(authIDs, backupAuthID)
	}
	store := newBackendStoreSet(stores)
	var selector coreauth.Selector
	var seriesSelector *backendSeriesSelector
	pinnedAuthID := ""
	if pinSingle {
		pinnedAuthID = authIDs[0]
		selector = &backendSelector{authID: pinnedAuthID, provider: provider}
	} else {
		seriesSelector = &backendSeriesSelector{authIDs: subscriptionAuthIDs, backupAuthID: backupAuthID, provider: provider, names: profileNames}
		routes, errRoutes := newBackendSessionStore(credentials, loaded, backupAuthID, backupAPIKey)
		if errRoutes != nil {
			return nil, errRoutes
		}
		seriesSelector.routes = routes
		selector = seriesSelector
	}
	cfg := backendConfig(credentials[0].AuthDir)
	cfg.MaxRetryCredentials = len(loaded)
	manager := coreauth.NewManager(store, selector, nil)
	manager.SetConfig(cfg)
	manager.SetRetryConfig(0, 0, len(loaded))
	if useRequestModel || seriesSelector != nil {
		setBackendResultPolicy(manager, seriesSelector)
	}
	switch provider {
	case "claude":
		executor := newSharedClaudeExecutor(runtimeexecutor.NewClaudeExecutor(cfg), stores)
		manager.RegisterExecutor(withBackendUpstreamRecordingObserved(executor, func(authID string, record *backendUpstreamError) {
			if seriesSelector != nil {
				seriesSelector.noteBackupUpstream(authID, record)
			}
		}))
		if quotaRequest == nil {
			quotaRequest = manager.HttpRequest // the default requester, resolved here so the cache wraps it
		}
		quotaRequest = sharedUsageRequest(quotaRequest, stores)
		if seriesSelector != nil {
			seriesSelector.prepareIdentity = func(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
				prepareCtx, cancelPrepare := context.WithTimeout(ctx, claudeQuotaDefaultTimeout)
				defer cancelPrepare()
				return manager.PrepareRequestAuth(prepareCtx, executor, auth)
			}
		}
	case "codex":
		manager.RegisterExecutor(runtimeexecutor.NewCodexExecutor(cfg))
	}
	registered := make([]string, 0, len(loaded))
	for _, credential := range loaded {
		registry.GetGlobalRegistry().RegisterClient(credential.ID, provider, models)
		registered = append(registered, credential.ID)
		if _, errRegister := manager.Register(coreauth.WithSkipPersist(ctx), credential); errRegister != nil {
			for _, authID := range registered {
				registry.GetGlobalRegistry().UnregisterClient(authID)
			}
			if seriesSelector != nil {
				seriesSelector.Stop()
			}
			store.seal()
			return nil, errors.New("cannot register the selected inference credentials")
		}
	}
	pollQuota := seriesSelector != nil && (len(subscriptionAuthIDs) > 1 || backupAuthID != "")
	if pollQuota {
		loadBackendWeeklyQuotas(ctx, manager, seriesSelector, subscriptionAuthIDs, quotaRequest)
	}
	runCtx, cancel := context.WithCancel(ctx)
	backend := &Backend{
		stores:         stores,
		manager:        manager,
		store:          store,
		authIDs:        authIDs,
		seriesSelector: seriesSelector,
		cancel:         cancel,
	}
	backend.handler = newBackendHandler(runCtx, BackendOptions{
		Provider: provider, AuthID: pinnedAuthID, Model: modelName,
		UseRequestModel: useRequestModel,
		ModelMap:        modelMap,
	}, handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager))
	manager.StartAutoRefresh(runCtx, 15*time.Minute)
	if pollQuota {
		backend.quotaPollDone = startBackendQuotaPolling(runCtx, manager, seriesSelector, subscriptionAuthIDs, quotaRequest, nil)
	}
	if seriesSelector != nil && backupAuthID != "" && routing.checkBackupCredit {
		backend.backupDone = startBackupCreditChecks(runCtx, seriesSelector, newBackupCreditCheck(backupAPIKey, backendBackupCheckBaseURL, &http.Client{Timeout: 20 * time.Second}), nil)
	}
	if seriesSelector != nil {
		subscriptions := make([]string, 0, len(subscriptionAuthIDs))
		for _, id := range subscriptionAuthIDs {
			subscriptions = append(subscriptions, profileNames[id])
		}
		lg().Info("inference backend started", "mode", "pool", "profiles", strings.Join(subscriptions, ","), "api_backup", backupAPIKey != "")
		backend.observeDone = startBackendObservation(runCtx, seriesSelector, routing.snapshotInterval)
		registerStateSource(backend)
	} else {
		lg().Info("inference backend started", "mode", "single", "profile", profileNames[authIDs[0]])
	}
	return backend, nil
}

func (b *Backend) Handler() http.Handler { return b.handler }

func (b *Backend) Close() error {
	if b == nil {
		return nil
	}
	b.once.Do(func() {
		b.cancel()
		if b.backupDone != nil {
			<-b.backupDone
		}
		if b.quotaPollDone != nil {
			<-b.quotaPollDone
		}
		if b.observeDone != nil {
			<-b.observeDone
		}
		unregisterStateSource(b)
		lg().Info("inference backend stopped")
		b.manager.StopAutoRefreshAndWait()
		// StopAutoRefreshAndWait also stops a stoppable selector. Keep the
		// explicit call so this backend still owns the cache lifecycle if the
		// manager shutdown implementation changes; Stop is idempotent.
		if b.seriesSelector != nil {
			b.seriesSelector.Stop()
		}
		// A canceled stream's detached result observer can finish after its
		// HTTP handler. It must not rewrite old credentials after another
		// process obtains the profile lock. Account refresh itself is joined
		// above, so sealing cannot discard an already-rotated refresh token.
		b.store.seal()
		b.manager.CloseExecutionSession(coreauth.CloseAllExecutionSessionsID)
		for _, authID := range b.authIDs {
			registry.GetGlobalRegistry().UnregisterClient(authID)
		}
	})
	return nil
}

func backendConfig(authDir string) *config.Config {
	cfg := &config.Config{
		AuthDir: authDir, CommercialMode: true, MaxRetryCredentials: 1,
		DisableClaudeCloakMode: true, AuthAutoRefreshWorkers: 1,
	}
	cfg.SDKConfig.DisableImageGeneration = config.DisableImageGenerationPassthrough
	// The native adapter applies its own narrow response-header boundary below.
	cfg.SDKConfig.PassthroughHeaders = true
	cfg.ProxyURL = "direct"
	cfg.ClaudeCode.DisableCloakingModelList = true
	cfg.Codex.DisableCodexCloaking = true
	cfg.RemoteManagement.DisableControlPanel = true
	cfg.RemoteManagement.DisableAutoUpdatePanel = true
	// All retry, quota-switch, logging, plugin, Home, and update settings remain
	// zero. We intentionally do not call the CLI's config/default loaders.
	return cfg
}

func backendModels(opts BackendOptions) ([]*registry.ModelInfo, error) {
	if opts.Provider != "claude" && opts.Provider != "codex" {
		return nil, errors.New("inference provider must be claude or codex")
	}
	if !opts.UseRequestModel {
		model := strings.TrimSpace(opts.Model)
		if model == "" {
			return nil, errors.New("pinned model is required")
		}
		// Probe is diagnostic only. Register its caller-supplied model as a route
		// key and let Anthropic decide whether that model is available; this
		// launcher deliberately has no local model allowlist.
		return []*registry.ModelInfo{{ID: model}}, nil
	}
	if opts.Provider != "claude" || strings.TrimSpace(opts.Model) != "" {
		return nil, errors.New("request-selected models require an unpinned Claude backend")
	}
	// Auth selection needs a stable registry key, but execution is forced to the
	// Claude provider and restores the native request model before the executor.
	// This marker is never sent upstream and imposes no local model allowlist.
	return []*registry.ModelInfo{{ID: backendAuthSelectionModel}}, nil
}

type backendCredentialStore interface {
	coreauth.Store
	seal()
}

// backendStore prevents refresh/persistence from escaping the selected profile.
type backendStore struct {
	mu         sync.Mutex
	closed     bool
	opts       BackendOptions
	path       string
	runtimeID  string
	renameFile func(string, string) error
	view       diskView
}

func loadBackendCredential(ctx context.Context, opts BackendOptions) (*backendStore, *coreauth.Auth, error) {
	return loadBackendCredentialAs(ctx, opts, opts.AuthID)
}

func loadBackendCredentialAs(ctx context.Context, opts BackendOptions, runtimeID string) (*backendStore, *coreauth.Auth, error) {
	if opts.Provider != "claude" && opts.Provider != "codex" {
		return nil, nil, errors.New("inference provider must be claude or codex")
	}
	if !filepath.IsAbs(opts.AuthDir) || opts.AuthID == "" || filepath.Base(opts.AuthID) != opts.AuthID ||
		strings.ContainsAny(opts.AuthID, `/\\`) || !strings.HasSuffix(opts.AuthID, ".json") || strings.TrimSpace(runtimeID) == "" {
		return nil, nil, errors.New("invalid inference credential location")
	}
	info, err := os.Lstat(opts.AuthDir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return nil, nil, errors.New("inference auth directory must be private and not a symlink")
	}
	unlockRefresh, err := lockRefresh(ctx, opts.AuthDir)
	if err != nil {
		return nil, nil, err
	}
	defer unlockRefresh()
	if err := recoverStagedRefresh(opts.AuthDir, opts.AuthID, opts.Provider); err != nil {
		return nil, nil, err
	}
	entries, err := os.ReadDir(opts.AuthDir)
	if err != nil || len(entries) != 1 || entries[0].Name() != opts.AuthID {
		return nil, nil, errors.New("inference auth directory must contain exactly the selected credential")
	}
	path := filepath.Join(opts.AuthDir, opts.AuthID)
	info, err = os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return nil, nil, errors.New("inference credential must be a private regular file")
	}
	inner := sdkauth.NewFileTokenStore()
	inner.SetBaseDir(opts.AuthDir)
	records, err := inner.List(ctx)
	if err != nil || len(records) != 1 {
		return nil, nil, errors.New("cannot load the selected inference credential")
	}
	credential := records[0]
	store := &backendStore{opts: opts, path: path, runtimeID: opts.AuthID}
	if errValidate := store.validate(credential); errValidate != nil {
		return nil, nil, errValidate
	}
	if credential.Disabled || credential.Status == coreauth.StatusDisabled {
		return nil, nil, errors.New("selected inference credential is disabled")
	}
	access, _ := credential.Metadata["access_token"].(string)
	refresh, _ := credential.Metadata["refresh_token"].(string)
	if strings.TrimSpace(access) == "" || strings.TrimSpace(refresh) == "" {
		return nil, nil, errors.New("selected profile requires an independent OAuth login")
	}
	for _, key := range []string{"proxy_url", "base_url", "headers", "custom_headers", "prefix", "plugins"} {
		if value, exists := credential.Metadata[key]; exists && value != nil && value != "" {
			return nil, nil, errors.New("profile credential contains unsupported routing overrides")
		}
	}
	if mode, exists := credential.Metadata["cloak_mode"]; exists && mode != "never" && mode != "" {
		return nil, nil, errors.New("inference credential must not enable request cloaking")
	}
	if disabled, exists := credential.DisableCoolingOverride(); exists && disabled {
		return nil, nil, errors.New("inference credential must not disable quota cooling")
	}
	credential.ID = runtimeID
	store.runtimeID = runtimeID
	credential.Metadata["cloak_mode"] = "never"
	credential.Metadata["request_retry"] = 0
	credential.Metadata["disable_cooling"] = false
	delete(credential.Metadata, "disable-cooling")
	return store, credential, nil
}

func (s *backendStore) validate(auth *coreauth.Auth) error {
	if auth == nil || auth.ID != s.runtimeID || auth.Provider != s.opts.Provider || auth.FileName != s.opts.AuthID {
		return errors.New("inference credential does not match the pinned profile")
	}
	if auth.Attributes[coreauth.AttributePath] != s.path || auth.ProxyURL != "" || auth.Prefix != "" {
		return errors.New("inference credential has an invalid persistence path or routing override")
	}
	return nil
}

func (s *backendStore) List(ctx context.Context) ([]*coreauth.Auth, error) {
	_, auth, err := loadBackendCredentialAs(ctx, s.opts, s.runtimeID)
	if err != nil {
		return nil, err
	}
	return []*coreauth.Auth{auth}, nil
}

// Save persists a credential. A rotation that already happened must reach disk even if the request
// that caused it was canceled, so the lock wait ignores cancellation.
func (s *backendStore) Save(ctx context.Context, auth *coreauth.Auth) (string, error) {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed { // a late result after shutdown must not touch the profile directory again
		return "", errors.New("inference credential store is closed")
	}
	unlock, err := lockRefresh(context.WithoutCancel(ctx), s.opts.AuthDir)
	if err != nil {
		return "", err
	}
	defer unlock()
	return s.saveLocked(auth)
}

// saveLocked writes under the profile's refresh lock. It never replaces newer tokens on disk with
// older ones: if another process rotated the login, its tokens win and only this process's other
// fields are merged in.
func (s *backendStore) saveLocked(auth *coreauth.Auth) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", errors.New("inference credential store is closed")
	}
	if err := s.validate(auth); err != nil {
		return "", err
	}
	// Loaded OAuth records and both selected provider refresh executors use
	// Metadata, not an arbitrary TokenStorage implementation. Refuse to delegate
	// persistence to code that could select a different destination.
	if auth.Storage != nil || auth.Metadata == nil {
		return "", errors.New("unsupported inference credential storage")
	}
	// Finish a previous failed commit before creating another stage. Otherwise
	// an older retained snapshot could overwrite a later successful save at restart.
	if err := recoverStagedRefresh(s.opts.AuthDir, s.opts.AuthID, s.opts.Provider); err != nil {
		return "", err
	}
	info, err := os.Lstat(s.opts.AuthDir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return "", errors.New("inference auth directory is no longer private")
	}
	info, err = os.Lstat(s.path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return "", errors.New("selected credential file is no longer safe to update")
	}
	metadata := make(map[string]any, len(auth.Metadata)+4)
	for key, value := range auth.Metadata {
		metadata[key] = value
	}
	metadata["cloak_mode"] = "never"
	metadata["request_retry"] = 0
	metadata["disable_cooling"] = false
	delete(metadata, "disable-cooling")
	metadata["disabled"] = auth.Disabled
	disk, errDisk := readRefreshSnapshot(s.path)
	if errDisk == nil && diskAhead(disk, metadata) {
		for _, key := range credentialTokenKeys {
			if value, ok := disk[key]; ok {
				metadata[key] = value
			}
		}
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		return "", errors.New("cannot serialize refreshed inference credential")
	}
	if errDisk == nil {
		if onDisk, errMarshal := json.Marshal(disk); errMarshal == nil && bytes.Equal(onDisk, raw) {
			return s.path, nil // the saved file already says exactly this
		}
	}
	// Once OAuth has rotated a token, finish its durable write even if the
	// request context was canceled. Backend.Close joins the refresh workers
	// before the launcher releases its exclusive profile lock.
	temp, err := os.CreateTemp(s.opts.AuthDir, ".refresh-*")
	if err != nil {
		return "", errors.New("cannot stage refreshed inference credential")
	}
	tempPath := temp.Name()
	retainStage := false
	defer func() {
		// A complete stage may hold the only valid rotated token. Keep it if
		// sync/close/rename fails so locked startup recovery can finish it.
		_ = temp.Close()
		if !retainStage {
			_ = os.Remove(tempPath)
		}
	}()
	if n, errWrite := temp.Write(raw); errWrite != nil || n != len(raw) {
		return "", errors.New("cannot write refreshed inference credential")
	}
	retainStage = true
	if errSync := temp.Sync(); errSync != nil {
		return "", errors.New("cannot sync refreshed inference credential")
	}
	if errClose := temp.Close(); errClose != nil {
		return "", errors.New("cannot close refreshed inference credential")
	}
	renameFile := os.Rename
	if s.renameFile != nil {
		renameFile = s.renameFile
	}
	if errRename := renameFile(tempPath, s.path); errRename != nil {
		return "", errors.New("cannot commit refreshed inference credential")
	}
	retainStage = false
	dir, err := os.Open(s.opts.AuthDir)
	if err != nil {
		return "", errors.New("refreshed credential was committed but its directory could not be opened for sync")
	}
	errSync := dir.Sync()
	errClose := dir.Close()
	if errSync != nil || errClose != nil {
		return "", errors.New("refreshed credential was committed but its directory sync failed")
	}
	return s.path, nil
}

func (s *backendStore) seal() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
}

func (s *backendStore) Delete(context.Context, string) error {
	return errors.New("running inference backend cannot delete credentials")
}

type backendStoreSet struct {
	stores []*backendStore
	byID   map[string]*backendStore
}

func newBackendStoreSet(stores []*backendStore) backendCredentialStore {
	if len(stores) == 1 {
		return stores[0]
	}
	set := &backendStoreSet{
		stores: append([]*backendStore(nil), stores...),
		byID:   make(map[string]*backendStore, len(stores)),
	}
	for _, store := range stores {
		set.byID[store.runtimeID] = store
	}
	return set
}

func (s *backendStoreSet) List(ctx context.Context) ([]*coreauth.Auth, error) {
	auths := make([]*coreauth.Auth, 0, len(s.stores))
	for _, store := range s.stores {
		loaded, err := store.List(ctx)
		if err != nil {
			return nil, err
		}
		auths = append(auths, loaded...)
	}
	return auths, nil
}

func (s *backendStoreSet) Save(ctx context.Context, auth *coreauth.Auth) (string, error) {
	if auth == nil {
		return "", errors.New("inference credential does not match the ordered profiles")
	}
	store := s.byID[auth.ID]
	if store == nil {
		return "", errors.New("inference credential does not match the ordered profiles")
	}
	return store.Save(ctx, auth)
}

func (s *backendStoreSet) Delete(context.Context, string) error {
	return errors.New("running inference backend cannot delete credentials")
}

func (s *backendStoreSet) seal() {
	for _, store := range s.stores {
		store.seal()
	}
}

// The explicit selector is defense in depth: even an accidentally unpinned
// caller can never select an additional account registered in this manager.
type backendSelector struct {
	authID   string
	provider string
}

const backendWeeklyReserve = 0.10

type backendWeeklyQuota struct {
	known    bool
	used     float64
	resetsAt time.Time
}

type backendSeriesCandidate struct {
	auth  *coreauth.Auth
	quota backendWeeklyQuota
	order int
}

// backendSeriesSelector drains the account whose weekly allocation resets
// first. It keeps the last ten percent of an account for opaque continuations
// whenever another account still has quota.
type backendSeriesSelector struct {
	mu                sync.Mutex
	authIDs           []string
	backupAuthID      string
	provider          string
	quota             map[string]backendWeeklyQuota
	quotaRevision     map[string]uint64
	quotaBlockedUntil map[string]time.Time
	// fiveHour is the last polled five-hour window per account; it is reported, not used to route.
	fiveHour          map[string]ClaudeFiveHourQuota
	quotaBackoffLevel map[string]int
	sessions          *coreauth.SessionCache
	routes            *backendSessionStore
	prepareIdentity   func(context.Context, *coreauth.Auth) (*coreauth.Auth, error)
	activeRoutes      map[string]map[string]int
	now               func() time.Time
	usagePollAfter    map[string]time.Time // an account's usage poll waits until then after a 429
	stopped           bool

	names       map[string]string // runtime auth id -> the profile's own name, for logs and metrics
	quotaBands  map[string]string
	rateLimited map[string]bool
	stats       *selectorStats
	limiter     *every

	// The API-key backup's health (backend_backup_health.go). Unknown until the first check, which runs at start.
	backupChecked     bool
	backupUnavailable bool
	backupWhy         string
}

// Pick chooses an account for a request and then records the decision (log and metrics).
func (s *backendSeriesSelector) Pick(ctx context.Context, provider, model string, opts coreexecutor.Options, auths []*coreauth.Auth) (*coreauth.Auth, error) {
	started := time.Now()
	picked, err := s.pick(ctx, provider, model, opts, auths)
	sessionID, _ := backendSeriesSessionIDs(opts)
	s.noteSelection(sessionID, model, picked, err, time.Since(started))
	if err != nil {
		noteBackendRefusal(ctx, err)
	}
	return picked, err
}

func (s *backendSeriesSelector) pick(ctx context.Context, provider, model string, opts coreexecutor.Options, auths []*coreauth.Auth) (*coreauth.Auth, error) {
	if provider != s.provider && provider != "mixed" {
		return nil, backendRefuse(backendRefusalInternal, "inference provider differs from the ordered profiles")
	}
	sessionID, parentSessionID := backendSeriesSessionIDs(opts)
	affinity := analyzeNativeRequestAffinity(opts.OriginalRequest)
	switchable := affinity.validJSON && !affinity.requiresAccount
	// Empty OriginalRequest is used by body-less programmatic selection. Every
	// native inference call supplies its validated body; malformed bodies stay
	// conservative rather than establishing a guessed continuation origin.
	opaque := !switchable && len(opts.OriginalRequest) != 0

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return nil, backendRefuse(backendRefusalUnavailable, "ordered inference selector is stopped")
	}
	s.initializeLocked()
	// A paid API credential is not subscription capacity: do not use it to
	// bypass the ten-percent reserve or an ordinary provider/request failure.
	if switchable && s.backupAuthID != "" && s.backupUsableLocked() && !backendAttemptFailed(ctx, "", backendAvailabilityModel(model, opts)) && s.subscriptionsExhaustedLocked() {
		return s.pickBackupLocked(ctx, model, opts, auths, sessionID)
	}
	excludedAuthID := ""
	if sessionID != "" {
		boundID, boundFound, errBinding := s.sessionBindingLocked(sessionID, auths, opaque)
		if errBinding != nil {
			return nil, errBinding
		}
		inheritedParent := false
		if !boundFound && parentSessionID != "" {
			boundID, boundFound, errBinding = s.sessionBindingLocked(parentSessionID, auths, opaque)
			if errBinding != nil {
				return nil, errBinding
			}
			inheritedParent = boundFound
			if inheritedParent && opaque {
				changed, errEpoch := s.parentOriginChangedLocked(parentSessionID)
				if errEpoch != nil {
					return nil, errEpoch
				}
				if changed {
					return nil, backendRefuse(backendRefusalInvalid, "the parent Claude conversation changed accounts; the first opaque child origin is ambiguous, so start the child with self-contained context")
				}
			}
		}
		// A self-contained child starts its own binding. It inherits the parent
		// only when its first payload carries state that cannot cross accounts.
		if boundFound && !(inheritedParent && switchable) {
			if boundID == s.backupAuthID && switchable {
				// Return clean work to subscriptions as soon as capacity resets.
				// Opaque API continuations still take the ordinary bound path below.
				selected := s.preferredAuthLocked(auths, "")
				if selected == nil {
					return nil, s.quotaRefusalLocked("subscription capacity is unavailable; no quota handoff to the API-key backup is permitted")
				}
				picked, err := s.pickAuthLocked(ctx, model, opts, selected)
				if err == nil && picked != nil {
					return s.bindSessionLocked(ctx, sessionID, picked)
				}
				return picked, err
			}
			if bound := backendSeriesAuthByID(auths, s.provider, boundID); bound != nil {
				available := backendSeriesAvailableAuths(ctx, s.provider, backendAvailabilityModel(model, opts), opts, auths)
				if !switchable || !s.shouldMoveCleanWorkLocked(boundID, available) {
					picked, err := s.pickAuthLocked(ctx, model, opts, bound)
					if err == nil && picked != nil {
						if inheritedParent && opaque {
							return s.bindInheritedSessionLocked(ctx, sessionID, parentSessionID, picked)
						}
						return s.bindSessionLocked(ctx, sessionID, picked)
					}
					// The core refused the bound subscription (a model cooldown, an attempt that failed in
					// this request): say which subscription, which model and why, in front of its error.
					if err != nil && backendGenericRefusal(err) {
						explained := s.boundUnavailableErrorLocked(ctx, boundID, backendAvailabilityModel(model, opts)).(*backendRefusalError)
						explained.cause = err
						err = explained
					}
					return picked, err
				}
				selected := s.preferredAvailableAuthLocked(available, boundID)
				if selected == nil {
					picked, err := s.pickAuthLocked(ctx, model, opts, bound)
					if err == nil && picked != nil {
						return s.bindSessionLocked(ctx, sessionID, picked)
					}
					return picked, err
				}
				picked, err := s.pickAuthLocked(ctx, model, opts, selected)
				if err != nil {
					return nil, err
				}
				return s.bindSessionLocked(ctx, sessionID, picked)
			} else {
				if !switchable {
					return nil, backendRefuse(backendRefusalInvalid, "the current Claude continuation account is unavailable; refusing to move opaque conversation state")
				}
				// Manager-side model cooldowns and request-scoped failures also
				// remove an auth from this candidate slice. They must not rotate a
				// subscription; only the weekly reserve policy may move clean work.
				quota := s.currentQuotaLocked(boundID)
				if backendAttemptFailed(ctx, boundID, backendAvailabilityModel(model, opts)) || (!strings.HasPrefix(boundID, "missing:") && !s.quotaBlockedLocked(boundID) && (!quota.known || quota.used < 1)) {
					return nil, s.boundUnavailableErrorLocked(ctx, boundID, backendAvailabilityModel(model, opts))
				}
			}
			excludedAuthID = boundID
		}
	}
	if opaque && excludedAuthID == "" {
		return nil, backendRefuse(backendRefusalInvalid, "the Claude continuation origin is unknown; start a self-contained session before switching subscriptions")
	}

	selected := s.preferredAuthLocked(auths, excludedAuthID)
	if selected == nil {
		return nil, s.quotaRefusalLocked("ordered inference subscriptions are exhausted; no fallback is configured")
	}
	picked, err := s.pickAuthLocked(ctx, model, opts, selected)
	if err != nil {
		return nil, err
	}
	if sessionID != "" && picked != nil {
		return s.bindSessionLocked(ctx, sessionID, picked)
	}
	return picked, nil
}

// boundUnavailableErrorLocked explains why a conversation stays on a subscription that cannot
// take it right now. Only a used-up weekly quota moves a conversation; an upstream error on the
// bound subscription (a model it does not serve, a 5xx) does not, so the person can act on it:
// the subscription's own name, the model, the status, and what to do.
func (s *backendSeriesSelector) boundUnavailableErrorLocked(ctx context.Context, boundID, model string) error {
	name := boundID
	if n := s.names[boundID]; n != "" {
		name = n
	}
	refusal := &backendRefusalError{kind: backendRefusalUnavailable, authID: boundID, model: model}
	if status, failed := backendAttemptStatus(ctx, boundID, model); failed {
		refusal.relayed = status != 0
		switch {
		case status == http.StatusNotFound && backendUpstreamErrorFrom(ctx).notFoundWithoutAnthropicError():
			// Not "model not found": Anthropic names that. Retried once already (backendRetryNotFound).
			refusal.reason = fmt.Sprintf("subscription %s answered HTTP 404 with no Anthropic error for model %s, twice; that is not a model it cannot serve, and the pool moves a conversation only for a used-up weekly quota. Retry, or switch model with /model", name, model)
		case status == http.StatusNotFound && !backendUpstreamErrorFrom(ctx).notFoundNamesModel(model):
			// Anthropic's not_found_error, but about something in the request (a file, a container, a
			// resource of another account), not the model. The session has Anthropic's own message.
			refusal.kind = backendRefusalNotFound
			refusal.reason = fmt.Sprintf("subscription %s: Anthropic answered 404 not_found_error for model %s about something in the request, not the model (the message is in the response and the server log); the pool moves a conversation only for a used-up weekly quota. Start a fresh session, or switch model with /model", name, model)
		case status == http.StatusNotFound:
			refusal.kind = backendRefusalNotFound
			refusal.reason = fmt.Sprintf("subscription %s does not serve model %s (HTTP 404); the pool moves a conversation only for a used-up weekly quota, not for a model it cannot serve. Switch model with /model", name, model)
		case status != 0:
			refusal.reason = fmt.Sprintf("subscription %s answered HTTP %d for model %s; the pool moves a conversation only for a used-up weekly quota, not for an upstream error. Retry, or switch model with /model", name, status, model)
		default:
			refusal.reason = fmt.Sprintf("subscription %s failed this request for model %s; the pool moves a conversation only for a used-up weekly quota, not for an upstream error. Retry, or switch model with /model", name, model)
		}
		return refusal
	}
	refusal.reason = fmt.Sprintf("subscription %s is not available for model %s right now (cooling down after an upstream error, not out of weekly quota), so this conversation stays with it. Retry shortly, or switch model with /model", name, model)
	return refusal
}

// backendUsagePollWhy says what a failed usage poll failed on: the fetch error without any URL
// (bounded), or that the response carried no weekly figure. Before this every failure read
// "usage request failed or carried no weekly figure", about every two minutes per profile.
func backendUsagePollWhy(err error, known bool) string {
	if err == nil {
		if !known {
			return "usage response carried no weekly figure"
		}
		return "usage request failed"
	}
	why := backendUsagePollURLs.ReplaceAllString(err.Error(), "<url>")
	why = strings.Join(strings.Fields(why), " ")
	if len(why) > 160 {
		why = why[:160]
	}
	return "usage request failed: " + why
}

var backendUsagePollURLs = regexp.MustCompile(`https?://[^\s"]+`)

func backendSeriesSessionIDs(opts coreexecutor.Options) (sessionID, parentSessionID string) {
	sessionID = coreauth.CanonicalSessionID(opts.Headers, opts.OriginalRequest, opts.Metadata)
	info, ok := cliproxysession.ExtractSessionInfo(opts.Headers, opts.OriginalRequest, opts.Metadata)
	if ok && info.ParentSessionID != "" && info.ParentSessionID != info.SessionID {
		parentSessionID = cliproxysession.BoundSessionIdentity(info.ParentSessionID)
	} else if opts.Metadata != nil {
		if parent, okParent := opts.Metadata[coreexecutor.ParentSessionIDMetadataKey].(string); okParent {
			parentSessionID = cliproxysession.BoundSessionIdentity(strings.TrimSpace(parent))
		}
	}
	if parentSessionID == sessionID {
		parentSessionID = ""
	}
	return sessionID, parentSessionID
}

func (s *backendSeriesSelector) subscriptionsExhaustedLocked() bool {
	if len(s.authIDs) == 0 {
		return false
	}
	for _, authID := range s.authIDs {
		quota := s.currentQuotaLocked(authID)
		if (!quota.known || quota.used < 1) && !s.quotaBlockedLocked(authID) {
			return false
		}
	}
	return true
}

func (s *backendSeriesSelector) pickBackupLocked(ctx context.Context, model string, opts coreexecutor.Options, auths []*coreauth.Auth, sessionID string) (*coreauth.Auth, error) {
	backup := backendSeriesAuthByID(auths, s.provider, s.backupAuthID)
	if backup == nil {
		return nil, backendRefuse(backendRefusalUnavailable, "the final API-key backup is unavailable")
	}
	if s.limiter == nil {
		s.limiter = newEvery(time.Minute)
	}
	if s.limiter.allow("backup") {
		lg().Warn("every subscription is exhausted or rate limited: using the paid API-key backup", "session", sessionTag(sessionID))
	}
	picked, err := s.pickAuthLocked(ctx, model, opts, backup)
	if err == nil && sessionID != "" && picked != nil {
		return s.bindSessionLocked(ctx, sessionID, picked)
	}
	return picked, err
}

func (s *backendSeriesSelector) initializeLocked() {
	if s.quota == nil {
		s.quota = make(map[string]backendWeeklyQuota, len(s.authIDs))
	}
	if s.quotaRevision == nil {
		s.quotaRevision = make(map[string]uint64, len(s.authIDs))
	}
	if s.quotaBlockedUntil == nil {
		s.quotaBlockedUntil = make(map[string]time.Time, len(s.authIDs))
	}
	if s.quotaBackoffLevel == nil {
		s.quotaBackoffLevel = make(map[string]int, len(s.authIDs))
	}
	if s.sessions == nil {
		s.sessions = coreauth.NewSessionCacheWithCapacity(30*24*time.Hour, 4096)
	}
	if s.now == nil {
		s.now = time.Now
	}
}

func (s *backendSeriesSelector) Stop() {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.stopped = true
	cache := s.sessions
	s.sessions = nil
	s.mu.Unlock()
	if cache != nil {
		cache.Stop()
	}
}

func (s *backendSeriesSelector) pickAuthLocked(ctx context.Context, model string, opts coreexecutor.Options, auth *coreauth.Auth) (*coreauth.Auth, error) {
	if auth != nil && auth.ID != s.backupAuthID {
		quota := s.currentQuotaLocked(auth.ID)
		if s.quotaBlockedLocked(auth.ID) || (quota.known && quota.used >= 1) {
			return nil, s.quotaRefusalLocked("the current Claude subscription quota is exhausted")
		}
	}
	return (&coreauth.FillFirstSelector{}).Pick(ctx, s.provider, backendAvailabilityModel(model, opts), opts, []*coreauth.Auth{auth})
}

func backendSeriesAuthByID(auths []*coreauth.Auth, provider, authID string) *coreauth.Auth {
	for _, auth := range auths {
		if auth != nil && auth.ID == authID && auth.Provider == provider {
			return auth
		}
	}
	return nil
}

func (s *backendSeriesSelector) shouldMoveCleanWorkLocked(boundID string, auths []*coreauth.Auth) bool {
	if s.quotaBlockedLocked(boundID) {
		return true
	}
	quota := s.currentQuotaLocked(boundID)
	if !quota.known || quota.used < 1-backendWeeklyReserve {
		return false
	}
	for _, auth := range auths {
		if auth == nil || auth.Provider != s.provider || auth.ID == boundID || auth.ID == s.backupAuthID || s.quotaBlockedLocked(auth.ID) {
			continue
		}
		other := s.currentQuotaLocked(auth.ID)
		if !other.known || other.used < 1-backendWeeklyReserve {
			return true
		}
	}
	// Once every usable subscription is in reserve, drain the earliest reset
	// instead of excluding the current binding and bouncing each clean turn.
	preferred := s.preferredAvailableAuthLocked(auths, "")
	return preferred != nil && preferred.ID != boundID
}

func backendSeriesAvailableAuths(ctx context.Context, provider, model string, opts coreexecutor.Options, auths []*coreauth.Auth) []*coreauth.Auth {
	available := make([]*coreauth.Auth, 0, len(auths))
	for _, auth := range auths {
		if auth == nil {
			continue
		}
		if picked, err := (&coreauth.FillFirstSelector{}).Pick(ctx, provider, model, opts, []*coreauth.Auth{auth}); err == nil && picked != nil {
			available = append(available, auth)
		}
	}
	return available
}

func (s *backendSeriesSelector) preferredAuthLocked(auths []*coreauth.Auth, excludedAuthID string) *coreauth.Auth {
	// Rank against the complete configured series. If the quota-preferred account
	// is absent from auths because of a request/model/auth failure, return nil
	// instead of rotating subscriptions for a non-weekly-quota condition.
	return s.preferredAuthWithAvailabilityLocked(auths, excludedAuthID, false)
}

func (s *backendSeriesSelector) preferredAvailableAuthLocked(auths []*coreauth.Auth, excludedAuthID string) *coreauth.Auth {
	return s.preferredAuthWithAvailabilityLocked(auths, excludedAuthID, true)
}

func (s *backendSeriesSelector) preferredAuthWithAvailabilityLocked(auths []*coreauth.Auth, excludedAuthID string, availableOnly bool) *coreauth.Auth {
	available := make([]backendSeriesCandidate, 0, len(s.authIDs))
	for order, authID := range s.authIDs {
		if authID == excludedAuthID || s.quotaBlockedLocked(authID) {
			continue
		}
		if availableOnly && backendSeriesAuthByID(auths, s.provider, authID) == nil {
			continue
		}
		available = append(available, backendSeriesCandidate{auth: &coreauth.Auth{ID: authID, Provider: s.provider}, quota: s.currentQuotaLocked(authID), order: order})
	}
	if len(available) == 0 {
		return nil
	}

	choose := func(allowReserve bool) *coreauth.Auth {
		var best *backendSeriesCandidate
		for i := range available {
			item := &available[i]
			if item.quota.known {
				if item.quota.used >= 1 {
					continue
				}
				if !allowReserve && item.quota.used >= 1-backendWeeklyReserve {
					continue
				}
			}
			if best == nil || backendQuotaBefore(*item, *best) {
				best = item
			}
		}
		if best == nil {
			return nil
		}
		return backendSeriesAuthByID(auths, s.provider, best.auth.ID)
	}
	if selected := choose(false); selected != nil {
		return selected
	}
	return choose(true)
}

func backendQuotaBefore(a, b backendSeriesCandidate) bool {
	if a.quota.known != b.quota.known {
		return a.quota.known
	}
	if a.quota.known && !a.quota.resetsAt.Equal(b.quota.resetsAt) {
		if a.quota.resetsAt.IsZero() {
			return false
		}
		if b.quota.resetsAt.IsZero() {
			return true
		}
		return a.quota.resetsAt.Before(b.quota.resetsAt)
	}
	return a.order < b.order
}

func (s *backendSeriesSelector) currentQuotaLocked(authID string) backendWeeklyQuota {
	quota := s.quota[authID]
	if quota.known && !quota.resetsAt.IsZero() {
		now := s.now()
		for !now.Before(quota.resetsAt) {
			quota.resetsAt = quota.resetsAt.Add(7 * 24 * time.Hour)
			quota.used = 0
		}
		s.quota[authID] = quota
	}
	return quota
}

func (s *backendSeriesSelector) quotaBlockedLocked(authID string) bool {
	until, blocked := s.quotaBlockedUntil[authID]
	if blocked && !s.now().Before(until) {
		delete(s.quotaBlockedUntil, authID)
		return false
	}
	return blocked
}

func (s *backendSeriesSelector) observeQuota(authID string, quota backendWeeklyQuota) {
	if strings.TrimSpace(authID) == "" || authID == s.backupAuthID || !quota.known {
		return
	}
	if quota.used < 0 {
		quota.used = 0
	} else if quota.used > 1 {
		quota.used = 1
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return
	}
	s.initializeLocked()
	// Even a passive snapshot rejected by monotonic ordering is a newer
	// observation than an already in-flight usage poll.
	s.quotaRevision[authID]++
	existing := s.currentQuotaLocked(authID)
	if existing.known {
		switch {
		case quota.resetsAt.IsZero() && !existing.resetsAt.IsZero():
			if quota.used > existing.used {
				existing.used = quota.used
				s.quota[authID] = existing
				s.noteQuotaLocked(authID, existing)
			}
			return
		case !quota.resetsAt.IsZero() && !existing.resetsAt.IsZero() && quota.resetsAt.Before(existing.resetsAt):
			return
		case quota.resetsAt.Equal(existing.resetsAt) && quota.used < existing.used:
			return
		}
	}
	s.quota[authID] = quota
	s.noteQuotaLocked(authID, quota)
}

func backendAvailabilityModel(selectionModel string, opts coreexecutor.Options) string {
	if requestedModel, ok := opts.Metadata[coreexecutor.RequestedModelMetadataKey].(string); ok {
		if requestedModel = strings.TrimSpace(requestedModel); requestedModel != "" {
			return requestedModel
		}
	}
	return selectionModel
}

func backendBlockedModelKey(model string) string {
	parsed := thinking.ParseSuffix(strings.TrimSpace(model))
	if parsed.ModelName != "" {
		return strings.TrimSpace(parsed.ModelName)
	}
	return strings.TrimSpace(model)
}

func (s *backendSeriesSelector) OnResult(result coreauth.Result) {
	if strings.TrimSpace(result.AuthID) == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return
	}
	s.initializeLocked()
	if result.AuthID == s.backupAuthID {
		// API rate limits are not subscription weekly meters. The manager owns
		// the final credential's own cooldown and there is no further fallback.
		return
	}
	if result.Success {
		// A delayed in-flight success does not cancel a newer credential quota
		// rejection; the manager likewise retains an active quota deadline.
		if !s.quotaBlockedLocked(result.AuthID) {
			delete(s.quotaBackoffLevel, result.AuthID)
		}
		s.noteResultLocked(result, 0, time.Time{}, false)
		return
	}
	if result.Error == nil {
		return
	}
	credentialQuota := result.Error.HTTPStatus == http.StatusTooManyRequests && result.CredentialScope &&
		result.Error.Code != coreauth.ErrorCodeRequestScoped && result.Error.Code != coreauth.ErrorCodeForceCooldown
	if !credentialQuota {
		s.noteResultLocked(result, 0, time.Time{}, false)
		return
	}
	s.quotaRevision[result.AuthID]++
	// Credential-wide rejection can be a five-hour limit, not an exhausted
	// weekly allocation. Keep that deadline separate from measured weekly usage.
	now := s.now()
	previous := s.quotaBlockedUntil[result.AuthID]
	var cooldown time.Duration
	if result.RetryAfter != nil {
		cooldown = *result.RetryAfter
		if cooldown < 10*time.Second {
			cooldown = 10 * time.Second
		}
	} else {
		// Match the manager's one-second exponential quota backoff, capped at
		// thirty minutes. Concurrent failures reuse a still-live window.
		if previous.After(now) {
			return
		}
		level := s.quotaBackoffLevel[result.AuthID]
		if level >= 11 {
			cooldown = 30 * time.Minute
		} else {
			cooldown = time.Second * time.Duration(1<<level)
			s.quotaBackoffLevel[result.AuthID] = level + 1
		}
	}
	until := now.Add(cooldown)
	if previous.After(until) {
		until = previous
	}
	s.quotaBlockedUntil[result.AuthID] = until
	s.noteResultLocked(result, cooldown, until, result.RetryAfter != nil)
}

func setBackendResultPolicy(manager *coreauth.Manager, selector *backendSeriesSelector) {
	manager.SetResultPolicy(coreauth.ResultPolicyFunc(func(ctx context.Context, result coreauth.Result) coreauth.Result {
		if metadata := result.Options.Metadata; metadata != nil {
			if requestedModel, ok := metadata[coreexecutor.RequestedModelMetadataKey].(string); ok && strings.TrimSpace(requestedModel) != "" {
				result.Model = requestedModel
				result.RouteModel = requestedModel
			}
		}
		// The scheduler underneath is built for pools of API keys and benches a credential after a
		// failure: twelve hours for a 404, thirty minutes for a 403. For three family subscriptions
		// that is wrong: on 2026-10-09 two 404s that Anthropic did not repeat benched a subscription
		// for Opus and, with another in its reserve band and the third out of quota, left the pool
		// with no one to serve, twice in one day. Only two things move work off a subscription: its
		// weekly quota is used up (a 429, which the selector tracks itself) or its login is dead (a
		// 401, which the shared executor refreshes itself). Every other failure is this request's,
		// relayed to the session exactly as Anthropic sent it, for the client to retry or not.
		backendScopeFailureToRequest(&result)
		recordBackendAttempt(ctx, result)
		if result.Success {
			commitBackendRouteAttempt(ctx, result.AuthID)
		} else {
			releaseFailedBackendRouteAttempt(ctx, result.AuthID)
		}
		// MarkResult applies this policy before publishing quota/cooldown state.
		// Advancing here prevents a concurrent pick from observing the old
		// cursor after the active credential has already become unavailable.
		if selector != nil {
			if !result.SkipQuotaObservation {
				if quota, ok := parseBackendWeeklyQuotaHeaders(internallogging.GetResponseHeaders(ctx)); ok {
					selector.observeQuota(result.AuthID, quota)
				}
			}
			selector.OnResult(result)
		}
		return result
	}))
}

// backendScopeFailureToRequest marks a failed result as request-scoped unless it is a dead
// login (401) or a used-up quota (429), so the scheduler does not bench the subscription.
func backendScopeFailureToRequest(result *coreauth.Result) {
	if result == nil || result.Success || result.Error == nil || result.Error.Code != "" {
		return
	}
	switch result.Error.HTTPStatus {
	case http.StatusUnauthorized, http.StatusTooManyRequests:
		return
	}
	result.Error.Code = coreauth.ErrorCodeRequestScoped
}

func setBackendSeriesResultPolicy(manager *coreauth.Manager, selector *backendSeriesSelector) {
	setBackendResultPolicy(manager, selector)
}

func loadBackendWeeklyQuotas(ctx context.Context, manager *coreauth.Manager, selector *backendSeriesSelector, authIDs []string, quotaRequest ClaudeQuotaRequestFunc) {
	if ctx == nil || manager == nil || selector == nil {
		return
	}
	if quotaRequest == nil {
		quotaRequest = manager.HttpRequest
	}
	var group sync.WaitGroup
	for _, authID := range authIDs {
		authID := authID
		group.Add(1)
		go func() {
			defer group.Done()
			revision := selector.quotaPollRevision(authID)
			auth, ok := manager.GetByID(authID)
			if !ok || auth == nil || auth.AuthKind() != coreauth.AuthKindOAuth {
				return
			}
			if !auth.HasValidAccessToken(time.Now()) {
				// Only credential acquisition has a deadline. Usage HTTP/body reads
				// remain controlled by the backend's lifetime cancellation.
				refreshCtx, cancelRefresh := context.WithTimeout(ctx, claudeQuotaDefaultTimeout)
				refreshed, errRefresh := manager.ForceRefreshAuth(refreshCtx, authID)
				cancelRefresh()
				if errRefresh != nil || refreshed == nil {
					selector.noteUsagePoll(authID, "login could not be refreshed for the poll")
					return
				}
				auth = refreshed
			}
			payload, errFetch := fetchClaudeUsage(ctx, auth, quotaRequest)
			var statusErr *claudeUsageStatusError
			if errors.As(errFetch, &statusErr) && statusErr.status == http.StatusTooManyRequests {
				selector.deferUsagePoll(authID, statusErr.retryAfter)
			}
			if errFetch == nil {
				if fiveHour, okFiveHour, errFiveHour := ParseClaudeFiveHourQuota(payload); errFiveHour == nil && okFiveHour {
					selector.observeFiveHourQuota(authID, fiveHour)
				}
			}
			quota, known, errQuota := ClaudeWeeklyQuota{}, false, errFetch
			if errFetch == nil {
				quota, known, errQuota = ParseClaudeWeeklyQuota(payload)
			}
			if errQuota != nil || !known || ctx.Err() != nil {
				if ctx.Err() == nil {
					selector.noteUsagePoll(authID, backendUsagePollWhy(errQuota, known))
				}
				return
			}
			selector.observePolledQuota(authID, backendWeeklyQuota{known: true, used: quota.UsedFraction, resetsAt: quota.ResetsAt}, revision)
		}()
	}
	group.Wait()
}

func (s *backendSelector) Pick(ctx context.Context, provider, model string, opts coreexecutor.Options, auths []*coreauth.Auth) (*coreauth.Auth, error) {
	// The manager's mixed-provider dispatcher also serves single-provider
	// requests, and passes the synthetic label "mixed" to custom selectors.
	// Validate the actual credential below; the dispatch label is not its
	// provider and must not prevent the one pinned account from being used.
	if provider != s.provider && provider != "mixed" {
		return nil, errors.New("inference provider differs from the pinned profile")
	}
	for _, auth := range auths {
		if auth != nil && auth.ID == s.authID && auth.Provider == s.provider {
			return (&coreauth.FillFirstSelector{}).Pick(ctx, s.provider, backendAvailabilityModel(model, opts), opts, []*coreauth.Auth{auth})
		}
	}
	return nil, errors.New("selected inference account is unavailable; no fallback is configured")
}

const backendMaxBodyBytes = 32 << 20

// BackendErrorStage is a fixed, privacy-safe diagnostic category, not an error
// message. It never includes account details, URLs, headers, or provider bodies.
type BackendErrorStage string

const (
	BackendErrorNone               BackendErrorStage = "none"
	BackendErrorInitialization     BackendErrorStage = "initialization"
	BackendErrorCanceled           BackendErrorStage = "canceled"
	BackendErrorTimeout            BackendErrorStage = "timeout"
	BackendErrorRequest            BackendErrorStage = "request"
	BackendErrorClosed             BackendErrorStage = "closed"
	BackendErrorAuthSelection      BackendErrorStage = "auth_selection"
	BackendErrorAuthNotFound       BackendErrorStage = "auth_not_found"
	BackendErrorAuthUnavailable    BackendErrorStage = "auth_unavailable"
	BackendErrorProviderNotFound   BackendErrorStage = "provider_not_found"
	BackendErrorExecutorNotFound   BackendErrorStage = "executor_not_found"
	BackendErrorModelNotFound      BackendErrorStage = "model_not_found"
	BackendErrorCredentialProfile  BackendErrorStage = "credential_profile"
	BackendErrorCredentialIdentity BackendErrorStage = "credential_identity"
	BackendErrorCredentialStore    BackendErrorStage = "credential_store"
	BackendErrorSigning            BackendErrorStage = "signing"
	BackendErrorThinking           BackendErrorStage = "thinking"
	BackendErrorModelSystemTurn    BackendErrorStage = "model_system_turn"
	BackendErrorSystemBlockType    BackendErrorStage = "system_block_type"
	BackendErrorTLS                BackendErrorStage = "tls"
	BackendErrorTransport          BackendErrorStage = "transport"
	// SDK status-bearing errors can be local validation or actual upstream HTTP.
	BackendErrorUpstreamHTTP BackendErrorStage = "http_error"
	BackendErrorResponse     BackendErrorStage = "response"
	BackendErrorInternal     BackendErrorStage = "internal"
)

type backendErrorObservationKey struct{}

// Only the classified enum is retained. Raw errors are never sent to an
// observer callback, HTTP response, response header, or diagnostic log.
type backendErrorObservation struct {
	mu    sync.Mutex
	stage BackendErrorStage
}

func (o *backendErrorObservation) result() BackendErrorStage {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.stage == "" {
		return BackendErrorNone
	}
	return o.stage
}

func observeBackendStage(ctx context.Context, stage BackendErrorStage) {
	if ctx == nil || stage == BackendErrorNone {
		return
	}
	o, _ := ctx.Value(backendErrorObservationKey{}).(*backendErrorObservation)
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.stage == "" {
		o.stage = stage
	}
}

func observeBackendError(ctx context.Context, message *interfaces.ErrorMessage) {
	if message != nil {
		observeBackendStage(ctx, classifyBackendError(message.Error))
	}
}

func classifyBackendError(err error) BackendErrorStage {
	if err == nil {
		return BackendErrorInternal
	}
	if errors.Is(err, context.Canceled) {
		return BackendErrorCanceled
	}
	// Inspect only known local prefixes, with a bounded walk and prefix length.
	// Do not parse upstream bodies or return any portion of an error string.
	for current, depth := err, 0; current != nil && depth < 8; current, depth = errors.Unwrap(current), depth+1 {
		prefix := current.Error()
		if len(prefix) > 128 {
			prefix = prefix[:128]
		}
		switch {
		case strings.HasPrefix(prefix, "invalid_request_error: role 'system' is not supported on this model."):
			return BackendErrorModelSystemTurn
		case strings.HasPrefix(prefix, "invalid_request_error: system.") && strings.Contains(prefix, "Input should be 'text'"):
			return BackendErrorSystemBlockType
		case strings.HasPrefix(prefix, "populate Claude OAuth account profile:"), strings.HasPrefix(prefix, "fetch Claude OAuth profile:"):
			return BackendErrorCredentialProfile
		case strings.HasPrefix(prefix, "ensure Claude credential device pool:"), strings.HasPrefix(prefix, "ensure Claude CLI fingerprint identity:"), strings.HasPrefix(prefix, "apply Claude credential metadata:"), strings.HasPrefix(prefix, "set Claude credential metadata:"):
			return BackendErrorCredentialIdentity
		case strings.HasPrefix(prefix, "finalize Claude CCH:"), strings.HasPrefix(prefix, "insert Claude CCH placeholder:"), strings.HasPrefix(prefix, "prepend Claude CCH billing block:"):
			return BackendErrorSigning
		case strings.HasPrefix(prefix, "claude tls:"):
			return BackendErrorTLS
		case strings.HasPrefix(prefix, "cannot serialize refreshed inference credential"), strings.HasPrefix(prefix, "cannot stage refreshed inference credential"), strings.HasPrefix(prefix, "cannot write refreshed inference credential"), strings.HasPrefix(prefix, "cannot sync refreshed inference credential"), strings.HasPrefix(prefix, "cannot close refreshed inference credential"), strings.HasPrefix(prefix, "cannot commit refreshed inference credential"), strings.HasPrefix(prefix, "inference credential store is closed"), strings.HasPrefix(prefix, "unsupported inference credential storage"), strings.HasPrefix(prefix, "inference credential does not match"), strings.HasPrefix(prefix, "inference credential has an invalid persistence"), strings.HasPrefix(prefix, "selected credential file is no longer safe"), strings.HasPrefix(prefix, "inference auth directory is no longer private"), strings.HasPrefix(prefix, "refreshed credential was committed but"):
			return BackendErrorCredentialStore
		case strings.HasPrefix(prefix, "selected inference account is unavailable;"),
			strings.HasPrefix(prefix, "inference provider differs from the pinned profile"),
			strings.HasPrefix(prefix, "inference provider differs from the ordered profiles"),
			strings.HasPrefix(prefix, "active ordered inference account is unavailable;"),
			strings.HasPrefix(prefix, "ordered inference subscriptions are exhausted;"):
			return BackendErrorAuthSelection
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return BackendErrorTimeout
	}
	var thinkingErr *thinking.ThinkingError
	if errors.As(err, &thinkingErr) {
		return BackendErrorThinking
	}
	var networkErr net.Error
	if errors.As(err, &networkErr) {
		return BackendErrorTransport
	}
	var authErr *coreauth.Error
	if errors.As(err, &authErr) && authErr != nil {
		switch authErr.Code {
		case "auth_not_found":
			return BackendErrorAuthNotFound
		case "auth_unavailable":
			return BackendErrorAuthUnavailable
		case "provider_not_found":
			return BackendErrorProviderNotFound
		case "executor_not_found":
			return BackendErrorExecutorNotFound
		case "model_not_found":
			return BackendErrorModelNotFound
		}
	}
	var statusErr interface{ StatusCode() int }
	if errors.As(err, &statusErr) && statusErr.StatusCode() >= 400 && statusErr.StatusCode() <= 599 {
		return BackendErrorUpstreamHTTP
	}
	return BackendErrorInternal
}

func newBackendHandler(lifetime context.Context, opts BackendOptions, base *handlers.BaseAPIHandler) http.Handler {
	router := gin.New()
	dispatch := func(c *gin.Context) {
		started := time.Now()
		metered := newMeteredWriter(c.Writer)
		c.Writer = metered
		var raw []byte
		var model string
		var isCount, isStream bool
		ctx, cancel := context.WithCancel(c.Request.Context())
		// Registered first, so it runs LAST: after the request's own cleanup the chosen account and the
		// upstream headers are final.
		defer func() { finishRequest(c, ctx, opts, metered, started, raw, model, isCount, isStream) }()
		ctx = withBackendAttempt(ctx)
		ctx = coreexecutor.WithNativeClaudeUpstreamSuccessCallback(ctx, commitBackendRouteAttempt)
		ctx = coreexecutor.WithNativeClaudeResponseStatusHolder(ctx)
		stop := context.AfterFunc(lifetime, cancel)
		defer func() { stop(); cancel(); releaseBackendRouteAttempt(ctx) }()
		ctx = internallogging.WithResponseHeadersHolder(ctx)
		if lifetime.Err() != nil || ctx.Err() != nil {
			observeBackendStage(ctx, BackendErrorClosed)
			backendError(c.Writer, http.StatusServiceUnavailable, "inference backend is closed")
			return
		}
		raw, model, err := prepareBackendRequest(c.Request, opts)
		if err != nil {
			observeBackendStage(ctx, BackendErrorRequest)
			backendError(c.Writer, http.StatusBadRequest, "invalid inference request")
			return
		}
		// GetContextWithCancel in the general Claude handler starts from a
		// background context. Call its execution helpers directly so our
		// selection metadata and cancellation remain request-scoped.
		if opts.AuthID != "" {
			ctx = handlers.WithPinnedAuthID(ctx, opts.AuthID)
		}
		ctx = context.WithValue(ctx, "gin", c)
		if c.Request.URL.Path == "/v1/messages/count_tokens" {
			isCount = true
			backendSetCountRequest(ctx)
			if opts.UseRequestModel {
				response, errMsg := base.CountProtocolWithAuthManager(ctx, backendProtocolRequest(model, raw, false))
				observeBackendError(ctx, errMsg)
				writeBackendNativeHeaders(c.Writer.Header(), response.Headers)
				if errMsg == nil {
					writeBackendNativeSuccessStatus(ctx, c.Writer)
				}
				writeBackendNativeResult(ctx, c.Writer, response.Body, errMsg)
				return
			}
			payload, headers, errMsg := base.ExecuteCountWithAuthManager(ctx, "claude", model, raw, "")
			observeBackendError(ctx, errMsg)
			writeBackendProtocolHeaders(c.Writer.Header(), headers)
			writeBackendResult(ctx, c.Writer, payload, errMsg)
			return
		}
		var envelope struct {
			Stream bool `json:"stream"`
		}
		_ = json.Unmarshal(raw, &envelope)
		isStream = envelope.Stream
		if envelope.Stream {
			streamBackendResponse(ctx, c.Writer, base, model, raw, opts.UseRequestModel)
			return
		}
		if opts.UseRequestModel {
			response, errMsg := base.ExecuteProtocolWithAuthManager(ctx, backendProtocolRequest(model, raw, false))
			observeBackendError(ctx, errMsg)
			writeBackendNativeHeaders(c.Writer.Header(), response.Headers)
			if errMsg == nil {
				writeBackendNativeSuccessStatus(ctx, c.Writer)
			}
			writeBackendNativeResult(ctx, c.Writer, response.Body, errMsg)
			return
		}
		payload, headers, errMsg := base.ExecuteWithAuthManager(ctx, "claude", model, raw, "")
		observeBackendError(ctx, errMsg)
		writeBackendProtocolHeaders(c.Writer.Header(), headers)
		writeBackendResult(ctx, c.Writer, payload, errMsg)
	}
	router.POST("/v1/messages", dispatch)
	router.POST("/v1/messages/count_tokens", dispatch)
	return router
}

func backendProtocolRequest(model string, raw []byte, stream bool) handlers.ProtocolExecutionRequest {
	return handlers.ProtocolExecutionRequest{
		EntryProtocol:      "claude",
		ExitProtocol:       "claude",
		ForcedProvider:     "claude",
		AuthSelectionModel: backendAuthSelectionModel,
		Model:              model,
		Stream:             stream,
		Body:               raw,
	}
}

func prepareBackendRequest(request *http.Request, opts BackendOptions) ([]byte, string, error) {
	if encoding := request.Header.Get("Content-Encoding"); encoding != "" && encoding != "identity" {
		return nil, "", errors.New("unsupported request encoding")
	}
	if request.Body == nil {
		return nil, "", errors.New("missing request body")
	}
	raw, err := io.ReadAll(io.LimitReader(request.Body, backendMaxBodyBytes+1))
	if err != nil || len(raw) > backendMaxBodyBytes {
		return nil, "", errors.New("cannot read bounded inference body")
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil || body == nil {
		return nil, "", errors.New("inference body must be a JSON object")
	}
	if stream, exists := body["stream"]; exists {
		var flag bool
		if err := json.Unmarshal(stream, &flag); err != nil || string(stream) == "null" {
			return nil, "", errors.New("stream must be a boolean")
		}
	}
	model := opts.Model
	if opts.UseRequestModel {
		rawModel, exists := body["model"]
		if !exists || json.Unmarshal(rawModel, &model) != nil || strings.TrimSpace(model) == "" {
			return nil, "", errors.New("model must be a non-empty string")
		}
	} else if strings.TrimSpace(model) == "" {
		return nil, "", errors.New("pinned model is required")
	}
	if !opts.UseRequestModel {
		body["model"], _ = json.Marshal(model)
	}
	if rawMetadata, exists := body["metadata"]; exists {
		var metadata map[string]json.RawMessage
		if err := json.Unmarshal(rawMetadata, &metadata); err != nil || metadata == nil {
			return nil, "", errors.New("metadata must be a JSON object")
		}
		if !opts.UseRequestModel {
			delete(metadata, "user_id")
			if len(metadata) == 0 {
				delete(body, "metadata")
			} else {
				body["metadata"], _ = json.Marshal(metadata)
			}
		}
	}
	// Keep reviewed native protocol headers, never the native master's auth or
	// transport identity. Repeat the boundary for direct Backend.Handler callers.
	header := coreexecutor.NativeClaudeProtocolHeaders(request.Header)
	if opts.UseRequestModel {
		if strings.TrimSpace(header.Get("X-Claude-Code-Session-Id")) == "" {
			if sessionID := cliproxysession.ClaudeMetadataSessionID(raw); sessionID != "" {
				header.Set("X-Claude-Code-Session-Id", sessionID)
			}
		}
	} else {
		header.Del("X-Claude-Code-Session-Id")
	}
	header.Set("Content-Type", "application/json")
	request.Header = header
	request.URL.RawQuery = ""
	request.URL.User = nil
	request.Host = ""
	clean := raw
	if opts.UseRequestModel {
		if target, mapped := opts.ModelMap[model]; mapped && target != model {
			if errUnique := validateBackendMappedModel(raw); errUnique != nil {
				return nil, "", errUnique
			}
			if strings.TrimSpace(target) == "" {
				return nil, "", errors.New("mapped model must be a non-empty string")
			}
			clean, err = sjson.SetBytes(raw, "model", target)
			if err != nil {
				return nil, "", errors.New("cannot apply model mapping")
			}
			model = target
		}
	}
	if !opts.UseRequestModel {
		clean, err = json.Marshal(body)
		if err != nil {
			return nil, "", err
		}
	}
	// Unmapped native requests keep the validated body byte-for-byte. The Claude OAuth
	// executor replaces metadata.user_id account_uuid and device_id with the
	// selected credential at its final upstream boundary while retaining the
	// native conversation session.
	request.Body = io.NopCloser(bytes.NewReader(clean))
	request.ContentLength = int64(len(clean))
	return clean, model, nil
}

// JSON readers disagree on which duplicate member wins. A model edit must not
// leave a second model behind that differs from the execution/selection model.
func validateBackendMappedModel(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if _, err := decoder.Token(); err != nil {
		return errors.New("cannot inspect mapped model")
	}
	seen := false
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return errors.New("cannot inspect mapped model")
		}
		if key == "model" {
			if seen {
				return errors.New("mapped request must have one model member")
			}
			seen = true
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return errors.New("cannot inspect mapped model")
		}
	}
	return nil
}

func writeBackendResult(ctx context.Context, w http.ResponseWriter, payload []byte, errMsg *interfaces.ErrorMessage) {
	if errMsg != nil {
		writeBackendUpstreamError(ctx, w, errMsg)
		return
	}
	if len(payload) >= 2 && payload[0] == 0x1f && payload[1] == 0x8b {
		reader, err := gzip.NewReader(bytes.NewReader(payload))
		if err != nil {
			backendError(w, http.StatusBadGateway, "invalid compressed inference response")
			return
		}
		decoded, errRead := io.ReadAll(io.LimitReader(reader, backendMaxBodyBytes+1))
		errClose := reader.Close()
		if errRead != nil || errClose != nil || len(decoded) > backendMaxBodyBytes {
			backendError(w, http.StatusBadGateway, "invalid compressed inference response")
			return
		}
		payload = decoded
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json")
	}
	_, _ = w.Write(payload)
}

func writeBackendNativeResult(ctx context.Context, w http.ResponseWriter, payload []byte, errMsg *interfaces.ErrorMessage) {
	if errMsg != nil {
		writeBackendNativeUpstreamError(ctx, w, errMsg)
		return
	}
	_, _ = w.Write(payload)
}

func writeBackendNativeSuccessStatus(ctx context.Context, w http.ResponseWriter) {
	if status := coreexecutor.NativeClaudeResponseStatusFromContext(ctx); status >= http.StatusOK && status < http.StatusMultipleChoices {
		w.WriteHeader(status)
	}
}

func streamBackendResponse(ctx context.Context, w http.ResponseWriter, base *handlers.BaseAPIHandler, model string, raw []byte, useRequestModel bool) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		observeBackendStage(ctx, BackendErrorInternal)
		backendError(w, http.StatusInternalServerError, "streaming is unavailable")
		return
	}
	if useRequestModel {
		streamBackendProtocolResponse(ctx, w, flusher, base, model, raw)
		return
	}
	data, headers, errs := base.ExecuteStreamWithAuthManager(ctx, "claude", model, raw, "")
	started := false
	for data != nil || errs != nil {
		select {
		case <-ctx.Done():
			return
		case errMsg, open := <-errs:
			if !open {
				errs = nil
				continue
			}
			if errMsg == nil {
				continue
			}
			observeBackendError(ctx, errMsg)
			if !started {
				writeBackendUpstreamError(ctx, w, errMsg)
			}
			return
		case chunk, open := <-data:
			if !open {
				data = nil
				continue
			}
			if len(chunk) == 0 {
				continue
			}
			if !started {
				writeBackendProtocolHeaders(w.Header(), headers)
				if w.Header().Get("Content-Type") == "" {
					w.Header().Set("Content-Type", "text/event-stream")
				}
				if w.Header().Get("Cache-Control") == "" {
					w.Header().Set("Cache-Control", "no-cache")
				}
				started = true
			}
			if _, err := w.Write(chunk); err != nil {
				return
			}
			flusher.Flush()
		}
	}
	if !started {
		observeBackendStage(ctx, BackendErrorResponse)
		backendError(w, http.StatusBadGateway, "selected inference account returned an empty stream")
	}
}

func streamBackendProtocolResponse(ctx context.Context, w http.ResponseWriter, flusher http.Flusher, base *handlers.BaseAPIHandler, model string, raw []byte) {
	stream, errMsg := base.ExecuteProtocolStreamWithAuthManager(ctx, backendProtocolRequest(model, raw, true))
	if errMsg != nil {
		observeBackendError(ctx, errMsg)
		writeBackendNativeUpstreamError(ctx, w, errMsg)
		return
	}
	started := false
	if _, native := coreexecutor.NativeClaudeProtocolHeadersFromContext(ctx); native {
		// The native executor has already received successful upstream headers.
		// Forward them now rather than waiting through a long first body read.
		writeBackendNativeHeaders(w.Header(), stream.Headers)
		writeBackendNativeSuccessStatus(ctx, w)
		started = true
		flusher.Flush()
	}
	for {
		select {
		case <-ctx.Done():
			return
		case chunk, open := <-stream.Chunks:
			if !open {
				if !started {
					observeBackendStage(ctx, BackendErrorResponse)
					backendError(w, http.StatusBadGateway, "selected inference account returned an empty stream")
				}
				return
			}
			if len(chunk.Payload) > 0 {
				if !started {
					writeBackendNativeHeaders(w.Header(), stream.Headers)
					if w.Header().Get("Content-Type") == "" {
						w.Header().Set("Content-Type", "text/event-stream")
					}
					if w.Header().Get("Cache-Control") == "" {
						w.Header().Set("Cache-Control", "no-cache")
					}
					started = true
				}
				if _, err := w.Write(chunk.Payload); err != nil {
					return
				}
				flusher.Flush()
			}
			if chunk.Err != nil {
				streamErr := &interfaces.ErrorMessage{StatusCode: chunk.Err.StatusCode, Error: chunk.Err, Addon: chunk.Err.Headers}
				observeBackendError(ctx, streamErr)
				if !started {
					writeBackendNativeUpstreamError(ctx, w, streamErr)
				}
				return
			}
		}
	}
}

func writeBackendUpstreamError(ctx context.Context, w http.ResponseWriter, errMsg *interfaces.ErrorMessage) {
	if status, headers, body, ok := backendDirectErrorResponse(errMsg); ok {
		writeBackendProtocolHeaders(w.Header(), headers)
		w.WriteHeader(status)
		_, _ = w.Write(body)
		return
	}
	if errMsg == nil {
		writeBackendFailure(ctx, w, http.StatusBadGateway, writeBackendProtocolHeaders)
		return
	}
	if errMsg.StatusCode == clienterror.StatusClientClosedRequest {
		writeBackendCancelled(w)
		return
	}
	writeBackendProtocolHeaders(w.Header(), errMsg.Addon)
	status := errMsg.StatusCode
	if status < 400 || status > 599 {
		status = http.StatusBadGateway
	}
	writeBackendFailure(ctx, w, status, writeBackendProtocolHeaders)
}

func writeBackendNativeUpstreamError(ctx context.Context, w http.ResponseWriter, errMsg *interfaces.ErrorMessage) {
	if status, headers, body, ok := backendDirectErrorResponse(errMsg); ok {
		writeBackendNativeHeaders(w.Header(), headers)
		w.WriteHeader(status)
		_, _ = w.Write(body)
		return
	}
	if errMsg == nil {
		writeBackendFailure(ctx, w, http.StatusBadGateway, writeBackendNativeHeaders)
		return
	}
	if errMsg.StatusCode == clienterror.StatusClientClosedRequest {
		writeBackendCancelled(w)
		return
	}
	writeBackendNativeHeaders(w.Header(), errMsg.Addon)
	status := errMsg.StatusCode
	if status < 400 || status > 599 {
		status = http.StatusBadGateway
	}
	writeBackendFailure(ctx, w, status, writeBackendNativeHeaders)
}

func backendDirectErrorResponse(errMsg *interfaces.ErrorMessage) (int, http.Header, []byte, bool) {
	if errMsg == nil {
		return 0, nil, nil, false
	}
	if errMsg.DirectResponse || errMsg.Body != nil || errMsg.Headers != nil {
		return backendDirectErrorStatus(errMsg.StatusCode), errMsg.Headers, errMsg.Body, true
	}

	var terminated *coreexecutor.RequestTerminatedError
	if errors.As(errMsg.Error, &terminated) && terminated != nil {
		return backendDirectErrorStatus(terminated.StatusCode()), terminated.ResponseHeaders(), terminated.ResponseBody(), true
	}

	type directResponseError interface {
		DirectResponse() bool
		ResponseBody() []byte
	}
	var direct directResponseError
	if !errors.As(errMsg.Error, &direct) || direct == nil || !direct.DirectResponse() {
		return 0, nil, nil, false
	}
	status := errMsg.StatusCode
	var statusProvider interface{ StatusCode() int }
	if errors.As(errMsg.Error, &statusProvider) && statusProvider != nil {
		status = statusProvider.StatusCode()
	}
	var headers http.Header
	var headersProvider interface{ ResponseHeaders() http.Header }
	if errors.As(errMsg.Error, &headersProvider) && headersProvider != nil {
		headers = headersProvider.ResponseHeaders()
	}
	return backendDirectErrorStatus(status), headers, direct.ResponseBody(), true
}

func backendDirectErrorStatus(status int) int {
	if status < 100 || status > 599 {
		return http.StatusBadGateway
	}
	return status
}

// Native Claude responses are relayed as raw representation bytes. Preserve
// every end-to-end upstream header, including representation framing, while
// removing only hop-by-hop fields and headers nominated by Connection.
func writeBackendNativeHeaders(dst, src http.Header) {
	if src == nil {
		return
	}
	connectionScoped := make(map[string]struct{})
	for _, value := range src.Values("Connection") {
		for _, token := range strings.Split(value, ",") {
			if name := http.CanonicalHeaderKey(strings.TrimSpace(token)); name != "" {
				connectionScoped[name] = struct{}{}
			}
		}
	}
	for key := range connectionScoped {
		dst.Del(key)
	}
	for _, key := range []string{
		"Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Proxy-Connection",
		"Te", "Trailer", "Transfer-Encoding", "Upgrade",
	} {
		dst.Del(key)
	}
	for key, values := range src {
		canonical := http.CanonicalHeaderKey(key)
		if backendNativeResponseHeaderBlocked(canonical) {
			continue
		}
		if _, blocked := connectionScoped[canonical]; blocked {
			continue
		}
		dst.Del(canonical)
		for _, value := range values {
			dst.Add(canonical, value)
		}
	}
}

func backendNativeResponseHeaderBlocked(key string) bool {
	switch key {
	case "Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Proxy-Connection",
		"Te", "Trailer", "Transfer-Encoding", "Upgrade":
		return true
	default:
		return false
	}
}

// The non-native backend path receives decoded bodies and keeps its existing
// narrow protocol boundary.
func writeBackendProtocolHeaders(dst, src http.Header) {
	filtered := handlers.FilterUpstreamHeaders(src)
	retryAfter := filtered.Get("Retry-After")
	if seconds, err := strconv.ParseUint(retryAfter, 10, 32); err == nil {
		dst.Set("Retry-After", strconv.FormatUint(seconds, 10))
	} else if date, err := http.ParseTime(retryAfter); err == nil {
		dst.Set("Retry-After", date.UTC().Format(http.TimeFormat))
	}
	for key, values := range filtered {
		lower := strings.ToLower(key)
		if lower == "request-id" || lower == "x-request-id" || lower == "x-should-retry" ||
			lower == "retry-after-ms" || strings.HasPrefix(lower, "anthropic-ratelimit-") {
			dst[http.CanonicalHeaderKey(key)] = append([]string(nil), values...)
		}
	}
}

func backendError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `{"type":"error","error":{"type":"api_error","message":%q}}`, message)
}
