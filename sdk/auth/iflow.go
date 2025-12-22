package auth

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/iflow"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// IFlowAuthenticator implements the OAuth login flow for iFlow accounts.
type IFlowAuthenticator struct{}

// NewIFlowAuthenticator constructs a new authenticator instance.
func NewIFlowAuthenticator() *IFlowAuthenticator { return &IFlowAuthenticator{} }

// Provider returns the provider key for the authenticator.
func (a *IFlowAuthenticator) Provider() string { return "iflow" }

// RefreshLead indicates how soon before expiry a refresh should be attempted.
func (a *IFlowAuthenticator) RefreshLead() *time.Duration {
	d := 24 * time.Hour
	return &d
}

// Login performs the OAuth code flow using a local callback server.
func (a *IFlowAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if opts == nil {
		opts = &LoginOptions{}
	}

	authSvc := iflow.NewIFlowAuth(cfg)

	oauthServer := iflow.NewOAuthServer(iflow.CallbackPort)
	if err := oauthServer.Start(); err != nil {
		if strings.Contains(err.Error(), "already in use") {
			return nil, fmt.Errorf("iflow authentication server port in use: %w", err)
		}
		return nil, fmt.Errorf("iflow authentication server failed: %w", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if stopErr := oauthServer.Stop(stopCtx); stopErr != nil {
			log.Warnf("iflow oauth server stop error: %v", stopErr)
		}
	}()

	state, err := misc.GenerateRandomState()
	if err != nil {
		return nil, fmt.Errorf("iflow auth: failed to generate state: %w", err)
	}

	authURL, redirectURI := authSvc.AuthorizationURL(state, iflow.CallbackPort)

	if !opts.NoBrowser {
		fmt.Println("Opening browser for iFlow authentication")
		if !browser.IsAvailable() {
			log.Warn("No browser available; please open the URL manually")
			util.PrintSSHTunnelInstructions(iflow.CallbackPort)
			fmt.Printf("Visit the following URL to continue authentication:\n%s\n", authURL)
		} else if err = browser.OpenURL(authURL); err != nil {
			log.Warnf("Failed to open browser automatically: %v", err)
			util.PrintSSHTunnelInstructions(iflow.CallbackPort)
			fmt.Printf("Visit the following URL to continue authentication:\n%s\n", authURL)
		}
	} else {
		util.PrintSSHTunnelInstructions(iflow.CallbackPort)
		fmt.Printf("Visit the following URL to continue authentication:\n%s\n", authURL)
	}

	fmt.Println("Waiting for iFlow authentication callback...")

	callbackCh := make(chan *iflow.OAuthResult, 1)
	callbackErrCh := make(chan error, 1)

	go func() {
		result, errWait := oauthServer.WaitForCallback(5 * time.Minute)
		if errWait != nil {
			callbackErrCh <- errWait
			return
		}
		callbackCh <- result
	}()

	var result *iflow.OAuthResult
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
		case result = <-callbackCh:
			break waitForCallback
		case err = <-callbackErrCh:
			return nil, fmt.Errorf("iflow auth: callback wait failed: %w", err)
		case <-manualPromptC:
			manualPromptC = nil
			if manualPromptTimer != nil {
				manualPromptTimer.Stop()
			}
			select {
			case result = <-callbackCh:
				break waitForCallback
			case err = <-callbackErrCh:
				return nil, fmt.Errorf("iflow auth: callback wait failed: %w", err)
			default:
			}
			input, errPrompt := opts.Prompt("Paste the iFlow callback URL (or press Enter to keep waiting): ")
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
			result = &iflow.OAuthResult{
				Code:  parsed.Code,
				State: parsed.State,
				Error: parsed.Error,
			}
			break waitForCallback
		}
	}
	if result.Error != "" {
		return nil, fmt.Errorf("iflow auth: provider returned error %s", result.Error)
	}
	if result.State != state {
		return nil, fmt.Errorf("iflow auth: state mismatch")
	}

	tokenData, err := authSvc.ExchangeCodeForTokens(ctx, result.Code, redirectURI)
	if err != nil {
		return nil, fmt.Errorf("iflow authentication failed: %w", err)
	}

	tokenStorage := authSvc.CreateTokenStorage(tokenData)

	email := strings.TrimSpace(tokenStorage.Email)
	if email == "" {
		return nil, fmt.Errorf("iflow authentication failed: missing account identifier")
	}

	fileName := fmt.Sprintf("iflow-%s-%d.json", email, time.Now().Unix())
	metadata := map[string]any{
		"email":         email,
		"api_key":       tokenStorage.APIKey,
		"access_token":  tokenStorage.AccessToken,
		"refresh_token": tokenStorage.RefreshToken,
		"expired":       tokenStorage.Expire,
	}

	fmt.Println("iFlow authentication successful")

	return &coreauth.Auth{
		ID:       fileName,
		Provider: a.Provider(),
		FileName: fileName,
		Storage:  tokenStorage,
		Metadata: metadata,
		Attributes: map[string]string{
			"api_key": tokenStorage.APIKey,
		},
	}, nil
}
