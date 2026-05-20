package auth

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	xaiauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/xai"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// XAIAuthenticator implements the xAI Grok OAuth loopback flow.
type XAIAuthenticator struct{}

// NewXAIAuthenticator constructs a new xAI authenticator.
func NewXAIAuthenticator() Authenticator {
	return &XAIAuthenticator{}
}

// Provider returns the provider key for xAI.
func (XAIAuthenticator) Provider() string {
	return "xai"
}

// RefreshLead instructs the manager to refresh before token expiry.
func (XAIAuthenticator) RefreshLead() *time.Duration {
	lead := xaiauth.RefreshLead()
	return &lead
}

// Login launches a local OAuth flow to obtain xAI tokens and persists them.
func (a XAIAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if opts == nil {
		opts = &LoginOptions{}
	}

	callbackPort := xaiauth.CallbackPort
	if opts.CallbackPort > 0 {
		callbackPort = opts.CallbackPort
	}

	pkceCodes, err := xaiauth.GeneratePKCECodes()
	if err != nil {
		return nil, fmt.Errorf("xai pkce generation failed: %w", err)
	}
	state, err := misc.GenerateRandomState()
	if err != nil {
		return nil, fmt.Errorf("xai state generation failed: %w", err)
	}
	nonce, err := misc.GenerateRandomState()
	if err != nil {
		return nil, fmt.Errorf("xai nonce generation failed: %w", err)
	}

	authSvc := xaiauth.NewXAIAuth(cfg)
	discovery, err := authSvc.Discover(ctx)
	if err != nil {
		return nil, err
	}

	srv, port, callbackCh, errServer := startXAICallbackServer(callbackPort)
	if errServer != nil {
		return nil, fmt.Errorf("xai: failed to start callback server: %w", errServer)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if errShutdown := srv.Shutdown(shutdownCtx); errShutdown != nil {
			log.Warnf("xai callback server shutdown error: %v", errShutdown)
		}
	}()

	redirectURI := fmt.Sprintf("http://%s:%d%s", xaiauth.RedirectHost, port, xaiauth.RedirectPath)
	authURL, err := xaiauth.BuildAuthorizeURL(xaiauth.AuthorizeURLParams{
		AuthorizationEndpoint: discovery.AuthorizationEndpoint,
		RedirectURI:           redirectURI,
		CodeChallenge:         pkceCodes.CodeChallenge,
		State:                 state,
		Nonce:                 nonce,
	})
	if err != nil {
		return nil, err
	}

	if !opts.NoBrowser {
		fmt.Println("Opening browser for xAI authentication")
		if !browser.IsAvailable() {
			log.Warn("No browser available; please open the URL manually")
			util.PrintSSHTunnelInstructions(port)
			fmt.Printf("Visit the following URL to continue authentication:\n%s\n", authURL)
		} else if errOpen := browser.OpenURL(authURL); errOpen != nil {
			log.Warnf("Failed to open browser automatically: %v", errOpen)
			util.PrintSSHTunnelInstructions(port)
			fmt.Printf("Visit the following URL to continue authentication:\n%s\n", authURL)
		}
	} else {
		util.PrintSSHTunnelInstructions(port)
		fmt.Printf("Visit the following URL to continue authentication:\n%s\n", authURL)
	}

	fmt.Println("Waiting for xAI authentication callback...")

	var result callbackResult
	timeoutTimer := time.NewTimer(5 * time.Minute)
	defer timeoutTimer.Stop()

	var manualPromptTimer *time.Timer
	var manualPromptC <-chan time.Time
	if opts.Prompt != nil {
		manualPromptTimer = time.NewTimer(15 * time.Second)
		manualPromptC = manualPromptTimer.C
		defer manualPromptTimer.Stop()
	}

	var manualInputCh <-chan string
	var manualInputErrCh <-chan error

waitForCallback:
	for {
		select {
		case result = <-callbackCh:
			break waitForCallback
		case <-manualPromptC:
			manualPromptC = nil
			if manualPromptTimer != nil {
				manualPromptTimer.Stop()
			}
			select {
			case result = <-callbackCh:
				break waitForCallback
			default:
			}
			manualInputCh, manualInputErrCh = misc.AsyncPrompt(opts.Prompt, "Paste the xAI callback Token (or press Enter to keep waiting): ")
			continue
		case input := <-manualInputCh:
			manualInputCh = nil
			manualInputErrCh = nil
			manualResult, ok, errParse := parseXAIManualCallbackToken(input, state)
			if errParse != nil {
				return nil, errParse
			}
			if !ok {
				continue
			}
			result = manualResult
			break waitForCallback
		case errManual := <-manualInputErrCh:
			return nil, errManual
		case <-timeoutTimer.C:
			return nil, fmt.Errorf("xai: authentication timed out")
		}
	}

	if result.Error != "" {
		return nil, fmt.Errorf("xai: authentication failed: %s", result.Error)
	}
	if result.State != state {
		return nil, fmt.Errorf("xai: invalid state")
	}
	if result.Code == "" {
		return nil, fmt.Errorf("xai: missing authorization code")
	}

	bundle, errExchange := authSvc.ExchangeCodeForTokens(ctx, result.Code, redirectURI, pkceCodes, discovery.TokenEndpoint)
	if errExchange != nil {
		return nil, fmt.Errorf("xai: token exchange failed: %w", errExchange)
	}
	tokenStorage := authSvc.CreateTokenStorage(bundle)
	if tokenStorage == nil || strings.TrimSpace(tokenStorage.AccessToken) == "" {
		return nil, fmt.Errorf("xai token storage missing access token")
	}

	fileName := xaiauth.CredentialFileName(tokenStorage.Email, tokenStorage.Subject)
	label := strings.TrimSpace(tokenStorage.Email)
	if label == "" {
		label = "xAI"
	}

	metadata := map[string]any{
		"type":           "xai",
		"access_token":   tokenStorage.AccessToken,
		"refresh_token":  tokenStorage.RefreshToken,
		"id_token":       tokenStorage.IDToken,
		"token_type":     tokenStorage.TokenType,
		"expires_in":     tokenStorage.ExpiresIn,
		"expired":        tokenStorage.Expire,
		"last_refresh":   tokenStorage.LastRefresh,
		"base_url":       tokenStorage.BaseURL,
		"redirect_uri":   tokenStorage.RedirectURI,
		"token_endpoint": tokenStorage.TokenEndpoint,
		"auth_kind":      "oauth",
	}
	if tokenStorage.Email != "" {
		metadata["email"] = tokenStorage.Email
	}
	if tokenStorage.Subject != "" {
		metadata["sub"] = tokenStorage.Subject
	}

	fmt.Println("xAI authentication successful")

	return &coreauth.Auth{
		ID:       fileName,
		Provider: a.Provider(),
		FileName: fileName,
		Label:    label,
		Storage:  tokenStorage,
		Metadata: metadata,
		Attributes: map[string]string{
			"auth_kind": "oauth",
			"base_url":  tokenStorage.BaseURL,
		},
	}, nil
}

func parseXAIManualCallbackToken(input string, state string) (callbackResult, bool, error) {
	token := strings.TrimSpace(input)
	if token == "" {
		return callbackResult{}, false, nil
	}
	if strings.Contains(token, "://") || strings.Contains(token, "?") || strings.Contains(token, "code=") {
		return callbackResult{}, false, fmt.Errorf("xai: paste only the callback token")
	}
	return callbackResult{Code: token, State: state}, true, nil
}

func startXAICallbackServer(port int) (*http.Server, int, <-chan callbackResult, error) {
	if port <= 0 {
		port = xaiauth.CallbackPort
	}
	addr := fmt.Sprintf("%s:%d", xaiauth.RedirectHost, port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, 0, nil, err
	}
	port = listener.Addr().(*net.TCPAddr).Port
	resultCh := make(chan callbackResult, 1)

	mux := http.NewServeMux()
	mux.HandleFunc(xaiauth.RedirectPath, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		result := callbackResult{
			Code:  strings.TrimSpace(q.Get("code")),
			Error: strings.TrimSpace(q.Get("error")),
			State: strings.TrimSpace(q.Get("state")),
		}
		resultCh <- result
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if result.Code != "" && result.Error == "" {
			_, _ = w.Write([]byte("<h1>Login successful</h1><p>You can close this window.</p>"))
			return
		}
		_, _ = w.Write([]byte("<h1>Login failed</h1><p>Please check the CLI output.</p>"))
	})

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      5 * time.Second,
	}
	go func() {
		if errServe := srv.Serve(listener); errServe != nil && !strings.Contains(errServe.Error(), "Server closed") {
			log.Warnf("xai callback server error: %v", errServe)
		}
	}()

	return srv, port, resultCh, nil
}
