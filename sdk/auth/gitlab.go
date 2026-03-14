package auth

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	gitlabauth "github.com/router-for-me/CLIProxyAPI/v6/internal/auth/gitlab"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	gitLabLoginModeMetadataKey           = "login_mode"
	gitLabLoginModeOAuth                 = "oauth"
	gitLabLoginModePAT                   = "pat"
	gitLabBaseURLMetadataKey             = "base_url"
	gitLabOAuthClientIDMetadataKey       = "oauth_client_id"
	gitLabOAuthClientSecretMetadataKey   = "oauth_client_secret"
	gitLabPersonalAccessTokenMetadataKey = "personal_access_token"
)

var gitLabRefreshLead = 5 * time.Minute

type GitLabAuthenticator struct {
	CallbackPort int
}

func NewGitLabAuthenticator() *GitLabAuthenticator {
	return &GitLabAuthenticator{CallbackPort: gitlabauth.DefaultCallbackPort}
}

func (a *GitLabAuthenticator) Provider() string {
	return "gitlab"
}

func (a *GitLabAuthenticator) RefreshLead() *time.Duration {
	return &gitLabRefreshLead
}

func (a *GitLabAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if opts == nil {
		opts = &LoginOptions{}
	}

	switch strings.ToLower(strings.TrimSpace(opts.Metadata[gitLabLoginModeMetadataKey])) {
	case "", gitLabLoginModeOAuth:
		return a.loginOAuth(ctx, cfg, opts)
	case gitLabLoginModePAT:
		return a.loginPAT(ctx, cfg, opts)
	default:
		return nil, fmt.Errorf("gitlab auth: unsupported login mode %q", opts.Metadata[gitLabLoginModeMetadataKey])
	}
}

func (a *GitLabAuthenticator) loginOAuth(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	client := gitlabauth.NewAuthClient(cfg)
	baseURL := a.resolveString(opts, gitLabBaseURLMetadataKey, gitlabauth.DefaultBaseURL)
	clientID, err := a.requireInput(opts, gitLabOAuthClientIDMetadataKey, "Enter GitLab OAuth application client ID: ")
	if err != nil {
		return nil, err
	}
	clientSecret, err := a.optionalInput(opts, gitLabOAuthClientSecretMetadataKey, "Enter GitLab OAuth application client secret (press Enter for public PKCE app): ")
	if err != nil {
		return nil, err
	}

	callbackPort := a.CallbackPort
	if opts.CallbackPort > 0 {
		callbackPort = opts.CallbackPort
	}
	redirectURI := gitlabauth.RedirectURL(callbackPort)

	pkceCodes, err := gitlabauth.GeneratePKCECodes()
	if err != nil {
		return nil, err
	}
	state, err := misc.GenerateRandomState()
	if err != nil {
		return nil, fmt.Errorf("gitlab state generation failed: %w", err)
	}

	oauthServer := gitlabauth.NewOAuthServer(callbackPort)
	if err := oauthServer.Start(); err != nil {
		return nil, err
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if stopErr := oauthServer.Stop(stopCtx); stopErr != nil {
			log.Warnf("gitlab oauth server stop error: %v", stopErr)
		}
	}()

	authURL, err := client.GenerateAuthURL(baseURL, clientID, redirectURI, state, pkceCodes)
	if err != nil {
		return nil, err
	}

	if !opts.NoBrowser {
		fmt.Println("Opening browser for GitLab Duo authentication")
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

	fmt.Println("Waiting for GitLab OAuth callback...")

	callbackCh := make(chan *gitlabauth.OAuthResult, 1)
	callbackErrCh := make(chan error, 1)
	go func() {
		result, waitErr := oauthServer.WaitForCallback(5 * time.Minute)
		if waitErr != nil {
			callbackErrCh <- waitErr
			return
		}
		callbackCh <- result
	}()

	var result *gitlabauth.OAuthResult
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
			return nil, err
		case <-manualPromptC:
			manualPromptC = nil
			if manualPromptTimer != nil {
				manualPromptTimer.Stop()
			}
			input, promptErr := opts.Prompt("Paste the GitLab callback URL (or press Enter to keep waiting): ")
			if promptErr != nil {
				return nil, promptErr
			}
			parsed, parseErr := misc.ParseOAuthCallback(input)
			if parseErr != nil {
				return nil, parseErr
			}
			if parsed == nil {
				continue
			}
			result = &gitlabauth.OAuthResult{
				Code:  parsed.Code,
				State: parsed.State,
				Error: parsed.Error,
			}
			break waitForCallback
		}
	}

	if result.Error != "" {
		return nil, fmt.Errorf("gitlab oauth returned error: %s", result.Error)
	}
	if result.State != state {
		return nil, fmt.Errorf("gitlab auth: state mismatch")
	}

	tokenResp, err := client.ExchangeCodeForTokens(ctx, baseURL, clientID, clientSecret, redirectURI, result.Code, pkceCodes.CodeVerifier)
	if err != nil {
		return nil, err
	}
	accessToken := strings.TrimSpace(tokenResp.AccessToken)
	if accessToken == "" {
		return nil, fmt.Errorf("gitlab auth: missing access token")
	}

	user, err := client.GetCurrentUser(ctx, baseURL, accessToken)
	if err != nil {
		return nil, err
	}
	direct, err := client.FetchDirectAccess(ctx, baseURL, accessToken)
	if err != nil {
		return nil, err
	}

	identifier := gitLabAccountIdentifier(user)
	fileName := fmt.Sprintf("gitlab-%s.json", sanitizeGitLabFileName(identifier))
	metadata := buildGitLabAuthMetadata(baseURL, gitLabLoginModeOAuth, tokenResp, direct)
	metadata["auth_kind"] = "oauth"
	metadata[gitLabOAuthClientIDMetadataKey] = clientID
	metadata["username"] = strings.TrimSpace(user.Username)
	if email := strings.TrimSpace(primaryGitLabEmail(user)); email != "" {
		metadata["email"] = email
	}
	metadata["name"] = strings.TrimSpace(user.Name)

	fmt.Println("GitLab Duo authentication successful")

	return &coreauth.Auth{
		ID:       fileName,
		Provider: a.Provider(),
		FileName: fileName,
		Label:    identifier,
		Metadata: metadata,
	}, nil
}

func (a *GitLabAuthenticator) loginPAT(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	client := gitlabauth.NewAuthClient(cfg)
	baseURL := a.resolveString(opts, gitLabBaseURLMetadataKey, gitlabauth.DefaultBaseURL)
	token, err := a.requireInput(opts, gitLabPersonalAccessTokenMetadataKey, "Enter GitLab personal access token: ")
	if err != nil {
		return nil, err
	}

	user, err := client.GetCurrentUser(ctx, baseURL, token)
	if err != nil {
		return nil, err
	}
	_, err = client.GetPersonalAccessTokenSelf(ctx, baseURL, token)
	if err != nil {
		return nil, err
	}
	direct, err := client.FetchDirectAccess(ctx, baseURL, token)
	if err != nil {
		return nil, err
	}

	identifier := gitLabAccountIdentifier(user)
	fileName := fmt.Sprintf("gitlab-%s-pat.json", sanitizeGitLabFileName(identifier))
	metadata := buildGitLabAuthMetadata(baseURL, gitLabLoginModePAT, nil, direct)
	metadata["auth_kind"] = "personal_access_token"
	metadata[gitLabPersonalAccessTokenMetadataKey] = strings.TrimSpace(token)
	metadata["token_preview"] = maskGitLabToken(token)
	metadata["username"] = strings.TrimSpace(user.Username)
	if email := strings.TrimSpace(primaryGitLabEmail(user)); email != "" {
		metadata["email"] = email
	}
	metadata["name"] = strings.TrimSpace(user.Name)

	fmt.Println("GitLab Duo PAT authentication successful")

	return &coreauth.Auth{
		ID:       fileName,
		Provider: a.Provider(),
		FileName: fileName,
		Label:    identifier + " (PAT)",
		Metadata: metadata,
	}, nil
}

func buildGitLabAuthMetadata(baseURL, mode string, tokenResp *gitlabauth.TokenResponse, direct *gitlabauth.DirectAccessResponse) map[string]any {
	metadata := map[string]any{
		"type":                     "gitlab",
		"auth_method":              strings.TrimSpace(mode),
		gitLabBaseURLMetadataKey:   gitlabauth.NormalizeBaseURL(baseURL),
		"last_refresh":             time.Now().UTC().Format(time.RFC3339),
		"refresh_interval_seconds": 240,
	}
	if tokenResp != nil {
		metadata["access_token"] = strings.TrimSpace(tokenResp.AccessToken)
		if refreshToken := strings.TrimSpace(tokenResp.RefreshToken); refreshToken != "" {
			metadata["refresh_token"] = refreshToken
		}
		if tokenType := strings.TrimSpace(tokenResp.TokenType); tokenType != "" {
			metadata["token_type"] = tokenType
		}
		if scope := strings.TrimSpace(tokenResp.Scope); scope != "" {
			metadata["scope"] = scope
		}
		if expiry := gitlabauth.TokenExpiry(time.Now(), tokenResp); !expiry.IsZero() {
			metadata["oauth_expires_at"] = expiry.Format(time.RFC3339)
		}
	}
	mergeGitLabDirectAccessMetadata(metadata, direct)
	return metadata
}

func mergeGitLabDirectAccessMetadata(metadata map[string]any, direct *gitlabauth.DirectAccessResponse) {
	if metadata == nil || direct == nil {
		return
	}
	if base := strings.TrimSpace(direct.BaseURL); base != "" {
		metadata["duo_gateway_base_url"] = base
	}
	if token := strings.TrimSpace(direct.Token); token != "" {
		metadata["duo_gateway_token"] = token
	}
	if direct.ExpiresAt > 0 {
		expiry := time.Unix(direct.ExpiresAt, 0).UTC()
		metadata["duo_gateway_expires_at"] = expiry.Format(time.RFC3339)
		now := time.Now().UTC()
		if ttl := expiry.Sub(now); ttl > 0 {
			interval := int(ttl.Seconds()) / 2
			switch {
			case interval < 60:
				interval = 60
			case interval > 240:
				interval = 240
			}
			metadata["refresh_interval_seconds"] = interval
		}
	}
	if len(direct.Headers) > 0 {
		headers := make(map[string]string, len(direct.Headers))
		for key, value := range direct.Headers {
			key = strings.TrimSpace(key)
			value = strings.TrimSpace(value)
			if key == "" || value == "" {
				continue
			}
			headers[key] = value
		}
		if len(headers) > 0 {
			metadata["duo_gateway_headers"] = headers
		}
	}
	if direct.ModelDetails != nil {
		modelDetails := map[string]any{}
		if provider := strings.TrimSpace(direct.ModelDetails.ModelProvider); provider != "" {
			modelDetails["model_provider"] = provider
			metadata["model_provider"] = provider
		}
		if model := strings.TrimSpace(direct.ModelDetails.ModelName); model != "" {
			modelDetails["model_name"] = model
			metadata["model_name"] = model
		}
		if len(modelDetails) > 0 {
			metadata["model_details"] = modelDetails
		}
	}
}

func (a *GitLabAuthenticator) resolveString(opts *LoginOptions, key, fallback string) string {
	if opts != nil && opts.Metadata != nil {
		if value := strings.TrimSpace(opts.Metadata[key]); value != "" {
			return value
		}
	}
	for _, envKey := range gitLabEnvKeys(key) {
		if raw, ok := os.LookupEnv(envKey); ok {
			if trimmed := strings.TrimSpace(raw); trimmed != "" {
				return trimmed
			}
		}
	}
	if strings.TrimSpace(fallback) != "" {
		return fallback
	}
	return ""
}

func (a *GitLabAuthenticator) requireInput(opts *LoginOptions, key, prompt string) (string, error) {
	if value := a.resolveString(opts, key, ""); value != "" {
		return value, nil
	}
	if opts != nil && opts.Prompt != nil {
		value, err := opts.Prompt(prompt)
		if err != nil {
			return "", err
		}
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed, nil
		}
	}
	return "", fmt.Errorf("gitlab auth: missing required %s", key)
}

func (a *GitLabAuthenticator) optionalInput(opts *LoginOptions, key, prompt string) (string, error) {
	if value := a.resolveString(opts, key, ""); value != "" {
		return value, nil
	}
	if opts != nil && opts.Prompt != nil {
		value, err := opts.Prompt(prompt)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(value), nil
	}
	return "", nil
}

func primaryGitLabEmail(user *gitlabauth.User) string {
	if user == nil {
		return ""
	}
	if value := strings.TrimSpace(user.Email); value != "" {
		return value
	}
	return strings.TrimSpace(user.PublicEmail)
}

func gitLabAccountIdentifier(user *gitlabauth.User) string {
	if user == nil {
		return "user"
	}
	for _, value := range []string{user.Username, primaryGitLabEmail(user), user.Name} {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return "user"
}

func sanitizeGitLabFileName(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return "user"
	}
	var builder strings.Builder
	lastDash := false
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			builder.WriteRune(r)
			lastDash = false
		case r >= '0' && r <= '9':
			builder.WriteRune(r)
			lastDash = false
		case r == '-' || r == '_' || r == '.':
			builder.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				builder.WriteRune('-')
				lastDash = true
			}
		}
	}
	result := strings.Trim(builder.String(), "-")
	if result == "" {
		return "user"
	}
	return result
}

func maskGitLabToken(token string) string {
	trimmed := strings.TrimSpace(token)
	if trimmed == "" {
		return ""
	}
	if len(trimmed) <= 8 {
		return trimmed
	}
	return trimmed[:4] + "..." + trimmed[len(trimmed)-4:]
}

func gitLabEnvKeys(key string) []string {
	switch strings.TrimSpace(key) {
	case gitLabBaseURLMetadataKey:
		return []string{"GITLAB_BASE_URL"}
	case gitLabOAuthClientIDMetadataKey:
		return []string{"GITLAB_OAUTH_CLIENT_ID"}
	case gitLabOAuthClientSecretMetadataKey:
		return []string{"GITLAB_OAUTH_CLIENT_SECRET"}
	case gitLabPersonalAccessTokenMetadataKey:
		return []string{"GITLAB_PERSONAL_ACCESS_TOKEN"}
	default:
		return nil
	}
}
