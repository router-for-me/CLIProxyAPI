package gitlab

import (
	"context"
	"fmt"
	"time"

	"github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/config"
)

const (
	DefaultCallbackPort = 54545
	DefaultBaseURL      = "https://gitlab.com"
)

type PKCECodes struct {
	CodeVerifier  string
	CodeChallenge string
}

type OAuthResult struct {
	Code  string
	State string
	Error string
}

type OAuthServer struct {
	port int
}

func NewOAuthServer(port int) *OAuthServer {
	return &OAuthServer{port: port}
}

func (s *OAuthServer) Start() error { return nil }

func (s *OAuthServer) Stop(context.Context) error { return nil }

func (s *OAuthServer) WaitForCallback(time.Duration) (*OAuthResult, error) {
	return nil, fmt.Errorf("gitlab oauth callback server not implemented")
}

type AuthClient struct {
	cfg *config.Config
}

func NewAuthClient(cfg *config.Config) *AuthClient {
	return &AuthClient{cfg: cfg}
}

func RedirectURL(port int) string {
	return fmt.Sprintf("http://localhost:%d/callback", port)
}

func GeneratePKCECodes() (*PKCECodes, error) {
	return &PKCECodes{}, nil
}

func NormalizeBaseURL(baseURL string) string {
	if baseURL == "" {
		return DefaultBaseURL
	}
	return baseURL
}

type TokenResponse struct {
	AccessToken  string
	RefreshToken string
	TokenType    string
	Scope        string
	ExpiresIn    int64
	ExpiresAt    int64
}

func TokenExpiry(now time.Time, token *TokenResponse) time.Time {
	if token == nil {
		return time.Time{}
	}
	if token.ExpiresAt > 0 {
		return time.Unix(token.ExpiresAt, 0).UTC()
	}
	if token.ExpiresIn > 0 {
		return now.Add(time.Duration(token.ExpiresIn) * time.Second).UTC()
	}
	return time.Time{}
}

type DirectAccessResponse struct {
	BaseURL      string
	Token        string
	ExpiresAt    int64
	Headers      map[string]string
	ModelDetails *DirectAccessModelDetails
}

type DirectAccessModelDetails struct {
	ModelProvider string
	ModelName     string
}

type User struct {
	Username    string
	Email       string
	PublicEmail string
	Name        string
}

func (c *AuthClient) GenerateAuthURL(baseURL, clientID, redirectURI, state string, pkce *PKCECodes) (string, error) {
	return NormalizeBaseURL(baseURL) + "/oauth/authorize", nil
}

func (c *AuthClient) ExchangeCodeForTokens(context.Context, string, string, string, string, string, string) (*TokenResponse, error) {
	return nil, fmt.Errorf("gitlab oauth token exchange not implemented")
}

func (c *AuthClient) RefreshTokens(context.Context, string, string, string, string) (*TokenResponse, error) {
	return nil, fmt.Errorf("gitlab oauth token refresh not implemented")
}

func (c *AuthClient) GetCurrentUser(context.Context, string, string) (*User, error) {
	return nil, fmt.Errorf("gitlab current user lookup not implemented")
}

func (c *AuthClient) FetchDirectAccess(context.Context, string, string) (*DirectAccessResponse, error) {
	return nil, fmt.Errorf("gitlab direct access lookup not implemented")
}

func (c *AuthClient) GetPersonalAccessTokenSelf(context.Context, string, string) (map[string]any, error) {
	return nil, fmt.Errorf("gitlab personal access token validation not implemented")
}

type DiscoveredModel struct {
	ModelName     string
	ModelProvider string
}

func ExtractDiscoveredModels(map[string]any) []DiscoveredModel {
	return nil
}
