// Package kiro provides Cognito Identity credential exchange for IDC authentication.
// AWS Identity Center (IDC) requires SigV4 signing with Cognito-exchanged credentials
// instead of Bearer token authentication.
package kiro

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	log "github.com/sirupsen/logrus"
)

const (
	// Cognito Identity endpoints
	cognitoIdentityEndpoint = "https://cognito-identity.us-east-1.amazonaws.com"

	// Identity Pool ID for Q Developer / CodeWhisperer
	// This is the identity pool used by kiro-cli and Amazon Q CLI
	cognitoIdentityPoolID = "us-east-1:70717e99-906f-485d-8d89-c89a0b5d49c5"

	// Cognito provider name for SSO OIDC
	cognitoProviderName = "cognito-identity.amazonaws.com"
)

// CognitoCredentials holds temporary AWS credentials from Cognito Identity.
type CognitoCredentials struct {
	AccessKeyID     string    `json:"access_key_id"`
	SecretAccessKey string    `json:"secret_access_key"`
	SessionToken    string    `json:"session_token"`
	Expiration      time.Time `json:"expiration"`
}

// CognitoIdentityClient handles Cognito Identity credential exchange.
type CognitoIdentityClient struct {
	httpClient *http.Client
	cfg        *config.Config
}

// NewCognitoIdentityClient creates a new Cognito Identity client.
func NewCognitoIdentityClient(cfg *config.Config) *CognitoIdentityClient {
	client := &http.Client{Timeout: 30 * time.Second}
	if cfg != nil {
		client = util.SetProxy(&cfg.SDKConfig, client)
	}
	return &CognitoIdentityClient{
		httpClient: client,
		cfg:        cfg,
	}
}

// GetIdentityID retrieves a Cognito Identity ID using the SSO access token.
func (c *CognitoIdentityClient) GetIdentityID(ctx context.Context, accessToken, region string) (string, error) {
	if region == "" {
		region = "us-east-1"
	}

	endpoint := fmt.Sprintf("https://cognito-identity.%s.amazonaws.com", region)

	// Build the GetId request
	// The SSO token is passed as a login token for the identity pool
	payload := map[string]interface{}{
		"IdentityPoolId": cognitoIdentityPoolID,
		"Logins": map[string]string{
			// Use the OIDC provider URL as the key
			fmt.Sprintf("oidc.%s.amazonaws.com", region): accessToken,
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("failed to marshal GetId request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return "", fmt.Errorf("failed to create GetId request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "AWSCognitoIdentityService.GetId")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("GetId request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read GetId response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		log.Debugf("Cognito GetId failed (status %d): %s", resp.StatusCode, string(respBody))
		return "", fmt.Errorf("GetId failed (status %d): %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		IdentityID string `json:"IdentityId"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("failed to parse GetId response: %w", err)
	}

	if result.IdentityID == "" {
		return "", fmt.Errorf("empty IdentityId in GetId response")
	}

	log.Debugf("Cognito Identity ID: %s", result.IdentityID)
	return result.IdentityID, nil
}

// GetCredentialsForIdentity exchanges an identity ID and login token for temporary AWS credentials.
func (c *CognitoIdentityClient) GetCredentialsForIdentity(ctx context.Context, identityID, accessToken, region string) (*CognitoCredentials, error) {
	if region == "" {
		region = "us-east-1"
	}

	endpoint := fmt.Sprintf("https://cognito-identity.%s.amazonaws.com", region)

	payload := map[string]interface{}{
		"IdentityId": identityID,
		"Logins": map[string]string{
			fmt.Sprintf("oidc.%s.amazonaws.com", region): accessToken,
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal GetCredentialsForIdentity request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("failed to create GetCredentialsForIdentity request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "AWSCognitoIdentityService.GetCredentialsForIdentity")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GetCredentialsForIdentity request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read GetCredentialsForIdentity response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		log.Debugf("Cognito GetCredentialsForIdentity failed (status %d): %s", resp.StatusCode, string(respBody))
		return nil, fmt.Errorf("GetCredentialsForIdentity failed (status %d): %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Credentials struct {
			AccessKeyID     string `json:"AccessKeyId"`
			SecretKey       string `json:"SecretKey"`
			SessionToken    string `json:"SessionToken"`
			Expiration      int64  `json:"Expiration"`
		} `json:"Credentials"`
		IdentityID string `json:"IdentityId"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("failed to parse GetCredentialsForIdentity response: %w", err)
	}

	if result.Credentials.AccessKeyID == "" {
		return nil, fmt.Errorf("empty AccessKeyId in GetCredentialsForIdentity response")
	}

	// Expiration is in seconds since epoch
	expiration := time.Unix(result.Credentials.Expiration, 0)

	log.Debugf("Cognito credentials obtained, expires: %s", expiration.Format(time.RFC3339))

	return &CognitoCredentials{
		AccessKeyID:     result.Credentials.AccessKeyID,
		SecretAccessKey: result.Credentials.SecretKey,
		SessionToken:    result.Credentials.SessionToken,
		Expiration:      expiration,
	}, nil
}

// ExchangeSSOTokenForCredentials is a convenience method that performs the full
// Cognito Identity credential exchange flow: GetId -> GetCredentialsForIdentity
func (c *CognitoIdentityClient) ExchangeSSOTokenForCredentials(ctx context.Context, accessToken, region string) (*CognitoCredentials, error) {
	log.Debugf("Exchanging SSO token for Cognito credentials (region: %s)", region)

	// Step 1: Get Identity ID
	identityID, err := c.GetIdentityID(ctx, accessToken, region)
	if err != nil {
		return nil, fmt.Errorf("failed to get identity ID: %w", err)
	}

	// Step 2: Get credentials for the identity
	creds, err := c.GetCredentialsForIdentity(ctx, identityID, accessToken, region)
	if err != nil {
		return nil, fmt.Errorf("failed to get credentials for identity: %w", err)
	}

	return creds, nil
}

// SigV4Signer provides AWS Signature Version 4 signing for HTTP requests.
type SigV4Signer struct {
	credentials *CognitoCredentials
	region      string
	service     string
}

// NewSigV4Signer creates a new SigV4 signer with the given credentials.
func NewSigV4Signer(creds *CognitoCredentials, region, service string) *SigV4Signer {
	return &SigV4Signer{
		credentials: creds,
		region:      region,
		service:     service,
	}
}

// SignRequest signs an HTTP request using AWS Signature Version 4.
// The request body must be provided separately since it may have been read already.
func (s *SigV4Signer) SignRequest(req *http.Request, body []byte) error {
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")

	// Ensure required headers are set
	if req.Header.Get("Host") == "" {
		req.Header.Set("Host", req.URL.Host)
	}
	req.Header.Set("X-Amz-Date", amzDate)
	if s.credentials.SessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", s.credentials.SessionToken)
	}

	// Create canonical request
	canonicalRequest, signedHeaders := s.createCanonicalRequest(req, body)

	// Create string to sign
	algorithm := "AWS4-HMAC-SHA256"
	credentialScope := fmt.Sprintf("%s/%s/%s/aws4_request", dateStamp, s.region, s.service)
	stringToSign := fmt.Sprintf("%s\n%s\n%s\n%s",
		algorithm,
		amzDate,
		credentialScope,
		hashSHA256([]byte(canonicalRequest)),
	)

	// Calculate signature
	signingKey := s.getSignatureKey(dateStamp)
	signature := hex.EncodeToString(hmacSHA256(signingKey, []byte(stringToSign)))

	// Build Authorization header
	authHeader := fmt.Sprintf("%s Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		algorithm,
		s.credentials.AccessKeyID,
		credentialScope,
		signedHeaders,
		signature,
	)

	req.Header.Set("Authorization", authHeader)

	return nil
}

// createCanonicalRequest builds the canonical request string for SigV4.
func (s *SigV4Signer) createCanonicalRequest(req *http.Request, body []byte) (string, string) {
	// HTTP method
	method := req.Method

	// Canonical URI
	uri := req.URL.Path
	if uri == "" {
		uri = "/"
	}

	// Canonical query string (sorted)
	queryString := s.buildCanonicalQueryString(req)

	// Canonical headers (sorted, lowercase)
	canonicalHeaders, signedHeaders := s.buildCanonicalHeaders(req)

	// Hashed payload
	payloadHash := hashSHA256(body)

	canonicalRequest := fmt.Sprintf("%s\n%s\n%s\n%s\n%s\n%s",
		method,
		uri,
		queryString,
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	)

	return canonicalRequest, signedHeaders
}

// buildCanonicalQueryString builds a sorted, URI-encoded query string.
func (s *SigV4Signer) buildCanonicalQueryString(req *http.Request) string {
	if req.URL.RawQuery == "" {
		return ""
	}

	// Parse and sort query parameters
	params := make([]string, 0)
	for key, values := range req.URL.Query() {
		for _, value := range values {
			params = append(params, fmt.Sprintf("%s=%s", uriEncode(key), uriEncode(value)))
		}
	}
	sort.Strings(params)
	return strings.Join(params, "&")
}

// buildCanonicalHeaders builds sorted, lowercase canonical headers.
func (s *SigV4Signer) buildCanonicalHeaders(req *http.Request) (string, string) {
	// Headers to sign (must include host and x-amz-*)
	headerMap := make(map[string]string)
	headerMap["host"] = req.URL.Host

	for key, values := range req.Header {
		lowKey := strings.ToLower(key)
		// Include x-amz-* headers and content-type
		if strings.HasPrefix(lowKey, "x-amz-") || lowKey == "content-type" {
			headerMap[lowKey] = strings.TrimSpace(values[0])
		}
	}

	// Sort header names
	headerNames := make([]string, 0, len(headerMap))
	for name := range headerMap {
		headerNames = append(headerNames, name)
	}
	sort.Strings(headerNames)

	// Build canonical headers and signed headers
	var canonicalHeaders strings.Builder
	for _, name := range headerNames {
		canonicalHeaders.WriteString(name)
		canonicalHeaders.WriteString(":")
		canonicalHeaders.WriteString(headerMap[name])
		canonicalHeaders.WriteString("\n")
	}

	signedHeaders := strings.Join(headerNames, ";")

	return canonicalHeaders.String(), signedHeaders
}

// getSignatureKey derives the signing key for SigV4.
func (s *SigV4Signer) getSignatureKey(dateStamp string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+s.credentials.SecretAccessKey), []byte(dateStamp))
	kRegion := hmacSHA256(kDate, []byte(s.region))
	kService := hmacSHA256(kRegion, []byte(s.service))
	kSigning := hmacSHA256(kService, []byte("aws4_request"))
	return kSigning
}

// hmacSHA256 computes HMAC-SHA256.
func hmacSHA256(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}

// hashSHA256 computes SHA256 hash and returns hex string.
func hashSHA256(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

// uriEncode performs URI encoding for SigV4.
func uriEncode(s string) string {
	var result strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') ||
			(c >= '0' && c <= '9') || c == '-' || c == '.' || c == '_' || c == '~' {
			result.WriteByte(c)
		} else {
			result.WriteString(fmt.Sprintf("%%%02X", c))
		}
	}
	return result.String()
}

// IsExpired checks if the credentials are expired or about to expire.
func (c *CognitoCredentials) IsExpired() bool {
	// Consider expired if within 5 minutes of expiration
	return time.Now().Add(5 * time.Minute).After(c.Expiration)
}
