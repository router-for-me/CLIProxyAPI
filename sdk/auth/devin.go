package auth

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	devinauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/devin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// DevinAuthenticator implements OAuth and headless authentication for Devin / Cognition.
type DevinAuthenticator struct {
	CallbackPort int
}

// NewDevinAuthenticator constructs a new Devin authenticator instance.
func NewDevinAuthenticator() *DevinAuthenticator {
	return &DevinAuthenticator{
		CallbackPort: 0, // Bind to any available ephemeral port by default
	}
}

// Provider returns the unique provider identifier for Devin.
func (a *DevinAuthenticator) Provider() string {
	return "devin"
}

// RefreshLead returns nil since Devin OAuth tokens are permanent session tokens.
func (a *DevinAuthenticator) RefreshLead() *time.Duration {
	return nil
}

// Login executes the interactive browser-based or headless manual authentication flow for Devin.
func (a *DevinAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if opts == nil {
		opts = &LoginOptions{}
	}

	pkceCodes, errPKCE := devinauth.GeneratePKCECodes()
	if errPKCE != nil {
		return nil, fmt.Errorf("devin pkce generation failed: %w", errPKCE)
	}

	state, errState := misc.GenerateRandomState()
	if errState != nil {
		return nil, fmt.Errorf("devin state generation failed: %w", errState)
	}

	callbackPort := a.CallbackPort
	if opts.CallbackPort > 0 {
		callbackPort = opts.CallbackPort
	}

	oauthServer := devinauth.NewOAuthServer(callbackPort)
	actualPort, errStart := oauthServer.Start()
	if errStart != nil {
		return nil, fmt.Errorf("failed to start devin oauth callback server: %w", errStart)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = oauthServer.Stop(stopCtx)
	}()

	authSvc := devinauth.NewDevinAuthService(util.SetProxy(&cfg.SDKConfig, &http.Client{Timeout: 30 * time.Second}))
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d/callback", actualPort)
	authURL := authSvc.BuildAuthorizationURL(redirectURI, pkceCodes.CodeChallenge, state)

	if !opts.NoBrowser {
		fmt.Println("Opening browser for Devin authentication...")
		if !browser.IsAvailable() {
			log.Warn("No browser available; please open the URL manually")
			util.PrintSSHTunnelInstructions(actualPort)
			fmt.Printf("Visit the following URL to continue authentication:\n%s\n", authURL)
		} else if errOpen := browser.OpenURL(authURL); errOpen != nil {
			log.Warnf("Failed to open browser automatically: %v", errOpen)
			util.PrintSSHTunnelInstructions(actualPort)
			fmt.Printf("Visit the following URL to continue authentication:\n%s\n", authURL)
		}
	} else {
		util.PrintSSHTunnelInstructions(actualPort)
		fmt.Printf("Visit the following URL to continue Devin authentication:\n%s\n", authURL)
	}

	fmt.Println("Waiting for Devin authentication callback...")

	callbackCh := make(chan *devinauth.OAuthResult, 1)
	callbackErrCh := make(chan error, 1)

	go func() {
		result, errWait := oauthServer.WaitForCallbackWithContext(ctx, 5*time.Minute)
		if errWait != nil {
			select {
			case callbackErrCh <- errWait:
			case <-ctx.Done():
			}
			return
		}
		select {
		case callbackCh <- result:
		case <-ctx.Done():
		}
	}()

	var manualPromptTimer *time.Timer
	var manualPromptC <-chan time.Time
	if opts.Prompt != nil {
		manualPromptTimer = time.NewTimer(5 * time.Second)
		manualPromptC = manualPromptTimer.C
		defer manualPromptTimer.Stop()
	}

	var manualInputCh <-chan string
	var manualInputErrCh <-chan error
	var authCode string
	var rawPastedToken string

waitForResult:
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()

		case res := <-callbackCh:
			if res.Error != "" {
				return nil, fmt.Errorf("devin oauth error: %s", res.Error)
			}
			if state != "" && res.State != state {
				return nil, fmt.Errorf("devin oauth state mismatch (possible CSRF)")
			}
			authCode = res.Code
			break waitForResult

		case errWait := <-callbackErrCh:
			if authCode != "" || rawPastedToken != "" {
				break waitForResult
			}
			return nil, fmt.Errorf("devin oauth callback failed: %w", errWait)

		case <-manualPromptC:
			manualPromptC = nil
			if manualPromptTimer != nil {
				manualPromptTimer.Stop()
			}
			select {
			case res := <-callbackCh:
				if res.Error != "" {
					return nil, fmt.Errorf("devin oauth error: %s", res.Error)
				}
				if state != "" && res.State != state {
					return nil, fmt.Errorf("devin oauth state mismatch (possible CSRF)")
				}
				authCode = res.Code
				break waitForResult
			default:
			}
			manualInputCh, manualInputErrCh = misc.AsyncPrompt(
				opts.Prompt,
				"Paste the Devin callback URL, authorization code, or session token directly (or press Enter to keep waiting): ",
			)

		case input := <-manualInputCh:
			manualInputCh = nil
			manualInputErrCh = nil
			trimmed := strings.TrimSpace(input)
			if trimmed == "" {
				continue
			}

			// 1. Direct manual session token paste (supports Devin --force-manual-token-flow)
			if strings.HasPrefix(trimmed, "devin-session-token$") || strings.HasPrefix(trimmed, "eyJ") {
				rawPastedToken = trimmed
				break waitForResult
			}

			// 2. Full callback redirect URL
			parsed, errParse := misc.ParseOAuthCallback(trimmed)
			if errParse == nil && parsed != nil && parsed.Code != "" {
				if state != "" && parsed.State != state {
					return nil, fmt.Errorf("devin oauth state mismatch (possible CSRF)")
				}
				authCode = parsed.Code
				break waitForResult
			}

			// 3. Raw authorization code paste
			if !strings.ContainsAny(trimmed, " \t\r\n/?#=") {
				authCode = trimmed
				break waitForResult
			}

		case errInput := <-manualInputErrCh:
			manualInputCh = nil
			manualInputErrCh = nil
			if errInput != nil {
				log.Debugf("manual input prompt error: %v", errInput)
			}
		}
	}

	var sessionToken string
	if rawPastedToken != "" {
		sessionToken = devinauth.FormatSessionToken(rawPastedToken)
	} else if authCode != "" {
		token, errExchange := authSvc.ExchangeCodeForToken(ctx, authCode, pkceCodes.CodeVerifier)
		if errExchange != nil {
			return nil, fmt.Errorf("failed to exchange devin authorization code: %w", errExchange)
		}
		sessionToken = devinauth.FormatSessionToken(token)
	} else {
		return nil, fmt.Errorf("no authorization code or token received")
	}

	return authSvc.CreateAuthRecord(ctx, sessionToken)
}
