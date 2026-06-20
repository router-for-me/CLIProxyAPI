package zai

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	log "github.com/sirupsen/logrus"
)

const (
	// DefaultOAuthBaseURL is the ZCode CLI OAuth base endpoint. The init and poll
	// endpoints are derived from it. The same base serves both identity providers;
	// the provider is selected via the init request body.
	DefaultOAuthBaseURL = "https://zcode.z.ai/api/v1"

	// ProviderZAI authenticates against Z.AI international (chat.z.ai).
	ProviderZAI = "zai"
	// ProviderBigModel authenticates against Zhipu BigModel, China mainland (bigmodel.cn).
	ProviderBigModel = "bigmodel"

	// defaultPollInterval is used when the server does not advertise an interval.
	defaultPollInterval = 2 * time.Second
	// maxPollDuration bounds how long we wait for the user to authorize.
	maxPollDuration = 10 * time.Minute
	// pollTokenBytes is the size of the client-generated poll token (matches ZCode).
	pollTokenBytes = 32
	// maxConsecutivePollErrors is how many transient poll failures (network blips,
	// 5xx) are tolerated in a row before the login is aborted. The multi-minute
	// authorization window makes brief glitches likely, so we retry rather than
	// fail the whole flow on the first hiccup.
	maxConsecutivePollErrors = 5
)

// NormalizeProvider returns a supported identity provider value, defaulting to
// "zai" when the input is empty or unrecognized.
func NormalizeProvider(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case ProviderBigModel, "zhipu", "glm", "cn":
		return ProviderBigModel
	default:
		return ProviderZAI
	}
}

// InitResponse is the parsed payload of the OAuth init call.
type InitResponse struct {
	FlowID          string `json:"flow_id"`
	PollToken       string `json:"poll_token"`
	AuthorizeURL    string `json:"authorize_url"`
	ExpiresAt       int64  `json:"expires_at"`
	PollIntervalSec int    `json:"poll_interval_sec"`
}

// ReadyResult holds the credentials returned when authorization completes.
type ReadyResult struct {
	// Token is the minted coding-plan token, used as the Bearer credential.
	Token string
	// ZAIAccessToken is the Z.AI account access token returned alongside the token.
	ZAIAccessToken string
	UserID         string
	Email          string
	Name           string
}

// envelope is the common response wrapper used by the ZCode OAuth API.
// A code of 0 indicates success.
type envelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

// ZAIAuth handles the ZCode CLI OAuth flow for a single identity provider.
type ZAIAuth struct {
	httpClient *http.Client
	provider   string
	baseURL    string
}

// NewZAIAuth creates a ZAIAuth bound to the given identity provider. proxyURL
// overrides cfg.ProxyURL when non-empty.
func NewZAIAuth(cfg *config.Config, provider, proxyURL string) *ZAIAuth {
	client := &http.Client{Timeout: 30 * time.Second}
	var sdkCfg config.SDKConfig
	effectiveProxyURL := strings.TrimSpace(proxyURL)
	if cfg != nil {
		sdkCfg = cfg.SDKConfig
		if effectiveProxyURL == "" {
			effectiveProxyURL = strings.TrimSpace(cfg.ProxyURL)
		}
	}
	sdkCfg.ProxyURL = effectiveProxyURL
	client = util.SetProxy(&sdkCfg, client)

	return &ZAIAuth{
		httpClient: client,
		provider:   NormalizeProvider(provider),
		baseURL:    DefaultOAuthBaseURL,
	}
}

// Provider returns the identity provider this client authenticates against.
func (a *ZAIAuth) Provider() string { return a.provider }

// StartFlow initiates the OAuth flow and returns the authorization URL plus the
// poll token used to wait for completion.
func (a *ZAIAuth) StartFlow(ctx context.Context) (*InitResponse, error) {
	pollToken, err := newPollToken()
	if err != nil {
		return nil, fmt.Errorf("zai: generate poll token: %w", err)
	}

	body, _ := json.Marshal(map[string]string{"provider": a.provider})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/oauth/cli/init", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("zai: create init request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+pollToken)

	data, err := a.doEnvelope(req)
	if err != nil {
		return nil, err
	}
	var out InitResponse
	if err = json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("zai: parse init response: %w", err)
	}
	if strings.TrimSpace(out.FlowID) == "" || strings.TrimSpace(out.AuthorizeURL) == "" {
		return nil, fmt.Errorf("zai: invalid init response (missing flow_id or authorize_url)")
	}
	// The server returns the authoritative poll token; fall back to the
	// client-generated one only if the server omitted it.
	if strings.TrimSpace(out.PollToken) == "" {
		out.PollToken = pollToken
	}
	return &out, nil
}

// WaitForAuthorization polls until the user authorizes the request or the flow
// expires, then returns the minted credentials.
func (a *ZAIAuth) WaitForAuthorization(ctx context.Context, init *InitResponse) (*ReadyResult, error) {
	if init == nil {
		return nil, fmt.Errorf("zai: init response is nil")
	}

	interval := time.Duration(init.PollIntervalSec) * time.Second
	if interval < defaultPollInterval {
		interval = defaultPollInterval
	}

	deadline := time.Now().Add(maxPollDuration)
	if init.ExpiresAt > 0 {
		if codeDeadline := time.Unix(init.ExpiresAt, 0); codeDeadline.Before(deadline) {
			deadline = codeDeadline
		}
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	consecutiveErrors := 0
	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("zai: context cancelled: %w", ctx.Err())
		case <-ticker.C:
			if time.Now().After(deadline) {
				return nil, fmt.Errorf("zai: authorization timed out")
			}
			result, done, terminal, err := a.poll(ctx, init)
			if err != nil {
				if terminal {
					return nil, err
				}
				consecutiveErrors++
				if consecutiveErrors >= maxConsecutivePollErrors {
					return nil, fmt.Errorf("zai: polling failed after %d consecutive errors: %w", consecutiveErrors, err)
				}
				log.Warnf("zai: transient polling error (%d/%d), will retry: %v", consecutiveErrors, maxConsecutivePollErrors, err)
				continue
			}
			consecutiveErrors = 0
			if done {
				return result, nil
			}
			// Keep polling.
		}
	}
}

// poll performs a single poll request. It returns (result, done, terminal, error)
// where terminal reports whether the error is definitive (authorization denied or
// a protocol error) and must not be retried. Non-terminal errors are transient
// (network/HTTP/decode failures) and may be retried by the caller.
func (a *ZAIAuth) poll(ctx context.Context, init *InitResponse) (*ReadyResult, bool, bool, error) {
	url := fmt.Sprintf("%s/oauth/cli/poll/%s", a.baseURL, init.FlowID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, false, true, fmt.Errorf("zai: create poll request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+init.PollToken)

	data, err := a.doEnvelope(req)
	if err != nil {
		// Network / HTTP / envelope errors are transient: let the caller retry.
		return nil, false, false, err
	}

	var poll struct {
		Status string `json:"status"`
		Token  string `json:"token"`
		User   struct {
			UserID string `json:"user_id"`
			Email  string `json:"email"`
			Name   string `json:"name"`
		} `json:"user"`
		ZAI struct {
			AccessToken string `json:"access_token"`
		} `json:"zai"`
	}
	if err = json.Unmarshal(data, &poll); err != nil {
		return nil, false, true, fmt.Errorf("zai: parse poll response: %w", err)
	}

	switch poll.Status {
	case "pending", "":
		return nil, false, false, nil
	case "failed":
		return nil, false, true, fmt.Errorf("zai: authorization failed or was denied")
	case "ready":
		if strings.TrimSpace(poll.Token) == "" {
			return nil, false, true, fmt.Errorf("zai: ready response missing token")
		}
		return &ReadyResult{
			Token:          poll.Token,
			ZAIAccessToken: poll.ZAI.AccessToken,
			UserID:         poll.User.UserID,
			Email:          poll.User.Email,
			Name:           poll.User.Name,
		}, true, false, nil
	default:
		return nil, false, true, fmt.Errorf("zai: unexpected poll status %q", poll.Status)
	}
}

// doEnvelope executes the request and unwraps the {code,msg,data} envelope.
func (a *ZAIAuth) doEnvelope(req *http.Request) (json.RawMessage, error) {
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("zai: request failed: %w", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("zai: close response body error: %v", errClose)
		}
	}()

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("zai: read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("zai: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
	}

	var env envelope
	if err = json.Unmarshal(bodyBytes, &env); err != nil {
		return nil, fmt.Errorf("zai: parse response envelope: %w", err)
	}
	if env.Code != 0 {
		msg := strings.TrimSpace(env.Msg)
		if msg == "" {
			msg = fmt.Sprintf("business error %d", env.Code)
		}
		return nil, fmt.Errorf("zai: %s", msg)
	}
	return env.Data, nil
}

// CreateTokenStorage builds the on-disk token storage from the OAuth result, the
// minted coding-plan API key, and its Anthropic-compatible inference base URL.
func (a *ZAIAuth) CreateTokenStorage(ready *ReadyResult, apiKey, baseURL string) *TokenStorage {
	return &TokenStorage{
		Type:           "zai",
		Provider:       a.provider,
		AccessToken:    apiKey,
		ZAIAccessToken: ready.ZAIAccessToken,
		BaseURL:        baseURL,
		UserID:         ready.UserID,
		Email:          ready.Email,
		Name:           ready.Name,
	}
}

func newPollToken() (string, error) {
	buf := make([]byte, pollTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
