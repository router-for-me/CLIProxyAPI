package auth

import (
	"context"
	"errors"
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
	AuthService  *devinauth.DevinAuthService
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

	authSvc := a.AuthService
	if authSvc == nil {
		authSvc = devinauth.NewDevinAuthService(util.SetProxy(&cfg.SDKConfig, &http.Client{Timeout: 30 * time.Second}))
	}

	if opts.NoBrowser {
		if opts.Prompt == nil {
			return nil, fmt.Errorf("devin authentication in no-browser mode requires an interactive prompt")
		}

		authURL := authSvc.BuildAuthorizationURL("", pkceCodes.CodeChallenge, state)
		fmt.Printf("Visit the following URL to continue Devin authentication:\n%s\n\n", authURL)

		promptMsg := "Paste the Devin authorization code or session token directly: "
		var authCode string
		var rawPastedToken string

		manualInputCh, manualInputErrCh := misc.AsyncPrompt(opts.Prompt, promptMsg)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case errInput := <-manualInputErrCh:
			return nil, fmt.Errorf("failed to read devin input: %w", errInput)
		case input := <-manualInputCh:
			var errPaste error
			authCode, rawPastedToken, errPaste = parseDevinManualPaste(input, state)
			if errPaste != nil {
				return nil, errPaste
			}
			if authCode == "" && rawPastedToken == "" {
				return nil, fmt.Errorf("devin authentication canceled: empty input received")
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

	redirectURI := fmt.Sprintf("http://127.0.0.1:%d/callback", actualPort)
	authURL := authSvc.BuildAuthorizationURL(redirectURI, pkceCodes.CodeChallenge, state)

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
			pastedCode, pastedToken, errPaste := parseDevinManualPaste(input, state)
			if errPaste != nil {
				if errors.Is(errPaste, errDevinUnrecognizedPaste) {
					continue
				}
				return nil, errPaste
			}
			if pastedToken != "" {
				rawPastedToken = pastedToken
				break waitForResult
			}
			if pastedCode != "" {
				authCode = pastedCode
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

var errDevinUnrecognizedPaste = errors.New("unrecognized devin authorization code or token format")

// parseDevinManualPaste classifies a pasted authorization code, callback URL, or session token.
// Empty input returns empty values with a nil error; callers decide whether to abort or keep waiting.
func parseDevinManualPaste(input, expectedState string) (authCode, rawToken string, err error) {
	trimmed := strings.TrimSpace(input)
	trimmed = strings.Trim(trimmed, "\"'")
	trimmed = strings.TrimSpace(trimmed)
	if trimmed == "" {
		return "", "", nil
	}

	// Direct manual session token paste (supports Devin --force-manual-token-flow).
	if strings.HasPrefix(trimmed, "devin-session-token$") || strings.HasPrefix(trimmed, "eyJ") {
		return "", trimmed, nil
	}

	if parsed, errParse := misc.ParseOAuthCallback(trimmed); errParse == nil && parsed != nil {
		if errMsg := strings.TrimSpace(parsed.Error); errMsg != "" {
			if desc := strings.TrimSpace(parsed.ErrorDescription); desc != "" {
				errMsg = fmt.Sprintf("%s: %s", errMsg, desc)
			}
			return "", "", fmt.Errorf("devin oauth error: %s", errMsg)
		}
		if parsed.Code != "" {
			// If state is present in the pasted URL, it must match. PKCE still protects
			// a code pasted without state.
			if expectedState != "" && parsed.State != "" && parsed.State != expectedState {
				return "", "", fmt.Errorf("devin oauth state mismatch (possible CSRF)")
			}
			return parsed.Code, "", nil
		}
	}

	if !strings.ContainsAny(trimmed, " \t\r\n/?#=") {
		return trimmed, "", nil
	}
	return "", "", errDevinUnrecognizedPaste
}
