package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	log "github.com/sirupsen/logrus"
)

const (
	proberCheckInterval         = 60 * time.Second
	proberMaxConcurrency        = 4
	proberMaxConcurrencyCap     = 1024
	proberRatePerMinute         = 60
	proberMaxRateLimitPerMinute = 60_000_000
	proberDefaultPath           = "/models"
	proberMaxBodyBytes          = 1024
)

// authProberLoop runs periodic lightweight health probes for registered auths.
// Failures are fed back into the existing MarkResult/cooldown path.
type authProberLoop struct {
	manager *Manager
	cfg     internalconfig.CredentialProberConfig
}

func newAuthProberLoop(manager *Manager, cfg internalconfig.CredentialProberConfig) *authProberLoop {
	if cfg.Interval <= 0 {
		cfg.Interval = proberCheckInterval
	}
	if cfg.MaxConcurrency <= 0 {
		cfg.MaxConcurrency = proberMaxConcurrency
	}
	if cfg.RateLimitPerMinute <= 0 {
		cfg.RateLimitPerMinute = proberRatePerMinute
	}
	if cfg.RateLimitPerMinute > proberMaxRateLimitPerMinute {
		cfg.RateLimitPerMinute = proberMaxRateLimitPerMinute
	}
	if strings.TrimSpace(cfg.DefaultProbePath) == "" {
		cfg.DefaultProbePath = proberDefaultPath
	}
	if cfg.BackoffBase <= 0 {
		cfg.BackoffBase = 30 * time.Second
	}
	if cfg.BackoffMax <= 0 {
		cfg.BackoffMax = 5 * time.Minute
	}
	if cfg.BackoffMax < cfg.BackoffBase {
		cfg.BackoffMax = cfg.BackoffBase
	}
	return &authProberLoop{manager: manager, cfg: cfg}
}

// SetProberParentContext binds the prober to a service-wide context so it is
// cancelled when the parent service shuts down instead of outliving it.
// It does not start the prober; callers with a running manager must call
// restartProberLocked/StartProber explicitly.
func (m *Manager) SetProberParentContext(ctx context.Context) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.proberParent = ctx
	m.mu.Unlock()
}

// StartProber launches a background credential health prober.
// Only one loop is kept alive; starting a new one cancels the previous run.
func (m *Manager) StartProber(parent context.Context, cfg internalconfig.CredentialProberConfig) {
	if m == nil {
		return
	}
	m.proberLifecycleMu.Lock()
	defer m.proberLifecycleMu.Unlock()
	m.startProberUnlocked(parent, cfg)
}

func (m *Manager) startProberUnlocked(parent context.Context, cfg internalconfig.CredentialProberConfig) {
	m.stopProberUnlocked()

	m.mu.RLock()
	if m.proberParent != nil {
		parent = m.proberParent
	}
	m.mu.RUnlock()

	if parent == nil || parent.Err() != nil {
		return
	}

	ctx, cancel := context.WithCancel(parent)
	loop := newAuthProberLoop(m, cfg)

	m.mu.Lock()
	m.proberCancel = cancel
	m.proberLoop = loop
	m.mu.Unlock()

	m.proberWg.Add(1)
	go func() {
		defer m.proberWg.Done()
		loop.run(ctx)
	}()
}

// StopProber cancels the background prober loop, if running, and waits for it
// to return before service shutdown continues.
func (m *Manager) StopProber() {
	if m == nil {
		return
	}
	m.proberLifecycleMu.Lock()
	defer m.proberLifecycleMu.Unlock()
	m.stopProberUnlocked()
}

func (m *Manager) stopProberUnlocked() {
	m.mu.Lock()
	cancel := m.proberCancel
	m.proberCancel = nil
	m.proberLoop = nil
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	m.proberWg.Wait()
}

// RestartProber re-evaluates the runtime prober config and starts or stops
// the background loop. It is intended for the embeddable Service to start
// probing after auth/executor initialization.
func (m *Manager) RestartProber() {
	m.restartProberLocked()
}

func (m *Manager) restartProberLocked() {
	if m == nil {
		return
	}
	m.mu.RLock()
	parent := m.proberParent
	m.mu.RUnlock()
	if parent == nil || parent.Err() != nil {
		// No lifecycle parent or the service has shut down; do not start or
		// restart a prober.
		return
	}
	m.proberLifecycleMu.Lock()
	defer m.proberLifecycleMu.Unlock()
	if parent.Err() != nil {
		return
	}
	// Snapshot the runtime config only after acquiring the lifecycle lock so
	// concurrent SetConfig restarts see the most recently applied configuration.
	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	if cfg == nil {
		cfg = &internalconfig.Config{}
	}
	if !cfg.CredentialProber.Enabled {
		m.stopProberUnlocked()
		return
	}
	m.stopProberUnlocked()
	m.startProberUnlocked(parent, cfg.CredentialProber)
}

func (l *authProberLoop) run(ctx context.Context) {
	if l == nil || l.manager == nil {
		return
	}

	timer := time.NewTimer(0)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			l.sweep(ctx)
			interval := l.cfg.Interval
			if interval <= 0 {
				interval = proberCheckInterval
			}
			timer.Reset(interval)
		}
	}
}

func (l *authProberLoop) sweep(ctx context.Context) {
	auths := l.snapshotAuths()
	if len(auths) == 0 {
		return
	}

	concurrency := l.cfg.MaxConcurrency
	if concurrency <= 0 {
		concurrency = proberMaxConcurrency
	}
	if concurrency > proberMaxConcurrencyCap {
		concurrency = proberMaxConcurrencyCap
	}
	if concurrency > len(auths) {
		concurrency = len(auths)
	}

	ratePerMinute := l.cfg.RateLimitPerMinute
	if ratePerMinute <= 0 {
		ratePerMinute = proberRatePerMinute
	}

	var wg sync.WaitGroup
	sem := make(chan struct{}, concurrency)

	var ticker *time.Ticker
	if ratePerMinute > 0 {
		period := time.Minute / time.Duration(ratePerMinute)
		if period <= 0 {
			period = time.Nanosecond
		}
		ticker = time.NewTicker(period)
		defer ticker.Stop()
	}

	authLoop := false
	for _, auth := range auths {
		if ctx.Err() != nil {
			authLoop = true
			break
		}
		if ticker != nil {
			select {
			case <-ctx.Done():
				authLoop = true
			case <-ticker.C:
			}
		}
		if authLoop {
			break
		}

		wg.Add(1)
		sem <- struct{}{}
		go func(a *Auth) {
			defer wg.Done()
			defer func() { <-sem }()
			l.probeWithLimiter(ctx, a, ticker.C)
		}(auth)
	}

	wg.Wait()
}

func (l *authProberLoop) snapshotAuths() []*Auth {
	l.manager.mu.RLock()
	defer l.manager.mu.RUnlock()

	now := time.Now()
	out := make([]*Auth, 0, len(l.manager.auths))
	for _, auth := range l.manager.auths {
		if auth == nil {
			continue
		}
		if auth.Disabled || auth.Status == StatusDisabled {
			continue
		}
		// Only credential-scoped auth-level cooldown blocks probes. Model-only
		// cooldowns must not suppress the credential-wide health check.
		if auth.authLevelUnavailable && auth.authLevelNextRetryAfter.After(now) {
			continue
		}
		out = append(out, auth)
	}
	return out
}

func (l *authProberLoop) probe(parent context.Context, auth *Auth) {
	l.probeWithLimiter(parent, auth, nil)
}

func (l *authProberLoop) probeWithLimiter(parent context.Context, auth *Auth, limiter <-chan time.Time) {
	// Re-fetch the auth and resolve its executor under the manager lock in one
	// step. snapshotAuths may have returned a pointer that was replaced by an
	// auto-refresh or watcher update while this probe was waiting in the
	// rate-limit queue, and the replacement may use a different executor key.
	l.manager.mu.RLock()
	auth = l.manager.auths[auth.ID]
	exec := l.manager.executors[executorKeyFromAuth(auth)]
	l.manager.mu.RUnlock()

	if auth == nil || auth.Disabled || auth.Status == StatusDisabled {
		return
	}
	if exec == nil {
		return
	}

	// The prober must not carry a whole-request deadline into response
	// processing. Drop any inherited deadline while still allowing the parent
	// cancellation to stop the probe.
	probeCtx, cancel := context.WithCancel(context.WithoutCancel(parent))
	go func() {
		select {
		case <-parent.Done():
			cancel()
		case <-probeCtx.Done():
		}
	}()
	defer cancel()

	baseURL := proberBaseURLForProvider(auth, l.manager.currentConfig())
	if baseURL == "" {
		return
	}

	path := proberProbePathForProvider(exec.Identifier(), l.cfg.DefaultProbePath)
	if path == "" {
		return
	}

	probeURL, errParse := resolveProbeURL(baseURL, path)
	if errParse != nil {
		return
	}

	var (
		resp      *http.Response
		errExec   error
		resultErr *Error
	)
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 && limiter != nil {
			// A refreshed OAuth credential retry is a second probe request; consume
			// another rate-limit token instead of doubling the request rate.
			select {
			case <-probeCtx.Done():
				return
			case <-limiter:
			}
		}
		req, errReq := http.NewRequestWithContext(probeCtx, http.MethodGet, probeURL, nil)
		if errReq != nil {
			return
		}
		if strings.EqualFold(exec.Identifier(), "claude") {
			req.Header.Set("Anthropic-Version", "2023-06-01")
		}
		resp, errExec = exec.HttpRequest(probeCtx, auth, req)
		if resp != nil && resp.Body != nil {
			_, _ = io.CopyN(io.Discard, resp.Body, proberMaxBodyBytes)
			_ = resp.Body.Close()
		}

		if errors.Is(probeCtx.Err(), context.Canceled) {
			return
		}

		resultErr = nil
		if errExec != nil {
			resultErr = &Error{
				Code:       ErrorCodeForceCooldown,
				Message:    "prober: " + redactProbeError(errExec),
				HTTPStatus: http.StatusServiceUnavailable,
				Retryable:  true,
			}
		} else if resp == nil {
			resultErr = &Error{
				Code:       ErrorCodeForceCooldown,
				Message:    "prober: empty upstream response",
				HTTPStatus: http.StatusServiceUnavailable,
				Retryable:  true,
			}
		} else if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			resultErr = &Error{
				Code:       ErrorCodeForceCooldown,
				Message:    fmt.Sprintf("prober: upstream returned %d", resp.StatusCode),
				HTTPStatus: resp.StatusCode,
				Retryable:  resp.StatusCode >= 500 || resp.StatusCode == 429,
			}
		}

		if resultErr == nil || resultErr.HTTPStatus != http.StatusUnauthorized || attempt > 0 {
			break
		}

		if refreshed := proberTryRefreshOn401(probeCtx, l.manager, auth); refreshed != nil {
			auth = refreshed
		} else {
			break
		}
	}

	if resultErr == nil {
		l.manager.MarkResult(probeCtx, Result{
			AuthID:          auth.ID,
			Provider:        auth.Provider,
			Success:         true,
			CredentialScope: true,
			IsProbe:         true,
			SourceAuth:      auth,
		})
		return
	}

	if log.IsLevelEnabled(log.DebugLevel) {
		log.Debugf("credential prober failure for %s: %s", auth.ID, resultErr.Message)
	}

	// If the credential was replaced while the request was in flight, the
	// result belongs to stale state and must not be applied to the replacement.
	l.manager.mu.RLock()
	after := l.manager.auths[auth.ID]
	l.manager.mu.RUnlock()
	if after == nil || after != auth {
		return
	}

	l.manager.mu.RLock()
	level := auth.proberBackoff
	l.manager.mu.RUnlock()
	retryAfter := l.proberBackoffFor(level)

	l.manager.MarkResult(probeCtx, Result{
		AuthID:          auth.ID,
		Provider:        auth.Provider,
		Success:         false,
		CredentialScope: true,
		IsProbe:         true,
		SourceAuth:      auth,
		Error:           resultErr,
		RetryAfter:      &retryAfter,
	})
}

func (l *authProberLoop) proberBackoffFor(level int) time.Duration {
	if level < 0 {
		level = 0
	}
	d := l.cfg.BackoffBase
	for i := 0; i < level; i++ {
		d *= 2
		if d >= l.cfg.BackoffMax {
			return l.cfg.BackoffMax
		}
	}
	if d < l.cfg.BackoffBase {
		d = l.cfg.BackoffBase
	}
	return d
}

// proberProviderProbePaths maps canonical executor identifiers to probe paths
// that include the provider's API version, since the global /models default is
// only valid for OpenAI-compatible upstreams that embed the version in base_url.
var proberProviderProbePaths = map[string]string{
	"gemini":              "/v1beta/models",
	"gemini-interactions": "/v1beta/models",
	"aistudio":            "/v1beta/models",
	"xai":                 "/v1/models",
	"kimi":                "/v1/models",
	"claude":              "/v1/models",
	"codex":               "/v1/models",
}

// proberProviderBaseURLs supplies a default base URL for file-backed OAuth
// credentials that do not carry an explicit base_url attribute.
var proberProviderBaseURLs = map[string]string{
	"gemini":               "https://generativelanguage.googleapis.com",
	"gemini-interactions":  "https://generativelanguage.googleapis.com",
	"aistudio":             "https://generativelanguage.googleapis.com",
	"xai":                  "https://api.x.ai/v1",
	"kimi":                 "https://api.kimi.com/coding",
	"claude":               "https://api.anthropic.com",
	"openai-compatibility": "https://api.openai.com/v1",
}

// proberBaseURLForProvider returns the base URL for the probe, using the auth
// attribute or metadata if present and falling back to provider defaults for
// OAuth. File-backed credentials store the configured base_url in Metadata.
func proberBaseURLForProvider(auth *Auth, cfg *internalconfig.Config) string {
	if auth == nil {
		return ""
	}
	if auth.Attributes != nil {
		if baseURL := strings.TrimSpace(auth.Attributes["base_url"]); baseURL != "" {
			return baseURL
		}
	}
	if auth.Metadata != nil {
		if v := auth.Metadata["base_url"]; v != nil {
			if baseURL := strings.TrimSpace(fmt.Sprint(v)); baseURL != "" {
				return baseURL
			}
		}
	}
	provider := strings.ToLower(strings.TrimSpace(auth.Provider))
	if cfg != nil {
		if u := proberOpenAICompatBaseURL(auth, cfg); u != "" {
			return u
		}
	}
	if p, ok := proberProviderBaseURLs[provider]; ok {
		return p
	}
	return ""
}

func proberIsOpenAICompatibleProvider(provider string) bool {
	provider = strings.ToLower(strings.TrimSpace(provider))
	return provider == "openai-compatibility" || strings.HasPrefix(provider, "openai-compatible-")
}

// proberOpenAICompatBaseURL resolves a base URL from the configured OpenAI
// compatibility entries for custom openai-compatible providers that do not
// carry an explicit base_url attribute.
func proberOpenAICompatBaseURL(auth *Auth, cfg *internalconfig.Config) string {
	if auth == nil || cfg == nil || !proberIsOpenAICompatibleProvider(auth.Provider) {
		return ""
	}

	var candidates []string
	if auth.Attributes != nil {
		if v := strings.TrimSpace(auth.Attributes["config_index"]); v != "" {
			if idx, err := strconv.Atoi(v); err == nil && idx >= 0 && idx < len(cfg.OpenAICompatibility) {
				c := &cfg.OpenAICompatibility[idx]
				if !c.Disabled && strings.TrimSpace(c.BaseURL) != "" {
					return strings.TrimRight(c.BaseURL, "/")
				}
			}
		}
		if v := strings.TrimSpace(auth.Attributes["compat_name"]); v != "" {
			candidates = append(candidates, v)
		}
		if v := strings.TrimSpace(auth.Attributes["provider_key"]); v != "" {
			candidates = append(candidates, v)
		}
	}
	if v := strings.TrimSpace(auth.Provider); v != "" {
		candidates = append(candidates, v)
	}

	for i := range cfg.OpenAICompatibility {
		c := &cfg.OpenAICompatibility[i]
		if c.Disabled || strings.TrimSpace(c.BaseURL) == "" {
			continue
		}
		for _, cand := range candidates {
			if cand != "" && strings.EqualFold(c.Name, cand) {
				return strings.TrimRight(c.BaseURL, "/")
			}
		}
	}
	return ""
}

// proberProbePathForProvider returns the probe path for the provider.
// OpenAI-compatible executors use /v1/models. Unknown providers fall back to
// the configured default; if neither is set, the caller should skip the auth.
func proberProbePathForProvider(provider, configured string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		return strings.TrimSpace(configured)
	}
	if p, ok := proberProviderProbePaths[provider]; ok {
		return p
	}
	if proberIsOpenAICompatibleProvider(provider) {
		return "/v1/models"
	}
	if configured != "" {
		return configured
	}
	return ""
}

var proberURLRegex = regexp.MustCompile(`https?://[^ \t\n\r\"'<>]+`)

// redactProbeError removes userinfo and query parameters from any URL that
// appears in a transport error, so tokens in query strings are not logged or
// stored in LastError.
func redactProbeError(err error) string {
	if err == nil {
		return ""
	}
	return proberURLRegex.ReplaceAllStringFunc(err.Error(), func(raw string) string {
		u, parseErr := url.Parse(raw)
		if parseErr != nil {
			return raw
		}
		u.User = nil
		u.RawQuery = ""
		u.Fragment = ""
		return u.String()
	})
}

func proberAccessToken(auth *Auth) string {
	if auth == nil || auth.Metadata == nil {
		return ""
	}
	for _, key := range []string{"access_token", "token", "id_token"} {
		if v := auth.Metadata[key]; v != nil {
			return strings.TrimSpace(fmt.Sprint(v))
		}
	}
	return ""
}

// proberTryRefreshOn401 attempts to refresh an OAuth credential and returns
// the current auth if the token changed. It uses the manager's
// refreshAuthForRequest path so the refresh is serialized per auth and the
// live pointer is not mutated in place by the executor.
func proberTryRefreshOn401(ctx context.Context, m *Manager, auth *Auth) *Auth {
	if m == nil || auth == nil {
		return nil
	}
	before := proberAccessToken(auth)
	_, _ = m.refreshAuthForRequest(ctx, auth.ID, before)

	m.mu.RLock()
	current := m.auths[auth.ID]
	m.mu.RUnlock()
	if current == nil {
		return nil
	}
	if proberAccessToken(current) == before {
		return nil
	}
	return current
}

// resolveProbeURL resolves the probe path against baseURL without duplicating
// the API version segment. If the base path already ends with the first segment
// of the probe path, that segment is not duplicated.
func resolveProbeURL(baseURL, probePath string) (string, error) {
	base, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil {
		return "", err
	}
	probePath = strings.Trim(probePath, "/")
	if probePath == "" {
		probePath = strings.Trim(proberDefaultPath, "/")
	}

	basePath := strings.Trim(base.Path, "/")
	var baseSegs []string
	if basePath != "" {
		baseSegs = strings.Split(basePath, "/")
	}
	probeSegs := strings.Split(probePath, "/")

	if len(baseSegs) > 0 && len(probeSegs) > 0 && baseSegs[len(baseSegs)-1] == probeSegs[0] {
		probeSegs = probeSegs[1:]
	}

	base.Path = "/" + path.Join(append(baseSegs, probeSegs...)...)
	return base.String(), nil
}
