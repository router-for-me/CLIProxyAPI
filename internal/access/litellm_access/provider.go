// Package litellmaccess implements the on-the-fly LiteLLM access provider. It
// accepts a client API key issued by an external LiteLLM instance, validates it
// with GET /key/info (self-key), resolves the matching local litellm_api_keys
// row by token hash, and upserts the key's identity + raw metadata into the
// runtime api_keys table so subsequent requests authenticate locally. It runs
// after the pg-store provider and is local-first: a key already present and
// active in api_keys never triggers an external call.
package litellmaccess

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
)

// Identifier is surfaced via Result.Provider when this provider authenticates.
const Identifier = "litellm-api-key"

const (
	maxInfoBodyBytes     = 64 << 10
	maxInfoMetadataBytes = 32 << 10
	cacheCapacity        = 4096
	defaultTimeout       = 5 * time.Second
	defaultCacheTTL      = 60 * time.Second
	settingsRefresh      = 10 * time.Second
)

// Settings is the subset of the LiteLLM sync settings this provider needs.
type Settings struct {
	Enabled  bool
	BaseURL  string
	CacheTTL time.Duration
	Timeout  time.Duration
}

// SettingsFunc loads the current settings. It is called at most once per
// settingsRefresh window.
type SettingsFunc func(ctx context.Context) (Settings, error)

// PGLookup is the runtime api_keys read/write contract.
type PGLookup interface {
	LookupByHash(ctx context.Context, hash string) (*store.APIKey, *store.Policy, error)
	Upsert(ctx context.Context, k store.APIKey, policy *store.Policy) (store.APIKey, error)
}

// LiteLLMLookup resolves a Manage-LiteLLM key by id (token hash) or key hash.
type LiteLLMLookup interface {
	LookupByID(ctx context.Context, id string) (*store.LiteLLMKey, *store.LiteLLMPolicy, error)
	LookupByHash(ctx context.Context, hash string) (*store.LiteLLMKey, error)
}

// LogStore records an on-the-fly validation outcome.
type LogStore interface {
	RecordOnTheFly(ctx context.Context, e store.OnTheFlyLogEvent) error
}

type candidate struct {
	value  string
	source string
}

type cachedOutcome struct {
	outcome   string
	keyID     string
	userID    string
	expiresAt time.Time
}

// outcomeCache is a small TTL-bounded cache of validation outcomes keyed by key
// hash. When full it first drops expired entries, then one arbitrary entry, so
// its size never exceeds cap.
type outcomeCache struct {
	mu      sync.Mutex
	entries map[string]cachedOutcome
	cap     int
}

func newOutcomeCache(capacity int) *outcomeCache {
	return &outcomeCache{entries: make(map[string]cachedOutcome), cap: capacity}
}

func (o *outcomeCache) get(key string) (cachedOutcome, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	v, ok := o.entries[key]
	return v, ok
}

func (o *outcomeCache) set(key string, v cachedOutcome) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.entries) >= o.cap {
		now := time.Now()
		for k, e := range o.entries {
			if now.After(e.expiresAt) {
				delete(o.entries, k)
			}
		}
		if len(o.entries) >= o.cap {
			for k := range o.entries {
				delete(o.entries, k)
				break
			}
		}
	}
	o.entries[key] = v
}

// Provider validates LiteLLM-issued client keys on the request-auth path.
type Provider struct {
	name     string
	pg       PGLookup
	lite     LiteLLMLookup
	logs     LogStore
	settings SettingsFunc
	client   *http.Client
	outcomes *outcomeCache
	now      func() time.Time

	mu          sync.RWMutex
	settingsVal Settings
	settingsAt  time.Time
}

// New constructs a Provider. Returns nil when a required dependency is nil so
// callers can feature-detect via one nil check.
func New(pg PGLookup, lite LiteLLMLookup, logs LogStore, settings SettingsFunc, client *http.Client) *Provider {
	if pg == nil || lite == nil || settings == nil {
		return nil
	}
	if client == nil {
		client = &http.Client{}
	}
	return &Provider{
		name:     Identifier,
		pg:       pg,
		lite:     lite,
		logs:     logs,
		settings: settings,
		client:   client,
		outcomes: newOutcomeCache(cacheCapacity),
		now:      time.Now,
	}
}

// Identifier returns the provider name surfaced in Result.Provider.
func (p *Provider) Identifier() string {
	if p == nil {
		return ""
	}
	return p.name
}

func (p *Provider) getSettings(ctx context.Context) (Settings, error) {
	p.mu.RLock()
	s, at := p.settingsVal, p.settingsAt
	p.mu.RUnlock()
	if !at.IsZero() && p.now().Sub(at) < settingsRefresh {
		return s, nil
	}
	s, err := p.settings(ctx)
	if err != nil {
		return Settings{}, err
	}
	p.mu.Lock()
	p.settingsVal, p.settingsAt = s, p.now()
	p.mu.Unlock()
	return s, nil
}

// Authenticate extracts a credential, validates it locally then remotely, and
// lazily imports a matched LiteLLM key into the runtime api_keys table.
func (p *Provider) Authenticate(ctx context.Context, r *http.Request) (*sdkaccess.Result, *sdkaccess.AuthError) {
	if p == nil {
		return nil, sdkaccess.NewNotHandledError()
	}
	candidates := extractCandidates(r)
	if len(candidates) == 0 {
		return nil, sdkaccess.NewNoCredentialsError()
	}
	settings, err := p.getSettings(ctx)
	if err != nil {
		return nil, sdkaccess.NewInternalAuthError("litellm access: load settings failed", err)
	}
	if !settings.Enabled || strings.TrimSpace(settings.BaseURL) == "" {
		return nil, sdkaccess.NewNotHandledError()
	}
	timeout := settings.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	ttl := settings.CacheTTL
	if ttl <= 0 {
		ttl = defaultCacheTTL
	}

	rejected := false
	for _, c := range candidates {
		hash := store.HashSecret(c.value)

		// Local-first: an existing active runtime key wins without a remote call.
		key, _, errLookup := p.pg.LookupByHash(ctx, hash)
		if errLookup == nil && key != nil {
			if key.Status == store.APIKeyStatusActive {
				return resultFromKey(c.value, c.source, key.ID, key.UserID), nil
			}
			return nil, sdkaccess.NewInvalidCredentialError()
		}
		if errLookup != nil && !errors.Is(errLookup, store.ErrAPIKeyNotFound) {
			return nil, sdkaccess.NewInternalAuthError("litellm access: local lookup failed", errLookup)
		}

		// Short-lived outcome cache.
		if cached, ok := p.outcomes.get(hash); ok && p.now().Before(cached.expiresAt) {
			switch cached.outcome {
			case store.OnTheFlyOutcomeSynced:
				if k, _, e := p.pg.LookupByHash(ctx, hash); e == nil && k != nil {
					return resultFromKey(c.value, c.source, k.ID, k.UserID), nil
				}
			case store.OnTheFlyOutcomeUnmatched, store.OnTheFlyOutcomeInvalid:
				rejected = true
				continue
			}
		}

		res, authErr := p.validateAndSync(ctx, c, hash, settings.BaseURL, timeout, ttl)
		if authErr == nil {
			return res, nil
		}
		// An upstream/internal failure will not improve on another candidate,
		// so surface it immediately; an invalid credential may be one of
		// several supplied, so try the next one.
		if authErr.Code != sdkaccess.AuthErrorCodeInvalidCredential {
			return nil, authErr
		}
		rejected = true
	}
	if rejected {
		return nil, sdkaccess.NewInvalidCredentialError()
	}
	return nil, sdkaccess.NewNotHandledError()
}

type keyInfoResponse struct {
	Token string         `json:"token"`
	Key   string         `json:"key"`
	Info  map[string]any `json:"info"`
}

func (p *Provider) validateAndSync(ctx context.Context, c candidate, hash, baseURL string, timeout, ttl time.Duration) (*sdkaccess.Result, *sdkaccess.AuthError) {
	start := p.now()
	info, status, err := p.fetchKeyInfo(ctx, baseURL, c.value, timeout)
	latency := p.now().Sub(start).Milliseconds()

	if err != nil {
		p.record(ctx, store.OnTheFlyOutcomeError, c, "", "", latency, err.Error())
		return nil, sdkaccess.NewInternalAuthError("litellm access: validate key failed", err)
	}
	if status >= 500 {
		p.record(ctx, store.OnTheFlyOutcomeError, c, "", "", latency, fmt.Sprintf("litellm returned status %d", status))
		return nil, sdkaccess.NewInternalAuthError("litellm access: upstream error", fmt.Errorf("litellm returned status %d", status))
	}
	if status < 200 || status >= 300 {
		p.cacheOutcome(hash, store.OnTheFlyOutcomeInvalid, "", "", ttl)
		p.record(ctx, store.OnTheFlyOutcomeInvalid, c, "", "", latency, fmt.Sprintf("litellm returned status %d", status))
		return nil, sdkaccess.NewInvalidCredentialError()
	}

	token := firstNonEmpty(info.Token, info.Key, strAny(info.Info, "token"), strAny(info.Info, "key"))
	if token == "" {
		token = hash
	}
	liteKey, _, errLite := p.lite.LookupByID(ctx, token)
	if errLite != nil && errors.Is(errLite, store.ErrLiteLLMKeyNotFound) {
		liteKey, errLite = p.lite.LookupByHash(ctx, hash)
	}
	if errLite != nil || liteKey == nil {
		p.cacheOutcome(hash, store.OnTheFlyOutcomeUnmatched, "", "", ttl)
		p.record(ctx, store.OnTheFlyOutcomeUnmatched, c, token, "", latency, "no matching local litellm key")
		return nil, sdkaccess.NewInvalidCredentialError()
	}

	meta := map[string]any{}
	for k, v := range liteKey.Metadata {
		meta[k] = v
	}
	if info.Info != nil {
		if raw, errMarshal := json.Marshal(info.Info); errMarshal == nil && len(raw) <= maxInfoMetadataBytes {
			var decoded map[string]any
			if json.Unmarshal(raw, &decoded) == nil {
				meta["litellm_info"] = decoded
			}
		}
	}
	now := p.now().UTC()
	runtimeKey := store.APIKey{
		ID:         token,
		Name:       liteKey.Name,
		KeyAlias:   liteKey.KeyAlias,
		KeyHash:    hash,
		KeyPrefix:  store.SecretPrefixOf(c.value),
		Status:     store.APIKeyStatusActive,
		UserID:     liteKey.UserID,
		LastUsedAt: &now,
		Metadata:   meta,
	}
	if _, errUp := p.pg.Upsert(ctx, runtimeKey, nil); errUp != nil {
		p.record(ctx, store.OnTheFlyOutcomeError, c, token, liteKey.UserID, latency, errUp.Error())
		return nil, sdkaccess.NewInternalAuthError("litellm access: persist key failed", errUp)
	}
	p.cacheOutcome(hash, store.OnTheFlyOutcomeSynced, token, liteKey.UserID, ttl)
	p.record(ctx, store.OnTheFlyOutcomeSynced, c, token, liteKey.UserID, latency, "")
	return resultFromKey(c.value, c.source, token, liteKey.UserID), nil
}

// fetchKeyInfo performs GET {baseURL}/key/info. A non-2xx response is returned
// as (nil, status, nil); transport/decoding failures return a non-nil error.
func (p *Provider) fetchKeyInfo(ctx context.Context, baseURL, secret string, timeout time.Duration) (*keyInfoResponse, int, error) {
	endpoint := strings.TrimRight(baseURL, "/") + "/key/info"
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Accept", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			log.WithError(cerr).Debug("litellm access: close key info body failed")
		}
	}()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxInfoBodyBytes))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp.StatusCode, nil
	}
	var out keyInfoResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, resp.StatusCode, fmt.Errorf("litellm access: decode key info: %w", err)
	}
	return &out, resp.StatusCode, nil
}

func (p *Provider) cacheOutcome(hash, outcome, keyID, userID string, ttl time.Duration) {
	if p.outcomes == nil || ttl <= 0 {
		return
	}
	p.outcomes.set(hash, cachedOutcome{outcome: outcome, keyID: keyID, userID: userID, expiresAt: p.now().Add(ttl)})
}

func (p *Provider) record(ctx context.Context, outcome string, c candidate, keyID, userID string, latency int64, errMsg string) {
	if p.logs == nil {
		return
	}
	entry := store.OnTheFlyLogEvent{
		OccurredAt:   p.now().UTC(),
		RequestID:    logging.GetRequestID(ctx),
		Outcome:      outcome,
		KeyPrefix:    store.SecretPrefixOf(c.value),
		KeyID:        keyID,
		UserID:       userID,
		Source:       c.source,
		LatencyMs:    latency,
		ErrorMessage: errMsg,
	}
	go func() {
		cctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := p.logs.RecordOnTheFly(cctx, entry); err != nil {
			log.WithError(err).Warn("litellm access: record on-the-fly log failed")
		}
	}()
}

func resultFromKey(principal, source, keyID, userID string) *sdkaccess.Result {
	meta := map[string]string{"source": source, "key_id": keyID}
	if userID != "" {
		meta["user_id"] = userID
	}
	return &sdkaccess.Result{Provider: Identifier, Principal: principal, Metadata: meta}
}

func extractCandidates(r *http.Request) []candidate {
	authHeader := r.Header.Get("Authorization")
	raw := []candidate{
		{extractBearerToken(authHeader), "authorization"},
		{strings.TrimSpace(r.Header.Get("X-Goog-Api-Key")), "x-goog-api-key"},
		{strings.TrimSpace(r.Header.Get("X-Api-Key")), "x-api-key"},
	}
	if r.URL != nil {
		raw = append(raw,
			candidate{strings.TrimSpace(r.URL.Query().Get("key")), "query-key"},
			candidate{strings.TrimSpace(r.URL.Query().Get("auth_token")), "query-auth-token"},
		)
	}
	out := make([]candidate, 0, len(raw))
	for _, c := range raw {
		if c.value != "" {
			out = append(out, c)
		}
	}
	return out
}

func extractBearerToken(header string) string {
	header = strings.TrimSpace(header)
	if header == "" {
		return ""
	}
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 {
		return header
	}
	if strings.ToLower(parts[0]) != "bearer" {
		return header
	}
	return strings.TrimSpace(parts[1])
}

func strAny(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// Compile-time assertion.
var _ sdkaccess.Provider = (*Provider)(nil)
