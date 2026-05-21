package kiro

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

const (
	Provider                 = "kiro"
	DefaultRegion            = "us-east-1"
	DefaultAPIRegion         = "us-east-1"
	RuntimeHostTemplate      = "https://runtime.%s.kiro.dev"
	SSOOIDCTokenURLTemplate  = "https://oidc.%s.amazonaws.com/token"
	DesktopRefreshURLPattern = "https://prod.%s.auth.desktop.kiro.dev/refreshToken"
)

// TokenData contains the credential material needed to call Kiro runtime APIs.
type TokenData struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ProfileARN   string `json:"profile_arn"`
	ExpiresAt    string `json:"expires_at"`
	ExpiresIn    int64  `json:"expires_in,omitempty"`
	Region       string `json:"region,omitempty"`
	APIRegion    string `json:"api_region,omitempty"`
	SSORegion    string `json:"sso_region,omitempty"`
	StartURL     string `json:"start_url,omitempty"`
	ClientID     string `json:"client_id,omitempty"`
	ClientSecret string `json:"client_secret,omitempty"`
	Email        string `json:"email,omitempty"`
	AuthMethod   string `json:"auth_method,omitempty"`
}

// TokenStorage persists Kiro token metadata as an auth JSON file.
type TokenStorage struct {
	Data     TokenData      `json:"-"`
	Metadata map[string]any `json:"-"`
}

func (s *TokenStorage) SetMetadata(metadata map[string]any) {
	s.Metadata = metadata
}

func (s *TokenStorage) SaveTokenToFile(path string) error {
	if s == nil {
		return fmt.Errorf("kiro token storage is nil")
	}
	metadata := s.Metadata
	if metadata == nil {
		metadata = MetadataFromTokenData(&s.Data)
	}
	raw, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal kiro token metadata: %w", err)
	}
	if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create kiro token directory: %w", err)
	}
	if err = os.WriteFile(path, raw, 0o600); err != nil {
		return fmt.Errorf("write kiro token file: %w", err)
	}
	return nil
}

type Auth struct {
	cfg    *config.Config
	client *http.Client
}

func NewKiroAuth(cfg *config.Config) *Auth {
	return &Auth{cfg: cfg, client: http.DefaultClient}
}

func NewKiroAuthWithClient(cfg *config.Config, client *http.Client) *Auth {
	if client == nil {
		client = http.DefaultClient
	}
	return &Auth{cfg: cfg, client: client}
}

func RuntimeHost(region string) string {
	region = firstNonEmpty(region, DefaultAPIRegion)
	return fmt.Sprintf(RuntimeHostTemplate, region)
}

func SSOOIDCTokenURL(region string) string {
	region = firstNonEmpty(region, DefaultRegion)
	return fmt.Sprintf(SSOOIDCTokenURLTemplate, region)
}

func DesktopRefreshURL(region string) string {
	region = firstNonEmpty(region, DefaultRegion)
	return fmt.Sprintf(DesktopRefreshURLPattern, region)
}

func CredentialFileName(td *TokenData) string {
	id := ""
	if td != nil {
		id = firstNonEmpty(td.Email, lastARNPart(td.ProfileARN), td.ClientID)
	}
	if id == "" {
		id = fmt.Sprintf("%d", time.Now().UnixMilli())
	}
	return "kiro-" + sanitizeFilePart(id) + ".json"
}

func MetadataFromTokenData(td *TokenData) map[string]any {
	metadata := map[string]any{"type": Provider}
	if td == nil {
		return metadata
	}
	put := func(key string, value string) {
		if strings.TrimSpace(value) != "" {
			metadata[key] = strings.TrimSpace(value)
		}
	}
	put("access_token", td.AccessToken)
	put("refresh_token", td.RefreshToken)
	put("profile_arn", td.ProfileARN)
	put("expires_at", td.ExpiresAt)
	put("region", firstNonEmpty(td.Region, td.APIRegion, DefaultAPIRegion))
	put("api_region", firstNonEmpty(td.APIRegion, td.Region, DefaultAPIRegion))
	put("sso_region", firstNonEmpty(td.SSORegion, td.Region, DefaultRegion))
	put("start_url", td.StartURL)
	put("client_id", td.ClientID)
	put("client_secret", td.ClientSecret)
	put("email", td.Email)
	put("auth_method", firstNonEmpty(td.AuthMethod, "sso-oidc"))
	if td.ExpiresIn > 0 {
		metadata["expires_in"] = td.ExpiresIn
	}
	return metadata
}

func TokenDataFromMetadata(metadata map[string]any) *TokenData {
	if metadata == nil {
		return nil
	}
	return NormalizeTokenData(&TokenData{
		AccessToken:  stringAny(metadata, "access_token", "accessToken"),
		RefreshToken: stringAny(metadata, "refresh_token", "refreshToken"),
		ProfileARN:   stringAny(metadata, "profile_arn", "profileArn"),
		ExpiresAt:    stringAny(metadata, "expires_at", "expiresAt", "expired", "expire"),
		Region:       stringAny(metadata, "region"),
		APIRegion:    stringAny(metadata, "api_region", "apiRegion"),
		SSORegion:    stringAny(metadata, "sso_region", "ssoRegion"),
		StartURL:     stringAny(metadata, "start_url", "startUrl"),
		ClientID:     stringAny(metadata, "client_id", "clientId"),
		ClientSecret: stringAny(metadata, "client_secret", "clientSecret"),
		Email:        stringAny(metadata, "email"),
		AuthMethod:   stringAny(metadata, "auth_method", "authMethod"),
		ExpiresIn:    int64Any(metadata, "expires_in", "expiresIn"),
	})
}

func LoadTokenFile(path string) (*TokenData, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("kiro token file path is empty")
	}
	if strings.HasPrefix(path, "~") {
		if home, errHome := os.UserHomeDir(); errHome == nil {
			remainder := strings.TrimLeft(strings.TrimPrefix(path, "~"), `/\`)
			if remainder == "" {
				path = home
			} else {
				path = filepath.Join(home, filepath.FromSlash(strings.ReplaceAll(remainder, `\`, `/`)))
			}
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read kiro token file: %w", err)
	}
	var metadata map[string]any
	if err = json.Unmarshal(raw, &metadata); err != nil {
		return nil, fmt.Errorf("parse kiro token file: %w", err)
	}
	td := TokenDataFromMetadata(metadata)
	if strings.TrimSpace(td.AccessToken) == "" && strings.TrimSpace(td.RefreshToken) == "" {
		return nil, fmt.Errorf("kiro token file does not contain access_token or refresh_token")
	}
	return td, nil
}

func NormalizeTokenData(td *TokenData) *TokenData {
	if td == nil {
		return &TokenData{}
	}
	td.AccessToken = strings.TrimSpace(td.AccessToken)
	td.RefreshToken = strings.TrimSpace(td.RefreshToken)
	td.ProfileARN = strings.TrimSpace(td.ProfileARN)
	td.ExpiresAt = normalizeExpiry(td.ExpiresAt, td.ExpiresIn)
	td.Region = firstNonEmpty(td.Region, td.APIRegion, DefaultAPIRegion)
	td.APIRegion = firstNonEmpty(td.APIRegion, td.Region, DefaultAPIRegion)
	td.SSORegion = firstNonEmpty(td.SSORegion, td.Region, DefaultRegion)
	td.StartURL = strings.TrimSpace(td.StartURL)
	td.ClientID = strings.TrimSpace(td.ClientID)
	td.ClientSecret = strings.TrimSpace(td.ClientSecret)
	td.Email = strings.TrimSpace(td.Email)
	td.AuthMethod = firstNonEmpty(td.AuthMethod, "sso-oidc")
	return td
}

func (a *Auth) Refresh(ctx context.Context, td *TokenData) (*TokenData, error) {
	td = NormalizeTokenData(td)
	if td.RefreshToken == "" {
		return nil, fmt.Errorf("kiro refresh token is required")
	}
	if td.ClientID != "" && td.ClientSecret != "" {
		return a.refreshSSOOIDC(ctx, td)
	}
	return a.refreshDesktop(ctx, td)
}

func (a *Auth) refreshSSOOIDC(ctx context.Context, td *TokenData) (*TokenData, error) {
	payload := map[string]string{
		"grantType":    "refresh_token",
		"clientId":     td.ClientID,
		"clientSecret": td.ClientSecret,
		"refreshToken": td.RefreshToken,
	}
	return a.postRefresh(ctx, SSOOIDCTokenURL(td.SSORegion), payload, td)
}

func (a *Auth) refreshDesktop(ctx context.Context, td *TokenData) (*TokenData, error) {
	payload := map[string]string{"refreshToken": td.RefreshToken}
	return a.postRefresh(ctx, DesktopRefreshURL(td.SSORegion), payload, td)
}

func (a *Auth) postRefresh(ctx context.Context, endpoint string, payload map[string]string, previous *TokenData) (*TokenData, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal kiro refresh payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("create kiro refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "cli-proxy-api/kiro")

	client := a.client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kiro refresh request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("kiro refresh failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var result map[string]any
	if err = json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse kiro refresh response: %w", err)
	}
	next := &TokenData{
		AccessToken:  stringAny(result, "accessToken", "access_token"),
		RefreshToken: stringAny(result, "refreshToken", "refresh_token"),
		ProfileARN:   firstNonEmpty(stringAny(result, "profileArn", "profile_arn"), previous.ProfileARN),
		ExpiresIn:    int64Any(result, "expiresIn", "expires_in"),
		Region:       previous.Region,
		APIRegion:    previous.APIRegion,
		SSORegion:    previous.SSORegion,
		StartURL:     previous.StartURL,
		ClientID:     previous.ClientID,
		ClientSecret: previous.ClientSecret,
		Email:        previous.Email,
		AuthMethod:   previous.AuthMethod,
	}
	if next.AccessToken == "" {
		return nil, fmt.Errorf("kiro refresh response missing access token")
	}
	if next.RefreshToken == "" {
		next.RefreshToken = previous.RefreshToken
	}
	return NormalizeTokenData(next), nil
}

func Fingerprint(seed string) string {
	seed = strings.TrimSpace(seed)
	if seed == "" {
		seed = "cli-proxy-api-kiro"
	}
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:])[:16]
}

func normalizeExpiry(expiry string, expiresIn int64) string {
	expiry = strings.TrimSpace(expiry)
	if expiry != "" {
		if ts, ok := parseExpiry(expiry); ok {
			return ts.UTC().Format(time.RFC3339)
		}
		return expiry
	}
	if expiresIn <= 0 {
		return ""
	}
	return time.Now().UTC().Add(time.Duration(expiresIn) * time.Second).Format(time.RFC3339)
}

func parseExpiry(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339, time.RFC3339Nano, "2006-01-02T15:04:05Z0700", "2006-01-02 15:04:05"} {
		if ts, err := time.Parse(layout, value); err == nil {
			return ts, true
		}
	}
	return time.Time{}, false
}

func stringAny(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if v, ok := m[key]; ok {
			switch typed := v.(type) {
			case string:
				if s := strings.TrimSpace(typed); s != "" {
					return s
				}
			}
		}
	}
	return ""
}

func int64Any(m map[string]any, keys ...string) int64 {
	for _, key := range keys {
		if v, ok := m[key]; ok {
			switch typed := v.(type) {
			case float64:
				return int64(typed)
			case int64:
				return typed
			case int:
				return int64(typed)
			case json.Number:
				n, _ := typed.Int64()
				return n
			}
		}
	}
	return 0
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func lastARNPart(arn string) string {
	arn = strings.TrimSpace(arn)
	if arn == "" {
		return ""
	}
	parts := strings.Split(arn, "/")
	return strings.TrimSpace(parts[len(parts)-1])
}

func sanitizeFilePart(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "account"
	}
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), ".-")
	if out == "" {
		return "account"
	}
	return out
}
