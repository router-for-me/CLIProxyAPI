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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	cliproxysession "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/session"
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
}

// BackendCredential identifies one independently authenticated profile in an
// explicitly ordered inference series. Credential values remain in AuthDir.
type BackendCredential struct {
	AuthDir  string
	Provider string
	AuthID   string
}

// BackendSeriesOptions drains Claude Credentials in order. The native request
// chooses the model. A series advances only after a structured provider result
// confirms that the active subscription is drained.
type BackendSeriesOptions struct {
	Credentials []BackendCredential
}

// Backend embeds only inference and credential refresh. It has no listener,
// management API, config watcher, global token store, or update/download service.
// The caller must hold the profile's exclusive lock until Close completes.
// Stop accepting requests and join HTTP handlers before calling Close; the
// launcher does this by closing and joining its proxy first.
type Backend struct {
	handler http.Handler
	manager *coreauth.Manager
	store   backendCredentialStore
	authIDs []string
	cancel  context.CancelFunc
	once    sync.Once
}

// NewBackend loads one profile, then starts its account-local refresh loop.
func NewBackend(ctx context.Context, opts BackendOptions) (*Backend, error) {
	return newBackend(ctx, []BackendCredential{{AuthDir: opts.AuthDir, Provider: opts.Provider, AuthID: opts.AuthID}}, opts.Model, opts.UseRequestModel, true)
}

// NewBackendSeries loads an explicitly ordered set of profiles. The caller
// must hold every profile lock until Close completes.
func NewBackendSeries(ctx context.Context, opts BackendSeriesOptions) (*Backend, error) {
	if len(opts.Credentials) < 2 {
		return nil, errors.New("ordered inference requires at least two profiles")
	}
	if opts.Credentials[0].Provider != "claude" {
		return nil, errors.New("ordered inference currently supports Claude profiles only")
	}
	return newBackend(ctx, opts.Credentials, "", true, false)
}

func newBackend(ctx context.Context, credentials []BackendCredential, modelName string, useRequestModel, pinSingle bool) (*Backend, error) {
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
	for i, credentialOpts := range credentials {
		if credentialOpts.Provider != provider {
			return nil, errors.New("ordered inference profiles must use one provider")
		}
		opts := BackendOptions{AuthDir: credentialOpts.AuthDir, Provider: credentialOpts.Provider, AuthID: credentialOpts.AuthID, Model: modelName, UseRequestModel: useRequestModel}
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
			accountUUID, _ := credential.Metadata["account_uuid"].(string)
			accountUUID = strings.TrimSpace(accountUUID)
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
	}

	store := newBackendStoreSet(stores)
	var selector coreauth.Selector
	var seriesSelector *backendSeriesSelector
	pinnedAuthID := ""
	if pinSingle {
		pinnedAuthID = authIDs[0]
		selector = &backendSelector{authID: pinnedAuthID, provider: provider}
	} else {
		seriesSelector = &backendSeriesSelector{authIDs: append([]string(nil), authIDs...), provider: provider}
		selector = seriesSelector
	}
	cfg := backendConfig(credentials[0].AuthDir)
	cfg.MaxRetryCredentials = len(credentials)
	manager := coreauth.NewManager(store, selector, nil)
	manager.SetConfig(cfg)
	manager.SetRetryConfig(0, 0, len(credentials))
	if useRequestModel || seriesSelector != nil {
		setBackendResultPolicy(manager, seriesSelector)
	}
	switch provider {
	case "claude":
		manager.RegisterExecutor(runtimeexecutor.NewClaudeExecutor(cfg))
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
			store.seal()
			return nil, errors.New("cannot register the selected inference credentials")
		}
	}
	runCtx, cancel := context.WithCancel(ctx)
	backend := &Backend{
		manager: manager,
		store:   store,
		authIDs: authIDs,
		cancel:  cancel,
	}
	backend.handler = newBackendHandler(runCtx, BackendOptions{
		Provider: provider, AuthID: pinnedAuthID, Model: modelName,
		UseRequestModel: useRequestModel,
	}, handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager))
	manager.StartAutoRefresh(runCtx, 15*time.Minute)
	return backend, nil
}

func (b *Backend) Handler() http.Handler { return b.handler }

func (b *Backend) Close() error {
	if b == nil {
		return nil
	}
	b.once.Do(func() {
		b.cancel()
		b.manager.StopAutoRefreshAndWait()
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
	// Native request detection compares the client version against this
	// baseline. Keep it aligned with the launcher version we already verified.
	cfg.ClaudeHeaderDefaults.UserAgent = "claude-cli/" + NativeClaudeVersion + " (external, cli)"
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
	mu        sync.Mutex
	closed    bool
	opts      BackendOptions
	path      string
	runtimeID string
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
	s.mu.Lock()
	defer s.mu.Unlock()
	_, auth, err := loadBackendCredentialAs(ctx, s.opts, s.runtimeID)
	if err != nil {
		return nil, err
	}
	return []*coreauth.Auth{auth}, nil
}

func (s *backendStore) Save(_ context.Context, auth *coreauth.Auth) (string, error) {
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
	raw, err := json.Marshal(metadata)
	if err != nil {
		return "", errors.New("cannot serialize refreshed inference credential")
	}
	// Once OAuth has rotated a token, finish its durable write even if the
	// request context was canceled. Backend.Close joins the refresh workers
	// before the launcher releases its exclusive profile lock.
	temp, err := os.CreateTemp(s.opts.AuthDir, ".refresh-*")
	if err != nil {
		return "", errors.New("cannot stage refreshed inference credential")
	}
	tempPath := temp.Name()
	defer func() {
		// Best-effort cleanup also covers errors before rename; after a
		// successful rename the temporary name no longer exists.
		_ = temp.Close()
		_ = os.Remove(tempPath)
	}()
	if n, errWrite := temp.Write(raw); errWrite != nil || n != len(raw) {
		return "", errors.New("cannot write refreshed inference credential")
	}
	if errSync := temp.Sync(); errSync != nil {
		return "", errors.New("cannot sync refreshed inference credential")
	}
	if errClose := temp.Close(); errClose != nil {
		return "", errors.New("cannot close refreshed inference credential")
	}
	if errRename := os.Rename(tempPath, s.path); errRename != nil {
		return "", errors.New("cannot commit refreshed inference credential")
	}
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

// backendSeriesSelector exposes only the current profile and advances its
// process-local cursor after a confirmed shared subscription quota rejection.
// The cursor never wraps and stale concurrent results cannot skip profiles.
type backendSeriesSelector struct {
	mu       sync.Mutex
	authIDs  []string
	provider string
	active   int
}

func (s *backendSeriesSelector) Pick(ctx context.Context, provider, model string, opts coreexecutor.Options, auths []*coreauth.Auth) (*coreauth.Auth, error) {
	if provider != s.provider && provider != "mixed" {
		return nil, errors.New("inference provider differs from the ordered profiles")
	}
	s.mu.Lock()
	if s.active >= len(s.authIDs) {
		s.mu.Unlock()
		return nil, errors.New("ordered inference subscriptions are exhausted; no fallback is configured")
	}
	authID := s.authIDs[s.active]
	s.mu.Unlock()
	for _, auth := range auths {
		if auth != nil && auth.ID == authID && auth.Provider == s.provider {
			return (&coreauth.FillFirstSelector{}).Pick(ctx, s.provider, backendAvailabilityModel(model, opts), opts, []*coreauth.Auth{auth})
		}
	}
	return nil, errors.New("active ordered inference account is unavailable; no fallback is configured")
}

func backendAvailabilityModel(selectionModel string, opts coreexecutor.Options) string {
	if requestedModel, ok := opts.Metadata[coreexecutor.RequestedModelMetadataKey].(string); ok {
		if requestedModel = strings.TrimSpace(requestedModel); requestedModel != "" {
			return requestedModel
		}
	}
	return selectionModel
}

func (s *backendSeriesSelector) OnResult(result coreauth.Result) {
	if result.Success || result.Error == nil || result.Error.HTTPStatus != http.StatusTooManyRequests ||
		!result.CredentialScope || result.Error.Code == coreauth.ErrorCodeRequestScoped ||
		result.Error.Code == coreauth.ErrorCodeForceCooldown {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active < len(s.authIDs) && result.AuthID == s.authIDs[s.active] {
		s.active++
	}
}

func setBackendResultPolicy(manager *coreauth.Manager, selector *backendSeriesSelector) {
	manager.SetResultPolicy(coreauth.ResultPolicyFunc(func(_ context.Context, result coreauth.Result) coreauth.Result {
		if metadata := result.Options.Metadata; metadata != nil {
			if requestedModel, ok := metadata[coreexecutor.RequestedModelMetadataKey].(string); ok && strings.TrimSpace(requestedModel) != "" {
				result.Model = requestedModel
				result.RouteModel = requestedModel
			}
		}
		// MarkResult applies this policy before publishing quota/cooldown state.
		// Advancing here prevents a concurrent pick from observing the old
		// cursor after the active credential has already become unavailable.
		if selector != nil {
			selector.OnResult(result)
		}
		return result
	}))
}

func setBackendSeriesResultPolicy(manager *coreauth.Manager, selector *backendSeriesSelector) {
	setBackendResultPolicy(manager, selector)
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
		ctx, cancel := context.WithCancel(c.Request.Context())
		stop := context.AfterFunc(lifetime, cancel)
		defer func() { stop(); cancel() }()
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
			if opts.UseRequestModel {
				response, errMsg := base.CountProtocolWithAuthManager(ctx, backendProtocolRequest(model, raw, false))
				observeBackendError(ctx, errMsg)
				writeBackendProtocolHeaders(c.Writer.Header(), response.Headers)
				writeBackendResult(c.Writer, response.Body, errMsg)
				return
			}
			payload, headers, errMsg := base.ExecuteCountWithAuthManager(ctx, "claude", model, raw, "")
			observeBackendError(ctx, errMsg)
			writeBackendProtocolHeaders(c.Writer.Header(), headers)
			writeBackendResult(c.Writer, payload, errMsg)
			return
		}
		var envelope struct {
			Stream bool `json:"stream"`
		}
		_ = json.Unmarshal(raw, &envelope)
		if envelope.Stream {
			streamBackendResponse(ctx, c.Writer, base, model, raw, opts.UseRequestModel)
			return
		}
		if opts.UseRequestModel {
			response, errMsg := base.ExecuteProtocolWithAuthManager(ctx, backendProtocolRequest(model, raw, false))
			observeBackendError(ctx, errMsg)
			writeBackendProtocolHeaders(c.Writer.Header(), response.Headers)
			writeBackendResult(c.Writer, response.Body, errMsg)
			return
		}
		payload, headers, errMsg := base.ExecuteWithAuthManager(ctx, "claude", model, raw, "")
		observeBackendError(ctx, errMsg)
		writeBackendProtocolHeaders(c.Writer.Header(), headers)
		writeBackendResult(c.Writer, payload, errMsg)
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
	if !opts.UseRequestModel {
		clean, err = json.Marshal(body)
		if err != nil {
			return nil, "", err
		}
	}
	// Native mode keeps the validated body byte-for-byte. The Claude OAuth
	// executor replaces metadata.user_id account_uuid and device_id with the
	// selected credential at its final upstream boundary while retaining the
	// native conversation session.
	request.Body = io.NopCloser(bytes.NewReader(clean))
	request.ContentLength = int64(len(clean))
	return clean, model, nil
}

func writeBackendResult(w http.ResponseWriter, payload []byte, errMsg *interfaces.ErrorMessage) {
	if errMsg != nil {
		writeBackendUpstreamError(w, errMsg)
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
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(payload)
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
				writeBackendUpstreamError(w, errMsg)
			} else {
				_, _ = io.WriteString(w, "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"api_error\",\"message\":\"Configured inference failed; no fallback to the native master was attempted\"}}\n\n")
				flusher.Flush()
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
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("Cache-Control", "no-cache")
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
		writeBackendUpstreamError(w, errMsg)
		return
	}
	started := false
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
			if chunk.Err != nil {
				streamErr := &interfaces.ErrorMessage{StatusCode: chunk.Err.StatusCode, Error: chunk.Err, Addon: chunk.Err.Headers}
				observeBackendError(ctx, streamErr)
				if !started {
					writeBackendUpstreamError(w, streamErr)
				} else {
					_, _ = io.WriteString(w, "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"api_error\",\"message\":\"Configured inference failed; no fallback to the native master was attempted\"}}\n\n")
					flusher.Flush()
				}
				return
			}
			if len(chunk.Payload) == 0 {
				continue
			}
			if !started {
				writeBackendProtocolHeaders(w.Header(), stream.Headers)
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("Cache-Control", "no-cache")
				started = true
			}
			if _, err := w.Write(chunk.Payload); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func writeBackendUpstreamError(w http.ResponseWriter, errMsg *interfaces.ErrorMessage) {
	writeBackendProtocolHeaders(w.Header(), errMsg.Addon)
	status := errMsg.StatusCode
	if status < 400 || status > 599 {
		status = http.StatusBadGateway
	}
	backendError(w, status, "Configured inference failed; no fallback to the native master was attempted")
}

// Native uses these headers for retries and usage reporting. Authentication,
// cookies, account identifiers, framing and compression are never forwarded.
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
