package helps

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	zcodeDefaultBase   = "https://api.z.ai/api/anthropic"
	zcodeConfigURL     = "https://zcode.z.ai/api/v1/agent/configs"
	zcodeRouteTTL      = 5 * time.Minute
	zcodeRouteCooldown = 30 * time.Second
)

// ZCodeRouteResolver fetches the ZCode endpoint-routing mapping table and
// rewrites coding-plan base URLs to the ultra gateway. Fail-open: any error
// keeps the previous snapshot or falls back to the plain endpoint.
type ZCodeRouteResolver struct {
	cfg       *config.Config
	http      *http.Client
	configURL string

	mu       sync.RWMutex
	mapping  map[string]string
	loadedAt time.Time
	retryAt  time.Time
}

// NewZCodeRouteResolver constructs a resolver. cfg may be nil.
func NewZCodeRouteResolver(cfg *config.Config) *ZCodeRouteResolver {
	return &ZCodeRouteResolver{
		cfg:       cfg,
		http:      &http.Client{Timeout: 3 * time.Second},
		configURL: zcodeConfigURL,
		mapping:   map[string]string{},
	}
}

// Refresh pulls a fresh snapshot if not cached/cooldowned.
func (r *ZCodeRouteResolver) Refresh(ctx context.Context) error {
	r.mu.Lock()
	if time.Since(r.loadedAt) < zcodeRouteTTL {
		r.mu.Unlock()
		return nil
	}
	if time.Now().Before(r.retryAt) {
		r.mu.Unlock()
		return nil
	}
	httpClient := r.http
	r.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.configURL, nil)
	if err != nil {
		return err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		r.mu.Lock()
		r.retryAt = time.Now().Add(zcodeRouteCooldown)
		r.mu.Unlock()
		return err
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Errorf("zcode: close config response body: %v", err)
		}
	}()
	raw, _ := io.ReadAll(resp.Body)
	var env struct {
		Code int `json:"code"`
		Data struct {
			ProxyEndpoint struct {
				Mapping map[string]string `json:"mapping"`
			} `json:"proxyEndpoint"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil || env.Code != 0 {
		r.mu.Lock()
		r.retryAt = time.Now().Add(zcodeRouteCooldown)
		r.mu.Unlock()
		return fmt.Errorf("zcode: config fetch failed: code=%d err=%v", env.Code, err)
	}
	next := map[string]string{}
	for from, to := range env.Data.ProxyEndpoint.Mapping {
		if strings.HasPrefix(from, "https://") && strings.HasPrefix(to, "https://") {
			next[from] = to
		}
	}
	r.mu.Lock()
	r.mapping = next
	r.loadedAt = time.Now()
	r.mu.Unlock()
	return nil
}

// BaseURL returns the mapped base for path, falling back to the plain endpoint.
func (r *ZCodeRouteResolver) BaseURL(_ *cliproxyauth.Auth, path string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	// Map the plain coding endpoint to the ultra gateway, if a snapshot exists.
	from := zcodeDefaultBase
	if to, ok := r.mapping[from]; ok {
		_ = path
		return to
	}
	return from
}
