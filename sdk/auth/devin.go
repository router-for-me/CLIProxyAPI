package auth

import (
	"context"
	"fmt"
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

	authSvc := devinauth.NewDevinAuthService(nil)
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
		result, errWait := oauthServer.WaitForCallback(5 * time.Minute)
		if errWait != nil {
			callbackErrCh <- errWait
			return
		}
		callbackCh <- result
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
			if state != "" && res.State != "" && res.State != state {
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
				if state != "" && parsed.State != "" && parsed.State != state {
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

	userName, userID, orgID, errSelf := authSvc.FetchSelfProfile(ctx, sessionToken)
	if errSelf != nil {
		log.Warnf("failed to fetch devin user profile: %v", errSelf)
	}

	userStatus, errStatus := authSvc.FetchUserStatus(ctx, sessionToken, "")
	if errStatus != nil {
		log.Warnf("failed to fetch devin user status and quota: %v", errStatus)
	}

	var email, plan string
	if userStatus != nil {
		if userName == "" && userStatus.UserName != "" {
			userName = userStatus.UserName
		}
		if userID == "" && userStatus.UserID != "" {
			userID = userStatus.UserID
		}
		if orgID == "" && userStatus.OrgID != "" {
			orgID = userStatus.OrgID
		}
		email = userStatus.Email
		plan = userStatus.Plan
	}

	identifier := userName
	if identifier == "" {
		identifier = userID
	}
	if identifier == "" {
		identifier = "user"
	}

	fileName := fmt.Sprintf("devin-%s.json", identifier)
	label := fmt.Sprintf("Devin (%s)", identifier)
	if email != "" {
		label = fmt.Sprintf("Devin (%s - %s)", identifier, email)
	}

	attributes := map[string]string{
		"api_key":       sessionToken,
		"session_token": sessionToken,
		"user_name":     userName,
		"user_id":       userID,
		"org_id":        orgID,
		"base_url":      devinauth.DefaultServerURL,
		"auth_kind":     "oauth",
	}
	metadata := map[string]any{
		"type":          "devin",
		"api_key":       sessionToken,
		"session_token": sessionToken,
		"user_name":     userName,
		"user_id":       userID,
		"org_id":        orgID,
		"auth_kind":     "oauth",
	}
	if email != "" {
		attributes["email"] = email
		metadata["email"] = email
	}
	if plan != "" {
		attributes["plan"] = plan
		metadata["plan"] = plan
	}

	quotaSignals := make(map[string]string)
	if plan != "" {
		quotaSignals["plan"] = plan
	}
	if userStatus != nil {
		metadata["daily_quota_remaining_percent"] = userStatus.DailyQuotaRemainingPercent
		metadata["weekly_quota_remaining_percent"] = userStatus.WeeklyQuotaRemainingPercent
		quotaSignals["daily_quota_remaining_percent"] = fmt.Sprintf("%d%%", userStatus.DailyQuotaRemainingPercent)
		quotaSignals["weekly_quota_remaining_percent"] = fmt.Sprintf("%d%%", userStatus.WeeklyQuotaRemainingPercent)
		if !userStatus.DailyQuotaResetAt.IsZero() {
			metadata["daily_quota_reset_at"] = userStatus.DailyQuotaResetAt.Format(time.RFC3339)
			quotaSignals["daily_quota_reset_at"] = userStatus.DailyQuotaResetAt.Format(time.RFC3339)
		}
		if !userStatus.WeeklyQuotaResetAt.IsZero() {
			metadata["weekly_quota_reset_at"] = userStatus.WeeklyQuotaResetAt.Format(time.RFC3339)
			quotaSignals["weekly_quota_reset_at"] = userStatus.WeeklyQuotaResetAt.Format(time.RFC3339)
		}
		if !userStatus.PlanStart.IsZero() {
			metadata["plan_start"] = userStatus.PlanStart.Format(time.RFC3339)
		}
		if !userStatus.PlanEnd.IsZero() {
			metadata["plan_end"] = userStatus.PlanEnd.Format(time.RFC3339)
		}
	}

	authRecord := &coreauth.Auth{
		ID:         fileName,
		Provider:   "devin",
		FileName:   fileName,
		Label:      label,
		Status:     coreauth.StatusActive,
		Attributes: attributes,
		Metadata:   metadata,
		Quota: coreauth.QuotaState{
			ObservedAt: time.Now(),
			Signals:    quotaSignals,
		},
	}

	return authRecord, nil
}
