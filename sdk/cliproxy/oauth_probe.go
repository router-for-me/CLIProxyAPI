package cliproxy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	log "github.com/sirupsen/logrus"
)

const (
	// oauthProbeTimeout bounds one control-plane probe. Probes run in the
	// background and never carry a client request.
	oauthProbeTimeout = 20 * time.Second
	// oauthProbeMaxBody caps how much of a probe response is read.
	oauthProbeMaxBody = 8 << 20

	claudeProbeUserAgent  = "claude-cli/2.1.280 (external, cli)"
	claudeProbeOAuthBeta  = "oauth-2025-04-20"
	claudeProbeAPIVersion = "2023-06-01"
	codexProbeUserAgent   = "codex-tui/0.154.0 (Mac OS 26.5.2; arm64) iTerm.app/3.6.11 (codex-tui; 0.154.0)"
	codexProbeOriginator  = "codex-tui"
)

// oauthProbeProvider returns the provider key when the credential is an enabled
// Claude or Codex OAuth credential with an access token, the only credentials
// the provider control-plane endpoints accept.
func oauthProbeProvider(auth *coreauth.Auth) (string, bool) {
	if auth == nil || auth.Disabled || strings.TrimSpace(auth.ID) == "" {
		return "", false
	}
	provider := strings.ToLower(strings.TrimSpace(auth.Provider))
	if provider != "claude" && provider != "codex" {
		return "", false
	}
	if auth.AuthKind() != coreauth.AuthKindOAuth || oauthProbeAccessToken(auth) == "" {
		return "", false
	}
	return provider, true
}

func oauthProbeAccessToken(auth *coreauth.Auth) string {
	if auth == nil || auth.Metadata == nil {
		return ""
	}
	token, _ := auth.Metadata["access_token"].(string)
	return strings.TrimSpace(token)
}

// oauthProbeHeaders builds the request headers the native client sends to the
// provider control plane for the credential.
func oauthProbeHeaders(cfg *config.Config, provider string, auth *coreauth.Auth) http.Header {
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer "+oauthProbeAccessToken(auth))
	headers.Set("Accept", "application/json")
	headers.Set("Content-Type", "application/json")
	switch provider {
	case "claude":
		userAgent := claudeProbeUserAgent
		if cfg != nil {
			if configured := strings.TrimSpace(cfg.ClaudeHeaderDefaults.UserAgent); configured != "" {
				userAgent = configured
			}
		}
		headers.Set("User-Agent", userAgent)
		headers.Set("Anthropic-Beta", claudeProbeOAuthBeta)
		headers.Set("Anthropic-Version", claudeProbeAPIVersion)
	case "codex":
		userAgent := codexProbeUserAgent
		if cfg != nil {
			if configured := strings.TrimSpace(cfg.CodexHeaderDefaults.UserAgent); configured != "" {
				userAgent = configured
			}
		}
		headers.Set("User-Agent", userAgent)
		headers.Set("Originator", codexProbeOriginator)
		if accountID, ok := auth.Metadata["account_id"].(string); ok && strings.TrimSpace(accountID) != "" {
			headers.Set("Chatgpt-Account-Id", strings.TrimSpace(accountID))
		}
	}
	return headers
}

// oauthProbeGET issues one authenticated GET against a provider control-plane
// endpoint through the credential's proxy settings and returns the body of a
// successful response.
func (s *Service) oauthProbeGET(ctx context.Context, cfg *config.Config, provider string, auth *coreauth.Auth, endpoint string) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	probeCtx, cancel := context.WithTimeout(ctx, oauthProbeTimeout)
	defer cancel()

	req, errRequest := http.NewRequestWithContext(probeCtx, http.MethodGet, endpoint, nil)
	if errRequest != nil {
		return nil, fmt.Errorf("create request: %w", errRequest)
	}
	req.Header = oauthProbeHeaders(cfg, provider, auth)

	client := helps.NewProxyAwareHTTPClient(probeCtx, cfg, auth, 0)
	resp, errDo := client.Do(req)
	if errDo != nil {
		return nil, fmt.Errorf("request failed: %w", errDo)
	}
	return readProbeBody(resp)
}

// readProbeBody reads and closes a probe response and returns its body when
// the provider answered with success.
func readProbeBody(resp *http.Response) ([]byte, error) {
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Debugf("provider probe: close response body: %v", errClose)
		}
	}()
	body, errRead := io.ReadAll(io.LimitReader(resp.Body, oauthProbeMaxBody))
	if errRead != nil {
		return nil, fmt.Errorf("read response: %w", errRead)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return body, nil
}
