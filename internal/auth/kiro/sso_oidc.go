package kiro

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	log "github.com/sirupsen/logrus"
)

const (
	ssoOIDCEndpoint      = "https://oidc.us-east-1.amazonaws.com"
	builderIDStartURL    = "https://view.awsapps.com/start"
	defaultIDCRegion     = "us-east-1"
	pollInterval         = 5 * time.Second
	authCodeCallbackPath = "/oauth/callback"
	authCodeCallbackPort = 19877
)

var (
	ErrAuthorizationPending = errors.New("authorization_pending")
	ErrSlowDown             = errors.New("slow_down")
)

type SSOOIDCClient struct {
	httpClient *http.Client
	cfg        *config.Config
}

func NewSSOOIDCClient(cfg *config.Config) *SSOOIDCClient {
	client := &http.Client{Timeout: 30 * time.Second}
	if cfg != nil {
		client = util.SetProxy(&cfg.SDKConfig, client)
	}
	return &SSOOIDCClient{
		httpClient: client,
		cfg:        cfg,
	}
}

type RegisterClientResponse struct {
	ClientID              string `json:"clientId"`
	ClientSecret          string `json:"clientSecret"`
	ClientIDIssuedAt      int64  `json:"clientIdIssuedAt"`
	ClientSecretExpiresAt int64  `json:"clientSecretExpiresAt"`
}

type StartDeviceAuthResponse struct {
	DeviceCode              string `json:"deviceCode"`
	UserCode                string `json:"userCode"`
	VerificationURI         string `json:"verificationUri"`
	VerificationURIComplete string `json:"verificationUriComplete"`
	ExpiresIn               int    `json:"expiresIn"`
	Interval                int    `json:"interval"`
}

type CreateTokenResponse struct {
	AccessToken  string `json:"accessToken"`
	TokenType    string `json:"tokenType"`
	ExpiresIn    int    `json:"expiresIn"`
	RefreshToken string `json:"refreshToken"`
}

func getOIDCEndpoint(region string) string {
	if region == "" {
		region = defaultIDCRegion
	}
	return fmt.Sprintf("https://oidc.%s.amazonaws.com", region)
}

func promptInput(prompt, defaultValue string) string {
	reader := bufio.NewReader(os.Stdin)
	if defaultValue != "" {
		fmt.Printf("%s [%s]: ", prompt, defaultValue)
	} else {
		fmt.Printf("%s: ", prompt)
	}
	input, err := reader.ReadString('\n')
	if err != nil {
		log.Warnf("Error reading input: %v", err)
		return defaultValue
	}
	input = strings.TrimSpace(input)
	if input == "" {
		return defaultValue
	}
	return input
}

func promptSelect(prompt string, options []string) int {
	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Println(prompt)
		for i, opt := range options {
			fmt.Printf("  %d) %s\n", i+1, opt)
		}
		fmt.Printf("Enter selection (1-%d): ", len(options))

		input, err := reader.ReadString('\n')
		if err != nil {
			log.Warnf("Error reading input: %v", err)
			return 0
		}
		input = strings.TrimSpace(input)

		var selection int
		if _, err := fmt.Sscanf(input, "%d", &selection); err != nil || selection < 1 || selection > len(options) {
			fmt.Printf("Invalid selection '%s'. Please enter a number between 1 and %d.\n\n", input, len(options))
			continue
		}
		return selection - 1
	}
}

func (c *SSOOIDCClient) RegisterClientWithRegion(ctx context.Context, region string) (*RegisterClientResponse, error) {
	endpoint := getOIDCEndpoint(region)
	payload := map[string]interface{}{
		"clientName": "Kiro IDE",
		"clientType": "public",
		"scopes": []string{
			"codewhisperer:completions",
			"codewhisperer:analysis",
			"codewhisperer:conversations",
			"codewhisperer:transformations",
			"codewhisperer:taskassist",
		},
		"grantTypes": []string{"urn:ietf:params:oauth:grant-type:device_code", "refresh_token"},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/client/register", strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	SetOIDCHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("register client failed with status %d: %s", resp.StatusCode, string(respBody))
	}

	var regResp RegisterClientResponse
	if err := json.Unmarshal(respBody, &regResp); err != nil {
		return nil, err
	}
	return &regResp, nil
}

func (c *SSOOIDCClient) StartDeviceAuthorizationWithIDC(ctx context.Context, clientID, clientSecret, startURL, region string) (*StartDeviceAuthResponse, error) {
	endpoint := getOIDCEndpoint(region)
	payload := map[string]interface{}{
		"clientId":     clientID,
		"clientSecret": clientSecret,
		"startUrl":     startURL,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/device_authorization", strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	SetOIDCHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("device auth failed with status %d: %s", resp.StatusCode, string(respBody))
	}

	var authResp StartDeviceAuthResponse
	if err := json.Unmarshal(respBody, &authResp); err != nil {
		return nil, err
	}
	return &authResp, nil
}

func (c *SSOOIDCClient) CreateTokenWithRegion(ctx context.Context, clientID, clientSecret, deviceCode, region string) (*CreateTokenResponse, error) {
	endpoint := getOIDCEndpoint(region)
	payload := map[string]interface{}{
		"clientId":     clientID,
		"clientSecret": clientSecret,
		"deviceCode":   deviceCode,
		"grantType":    "urn:ietf:params:oauth:grant-type:device_code",
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/token", strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	SetOIDCHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		var errResp struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(respBody, &errResp); err == nil {
			switch errResp.Error {
			case "authorization_pending":
				return nil, ErrAuthorizationPending
			case "slow_down":
				return nil, ErrSlowDown
			}
		}
		return nil, fmt.Errorf("create token failed with status %d: %s", resp.StatusCode, string(respBody))
	}

	var tokenResp CreateTokenResponse
	if err := json.Unmarshal(respBody, &tokenResp); err != nil {
		return nil, err
	}
	return &tokenResp, nil
}

func (c *SSOOIDCClient) RefreshTokenWithRegion(ctx context.Context, clientID, clientSecret, refreshToken, region, startURL string) (*KiroTokenData, error) {
	endpoint := getOIDCEndpoint(region)
	payload := map[string]interface{}{
		"clientId":     clientID,
		"clientSecret": clientSecret,
		"grantType":    "refresh_token",
		"refreshToken": refreshToken,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/token", strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	SetOIDCHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token refresh failed with status %d: %s", resp.StatusCode, string(respBody))
	}

	var tokenResp CreateTokenResponse
	if err := json.Unmarshal(respBody, &tokenResp); err != nil {
		return nil, err
	}

	newRefreshToken := tokenResp.RefreshToken
	if newRefreshToken == "" {
		newRefreshToken = refreshToken
	}

	expiresIn := tokenResp.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	expiresAt := time.Now().Add(time.Duration(expiresIn) * time.Second)

	return &KiroTokenData{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: newRefreshToken,
		ExpiresAt:    expiresAt.Format(time.RFC3339),
		AuthMethod:   "idc",
		Provider:     "Enterprise",
		ClientID:     clientID,
		ClientSecret: clientSecret,
		StartURL:     startURL,
		Region:       region,
	}, nil
}

func (c *SSOOIDCClient) LoginWithIDC(ctx context.Context, startURL, region string) (*KiroTokenData, error) {
	regResp, err := c.RegisterClientWithRegion(ctx, region)
	if err != nil {
		return nil, fmt.Errorf("failed to register client with region %s: %w", region, err)
	}

	authResp, err := c.StartDeviceAuthorizationWithIDC(ctx, regResp.ClientID, regResp.ClientSecret, startURL, region)
	if err != nil {
		return nil, fmt.Errorf("failed to start device authorization with IDC: %w", err)
	}

	verificationURI := authResp.VerificationURIComplete
	if verificationURI == "" {
		verificationURI = authResp.VerificationURI
	}

	fmt.Printf("\nYour authorization code is: %s\n", authResp.UserCode)
	fmt.Printf("Opening browser for authorization...\nURL: %s\n", verificationURI)

	if err := browser.OpenURL(verificationURI); err != nil {
		fmt.Printf("Could not open browser automatically. Please open the URL manually.\n")
	}

	interval := pollInterval
	if authResp.Interval > 0 {
		interval = time.Duration(authResp.Interval) * time.Second
	}
	deadline := time.Now().Add(time.Duration(authResp.ExpiresIn) * time.Second)

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
			tokenResp, err := c.CreateTokenWithRegion(ctx, regResp.ClientID, regResp.ClientSecret, authResp.DeviceCode, region)
			if err != nil {
				if errors.Is(err, ErrAuthorizationPending) {
					continue
				}
				if errors.Is(err, ErrSlowDown) {
					interval += 5 * time.Second
					continue
				}
				return nil, err
			}

			expiresIn := tokenResp.ExpiresIn
			if expiresIn <= 0 {
				expiresIn = 3600
			}
			expiresAt := time.Now().Add(time.Duration(expiresIn) * time.Second)

			email := c.FetchUserEmail(ctx, tokenResp.AccessToken)
			if email == "" {
				email = ExtractEmailFromJWT(tokenResp.AccessToken)
			}
			profileArn := c.FetchProfileArn(ctx, tokenResp.AccessToken, regResp.ClientID, tokenResp.RefreshToken)

			return &KiroTokenData{
				AccessToken:  tokenResp.AccessToken,
				RefreshToken: tokenResp.RefreshToken,
				ProfileArn:   profileArn,
				ExpiresAt:    expiresAt.Format(time.RFC3339),
				AuthMethod:   "idc",
				Provider:     "Enterprise",
				ClientID:     regResp.ClientID,
				ClientSecret: regResp.ClientSecret,
				Email:        email,
				StartURL:     startURL,
				Region:       region,
			}, nil
		}
	}
	return nil, errors.New("device authorization timed out")
}

type IDCLoginOptions struct {
	StartURL      string
	Region        string
	UseDeviceCode bool
}

func (c *SSOOIDCClient) LoginWithMethodSelection(ctx context.Context, opts *IDCLoginOptions) (*KiroTokenData, error) {
	if opts != nil && opts.StartURL != "" {
		region := opts.Region
		if region == "" {
			region = defaultIDCRegion
		}
		if opts.UseDeviceCode {
			return c.LoginWithIDC(ctx, opts.StartURL, region)
		}
		return c.LoginWithIDCAuthCode(ctx, opts.StartURL, region)
	}

	options := []string{
		"AWS Builder ID (Personal account, recommended)",
		"IAM Identity Center / IDC (Enterprise SSO)",
	}
	selection := promptSelect("Select Kiro authentication method:", options)

	switch selection {
	case 0:
		return c.LoginWithBuilderIDAuthCode(ctx)
	case 1:
		startURL := promptInput("Enter IDC Start URL (e.g., https://my-sso.awsapps.com/start)", "")
		if startURL == "" {
			return nil, errors.New("IDC Start URL is required")
		}
		region := promptInput("Enter IDC Region", defaultIDCRegion)
		return c.LoginWithIDCAuthCode(ctx, startURL, region)
	default:
		return nil, errors.New("invalid selection")
	}
}

func (c *SSOOIDCClient) RegisterClient(ctx context.Context) (*RegisterClientResponse, error) {
	return c.RegisterClientWithRegion(ctx, defaultIDCRegion)
}

func (c *SSOOIDCClient) StartDeviceAuthorization(ctx context.Context, clientID, clientSecret string) (*StartDeviceAuthResponse, error) {
	return c.StartDeviceAuthorizationWithIDC(ctx, clientID, clientSecret, builderIDStartURL, defaultIDCRegion)
}

func (c *SSOOIDCClient) CreateToken(ctx context.Context, clientID, clientSecret, deviceCode string) (*CreateTokenResponse, error) {
	return c.CreateTokenWithRegion(ctx, clientID, clientSecret, deviceCode, defaultIDCRegion)
}

func (c *SSOOIDCClient) RefreshToken(ctx context.Context, clientID, clientSecret, refreshToken string) (*KiroTokenData, error) {
	return c.RefreshTokenWithRegion(ctx, clientID, clientSecret, refreshToken, defaultIDCRegion, builderIDStartURL)
}

func (c *SSOOIDCClient) LoginWithBuilderID(ctx context.Context) (*KiroTokenData, error) {
	regResp, err := c.RegisterClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to register client: %w", err)
	}

	authResp, err := c.StartDeviceAuthorization(ctx, regResp.ClientID, regResp.ClientSecret)
	if err != nil {
		return nil, fmt.Errorf("failed to start device authorization: %w", err)
	}

	verificationURI := authResp.VerificationURIComplete
	if verificationURI == "" {
		verificationURI = authResp.VerificationURI
	}

	fmt.Printf("\nYour authorization code is: %s\n", authResp.UserCode)
	fmt.Printf("Opening browser for authorization...\nURL: %s\n", verificationURI)

	if err := browser.OpenURL(verificationURI); err != nil {
		fmt.Printf("Could not open browser automatically. Please open the URL manually.\n")
	}

	interval := pollInterval
	if authResp.Interval > 0 {
		interval = time.Duration(authResp.Interval) * time.Second
	}
	deadline := time.Now().Add(time.Duration(authResp.ExpiresIn) * time.Second)

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
			tokenResp, err := c.CreateToken(ctx, regResp.ClientID, regResp.ClientSecret, authResp.DeviceCode)
			if err != nil {
				if errors.Is(err, ErrAuthorizationPending) {
					continue
				}
				if errors.Is(err, ErrSlowDown) {
					interval += 5 * time.Second
					continue
				}
				return nil, err
			}

			expiresIn := tokenResp.ExpiresIn
			if expiresIn <= 0 {
				expiresIn = 3600
			}
			expiresAt := time.Now().Add(time.Duration(expiresIn) * time.Second)

			email := c.FetchUserEmail(ctx, tokenResp.AccessToken)
			if email == "" {
				email = ExtractEmailFromJWT(tokenResp.AccessToken)
			}
			profileArn := c.FetchProfileArn(ctx, tokenResp.AccessToken, regResp.ClientID, tokenResp.RefreshToken)

			return &KiroTokenData{
				AccessToken:  tokenResp.AccessToken,
				RefreshToken: tokenResp.RefreshToken,
				ProfileArn:   profileArn,
				ExpiresAt:    expiresAt.Format(time.RFC3339),
				AuthMethod:   "builder-id",
				Provider:     "AWS",
				ClientID:     regResp.ClientID,
				ClientSecret: regResp.ClientSecret,
				Email:        email,
				Region:       defaultIDCRegion,
			}, nil
		}
	}
	return nil, errors.New("device authorization timed out")
}

func (c *SSOOIDCClient) FetchUserEmail(ctx context.Context, accessToken string) string {
	email := c.tryUserInfoEndpoint(ctx, accessToken)
	if email != "" {
		return email
	}
	return ExtractEmailFromJWT(accessToken)
}

func (c *SSOOIDCClient) tryUserInfoEndpoint(ctx context.Context, accessToken string) string {
	for _, region := range []string{defaultIDCRegion, "us-west-2", "eu-west-1"} {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, getOIDCEndpoint(region)+"/userinfo", nil)
		if err != nil {
			continue
		}
		req.Header.Set("Authorization", "Bearer "+accessToken)
		SetOIDCHeaders(req)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			continue
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			continue
		}

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			continue
		}

		var userInfo struct {
			Email             string `json:"email"`
			PreferredUsername string `json:"preferred_username"`
		}
		if err := json.Unmarshal(body, &userInfo); err == nil {
			if userInfo.Email != "" {
				return userInfo.Email
			}
			if strings.Contains(userInfo.PreferredUsername, "@") {
				return userInfo.PreferredUsername
			}
		}
	}
	return ""
}

func (c *SSOOIDCClient) FetchProfileArn(ctx context.Context, accessToken, clientID, refreshToken string) string {
	if arn := c.tryListAvailableProfiles(ctx, accessToken, clientID, refreshToken); arn != "" {
		return arn
	}
	return c.tryListProfilesLegacy(ctx, accessToken)
}

func (c *SSOOIDCClient) tryListAvailableProfiles(ctx context.Context, accessToken, clientID, refreshToken string) string {
	accountKey := GetAccountKey(clientID, refreshToken)
	fp := GlobalFingerprintManager().GetFingerprint(accountKey)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://codewhisperer.us-east-1.amazonaws.com/listAvailableProfiles", nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-amz-user-agent", fmt.Sprintf("aws-sdk-js/%s KiroIDE-%s-%s", fp.RuntimeSDKVersion, fp.KiroVersion, fp.KiroHash))
	req.Header.Set("User-Agent", fmt.Sprintf(
		"aws-sdk-js/%s ua/2.1 os/%s#%s lang/js md/nodejs#%s api/codewhisperer#%s m/E KiroIDE-%s-%s",
		fp.RuntimeSDKVersion, fp.OSType, fp.OSVersion, fp.NodeVersion, fp.RuntimeSDKVersion, fp.KiroVersion, fp.KiroHash))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return ""
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return ""
	}

	var result struct {
		Profiles []struct {
			Arn string `json:"arn"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal(body, &result); err == nil && len(result.Profiles) > 0 {
		return result.Profiles[0].Arn
	}
	return ""
}

func (c *SSOOIDCClient) tryListProfilesLegacy(ctx context.Context, accessToken string) string {
	for _, region := range []string{defaultIDCRegion, "us-west-2", "eu-west-1"} {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("https://codewhisperer.%s.amazonaws.com/listProfiles", region), nil)
		if err != nil {
			continue
		}
		req.Header.Set("Authorization", "Bearer "+accessToken)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-amz-target", "AmazonCodeWhispererService.ListProfiles")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			continue
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			continue
		}

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			continue
		}

		var result struct {
			Profiles []struct {
				Arn string `json:"arn"`
			} `json:"profiles"`
		}
		if err := json.Unmarshal(body, &result); err == nil && len(result.Profiles) > 0 {
			return result.Profiles[0].Arn
		}
	}
	return ""
}

func (c *SSOOIDCClient) RegisterClientForAuthCode(ctx context.Context, redirectURI string) (*RegisterClientResponse, error) {
	return c.RegisterClientForAuthCodeWithIDC(ctx, redirectURI, builderIDStartURL, defaultIDCRegion)
}

func (c *SSOOIDCClient) RegisterClientForAuthCodeWithIDC(ctx context.Context, redirectURI, issuerUrl, region string) (*RegisterClientResponse, error) {
	endpoint := getOIDCEndpoint(region)
	payload := map[string]interface{}{
		"clientName":   "Kiro IDE",
		"clientType":   "public",
		"scopes":       []string{"codewhisperer:completions", "codewhisperer:analysis", "codewhisperer:conversations"},
		"grantTypes":   []string{"authorization_code", "refresh_token"},
		"redirectUris": []string{redirectURI},
		"issuerUrl":    issuerUrl,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/client/register", strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	SetOIDCHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("register client for authcode failed with status %d: %s", resp.StatusCode, string(respBody))
	}

	var regResp RegisterClientResponse
	if err := json.Unmarshal(respBody, &regResp); err != nil {
		return nil, err
	}
	return &regResp, nil
}

type AuthCodeCallbackResult struct {
	Code  string
	State string
	Error string
}

func (c *SSOOIDCClient) startAuthCodeCallbackServer(ctx context.Context, expectedState string) (string, <-chan AuthCodeCallbackResult, error) {
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", authCodeCallbackPort))
	if err != nil {
		listener, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return "", nil, fmt.Errorf("failed to start callback server: %w", err)
		}
	}

	port := listener.Addr().(*net.TCPAddr).Port
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d%s", port, authCodeCallbackPath)
	resultChan := make(chan AuthCodeCallbackResult, 1)

	server := &http.Server{ReadHeaderTimeout: 10 * time.Second}
	mux := http.NewServeMux()
	mux.HandleFunc(authCodeCallbackPath, func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		state := r.URL.Query().Get("state")
		errParam := r.URL.Query().Get("error")
		errDesc := r.URL.Query().Get("error_description")

		if errParam != "" {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusBadRequest)
			errMsg := errParam
			if errDesc != "" {
				errMsg = fmt.Sprintf("%s: %s", errParam, errDesc)
			}
			fmt.Fprintf(w, "<html><body><h1>Login Failed</h1><p>%s</p></body></html>", html.EscapeString(errMsg))
			resultChan <- AuthCodeCallbackResult{Error: errMsg}
			return
		}

		if state != expectedState {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, "<html><body><h1>Login Failed</h1><p>Invalid state parameter</p></body></html>")
			resultChan <- AuthCodeCallbackResult{Error: "state mismatch"}
			return
		}

		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<html><body><h1>Login Successful!</h1><p>You can close this window and return to the CLI.</p></body></html>")
		resultChan <- AuthCodeCallbackResult{Code: code, State: state}
	})

	server.Handler = mux

	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Debugf("callback server error: %v", err)
		}
	}()

	go func() {
		select {
		case <-ctx.Done():
		case <-time.After(10 * time.Minute):
		case <-resultChan:
		}
		_ = server.Shutdown(context.Background())
	}()

	return redirectURI, resultChan, nil
}

func generatePKCEForAuthCode() (verifier, challenge string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	verifier = base64.RawURLEncoding.EncodeToString(b)
	h := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(h[:])
	return verifier, challenge, nil
}

func generateStateForAuthCode() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (c *SSOOIDCClient) CreateTokenWithAuthCode(ctx context.Context, clientID, clientSecret, code, codeVerifier, redirectURI string) (*CreateTokenResponse, error) {
	return c.CreateTokenWithAuthCodeAndRegion(ctx, clientID, clientSecret, code, codeVerifier, redirectURI, defaultIDCRegion)
}

func (c *SSOOIDCClient) CreateTokenWithAuthCodeAndRegion(ctx context.Context, clientID, clientSecret, code, codeVerifier, redirectURI, region string) (*CreateTokenResponse, error) {
	endpoint := getOIDCEndpoint(region)
	payload := map[string]interface{}{
		"clientId":     clientID,
		"clientSecret": clientSecret,
		"grantType":    "authorization_code",
		"code":         code,
		"codeVerifier": codeVerifier,
		"redirectUri":  redirectURI,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/token", strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	SetOIDCHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("create token with auth code failed with status %d: %s", resp.StatusCode, string(respBody))
	}

	var tokenResp CreateTokenResponse
	if err := json.Unmarshal(respBody, &tokenResp); err != nil {
		return nil, err
	}
	return &tokenResp, nil
}

func (c *SSOOIDCClient) LoginWithBuilderIDAuthCode(ctx context.Context) (*KiroTokenData, error) {
	codeVerifier, codeChallenge, err := generatePKCEForAuthCode()
	if err != nil {
		return nil, fmt.Errorf("failed to generate PKCE: %w", err)
	}

	state, err := generateStateForAuthCode()
	if err != nil {
		return nil, fmt.Errorf("failed to generate state: %w", err)
	}

	redirectURI, resultChan, err := c.startAuthCodeCallbackServer(ctx, state)
	if err != nil {
		return nil, fmt.Errorf("failed to start callback server: %w", err)
	}

	regResp, err := c.RegisterClientForAuthCode(ctx, redirectURI)
	if err != nil {
		return nil, fmt.Errorf("failed to register client for auth code: %w", err)
	}

	authURL := buildAuthorizationURL(
		getOIDCEndpoint(defaultIDCRegion)+"/authorize",
		regResp.ClientID,
		redirectURI,
		"codewhisperer:completions codewhisperer:analysis codewhisperer:conversations",
		state,
		codeChallenge,
	)

	fmt.Printf("\nOpening browser for AWS Builder ID authorization...\nURL: %s\n", authURL)
	if err := browser.OpenURL(authURL); err != nil {
		fmt.Printf("Could not open browser automatically. Please open the URL manually.\n")
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(10 * time.Minute):
		return nil, errors.New("authorization timed out")
	case result := <-resultChan:
		if result.Error != "" {
			return nil, fmt.Errorf("authorization failed: %s", result.Error)
		}

		tokenResp, err := c.CreateTokenWithAuthCode(ctx, regResp.ClientID, regResp.ClientSecret, result.Code, codeVerifier, redirectURI)
		if err != nil {
			return nil, fmt.Errorf("failed to create token: %w", err)
		}

		expiresIn := tokenResp.ExpiresIn
		if expiresIn <= 0 {
			expiresIn = 3600
		}
		expiresAt := time.Now().Add(time.Duration(expiresIn) * time.Second)

		email := c.FetchUserEmail(ctx, tokenResp.AccessToken)
		if email == "" {
			email = ExtractEmailFromJWT(tokenResp.AccessToken)
		}
		profileArn := c.FetchProfileArn(ctx, tokenResp.AccessToken, regResp.ClientID, tokenResp.RefreshToken)

		return &KiroTokenData{
			AccessToken:  tokenResp.AccessToken,
			RefreshToken: tokenResp.RefreshToken,
			ProfileArn:   profileArn,
			ExpiresAt:    expiresAt.Format(time.RFC3339),
			AuthMethod:   "builder-id",
			Provider:     "AWS",
			ClientID:     regResp.ClientID,
			ClientSecret: regResp.ClientSecret,
			Email:        email,
			Region:       defaultIDCRegion,
		}, nil
	}
}

func (c *SSOOIDCClient) LoginWithIDCAuthCode(ctx context.Context, startURL, region string) (*KiroTokenData, error) {
	codeVerifier, codeChallenge, err := generatePKCEForAuthCode()
	if err != nil {
		return nil, fmt.Errorf("failed to generate PKCE: %w", err)
	}

	state, err := generateStateForAuthCode()
	if err != nil {
		return nil, fmt.Errorf("failed to generate state: %w", err)
	}

	redirectURI, resultChan, err := c.startAuthCodeCallbackServer(ctx, state)
	if err != nil {
		return nil, fmt.Errorf("failed to start callback server: %w", err)
	}

	regResp, err := c.RegisterClientForAuthCodeWithIDC(ctx, redirectURI, startURL, region)
	if err != nil {
		return nil, fmt.Errorf("failed to register client for IDC auth code: %w", err)
	}

	authURL := buildAuthorizationURL(
		getOIDCEndpoint(region)+"/authorize",
		regResp.ClientID,
		redirectURI,
		"codewhisperer:completions codewhisperer:analysis codewhisperer:conversations",
		state,
		codeChallenge,
	)

	fmt.Printf("\nOpening browser for IAM Identity Center authorization...\nURL: %s\n", authURL)
	if err := browser.OpenURL(authURL); err != nil {
		fmt.Printf("Could not open browser automatically. Please open the URL manually.\n")
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(10 * time.Minute):
		return nil, errors.New("authorization timed out")
	case result := <-resultChan:
		if result.Error != "" {
			return nil, fmt.Errorf("authorization failed: %s", result.Error)
		}

		tokenResp, err := c.CreateTokenWithAuthCodeAndRegion(ctx, regResp.ClientID, regResp.ClientSecret, result.Code, codeVerifier, redirectURI, region)
		if err != nil {
			return nil, fmt.Errorf("failed to create token: %w", err)
		}

		expiresIn := tokenResp.ExpiresIn
		if expiresIn <= 0 {
			expiresIn = 3600
		}
		expiresAt := time.Now().Add(time.Duration(expiresIn) * time.Second)

		email := c.FetchUserEmail(ctx, tokenResp.AccessToken)
		if email == "" {
			email = ExtractEmailFromJWT(tokenResp.AccessToken)
		}
		profileArn := c.FetchProfileArn(ctx, tokenResp.AccessToken, regResp.ClientID, tokenResp.RefreshToken)

		return &KiroTokenData{
			AccessToken:  tokenResp.AccessToken,
			RefreshToken: tokenResp.RefreshToken,
			ProfileArn:   profileArn,
			ExpiresAt:    expiresAt.Format(time.RFC3339),
			AuthMethod:   "idc",
			Provider:     "Enterprise",
			ClientID:     regResp.ClientID,
			ClientSecret: regResp.ClientSecret,
			Email:        email,
			StartURL:     startURL,
			Region:       region,
		}, nil
	}
}

func buildAuthorizationURL(endpoint, clientID, redirectURI, scopes, state, codeChallenge string) string {
	values := url.Values{}
	values.Set("response_type", "code")
	values.Set("client_id", clientID)
	values.Set("redirect_uri", redirectURI)
	values.Set("scope", scopes)
	values.Set("state", state)
	values.Set("code_challenge", codeChallenge)
	values.Set("code_challenge_method", "S256")
	return endpoint + "?" + values.Encode()
}
