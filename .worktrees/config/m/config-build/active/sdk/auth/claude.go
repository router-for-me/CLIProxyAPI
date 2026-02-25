package auth

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/browser"
	// legacy client removed
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// ClaudeAuthenticator implements the OAuth login flow for Anthropic Claude accounts.
type ClaudeAuthenticator struct {
	CallbackPort int
}

// NewClaudeAuthenticator constructs a Claude authenticator with default settings.
func NewClaudeAuthenticator() *ClaudeAuthenticator {
	return &ClaudeAuthenticator{CallbackPort: 54545}
}

func (a *ClaudeAuthenticator) Provider() string {
	return "claude"
}

func (a *ClaudeAuthenticator) RefreshLead() *time.Duration {
	return new(4 * time.Hour)
}

func (a *ClaudeAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if opts == nil {
		opts = &LoginOptions{}
	}

	callbackPort := a.CallbackPort
	if opts.CallbackPort > 0 {
		callbackPort = opts.CallbackPort
	}

	pkceCodes, err := claude.GeneratePKCECodes()
	if err != nil {
		return nil, fmt.Errorf("claude pkce generation failed: %w", err)
	}

	state, err := misc.GenerateRandomState()
	if err != nil {
		return nil, fmt.Errorf("claude state generation failed: %w", err)
	}

	oauthServer := claude.NewOAuthServer(callbackPort)
	if err = oauthServer.Start(); err != nil {
		if strings.Contains(err.Error(), "already in use") {
			return nil, claude.NewAuthenticationError(claude.ErrPortInUse, err)
		}
		return nil, claude.NewAuthenticationError(claude.ErrServerStartFailed, err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if stopErr := oauthServer.Stop(stopCtx); stopErr != nil {
			log.Warnf("claude oauth server stop error: %v", stopErr)
		}
	}()

	authSvc := claude.NewClaudeAuth(cfg)

	authURL, returnedState, err := authSvc.GenerateAuthURL(state, pkceCodes)
	if err != nil {
		return nil, fmt.Errorf("claude authorization url generation failed: %w", err)
	}
	state = returnedState

	if !opts.NoBrowser {
		fmt.Println("Opening browser for Claude authentication")
		if !browser.IsAvailable() {
			log.Warn("No browser available; please open the URL manually")
			util.PrintSSHTunnelInstructions(callbackPort)
			fmt.Printf("Visit the following URL to continue authentication:\n%s\n", authURL)
		} else if err = browser.OpenURL(authURL); err != nil {
			log.Warnf("Failed to open browser automatically: %v", err)
			util.PrintSSHTunnelInstructions(callbackPort)
			fmt.Printf("Visit the following URL to continue authentication:\n%s\n", authURL)
		}
	} else {
		util.PrintSSHTunnelInstructions(callbackPort)
		fmt.Printf("Visit the following URL to continue authentication:\n%s\n", authURL)
	}

	fmt.Println("Waiting for Claude authentication callback...")

	callbackCh := make(chan *claude.OAuthResult, 1)
	callbackErrCh := make(chan error, 1)
	manualDescription := ""

	go func() {
		result, errWait := oauthServer.WaitForCallback(5 * time.Minute)
		if errWait != nil {
			callbackErrCh <- errWait
			return
		}
		callbackCh <- result
	}()

	var result *claude.OAuthResult
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
			if strings.Contains(err.Error(), "timeout") {
				return nil, claude.NewAuthenticationError(claude.ErrCallbackTimeout, err)
			}
			return nil, err
		case <-manualPromptC:
			manualPromptC = nil
			if manualPromptTimer != nil {
				manualPromptTimer.Stop()
			}
			select {
			case result = <-callbackCh:
				break waitForCallback
			case err = <-callbackErrCh:
				if strings.Contains(err.Error(), "timeout") {
					return nil, claude.NewAuthenticationError(claude.ErrCallbackTimeout, err)
				}
				return nil, err
			default:
			}
			input, errPrompt := opts.Prompt("Paste the Claude callback URL (or press Enter to keep waiting): ")
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
			manualDescription = parsed.ErrorDescription
			result = &claude.OAuthResult{
				Code:  parsed.Code,
				State: parsed.State,
				Error: parsed.Error,
			}
			break waitForCallback
		}
	}

	if result.Error != "" {
		return nil, claude.NewOAuthError(result.Error, manualDescription, http.StatusBadRequest)
	}

	if result.State != state {
		log.Errorf("State mismatch: expected %s, got %s", state, result.State)
		return nil, claude.NewAuthenticationError(claude.ErrInvalidState, fmt.Errorf("state mismatch"))
	}

	log.Debug("Claude authorization code received; exchanging for tokens")
	log.Debugf("Code: %s, State: %s", result.Code[:min(20, len(result.Code))], state)

	authBundle, err := authSvc.ExchangeCodeForTokens(ctx, result.Code, state, pkceCodes)
	if err != nil {
		log.Errorf("Token exchange failed: %v", err)
		return nil, claude.NewAuthenticationError(claude.ErrCodeExchangeFailed, err)
	}

	tokenStorage := authSvc.CreateTokenStorage(authBundle)

	if tokenStorage == nil || tokenStorage.Email == "" {
		return nil, fmt.Errorf("claude token storage missing account information")
	}

	fileName := fmt.Sprintf("claude-%s.json", tokenStorage.Email)
	metadata := map[string]any{
		"email": tokenStorage.Email,
	}

	fmt.Println("Claude authentication successful")
	if authBundle.APIKey != "" {
		fmt.Println("Claude API key obtained and stored")
	}

	return &coreauth.Auth{
		ID:       fileName,
		Provider: a.Provider(),
		FileName: fileName,
		Storage:  tokenStorage,
		Metadata: metadata,
	}, nil
}
