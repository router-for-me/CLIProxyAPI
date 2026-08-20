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
	// DefaultOAuthBaseURL is the ZCode OAuth base endpoint for token exchange.
	// Both Z.AI international and BigModel exchange authorization codes here.
	DefaultOAuthBaseURL = "https://zcode.z.ai/api/v1"

	// ProviderZAI authenticates against Z.AI international (chat.z.ai).
	ProviderZAI = "zai"
	// ProviderBigModel authenticates against Zhipu BigModel, China mainland (bigmodel.cn).
	ProviderBigModel = "bigmodel"

	// maxPollDuration bounds how long we wait for the user to authorize.
	maxPollDuration = 10 * time.Minute
	// pollTokenBytes is the size of the client-generated state token.
	pollTokenBytes = 32

	// zaiAuthorizeURL is the Z.AI international OAuth authorize endpoint.
	// Discovered from the official ZCode desktop app (v3.7.7).
	zaiAuthorizeURL = "https://chat.z.ai/api/oauth/authorize"
	// zaiClientID is the Z.AI international OAuth client ID from the official ZCode app.
	zaiClientID = "client_P8X5CMWmlaRO9gyO-KSqtg"

	// bigModelLoginURL drives the BigModel browser-redirect login. BigModel
	// rejects the server-mediated CLI callback but accepts a localhost redirect,
	// so its login runs a local callback server and exchanges the returned code
	// at the ZCode oauth/token endpoint.
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

// InitResponse is the parsed payload of the OAuth flow init.
type InitResponse struct {
	FlowID       string `json:"flow_id"`
	PollToken    string `json:"poll_token"`
	AuthorizeURL string `json:"authorize_url"`
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

// ZAIAuth handles the OAuth flow for a single ZCode identity provider
// (Z.AI international or BigModel). Both providers now use the same
// browser-callback pattern: a local loopback server captures the
// authorization code, which is then exchanged at the ZCode OAuth token endpoint.
type ZAIAuth struct {
	httpClient *http.Client
	provider   string
	baseURL    string
	// callbackPort is the loopback port for the OAuth callback. Zero
	// selects an automatic free port; a positive value (from --oauth-callback-port)
	// is used verbatim, matching the other OAuth providers.
	callbackPort int

	// Browser-redirect flow state, populated by StartFlow and consumed by
	// WaitForAuthorization on the same instance. (The SDK login and management
	// handler both keep one instance.)
	server   *http.Server
	listener net.Listener
	state    string
	redirect string
	result   chan callbackResult
}

// callbackResult carries the OAuth result captured by the local callback.
type callbackResult struct {
	code  string
	state string
	err   error
}

// NewZAIAuth creates a ZAIAuth bound to the given identity provider. proxyURL
// overrides cfg.ProxyURL when non-empty. callbackPort overrides the automatic
// loopback callback port when positive (used by both Z.AI and BigModel flows).
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
	// Both Z.AI international and BigModel now use the browser-callback flow
	// (local loopback server captures the authorization code). The old
	// zcode.z.ai/api/v1/oauth/cli/init endpoint was deprecated/removed.
	return a.startBrowserFlow()
}

// WaitForAuthorization waits for the user to authorize the request in the browser
// (via the local loopback callback) or the flow to expire, then returns the
// minted credentials.
func (a *ZAIAuth) WaitForAuthorization(ctx context.Context, init *InitResponse) (*ReadyResult, error) {
	return a.waitForBrowserAuthorization(ctx)
}

// startBrowserFlow starts a local callback server and builds the OAuth
// authorize URL for the configured provider. Both Z.AI international and
// BigModel reject the server-mediated CLI callback but accept a localhost
// redirect, so the login runs a local callback server and exchanges the
// returned code at the ZCode oauth/token endpoint.
func (a *ZAIAuth) startBrowserFlow() (*InitResponse, error) {
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
	a.state = state
	a.redirect = fmt.Sprintf("http://127.0.0.1:%d/callback", port)
	a.result = make(chan callbackResult, 1)
	srv := &http.Server{Handler: http.HandlerFunc(a.handleCallback)}
	a.listener = ln
	a.server = srv
	go func() {
		if errServe := srv.Serve(ln); errServe != nil && errServe != http.ErrServerClosed {
			log.Debugf("zai: callback server stopped: %v", errServe)
		}
	}()

	authorizeURL, err := buildAuthorizeURL(a.provider, a.redirect, state)
	if err != nil {
		a.shutdownServer()
		return nil, err
	}

	return &InitResponse{
		FlowID:       a.provider,
		AuthorizeURL: authorizeURL,
		PollToken:    state,
	}, nil
}

// buildAuthorizeURL constructs the OAuth authorize URL for the given provider.
// Z.AI international uses the ZCode desktop client's OAuth endpoints.
// BigModel uses its own authorize URL with the "zcode" app ID.
func buildAuthorizeURL(provider, redirectURI, state string) (string, error) {
	switch provider {
	case ProviderZAI:
		params := url.Values{
			"client_id":     {zaiClientID},
			"response_type": {"code"},
			"redirect_uri":  {redirectURI},
			"state":         {state},
		}
		return zaiAuthorizeURL + "?" + params.Encode(), nil
	case ProviderBigModel:
		params := url.Values{
			"redirect": {redirectURI},
			"appId":    {bigModelAppID},
			"state":    {state},
		}
		return bigModelLoginURL + "?" + params.Encode(), nil
	default:
		return "", fmt.Errorf("zai: unknown provider %q", provider)
	}
}

// handleCallback captures the authorization code from the local redirect.
func (a *ZAIAuth) handleCallback(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.URL.Path, "/callback") {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	code := strings.TrimSpace(q.Get("code"))
	if code == "" {
		code = strings.TrimSpace(q.Get("authCode"))
	}
	state := strings.TrimSpace(q.Get("state"))
	errParam := strings.TrimSpace(q.Get("error"))
	if errParam == "" {
		errParam = strings.TrimSpace(q.Get("error_description"))
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, "<!doctype html><html><body style=\"font-family:sans-serif\"><h2>Authorization received. You can close this tab and return to the terminal.</h2></body></html>")

	cb := callbackResult{code: code, state: state}
	switch {
	case errParam != "":
		cb.err = fmt.Errorf("zai: authorization: %s", errParam)
	case code == "" || state == "":
		cb.err = fmt.Errorf("zai: callback missing code or state")
	}
	select {
	case a.result <- cb:
	default:
	}
}

// InjectCallback delivers a manually supplied authorization code into a pending
// flow. The management /oauth-callback watcher uses it when a remote
// browser cannot reach the loopback listener. The watcher has already validated
// the callback against the management session (which uses a different state),
// so the code is delivered with the flow's own state. It is a no-op when no
// flow is waiting.
func (a *ZAIAuth) InjectCallback(authCode string) {
	if a.result == nil || strings.TrimSpace(authCode) == "" {
		return
	}
	select {
	case a.result <- callbackResult{code: authCode, state: a.state}:
	default:
	}
}

// InjectError fails a pending flow with the given message — used when a
// pasted (or loopback) callback carried an OAuth error instead of an
// authorization code, so the login fails promptly instead of waiting for the
// authorization timeout. It is a no-op when no flow is waiting.
func (a *ZAIAuth) InjectError(message string) {
	if a.result == nil {
		return
	}
	if strings.TrimSpace(message) == "" {
		message = "authorization failed or was denied"
	}
	select {
	case a.result <- callbackResult{state: a.state, err: fmt.Errorf("zai: authorization: %s", message)}:
	default:
	}
}

// waitForBrowserAuthorization waits for the local callback, validates the state,
// and exchanges the authorization code for a provider access token via the
// ZCode oauth/token endpoint.
func (a *ZAIAuth) waitForBrowserAuthorization(ctx context.Context) (*ReadyResult, error) {
	defer a.shutdownServer()
	if a.result == nil {
		return nil, fmt.Errorf("zai: browser flow not started")
	}
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("zai: context cancelled: %w", ctx.Err())
	case <-time.After(maxPollDuration):
		return nil, fmt.Errorf("zai: authorization timed out")
	case cb := <-a.result:
		if cb.err != nil {
			return nil, cb.err
		}
		if cb.state != a.state {
			return nil, fmt.Errorf("zai: state mismatch")
		}
		return a.exchangeCode(ctx, cb.code)
	}
}

// exchangeCode swaps the authorization code for an access token via the
// ZCode oauth/token endpoint (the same exchange the official ZCode client uses).
// The ZCode oauth/token endpoint occasionally returns a transient upstream error
// (e.g. HTTP 500 {"code":2007,"msg":"http error"}) while it validates the
// authorization code with the identity provider, so retry a few times before giving up.
func (a *ZAIAuth) exchangeCode(ctx context.Context, code string) (*ReadyResult, error) {
	body, _ := json.Marshal(map[string]string{
		"provider":     a.provider,
		"code":         code,
		"redirect_uri": a.redirect,
		"state":        a.state,
	})

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
			log.Warnf("zai: token exchange attempt %d/3 failed, retrying: %v", attempt, lastErr)
			select {
			case <-ctx.Done():
				return nil, fmt.Errorf("zai: context cancelled: %w", ctx.Err())
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}
	}
	if lastErr != nil {
		return nil, fmt.Errorf("zai: token exchange: %w", lastErr)
	}
	var out struct {
		Token string `json:"token"`
		User  struct {
			UserID string `json:"user_id"`
			Email  string `json:"email"`
			Name   string `json:"name"`
		} `json:"user"`
		ZAI struct {
			AccessToken string `json:"access_token"`
		} `json:"zai"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("zai: parse token exchange: %w", err)
	}
	token := strings.TrimSpace(out.Token)
	if token == "" {
		return nil, fmt.Errorf("zai: token exchange returned no token")
	}
	access := strings.TrimSpace(out.ZAI.AccessToken)
	if access == "" {
		access = token
	}
	return &ReadyResult{
		Token:          token,
		ZAIAccessToken: access,
		UserID:         out.User.UserID,
		Email:          out.User.Email,
		Name:           out.User.Name,
	}, nil
}

// shutdownServer stops the local callback server, if running.
func (a *ZAIAuth) shutdownServer() {
	if a.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = a.server.Shutdown(ctx)
		cancel()
		a.server = nil
	}
	if a.listener != nil {
		_ = a.listener.Close()
		a.listener = nil
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
