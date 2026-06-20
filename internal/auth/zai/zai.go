package zai

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
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

	// bigModelLoginURL / bigModelAppID drive the BigModel browser-redirect login.
	// BigModel (unlike Z.AI international) rejects the server-mediated CLI callback
	// but accepts a localhost redirect, so its login runs a local callback server
	// and exchanges the returned code at the ZCode oauth/token endpoint.
	bigModelLoginURL = "https://bigmodel.cn/login"
	bigModelAppID    = "zcode"
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
	// callbackPort is the loopback port for the BigModel OAuth callback. Zero
	// selects an automatic free port; a positive value (from --oauth-callback-port)
	// is used verbatim, matching the other OAuth providers.
	callbackPort int

	// BigModel browser-redirect flow state, populated by StartFlow when the
	// provider is bigmodel and consumed by WaitForAuthorization on the same
	// instance. (The SDK login and management handler both keep one instance.)
	bmServer   *http.Server
	bmListener net.Listener
	bmState    string
	bmRedirect string
	bmResult   chan bmCallback
}

// bmCallback carries the BigModel OAuth result captured by the local callback.
type bmCallback struct {
	code  string
	state string
	err   error
}

// NewZAIAuth creates a ZAIAuth bound to the given identity provider. proxyURL
// overrides cfg.ProxyURL when non-empty. callbackPort overrides the automatic
// BigModel callback port when positive (used only by the bigmodel flow).
func NewZAIAuth(cfg *config.Config, provider, proxyURL string, callbackPort int) *ZAIAuth {
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
		httpClient:   client,
		provider:     NormalizeProvider(provider),
		baseURL:      DefaultOAuthBaseURL,
		callbackPort: callbackPort,
	}
}

// Provider returns the identity provider this client authenticates against.
func (a *ZAIAuth) Provider() string { return a.provider }

// StartFlow initiates the OAuth flow and returns the authorization URL plus the
// poll token used to wait for completion.
func (a *ZAIAuth) StartFlow(ctx context.Context) (*InitResponse, error) {
	if a.provider == ProviderBigModel {
		return a.startBigModelFlow()
	}
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
	if a.provider == ProviderBigModel {
		return a.waitForBigModelAuthorization(ctx)
	}
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

// startBigModelFlow starts a local callback server and builds the BigModel
// authorize URL. BigModel rejects the server-mediated CLI callback that Z.AI
// international accepts, but it does accept a localhost redirect, so the login
// captures the authorization code on a local HTTP server.
func (a *ZAIAuth) startBigModelFlow() (*InitResponse, error) {
	state, err := newPollToken()
	if err != nil {
		return nil, fmt.Errorf("zai: generate state: %w", err)
	}
	addr := "127.0.0.1:0"
	if a.callbackPort > 0 {
		addr = fmt.Sprintf("127.0.0.1:%d", a.callbackPort)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("zai: start callback listener: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	a.bmState = state
	a.bmRedirect = fmt.Sprintf("http://127.0.0.1:%d/callback", port)
	a.bmResult = make(chan bmCallback, 1)
	// Capture the server in a local so the serving goroutine never reads the
	// a.bmServer field, which shutdownBigModelServer may concurrently nil out.
	srv := &http.Server{Handler: http.HandlerFunc(a.handleBigModelCallback)}
	a.bmListener = ln
	a.bmServer = srv
	go func() {
		if errServe := srv.Serve(ln); errServe != nil && errServe != http.ErrServerClosed {
			log.Debugf("zai: bigmodel callback server stopped: %v", errServe)
		}
	}()

	params := url.Values{
		"redirect": {a.bmRedirect},
		"appId":    {bigModelAppID},
		"state":    {state},
	}
	return &InitResponse{
		FlowID:       "bigmodel-browser",
		AuthorizeURL: bigModelLoginURL + "?" + params.Encode(),
		PollToken:    state,
	}, nil
}

// handleBigModelCallback captures the authorization code from the local redirect.
func (a *ZAIAuth) handleBigModelCallback(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.URL.Path, "/callback") {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	code := strings.TrimSpace(q.Get("authCode"))
	if code == "" {
		code = strings.TrimSpace(q.Get("code"))
	}
	state := strings.TrimSpace(q.Get("state"))
	errParam := strings.TrimSpace(q.Get("error"))
	if errParam == "" {
		errParam = strings.TrimSpace(q.Get("error_description"))
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, "<!doctype html><html><body style=\"font-family:sans-serif\"><h2>Authorization received. You can close this tab and return to the terminal.</h2></body></html>")

	cb := bmCallback{code: code, state: state}
	switch {
	case errParam != "":
		cb.err = fmt.Errorf("zai: bigmodel authorization: %s", errParam)
	case code == "" || state == "":
		cb.err = fmt.Errorf("zai: bigmodel callback missing authCode or state")
	}
	select {
	case a.bmResult <- cb:
	default:
	}
}

// InjectCallback delivers a manually supplied authorization code into a pending
// BigModel flow. The management /oauth-callback watcher uses it when a remote
// browser cannot reach the loopback listener. The watcher has already validated
// the callback against the management session (which uses a different state than
// the BigModel OAuth redirect), so the code is delivered with the flow's own state.
// It is a no-op when no flow is waiting.
func (a *ZAIAuth) InjectCallback(authCode string) {
	if a.bmResult == nil || strings.TrimSpace(authCode) == "" {
		return
	}
	select {
	case a.bmResult <- bmCallback{code: authCode, state: a.bmState}:
	default:
	}
}

// InjectError fails a pending BigModel flow with the given message — used when a
// pasted (or loopback) callback carried an OAuth error instead of an authorization
// code, so the login fails promptly instead of waiting for the authorization
// timeout. It is a no-op when no flow is waiting.
func (a *ZAIAuth) InjectError(message string) {
	if a.bmResult == nil {
		return
	}
	if strings.TrimSpace(message) == "" {
		message = "authorization failed or was denied"
	}
	select {
	case a.bmResult <- bmCallback{state: a.bmState, err: fmt.Errorf("zai: bigmodel authorization: %s", message)}:
	default:
	}
}

// waitForBigModelAuthorization waits for the local callback, validates the state,
// and exchanges the authorization code for a BigModel access token.
func (a *ZAIAuth) waitForBigModelAuthorization(ctx context.Context) (*ReadyResult, error) {
	defer a.shutdownBigModelServer()
	if a.bmResult == nil {
		return nil, fmt.Errorf("zai: bigmodel flow not started")
	}
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("zai: context cancelled: %w", ctx.Err())
	case <-time.After(maxPollDuration):
		return nil, fmt.Errorf("zai: bigmodel authorization timed out")
	case cb := <-a.bmResult:
		if cb.err != nil {
			return nil, cb.err
		}
		if cb.state != a.bmState {
			return nil, fmt.Errorf("zai: bigmodel state mismatch")
		}
		return a.exchangeBigModelCode(ctx, cb.code)
	}
}

// exchangeBigModelCode swaps the BigModel authorization code for an access token
// via the ZCode oauth/token endpoint (the same exchange the official client uses).
func (a *ZAIAuth) exchangeBigModelCode(ctx context.Context, code string) (*ReadyResult, error) {
	body, _ := json.Marshal(map[string]string{
		"provider":     ProviderBigModel,
		"code":         code,
		"redirect_uri": a.bmRedirect,
		"state":        a.bmState,
	})

	// The ZCode oauth/token endpoint occasionally returns a transient upstream
	// error (e.g. HTTP 500 {"code":2007,"msg":"http error"}) while it validates the
	// authorization code with BigModel, so retry a few times before giving up.
	var data json.RawMessage
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		req, errReq := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/oauth/token", bytes.NewReader(body))
		if errReq != nil {
			return nil, fmt.Errorf("zai: create token exchange request: %w", errReq)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")

		data, lastErr = a.doEnvelope(req)
		if lastErr == nil {
			break
		}
		if attempt < 3 {
			log.Warnf("zai: bigmodel token exchange attempt %d/3 failed, retrying: %v", attempt, lastErr)
			select {
			case <-ctx.Done():
				return nil, fmt.Errorf("zai: context cancelled: %w", ctx.Err())
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}
	}
	if lastErr != nil {
		return nil, fmt.Errorf("zai: bigmodel token exchange: %w", lastErr)
	}
	var out struct {
		BigModel struct {
			AccessToken      string `json:"access_token"`
			AccessTokenCamel string `json:"accessToken"`
		} `json:"bigmodel"`
		AccessToken      string `json:"access_token"`
		AccessTokenCamel string `json:"accessToken"`
		User             struct {
			UserID string `json:"user_id"`
			Email  string `json:"email"`
			Name   string `json:"name"`
		} `json:"user"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("zai: parse bigmodel token exchange: %w", err)
	}
	token := strings.TrimSpace(out.BigModel.AccessToken)
	if token == "" {
		token = strings.TrimSpace(out.BigModel.AccessTokenCamel)
	}
	if token == "" {
		token = strings.TrimSpace(out.AccessToken)
	}
	if token == "" {
		token = strings.TrimSpace(out.AccessTokenCamel)
	}
	if token == "" {
		return nil, fmt.Errorf("zai: bigmodel token exchange returned no access token")
	}
	return &ReadyResult{
		Token:          token,
		ZAIAccessToken: token,
		UserID:         out.User.UserID,
		Email:          out.User.Email,
		Name:           out.User.Name,
	}, nil
}

// shutdownBigModelServer stops the local callback server, if running.
func (a *ZAIAuth) shutdownBigModelServer() {
	if a.bmServer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = a.bmServer.Shutdown(ctx)
		cancel()
		a.bmServer = nil
	}
	if a.bmListener != nil {
		_ = a.bmListener.Close()
		a.bmListener = nil
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
