package kiro

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

type PKCECodes struct {
	CodeVerifier  string `json:"code_verifier"`
	CodeChallenge string `json:"code_challenge"`
}

type KiroTokenData struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ProfileArn   string `json:"profileArn"`
	ExpiresAt    string `json:"expiresAt"`
	AuthMethod   string `json:"authMethod"`
	Provider     string `json:"provider"`
	ClientID     string `json:"clientId,omitempty"`
	ClientSecret string `json:"clientSecret,omitempty"`
	ClientIDHash string `json:"clientIdHash,omitempty"`
	Email        string `json:"email,omitempty"`
	StartURL     string `json:"startUrl,omitempty"`
	Region       string `json:"region,omitempty"`
	BaseURL      string `json:"baseUrl,omitempty"`
}

type KiroAuthBundle struct {
	TokenData   KiroTokenData `json:"token_data"`
	LastRefresh string        `json:"last_refresh"`
}

type KiroModel struct {
	ModelID        string  `json:"modelId"`
	ModelName      string  `json:"modelName"`
	Description    string  `json:"description"`
	RateMultiplier float64 `json:"rateMultiplier"`
	RateUnit       string  `json:"rateUnit"`
	MaxInputTokens int     `json:"maxInputTokens,omitempty"`
}

const KiroIDETokenFile = ".aws/sso/cache/kiro-auth-token.json"

const (
	defaultTokenReadMaxAttempts = 10
	defaultTokenReadBaseDelay   = 50 * time.Millisecond
	DefaultKiroRegion           = "us-east-1"
)

func isTransientFileError(err error) bool {
	if err == nil {
		return false
	}
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		errStr := pathErr.Err.Error()
		if strings.Contains(errStr, "being used by another process") ||
			strings.Contains(errStr, "sharing violation") ||
			strings.Contains(errStr, "lock violation") {
			return true
		}
	}
	errMsg := strings.ToLower(err.Error())
	for _, pattern := range []string{
		"being used by another process",
		"sharing violation",
		"lock violation",
		"access is denied",
		"unexpected end of json",
		"unexpected eof",
	} {
		if strings.Contains(errMsg, pattern) {
			return true
		}
	}
	return false
}

func LoadKiroIDETokenWithRetry(maxAttempts int, baseDelay time.Duration) (*KiroTokenData, error) {
	if maxAttempts <= 0 {
		maxAttempts = defaultTokenReadMaxAttempts
	}
	if baseDelay <= 0 {
		baseDelay = defaultTokenReadBaseDelay
	}

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		token, err := LoadKiroIDEToken()
		if err == nil {
			return token, nil
		}
		lastErr = err
		if !isTransientFileError(err) {
			return nil, err
		}
		delay := baseDelay * time.Duration(1<<uint(attempt))
		if delay > 500*time.Millisecond {
			delay = 500 * time.Millisecond
		}
		time.Sleep(delay)
	}

	return nil, fmt.Errorf("failed to read token file after %d attempts: %w", maxAttempts, lastErr)
}

func LoadKiroIDEToken() (*KiroTokenData, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get home directory: %w", err)
	}

	tokenPath := filepath.Join(homeDir, KiroIDETokenFile)
	data, err := os.ReadFile(tokenPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read Kiro IDE token file (%s): %w", tokenPath, err)
	}

	var token KiroTokenData
	if err := json.Unmarshal(data, &token); err != nil {
		return nil, fmt.Errorf("failed to parse Kiro IDE token: %w", err)
	}

	if token.AccessToken == "" {
		return nil, fmt.Errorf("access token is empty in Kiro IDE token file")
	}

	token.AuthMethod = strings.ToLower(token.AuthMethod)

	if token.ClientIDHash != "" && token.ClientID == "" {
		if err := loadDeviceRegistration(homeDir, token.ClientIDHash, &token); err != nil {
			log.Warnf("failed to load device registration for clientIdHash %s: %v", token.ClientIDHash, err)
		}
	}

	return &token, nil
}

func LoadDeviceRegistrationCredentials(clientIDHash string) (clientID, clientSecret string, err error) {
	if clientIDHash == "" {
		return "", "", fmt.Errorf("clientIdHash is empty")
	}
	if strings.Contains(clientIDHash, "/") || strings.Contains(clientIDHash, "\\") || strings.Contains(clientIDHash, "..") {
		return "", "", fmt.Errorf("invalid clientIdHash: contains path separator")
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", "", fmt.Errorf("failed to get home directory: %w", err)
	}

	deviceRegPath := filepath.Join(homeDir, ".aws", "sso", "cache", clientIDHash+".json")
	data, err := os.ReadFile(deviceRegPath)
	if err != nil {
		return "", "", fmt.Errorf("failed to read device registration file (%s): %w", deviceRegPath, err)
	}

	var deviceReg struct {
		ClientID     string `json:"clientId"`
		ClientSecret string `json:"clientSecret"`
	}

	if err := json.Unmarshal(data, &deviceReg); err != nil {
		return "", "", fmt.Errorf("failed to parse device registration: %w", err)
	}

	if deviceReg.ClientID == "" || deviceReg.ClientSecret == "" {
		return "", "", fmt.Errorf("device registration missing clientId or clientSecret")
	}

	return deviceReg.ClientID, deviceReg.ClientSecret, nil
}

func loadDeviceRegistration(homeDir, clientIDHash string, token *KiroTokenData) error {
	cID, cSec, err := LoadDeviceRegistrationCredentials(clientIDHash)
	if err != nil {
		return err
	}
	token.ClientID = cID
	token.ClientSecret = cSec
	return nil
}

type JWTClaims struct {
	Email         string `json:"email,omitempty"`
	Sub           string `json:"sub,omitempty"`
	PreferredUser string `json:"preferred_username,omitempty"`
	Name          string `json:"name,omitempty"`
	Iss           string `json:"iss,omitempty"`
}

func ExtractEmailFromJWT(accessToken string) string {
	if accessToken == "" {
		return ""
	}
	parts := strings.Split(accessToken, ".")
	if len(parts) != 3 {
		return ""
	}
	payload := parts[1]
	switch len(payload) % 4 {
	case 2:
		payload += "=="
	case 3:
		payload += "="
	}

	decoded, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		decoded, err = base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			return ""
		}
	}

	var claims JWTClaims
	if err := json.Unmarshal(decoded, &claims); err != nil {
		return ""
	}
	if claims.Email != "" {
		return claims.Email
	}
	if claims.PreferredUser != "" && strings.Contains(claims.PreferredUser, "@") {
		return claims.PreferredUser
	}
	if claims.Sub != "" && strings.Contains(claims.Sub, "@") {
		return claims.Sub
	}
	return ""
}

func SanitizeEmailForFilename(email string) string {
	if email == "" {
		return ""
	}
	result := email
	result = strings.ReplaceAll(result, "%2F", "_")
	result = strings.ReplaceAll(result, "%2f", "_")
	result = strings.ReplaceAll(result, "%5C", "_")
	result = strings.ReplaceAll(result, "%5c", "_")
	result = strings.ReplaceAll(result, "%2E", "_")
	result = strings.ReplaceAll(result, "%2e", "_")
	result = strings.ReplaceAll(result, "%00", "_")
	result = strings.ReplaceAll(result, "%", "_")

	for _, char := range []string{"/", "\\", ":", "*", "?", "\"", "<", ">", "|", " ", "\x00"} {
		result = strings.ReplaceAll(result, char, "_")
	}

	parts := strings.Split(result, "_")
	for i, part := range parts {
		for strings.HasPrefix(part, ".") {
			part = "_" + part[1:]
		}
		parts[i] = part
	}
	return strings.Join(parts, "_")
}

func ExtractIDCIdentifier(startURL string) string {
	if startURL == "" {
		return ""
	}
	urlStr := strings.TrimPrefix(startURL, "https://")
	urlStr = strings.TrimPrefix(urlStr, "http://")
	parts := strings.Split(urlStr, ".")
	if len(parts) > 0 && parts[0] != "" {
		identifier := parts[0]
		identifier = strings.ReplaceAll(identifier, "/", "_")
		identifier = strings.ReplaceAll(identifier, "\\", "_")
		identifier = strings.ReplaceAll(identifier, ":", "_")
		return identifier
	}
	return ""
}

type ProfileARN struct {
	Raw          string
	Partition    string
	Service      string
	Region       string
	AccountID    string
	ResourceType string
	ResourceID   string
}

func ParseProfileARN(arn string) *ProfileARN {
	if arn == "" {
		return nil
	}
	parts := strings.Split(arn, ":")
	if len(parts) < 6 {
		return nil
	}
	if parts[0] != "arn" || parts[1] == "" || parts[2] != "codewhisperer" {
		return nil
	}
	region := parts[3]
	if region == "" || !strings.Contains(region, "-") {
		return nil
	}
	accountID := parts[4]
	resource := strings.Join(parts[5:], ":")
	resourceType := ""
	resourceID := ""
	if idx := strings.Index(resource, "/"); idx > 0 {
		resourceType = resource[:idx]
		resourceID = resource[idx+1:]
	} else {
		resourceType = resource
	}

	return &ProfileARN{
		Raw:          arn,
		Partition:    parts[1],
		Service:      parts[2],
		Region:       region,
		AccountID:    accountID,
		ResourceType: resourceType,
		ResourceID:   resourceID,
	}
}

func ExtractRegionFromProfileArn(profileArn string) string {
	parsed := ParseProfileARN(profileArn)
	if parsed == nil {
		return ""
	}
	return parsed.Region
}

func GetKiroAPIEndpointFromProfileArn(profileArn string) string {
	region := ExtractRegionFromProfileArn(profileArn)
	return GetKiroAPIEndpoint(region)
}

func GetKiroAPIEndpoint(region string) string {
	if region == "" {
		region = DefaultKiroRegion
	}
	return "https://q." + region + ".amazonaws.com"
}

func GetCodeWhispererLegacyEndpoint(region string) string {
	if region == "" {
		region = DefaultKiroRegion
	}
	return "https://codewhisperer." + region + ".amazonaws.com"
}

func buildURL(endpoint, path string, queryParams map[string]string) string {
	fullURL := fmt.Sprintf("%s/%s", endpoint, path)
	if len(queryParams) > 0 {
		values := url.Values{}
		for key, value := range queryParams {
			if value == "" {
				continue
			}
			values.Set(key, value)
		}
		if encoded := values.Encode(); encoded != "" {
			fullURL = fullURL + "?" + encoded
		}
	}
	return fullURL
}
