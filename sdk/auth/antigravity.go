package auth

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/antigravity"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// AntigravityAuthenticator implements OAuth login for the antigravity provider.
type AntigravityAuthenticator struct{}

// NewAntigravityAuthenticator constructs a new authenticator instance.
func NewAntigravityAuthenticator() Authenticator { return &AntigravityAuthenticator{} }

// Provider returns the provider key for antigravity.
func (AntigravityAuthenticator) Provider() string { return "antigravity" }

// RefreshLead instructs the manager to refresh five minutes before expiry.
func (AntigravityAuthenticator) RefreshLead() *time.Duration {
	return new(5 * time.Minute)
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
			input, errPrompt := opts.Prompt("Paste the antigravity callback URL (or press Enter to keep waiting): ")
			if errPrompt != nil {
				return nil, errPrompt
			}
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

	// Fetch project ID via loadCodeAssist (same approach as Gemini CLI)
	projectID := ""
	if accessToken != "" {
		fetchedProjectID, errProject := authSvc.FetchProjectID(ctx, accessToken)
		if errProject != nil {
			log.Warnf("antigravity: failed to fetch project ID: %v", errProject)
		} else {
			projectID = fetchedProjectID
			log.Infof("antigravity: obtained project ID %s", projectID)
		}
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
	if email != "" {
		metadata["email"] = email
	}
	if projectID != "" {
		metadata["project_id"] = projectID
	}

	fileName := antigravity.CredentialFileName(email)
	label := email
	if label == "" {
		label = "antigravity"
	}

	fmt.Println("Antigravity authentication successful")
	if projectID != "" {
		fmt.Printf("Using GCP project: %s\n", projectID)
	}
	return &coreauth.Auth{
		ID:       fileName,
		Provider: "antigravity",
		FileName: fileName,
		Label:    label,
		Metadata: metadata,
	}, nil
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
