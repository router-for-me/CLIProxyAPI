package management

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const authFileQuotaConcurrency = 4

// claudeOAuthUsageClient is the slice of the Claude OAuth client this endpoint
// needs. The usage request goes through that client on purpose: it carries
// the Firefox-uTLS transport and Axios-shaped headers Anthropic's OAuth
// control plane expects, which a plain transport can trip over.
type claudeOAuthUsageClient interface {
	FetchOAuthUsage(ctx context.Context, accessToken string) (json.RawMessage, error)
}

// newClaudeOAuthUsageClient builds the client for one credential, honoring
// its own proxy_url over the global one. A variable so tests can substitute
// a fake without a network.
var newClaudeOAuthUsageClient = func(cfg *config.Config, proxyURL string) claudeOAuthUsageClient {
	return claude.NewClaudeAuthWithProxyURL(cfg, proxyURL)
}

// authFileQuotaWindow is one usage window as reported by the provider.
// Kind keeps the provider's own vocabulary (Anthropic: "session",
// "weekly_all", "weekly_scoped"); Scope names the model for scoped windows.
type authFileQuotaWindow struct {
	Kind        string  `json:"kind"`
	Utilization float64 `json:"utilization"`
	ResetsAt    string  `json:"resets_at,omitempty"`
	Scope       string  `json:"scope,omitempty"`
}

// authFileQuotaEntry is the per-credential result. Either Windows or Error is
// set; StatusCode is the upstream status when a request was made.
type authFileQuotaEntry struct {
	Name       string                `json:"name"`
	AuthIndex  string                `json:"auth_index"`
	Provider   string                `json:"provider"`
	Email      string                `json:"email,omitempty"`
	ObservedAt string                `json:"observed_at"`
	StatusCode int                   `json:"status_code,omitempty"`
	Windows    []authFileQuotaWindow `json:"windows,omitempty"`
	Error      string                `json:"error,omitempty"`
}

// claudeOAuthUsageResponse is the subset of the Anthropic usage payload that
// this endpoint reads. `limits` is the newer shape; `five_hour`/`seven_day`
// plus the per-model `seven_day_<model>` buckets are the older one and are
// only used when `limits` is absent.
type claudeOAuthUsageResponse struct {
	FiveHour *claudeOAuthUsageWindow `json:"five_hour"`
	SevenDay *claudeOAuthUsageWindow `json:"seven_day"`
	Limits   []claudeOAuthUsageLimit `json:"limits"`
}

type claudeOAuthUsageWindow struct {
	Utilization *float64 `json:"utilization"`
	ResetsAt    *string  `json:"resets_at"`
}

type claudeOAuthUsageLimit struct {
	Kind     string   `json:"kind"`
	Percent  *float64 `json:"percent"`
	ResetsAt *string  `json:"resets_at"`
	Scope    *struct {
		Model *struct {
			DisplayName *string `json:"display_name"`
		} `json:"model"`
	} `json:"scope"`
}

// GetAuthFileQuota handles GET /v0/management/auth-files/quota.
//
// Unlike the `quota` field of the auth file listing, which is observed from
// response headers as traffic flows through the proxy, this endpoint asks the
// provider for the credential's own usage report and returns it normalized:
// one entry per credential, one window per rate-limit bucket. Today only
// Claude OAuth credentials are supported (api.anthropic.com/api/oauth/usage);
// API-key credentials and other providers are left out of the response.
//
// Query parameters `name` and `auth_index` narrow the set the same way the
// other auth-files endpoints do. Credentials are queried concurrently under
// the request's context and a failing upstream call yields a per-entry
// `error` rather than failing the whole request. Upstream error bodies are
// never forwarded.
func (h *Handler) GetAuthFileQuota(c *gin.Context) {
	name := strings.TrimSpace(c.Query("name"))
	authIndex := strings.TrimSpace(c.Query("auth_index"))

	// One reload generation for the whole request: SetAuthManager/SetConfig
	// swap these under h.mu during a hot reload.
	var manager *coreauth.Manager
	var cfg *config.Config
	if h != nil {
		h.mu.Lock()
		manager, cfg = h.authManager, h.cfg
		h.mu.Unlock()
	}
	var targets []*coreauth.Auth
	if manager != nil {
		for _, auth := range manager.List() {
			if auth == nil || !matchesAuthFileLookup(auth, name, authIndex) {
				continue
			}
			if !strings.EqualFold(strings.TrimSpace(auth.Provider), "claude") {
				continue
			}
			// The credential's own classification decides, not the shape of
			// its metadata: an entry marked apikey that still carries an
			// access_token must not have that token sent to the OAuth
			// usage endpoint.
			if auth.AuthKind() != coreauth.AuthKindOAuth || tokenValueFromMetadata(auth.Metadata) == "" {
				continue
			}
			targets = append(targets, auth)
		}
	}

	entries := make([]authFileQuotaEntry, len(targets))
	var wg sync.WaitGroup
	slots := make(chan struct{}, authFileQuotaConcurrency)
	for i, auth := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			entries[i] = fetchClaudeOAuthQuota(c.Request.Context(), cfg, auth)
		}()
	}
	wg.Wait()

	c.JSON(http.StatusOK, gin.H{"files": entries})
}

func fetchClaudeOAuthQuota(ctx context.Context, cfg *config.Config, auth *coreauth.Auth) authFileQuotaEntry {
	name := strings.TrimSpace(auth.FileName)
	if name == "" {
		name = auth.ID
	}
	entry := authFileQuotaEntry{
		Name:       name,
		AuthIndex:  lockedAuthIndex(auth),
		Provider:   "claude",
		Email:      authEmail(auth),
		ObservedAt: time.Now().UTC().Format(time.RFC3339),
	}

	// No timeout beyond the transport's dial and TLS handshake bounds: once
	// the upstream connection is up the request only ends with the response
	// or the caller's context (the management client disconnecting).
	// Repository policy allows timeouts during credential acquisition only.
	client := newClaudeOAuthUsageClient(cfg, auth.ProxyURL)
	body, errFetch := client.FetchOAuthUsage(ctx, tokenValueFromMetadata(auth.Metadata))
	if errFetch != nil {
		var statusErr *claude.OAuthStatusError
		if errors.As(errFetch, &statusErr) {
			entry.StatusCode = statusErr.StatusCode
			entry.Error = fmt.Sprintf("upstream returned status %d", statusErr.StatusCode)
			return entry
		}
		log.WithError(errFetch).Debug("management auth file quota request failed")
		entry.Error = "request failed"
		return entry
	}
	entry.StatusCode = http.StatusOK

	windows, errParse := parseClaudeOAuthUsage(body)
	if errParse != nil {
		entry.Error = "invalid upstream response"
		return entry
	}
	entry.Windows = windows
	return entry
}

func parseClaudeOAuthUsage(body []byte) ([]authFileQuotaWindow, error) {
	var payload claudeOAuthUsageResponse
	if errUnmarshal := json.Unmarshal(body, &payload); errUnmarshal != nil {
		return nil, errUnmarshal
	}

	windows := make([]authFileQuotaWindow, 0, len(payload.Limits)+2)
	if len(payload.Limits) > 0 {
		for _, limit := range payload.Limits {
			if limit.Percent == nil || strings.TrimSpace(limit.Kind) == "" {
				continue
			}
			window := authFileQuotaWindow{Kind: limit.Kind, Utilization: *limit.Percent}
			if limit.ResetsAt != nil {
				window.ResetsAt = *limit.ResetsAt
			}
			if limit.Scope != nil && limit.Scope.Model != nil && limit.Scope.Model.DisplayName != nil {
				window.Scope = *limit.Scope.Model.DisplayName
			}
			windows = append(windows, window)
		}
		return windows, nil
	}

	appendWindow := func(kind string, raw *claudeOAuthUsageWindow) {
		if raw == nil || raw.Utilization == nil {
			return
		}
		window := authFileQuotaWindow{Kind: kind, Utilization: *raw.Utilization}
		if raw.ResetsAt != nil {
			window.ResetsAt = *raw.ResetsAt
		}
		windows = append(windows, window)
	}
	appendWindow("session", payload.FiveHour)
	appendWindow("weekly_all", payload.SevenDay)
	// Legacy per-model buckets (`seven_day_opus`, `seven_day_sonnet`, …)
	// become weekly_scoped windows so a model-scoped limit isn't reported as
	// spare quota. The model name is the key's suffix, title-cased.
	var raw map[string]json.RawMessage
	if errUnmarshal := json.Unmarshal(body, &raw); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	keys := make([]string, 0, len(raw))
	for key := range raw {
		if strings.HasPrefix(key, legacyScopedWeeklyPrefix) && len(key) > len(legacyScopedWeeklyPrefix) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		var scoped claudeOAuthUsageWindow
		if errUnmarshal := json.Unmarshal(raw[key], &scoped); errUnmarshal != nil || scoped.Utilization == nil {
			continue
		}
		window := authFileQuotaWindow{Kind: "weekly_scoped", Utilization: *scoped.Utilization,
			Scope: legacyScopedModelName(strings.TrimPrefix(key, legacyScopedWeeklyPrefix))}
		if scoped.ResetsAt != nil {
			window.ResetsAt = *scoped.ResetsAt
		}
		windows = append(windows, window)
	}
	return windows, nil
}

const legacyScopedWeeklyPrefix = "seven_day_"

// legacyScopedModelName turns a legacy key suffix ("opus", "sonnet_4") into
// the display form the `limits` shape carries ("Opus", "Sonnet 4").
func legacyScopedModelName(suffix string) string {
	parts := strings.Split(suffix, "_")
	for i, part := range parts {
		if part == "" {
			continue
		}
		parts[i] = strings.ToUpper(part[:1]) + part[1:]
	}
	return strings.Join(parts, " ")
}
