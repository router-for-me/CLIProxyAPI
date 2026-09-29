package service

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/system_setting"
)

const (
	TaskArtifactAccessQueryParameter = "access"
	taskArtifactAccessVersion        = "v1"
	taskArtifactAccessLength         = 43
	maxTaskArtifactTaskIDLength      = 191
	maxTaskArtifactKeyLength         = 128
)

var ErrTaskArtifactAccessInvalid = errors.New("task artifact access is invalid")

func taskArtifactAccessMessage(taskID, artifactKey string) []byte {
	return []byte(taskArtifactAccessVersion + "\x00" + taskID + "\x00" + artifactKey)
}

// IssueTaskArtifactAccess creates a stable capability bound to exactly one
// public task ID and artifact key. It contains no user or upstream data.
func IssueTaskArtifactAccess(taskID, artifactKey string) (string, error) {
	taskID = strings.TrimSpace(taskID)
	artifactKey = strings.TrimSpace(artifactKey)
	if taskID == "" || len(taskID) > maxTaskArtifactTaskIDLength ||
		artifactKey == "" || len(artifactKey) > maxTaskArtifactKeyLength ||
		common.CryptoSecret == "" {
		return "", ErrTaskArtifactAccessInvalid
	}

	mac := hmac.New(sha256.New, []byte(common.CryptoSecret))
	_, _ = mac.Write(taskArtifactAccessMessage(taskID, artifactKey))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

// VerifyTaskArtifactAccess verifies the route binding without reading task,
// user, or token state. Signature comparison is constant-time.
func VerifyTaskArtifactAccess(access, taskID, artifactKey string) bool {
	taskID = strings.TrimSpace(taskID)
	artifactKey = strings.TrimSpace(artifactKey)
	if len(access) != taskArtifactAccessLength ||
		taskID == "" || len(taskID) > maxTaskArtifactTaskIDLength ||
		artifactKey == "" || len(artifactKey) > maxTaskArtifactKeyLength ||
		common.CryptoSecret == "" {
		return false
	}

	actualSignature, err := base64.RawURLEncoding.Strict().DecodeString(access)
	if err != nil || len(actualSignature) != sha256.Size {
		return false
	}

	mac := hmac.New(sha256.New, []byte(common.CryptoSecret))
	_, _ = mac.Write(taskArtifactAccessMessage(taskID, artifactKey))
	return hmac.Equal(actualSignature, mac.Sum(nil))
}

// ValidateTaskArtifactBaseURL validates configuration syntax only. It
// deliberately performs no DNS lookup or reachability probe.
func ValidateTaskArtifactBaseURL(raw string) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return errors.New("task artifact base URL is empty")
	}
	if raw != trimmed {
		return errors.New("task artifact base URL must not contain surrounding whitespace")
	}
	raw = trimmed
	parsed, err := url.Parse(raw)
	if err != nil || parsed == nil {
		return errors.New("task artifact base URL is invalid")
	}
	if !strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https") {
		return errors.New("task artifact base URL must use http or https")
	}
	if parsed.Host == "" || parsed.User != nil || parsed.Opaque != "" {
		return errors.New("task artifact base URL must contain a host and no userinfo")
	}
	if parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || strings.Contains(raw, "#") {
		return errors.New("task artifact base URL must not contain a query or fragment")
	}
	return nil
}

// BuildTaskArtifactContentURL returns an absolute, long-lived capability URL.
// TaskPublicAddress wins when configured; ServerAddress is the only fallback.
// Request Host headers are intentionally not involved.
func BuildTaskArtifactContentURL(taskID, artifactKey string) (string, error) {
	taskID = strings.TrimSpace(taskID)
	artifactKey = strings.TrimSpace(artifactKey)
	if taskID == "" || len(taskID) > maxTaskArtifactTaskIDLength ||
		artifactKey == "" || len(artifactKey) > maxTaskArtifactKeyLength {
		return "", ErrTaskArtifactAccessInvalid
	}

	baseAddress := strings.TrimSpace(system_setting.TaskPublicAddress)
	if baseAddress == "" {
		baseAddress = strings.TrimSpace(system_setting.ServerAddress)
	}
	if err := ValidateTaskArtifactBaseURL(baseAddress); err != nil {
		return "", err
	}
	baseURL, err := url.Parse(baseAddress)
	if err != nil {
		return "", err
	}

	access, err := IssueTaskArtifactAccess(taskID, artifactKey)
	if err != nil {
		return "", err
	}

	basePath := strings.TrimRight(baseURL.Path, "/")
	escapedBasePath := strings.TrimRight(baseURL.EscapedPath(), "/")
	suffixPath := fmt.Sprintf("/v1/tasks/%s/artifacts/%s/content", taskID, artifactKey)
	escapedSuffixPath := fmt.Sprintf(
		"/v1/tasks/%s/artifacts/%s/content",
		url.PathEscape(taskID),
		url.PathEscape(artifactKey),
	)
	baseURL.Path = basePath + suffixPath
	baseURL.RawPath = escapedBasePath + escapedSuffixPath
	query := baseURL.Query()
	query.Set(TaskArtifactAccessQueryParameter, access)
	baseURL.RawQuery = query.Encode()
	return baseURL.String(), nil
}
