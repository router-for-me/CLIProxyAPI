package auth

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/oidc"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

type OIDCAuthenticator struct {
	CallbackPort int
}

func NewOIDCAuthenticator() *OIDCAuthenticator {
	return &OIDCAuthenticator{CallbackPort: oidc.DefaultCallbackPort}
}

func (a *OIDCAuthenticator) Provider() string {
	return "oidc"
}

func (a *OIDCAuthenticator) RefreshLead() *time.Duration {
	lead := 5 * time.Minute
	return &lead
}

func (a *OIDCAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if opts == nil {
		opts = &LoginOptions{}
	}

	oidcConfig, err := oidc.SelectOIDCConfig(cfg, opts.Metadata[oidc.MetadataNameKey])
	if err != nil {
		return nil, err
	}
	callbackPort := a.CallbackPort
	if opts.CallbackPort > 0 {
		callbackPort = opts.CallbackPort
	}
	redirectURI, err := oidcConfig.ResolveRedirectURI(callbackPort)
	if err != nil {
		return nil, err
	}
	bindPort, bindPath, localCallback, err := oidcConfig.CallbackBinding(callbackPort)
	if err != nil {
		return nil, err
	}
	pkceCodes, err := oidc.GeneratePKCECodes()
	if err != nil {
		return nil, fmt.Errorf("oidc pkce generation failed: %w", err)
	}
	state, err := misc.GenerateRandomState()
	if err != nil {
		return nil, fmt.Errorf("oidc state generation failed: %w", err)
	}

	authSvc := oidc.NewAuth(cfg, *oidcConfig)
	authURL, err := authSvc.AuthorizationURL(state, redirectURI, pkceCodes)
	if err != nil {
		return nil, fmt.Errorf("oidc authorization url generation failed: %w", err)
	}

	var oauthServer *oidc.OAuthServer
	if localCallback {
		oauthServer = oidc.NewOAuthServer(bindPort, bindPath)
		if err = oauthServer.Start(); err != nil {
			if strings.Contains(err.Error(), "already in use") || strings.Contains(err.Error(), "bind") || strings.Contains(err.Error(), "listen") {
				return nil, fmt.Errorf("oidc authentication server port in use: %w", err)
			}
			return nil, fmt.Errorf("oidc authentication server failed: %w", err)
		}
		defer func() {
			stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if stopErr := oauthServer.Stop(stopCtx); stopErr != nil {
				log.Warnf("oidc oauth server stop error: %v", stopErr)
			}
		}()
	}

	if !opts.NoBrowser {
		fmt.Println("Opening browser for OIDC authentication")
		if !browser.IsAvailable() {
			log.Warn("No browser available; please open the URL manually")
			if localCallback {
				util.PrintSSHTunnelInstructions(bindPort)
			}
			fmt.Printf("Visit the following URL to continue authentication:\n%s\n", authURL)
		} else if err = browser.OpenURL(authURL); err != nil {
			log.Warnf("Failed to open browser automatically: %v", err)
			if localCallback {
				util.PrintSSHTunnelInstructions(bindPort)
			}
			fmt.Printf("Visit the following URL to continue authentication:\n%s\n", authURL)
		}
	} else {
		if localCallback {
			util.PrintSSHTunnelInstructions(bindPort)
		}
		fmt.Printf("Visit the following URL to continue authentication:\n%s\n", authURL)
	}

	fmt.Println("Waiting for OIDC authentication callback...")

	var result *oidc.OAuthResult
	if localCallback {
		result, err = waitForOIDCCallback(oauthServer, opts.Prompt)
	} else {
		if opts.Prompt == nil {
			return nil, fmt.Errorf("oidc redirect uri %s requires manual callback input", redirectURI)
		}
		result, err = waitForManualOIDCCallback(opts.Prompt)
	}
	if err != nil {
		return nil, err
	}
	if result.Error != "" {
		if result.ErrorDescription != "" {
			return nil, fmt.Errorf("oidc authentication failed: %s", result.ErrorDescription)
		}
		return nil, fmt.Errorf("oidc authentication failed: %s", result.Error)
	}
	if result.State != "" && result.State != state {
		return nil, fmt.Errorf("oidc authentication failed: state mismatch")
	}

	tokenData, err := authSvc.ExchangeCodeForTokens(ctx, result.Code, redirectURI, pkceCodes.CodeVerifier)
	if err != nil {
		return nil, fmt.Errorf("oidc token exchange failed: %w", err)
	}
	tokenStorage := authSvc.CreateTokenStorage(tokenData, redirectURI)
	if tokenStorage == nil {
		return nil, fmt.Errorf("oidc token storage could not be created")
	}

	identity := sanitizeFileComponent(firstNonEmpty(tokenStorage.TokenData.Email, tokenStorage.TokenData.Username, tokenStorage.TokenData.Subject, tokenStorage.TokenData.Name))
	if identity == "" {
		identity = fmt.Sprintf("%d", time.Now().Unix())
	}
	providerName := sanitizeFileComponent(oidcConfig.Name)
	if providerName == "" {
		providerName = "oidc"
	}
	fileName := fmt.Sprintf("oidc-%s-%s.json", providerName, identity)
	now := time.Now().Format(time.RFC3339)
	metadata := map[string]any{
		"type":         "oidc",
		"oidc_name":    opts.Metadata[oidc.MetadataNameKey],
		"last_refresh": now,
	}
	fmt.Println("OIDC authentication successful")

	auth := &coreauth.Auth{
		ID:         fileName,
		Provider:   a.Provider(),
		FileName:   fileName,
		Storage:    tokenStorage,
		Metadata:   metadata,
		Attributes: map[string]string{},
	}
	return auth, nil
}

func waitForOIDCCallback(server *oidc.OAuthServer, prompt func(string) (string, error)) (*oidc.OAuthResult, error) {
	callbackCh := make(chan *oidc.OAuthResult, 1)
	callbackErrCh := make(chan error, 1)
	go func() {
		result, err := server.WaitForCallback(5 * time.Minute)
		if err != nil {
			callbackErrCh <- err
			return
		}
		callbackCh <- result
	}()
	var promptTimer *time.Timer
	var promptC <-chan time.Time
	if prompt != nil {
		promptTimer = time.NewTimer(15 * time.Second)
		promptC = promptTimer.C
		defer promptTimer.Stop()
	}
	var manualInputCh <-chan string
	var manualInputErrCh <-chan error
	for {
		select {
		case result := <-callbackCh:
			return result, nil
		case err := <-callbackErrCh:
			return nil, err
		case <-promptC:
			promptC = nil
			if promptTimer != nil {
				promptTimer.Stop()
			}
			select {
			case result := <-callbackCh:
				return result, nil
			case err := <-callbackErrCh:
				return nil, err
			default:
			}
			manualInputCh, manualInputErrCh = misc.AsyncPrompt(prompt, "Paste the OIDC callback URL (or press Enter to keep waiting): ")
		case input := <-manualInputCh:
			manualInputCh = nil
			manualInputErrCh = nil
			parsed, err := misc.ParseOAuthCallback(input)
			if err != nil {
				return nil, err
			}
			if parsed == nil {
				continue
			}
			return &oidc.OAuthResult{
				Code:             parsed.Code,
				State:            parsed.State,
				Error:            parsed.Error,
				ErrorDescription: parsed.ErrorDescription,
			}, nil
		case err := <-manualInputErrCh:
			return nil, err
		}
	}
}

func waitForManualOIDCCallback(prompt func(string) (string, error)) (*oidc.OAuthResult, error) {
	input, err := prompt("Paste the OIDC callback URL: ")
	if err != nil {
		return nil, err
	}
	parsed, err := misc.ParseOAuthCallback(input)
	if err != nil {
		return nil, err
	}
	if parsed == nil {
		return nil, fmt.Errorf("oidc callback url is required")
	}
	return &oidc.OAuthResult{
		Code:             parsed.Code,
		State:            parsed.State,
		Error:            parsed.Error,
		ErrorDescription: parsed.ErrorDescription,
	}, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func sanitizeFileComponent(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var builder strings.Builder
	for _, r := range raw {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '-' || r == '_' || r == '@' {
			builder.WriteRune(r)
			continue
		}
		if r == ' ' || r == '/' || r == '\\' || r == ':' {
			builder.WriteRune('-')
		}
	}
	return strings.Trim(builder.String(), "-")
}
