package kiro

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/google/uuid"
)

// Constants used across the kiro auth package for AWS CodeWhisperer
// runtime endpoint construction.
const (
	DefaultKiroRegion       = "us-east-1"
	pathGetUsageLimits      = "getUsageLimits"
	pathListAvailableModels = "listAvailableModels"
)

// ProfileARN is a parsed representation of an AWS CodeWhisperer profile ARN.
type ProfileARN struct {
	Raw          string
	Partition    string
	Service      string
	Region       string
	AccountID    string
	ResourceType string
	ResourceID   string
}

// ParseProfileARN parses a profile ARN of the form:
//
//	arn:<partition>:codewhisperer:<region>:<account-id>:<resourceType>/<resourceID>
//
// Returns nil if the ARN is malformed or not for CodeWhisperer.
func ParseProfileARN(arn string) *ProfileARN {
	if arn == "" {
		return nil
	}
	parts := strings.Split(arn, ":")
	if len(parts) < 6 || parts[0] != "arn" || parts[1] == "" || parts[2] != "codewhisperer" {
		return nil
	}
	region := strings.TrimSpace(parts[3])
	if region == "" || !strings.Contains(region, "-") {
		return nil
	}
	resource := strings.Join(parts[5:], ":")
	resourceType := resource
	resourceID := ""
	if idx := strings.Index(resource, "/"); idx > 0 {
		resourceType = resource[:idx]
		resourceID = resource[idx+1:]
	}
	return &ProfileARN{
		Raw:          arn,
		Partition:    parts[1],
		Service:      parts[2],
		Region:       region,
		AccountID:    parts[4],
		ResourceType: resourceType,
		ResourceID:   resourceID,
	}
}

// ExtractRegionFromProfileArn returns the region embedded in a CodeWhisperer
// profile ARN, or "" if the ARN is unparseable.
func ExtractRegionFromProfileArn(profileArn string) string {
	parsed := ParseProfileARN(profileArn)
	if parsed == nil {
		return ""
	}
	return parsed.Region
}

// GetKiroAPIEndpoint returns the Q runtime endpoint for the given AWS region,
// falling back to DefaultKiroRegion when region is empty.
func GetKiroAPIEndpoint(region string) string {
	if region == "" {
		region = DefaultKiroRegion
	}
	return "https://q." + region + ".amazonaws.com"
}

// buildURL composes a runtime URL from endpoint, path, and optional query
// parameters. Empty parameter values are skipped.
func buildURL(endpoint, path string, queryParams map[string]string) string {
	fullURL := fmt.Sprintf("%s/%s", endpoint, path)
	if len(queryParams) == 0 {
		return fullURL
	}
	values := url.Values{}
	for key, value := range queryParams {
		if strings.TrimSpace(value) == "" {
			continue
		}
		values.Set(key, value)
	}
	if encoded := values.Encode(); encoded != "" {
		fullURL += "?" + encoded
	}
	return fullURL
}

// GenerateAccountKey derives a stable short hash from a seed string,
// suitable for use as a per-account cache key.
func GenerateAccountKey(seed string) string {
	hash := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(hash[:8])
}

// GetAccountKey produces a stable account key derived from clientID
// (preferred) or refreshToken; falls back to a UUID if both are empty.
func GetAccountKey(clientID, refreshToken string) string {
	if clientID != "" {
		return GenerateAccountKey(clientID)
	}
	if refreshToken != "" {
		return GenerateAccountKey(refreshToken)
	}
	return GenerateAccountKey(uuid.New().String())
}

// Process-wide FingerprintManager singleton. Created on first use.
var (
	globalFingerprintManager     *FingerprintManager
	globalFingerprintManagerOnce sync.Once
)

// GetGlobalFingerprintManager returns the process-wide FingerprintManager,
// initializing it on first call.
func GetGlobalFingerprintManager() *FingerprintManager {
	globalFingerprintManagerOnce.Do(func() {
		globalFingerprintManager = NewFingerprintManager()
	})
	return globalFingerprintManager
}

// setRuntimeHeaders applies the Authorization, user-agent, and AWS SDK
// invocation headers to req using the per-account fingerprint.
func setRuntimeHeaders(req *http.Request, accessToken string, accountKey string) {
	fp := GetGlobalFingerprintManager().GetFingerprint(accountKey)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("x-amz-user-agent", fp.BuildAmzUserAgent())
	req.Header.Set("User-Agent", fp.BuildUserAgent())
	req.Header.Set("amz-sdk-invocation-id", uuid.New().String())
	req.Header.Set("amz-sdk-request", "attempt=1; max=1")
}

// FetchProfileArn is the exported wrapper around the internal sso_oidc.go
// fetchProfileArn helper. It delegates to the unexported method so callers
// outside this file (such as oauth_web.go) can resolve a profile ARN
// without exposing transport details. clientID and refreshToken are
// accepted for forward compatibility (per-account fingerprinting) but
// the underlying API call only requires the access token.
func (c *SSOOIDCClient) FetchProfileArn(ctx context.Context, accessToken, clientID, refreshToken string) string {
	_ = clientID
	_ = refreshToken
	return c.fetchProfileArn(ctx, accessToken)
}
