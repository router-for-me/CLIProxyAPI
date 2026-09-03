package management

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// claudeOAuthUsageURL is the Anthropic endpoint that reports the usage windows
// of a Claude OAuth credential. It is a variable so tests can point it at a
// local server.
var claudeOAuthUsageURL = "https://api.anthropic.com/api/oauth/usage"

const (
	claudeOAuthUsageBeta     = "oauth-2025-04-20"
	authFileQuotaMaxBodySize = 2 << 20
	authFileQuotaConcurrency = 4
)

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
// are the older one and are only used when `limits` is absent.
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
// other auth-files endpoints do. Credentials are queried concurrently and a
// failing upstream call yields a per-entry `error` rather than failing the
// whole request. Upstream error bodies are never forwarded.
func (h *Handler) GetAuthFileQuota(c *gin.Context) {
	name := strings.TrimSpace(c.Query("name"))
	authIndex := strings.TrimSpace(c.Query("auth_index"))

	var targets []*coreauth.Auth
	if h != nil && h.authManager != nil {
		for _, auth := range h.authManager.List() {
			if auth == nil || !matchesAuthFileLookup(auth, name, authIndex) {
				continue
			}
			if !strings.EqualFold(strings.TrimSpace(auth.Provider), "claude") {
				continue
			}
			if tokenValueFromMetadata(auth.Metadata) == "" {
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
			entries[i] = h.fetchClaudeOAuthQuota(c.Request.Context(), auth)
		}()
	}
	wg.Wait()

	c.JSON(http.StatusOK, gin.H{"files": entries})
}

func (h *Handler) fetchClaudeOAuthQuota(ctx context.Context, auth *coreauth.Auth) authFileQuotaEntry {
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

	req, errNewRequest := http.NewRequestWithContext(ctx, http.MethodGet, claudeOAuthUsageURL, nil)
	if errNewRequest != nil {
		entry.Error = "failed to build request"
		return entry
	}
	req.Header.Set("Authorization", "Bearer "+tokenValueFromMetadata(auth.Metadata))
	req.Header.Set("anthropic-beta", claudeOAuthUsageBeta)
	req.Header.Set("Accept", "application/json")

	httpClient := &http.Client{
		Timeout:   defaultAPICallTimeout,
		Transport: h.apiCallTransport(auth, ""),
	}
	resp, errDo := httpClient.Do(req)
	if errDo != nil {
		log.WithError(errDo).Debug("management auth file quota request failed")
		entry.Error = "request failed"
		return entry
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}
	}()

	entry.StatusCode = resp.StatusCode
	body, errReadAll := io.ReadAll(io.LimitReader(resp.Body, authFileQuotaMaxBodySize))
	if errReadAll != nil {
		entry.Error = "failed to read response"
		return entry
	}
	if resp.StatusCode != http.StatusOK {
		entry.Error = fmt.Sprintf("upstream returned status %d", resp.StatusCode)
		return entry
	}

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
	return windows, nil
}
