package auth

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/antigravity"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// AntigravityAuthenticator implements OAuth login for the antigravity provider.
type AntigravityAuthenticator struct{}

// NewAntigravityAuthenticator constructs a new authenticator instance.
func NewAntigravityAuthenticator() Authenticator { return &AntigravityAuthenticator{} }

// Provider returns the provider key for antigravity.
func (AntigravityAuthenticator) Provider() string { return "antigravity" }

// RefreshLead instructs the manager to refresh 30 minutes before expiry.
func (AntigravityAuthenticator) RefreshLead() *time.Duration {
	return new(30 * time.Minute)
}

// Login launches a local OAuth flow to obtain antigravity tokens and persists them.
func (AntigravityAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if opts == nil {
		opts = &LoginOptions{}
	}

	callbackPort := antigravity.CallbackPort
	if opts.CallbackPort > 0 {
		callbackPort = opts.CallbackPort
	}

	authSvc := antigravity.NewAntigravityAuth(cfg, nil)

	state, err := misc.GenerateRandomState()
	if err != nil {
		return nil, fmt.Errorf("antigravity: failed to generate state: %w", err)
	}

	srv, port, cbChan, errServer := startAntigravityCallbackServer(callbackPort)
	if errServer != nil {
		return nil, fmt.Errorf("antigravity: failed to start callback server: %w", errServer)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	redirectURI := fmt.Sprintf("http://localhost:%d/oauth-callback", port)
	authURL := authSvc.BuildAuthURL(state, redirectURI)

	if !opts.NoBrowser {
		fmt.Println("Opening browser for antigravity authentication")
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

	fmt.Println("Waiting for antigravity authentication callback...")

	var cbRes callbackResult
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
		case res := <-cbChan:
			cbRes = res
			break waitForCallback
		case <-manualPromptC:
			manualPromptC = nil
			if manualPromptTimer != nil {
				manualPromptTimer.Stop()
			}
			select {
			case res := <-cbChan:
				cbRes = res
				break waitForCallback
			default:
			}
			manualInputCh, manualInputErrCh = misc.AsyncPrompt(opts.Prompt, "Paste the antigravity callback URL (or press Enter to keep waiting): ")
			continue
		case input := <-manualInputCh:
			manualInputCh = nil
			manualInputErrCh = nil
			parsed, errParse := misc.ParseOAuthCallback(input)
			if errParse != nil {
				return nil, errParse
			}
			if parsed == nil {
				continue
			}
			cbRes = callbackResult{
				Code:  parsed.Code,
				State: parsed.State,
				Error: parsed.Error,
			}
			break waitForCallback
		case errManual := <-manualInputErrCh:
			return nil, errManual
		case <-timeoutTimer.C:
			return nil, fmt.Errorf("antigravity: authentication timed out")
		}
	}

	if cbRes.Error != "" {
		return nil, fmt.Errorf("antigravity: authentication failed: %s", cbRes.Error)
	}
	if cbRes.State != state {
		return nil, fmt.Errorf("antigravity: invalid state")
	}
	if cbRes.Code == "" {
		return nil, fmt.Errorf("antigravity: missing authorization code")
	}

	tokenResp, errToken := authSvc.ExchangeCodeForTokens(ctx, cbRes.Code, redirectURI)
	if errToken != nil {
		return nil, fmt.Errorf("antigravity: token exchange failed: %w", errToken)
	}

	accessToken := strings.TrimSpace(tokenResp.AccessToken)
	if accessToken == "" {
		return nil, fmt.Errorf("antigravity: token exchange returned empty access token")
	}

	email, errInfo := authSvc.FetchUserInfo(ctx, accessToken)
	if errInfo != nil {
		return nil, fmt.Errorf("antigravity: fetch user info failed: %w", errInfo)
	}
	email = strings.TrimSpace(email)
	if email == "" {
		return nil, fmt.Errorf("antigravity: empty email returned from user info")
	}

	// Fetch project ID via loadCodeAssist.
	projectID := ""
	if accessToken != "" {
		fetchedProjectID, errProject := authSvc.FetchProjectID(ctx, accessToken)
		if errProject != nil {
			return nil, fmt.Errorf("antigravity: failed to fetch project ID: %w", errProject)
		} else {
			projectID = fetchedProjectID
			log.Infof("antigravity: obtained project ID %s", util.HideAPIKey(projectID))
		}
	}
	if strings.TrimSpace(projectID) == "" {
		return nil, fmt.Errorf("antigravity: project ID discovery returned empty project")
	}

	fmt.Println("Antigravity authentication successful")
	if projectID != "" {
		fmt.Printf("Using GCP project: %s\n", util.HideAPIKey(projectID))
	}
	return BuildAntigravityAuth(&AntigravityTokenResponse{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		ExpiresIn:    tokenResp.ExpiresIn,
		TokenType:    tokenResp.TokenType,
	}, email, projectID), nil
}

type callbackResult struct {
	Code  string
	Error string
	State string
}

func startAntigravityCallbackServer(port int) (*http.Server, int, <-chan callbackResult, error) {
	if port <= 0 {
		port = antigravity.CallbackPort
	}
	addr := fmt.Sprintf(":%d", port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, 0, nil, err
	}
	port = listener.Addr().(*net.TCPAddr).Port
	resultCh := make(chan callbackResult, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/oauth-callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		res := callbackResult{
			Code:  strings.TrimSpace(q.Get("code")),
			Error: strings.TrimSpace(q.Get("error")),
			State: strings.TrimSpace(q.Get("state")),
		}
		resultCh <- res
		if res.Code != "" && res.Error == "" {
			_, _ = w.Write([]byte("<h1>Login successful</h1><p>You can close this window.</p>"))
		} else {
			_, _ = w.Write([]byte("<h1>Login failed</h1><p>Please check the CLI output.</p>"))
		}
	})

	srv := &http.Server{Handler: mux}
	go func() {
		if errServe := srv.Serve(listener); errServe != nil && !strings.Contains(errServe.Error(), "Server closed") {
			log.Warnf("antigravity callback server error: %v", errServe)
		}
	}()

	return srv, port, resultCh, nil
}

// FetchAntigravityProjectID exposes project discovery for external callers.
func FetchAntigravityProjectID(ctx context.Context, accessToken string, httpClient *http.Client) (string, error) {
	cfg := &config.Config{}
	authSvc := antigravity.NewAntigravityAuth(cfg, httpClient)
	return authSvc.FetchProjectID(ctx, accessToken)
}

// AntigravityDefaultCallbackPort is the default local port used for Antigravity OAuth loopback callbacks.
const AntigravityDefaultCallbackPort = antigravity.CallbackPort

// AntigravityTokenResponse captures the tokens returned by Antigravity OAuth exchange.
type AntigravityTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	TokenType    string `json:"token_type"`
}

// AntigravityDefaultCallbackURI returns the default loopback redirect URI for Antigravity OAuth.
func AntigravityDefaultCallbackURI() string {
	return fmt.Sprintf("http://localhost:%d/oauth-callback", AntigravityDefaultCallbackPort)
}

// AntigravityUserAgent returns the canonical User-Agent string used by the Antigravity Hub family.
func AntigravityUserAgent() string {
	return misc.AntigravityUserAgent()
}

// BuildAntigravityAuthURL generates the OAuth authorization URL for Antigravity.
// If redirectURI is empty, the default loopback callback URI is used.
func BuildAntigravityAuthURL(state, redirectURI string) string {
	authSvc := antigravity.NewAntigravityAuth(nil, nil)
	return authSvc.BuildAuthURL(state, redirectURI)
}

// ExchangeAntigravityCode exchanges an authorization code for access and refresh tokens.
func ExchangeAntigravityCode(ctx context.Context, code, redirectURI string, httpClient *http.Client) (*AntigravityTokenResponse, error) {
	if strings.TrimSpace(redirectURI) == "" {
		redirectURI = AntigravityDefaultCallbackURI()
	}
	authSvc := antigravity.NewAntigravityAuth(nil, httpClient)
	tokenResp, errExchange := authSvc.ExchangeCodeForTokens(ctx, code, redirectURI)
	if errExchange != nil {
		return nil, errExchange
	}
	if tokenResp == nil {
		return nil, fmt.Errorf("antigravity: empty token response")
	}
	return &AntigravityTokenResponse{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		ExpiresIn:    tokenResp.ExpiresIn,
		TokenType:    tokenResp.TokenType,
	}, nil
}

// FetchAntigravityUserInfo retrieves the user email address using the given access token.
func FetchAntigravityUserInfo(ctx context.Context, accessToken string, httpClient *http.Client) (string, error) {
	authSvc := antigravity.NewAntigravityAuth(nil, httpClient)
	return authSvc.FetchUserInfo(ctx, accessToken)
}

// BuildAntigravityAuth constructs an in-memory *coreauth.Auth object from token credentials and metadata.
func BuildAntigravityAuth(tokenResp *AntigravityTokenResponse, email, projectID string) *coreauth.Auth {
	if tokenResp == nil {
		return nil
	}
	now := time.Now()
	metadata := map[string]any{
		"type":          "antigravity",
		"access_token":  tokenResp.AccessToken,
		"refresh_token": tokenResp.RefreshToken,
		"expires_in":    tokenResp.ExpiresIn,
		"timestamp":     now.UnixMilli(),
		"expired":       now.Add(time.Duration(tokenResp.ExpiresIn) * time.Second).Format(time.RFC3339),
	}
	email = strings.TrimSpace(email)
	if email != "" {
		metadata["email"] = email
	}
	projectID = strings.TrimSpace(projectID)
	if projectID != "" {
		metadata["project_id"] = projectID
	}

	fileName := antigravity.CredentialFileName(email)
	label := email
	if label == "" {
		label = "antigravity"
	}

	return &coreauth.Auth{
		ID:       fileName,
		Provider: "antigravity",
		FileName: fileName,
		Label:    label,
		Metadata: metadata,
	}
}

// CompleteAntigravityOAuth performs code exchange, user identity retrieval, and GCP project discovery
// using an injected HTTP client, returning an assembled *coreauth.Auth without touching the filesystem.
func CompleteAntigravityOAuth(ctx context.Context, code, redirectURI string, httpClient *http.Client) (*coreauth.Auth, error) {
	tokenResp, errExchange := ExchangeAntigravityCode(ctx, code, redirectURI, httpClient)
	if errExchange != nil {
		return nil, fmt.Errorf("antigravity: token exchange failed: %w", errExchange)
	}

	accessToken := strings.TrimSpace(tokenResp.AccessToken)
	if accessToken == "" {
		return nil, fmt.Errorf("antigravity: token exchange returned empty access token")
	}

	email, errUserInfo := FetchAntigravityUserInfo(ctx, accessToken, httpClient)
	if errUserInfo != nil {
		return nil, fmt.Errorf("antigravity: fetch user info failed: %w", errUserInfo)
	}
	email = strings.TrimSpace(email)
	if email == "" {
		return nil, fmt.Errorf("antigravity: empty email returned from user info")
	}

	projectID, errProject := FetchAntigravityProjectID(ctx, accessToken, httpClient)
	if errProject != nil {
		return nil, fmt.Errorf("antigravity: failed to fetch project ID: %w", errProject)
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil, fmt.Errorf("antigravity: project ID discovery returned empty project")
	}

	return BuildAntigravityAuth(tokenResp, email, projectID), nil
}
