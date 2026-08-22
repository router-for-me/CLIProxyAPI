package auth

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	log "github.com/sirupsen/logrus"
)

const (
	proberCheckInterval  = 60 * time.Second
	proberMaxConcurrency = 4
	proberRatePerMinute  = 60
	proberDefaultPath    = "/models"
	proberMaxBodyBytes   = 1024
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

func (m *Manager) restartProberLocked(cfg *internalconfig.Config) {
	if m == nil || cfg == nil {
		return
	}
	m.proberLifecycleMu.Lock()
	defer m.proberLifecycleMu.Unlock()
	if cfg.CredentialProber.Enabled {
		m.startProberUnlocked(context.Background(), cfg.CredentialProber)
	} else {
		m.stopProberUnlocked()
	}
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

	ratePerMinute := l.cfg.RateLimitPerMinute
	if ratePerMinute <= 0 {
		ratePerMinute = proberRatePerMinute
	}

	var wg sync.WaitGroup
	sem := make(chan struct{}, concurrency)

	var ticker *time.Ticker
	if ratePerMinute > 0 {
		ticker = time.NewTicker(time.Minute / time.Duration(ratePerMinute))
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
			l.probe(ctx, a)
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
		if auth.Unavailable && auth.NextRetryAfter.After(now) {
			continue
		}
		out = append(out, auth)
	}
	return out
}

func (l *authProberLoop) probe(parent context.Context, auth *Auth) {
	l.manager.mu.RLock()
	exec := l.manager.executors[executorKeyFromAuth(auth)]
	l.manager.mu.RUnlock()

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

	baseURL := ""
	if auth.Attributes != nil {
		baseURL = strings.TrimSpace(auth.Attributes["base_url"])
	}
	if baseURL == "" {
		return
	}

	path := strings.TrimSpace(l.cfg.DefaultProbePath)
	if path == "" {
		path = proberDefaultPath
	}

	probeURL, errParse := resolveProbeURL(baseURL, path)
	if errParse != nil {
		return
	}

	req, errReq := http.NewRequestWithContext(probeCtx, http.MethodGet, probeURL, nil)
	if errReq != nil {
		return
	}

	resp, errExec := exec.HttpRequest(probeCtx, auth, req)
	var bodyBytes int64
	if resp != nil && resp.Body != nil {
		bodyBytes, _ = io.CopyN(io.Discard, resp.Body, proberMaxBodyBytes)
		_ = resp.Body.Close()
	}

	var resultErr *Error
	if errExec != nil {
		resultErr = &Error{
			Code:       ErrorCodeForceCooldown,
			Message:    "prober: " + errExec.Error(),
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
	} else if resp.StatusCode == http.StatusNoContent || (resp.StatusCode == http.StatusOK && bodyBytes == 0) {
		resultErr = &Error{
			Code:       ErrorCodeForceCooldown,
			Message:    "prober: empty 200/204 response",
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

	if resultErr == nil {
		l.manager.mu.Lock()
		auth.proberBackoff = 0
		l.manager.mu.Unlock()
		return
	}

	if log.IsLevelEnabled(log.DebugLevel) {
		log.Debugf("credential prober failure for %s: %s", auth.ID, resultErr.Message)
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
