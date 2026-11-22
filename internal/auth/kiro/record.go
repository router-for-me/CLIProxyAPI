package kiro

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// DetectLocalKiroToken searches local file locations for active Kiro CLI login tokens.
func DetectLocalKiroToken() (*KiroTokenBundle, error) {
	homeDir := ""
	if u, err := user.Current(); err == nil && u.HomeDir != "" {
		homeDir = u.HomeDir
	} else {
		homeDir = os.Getenv("HOME")
	}

	var candidatePaths []string
	if custom := os.Getenv("KIRO_AUTH_TOKEN_FILE"); custom != "" {
		candidatePaths = append(candidatePaths, custom)
	}
	if homeDir != "" {
		candidatePaths = append(candidatePaths,
			filepath.Join(homeDir, ".aws", "sso", "cache", "kiro-auth-token.json"),
			filepath.Join(homeDir, ".kiro", "identities", "kiro-auth-token.json"),
		)
	}

	var foundData []byte
	var foundPath string
	for _, p := range candidatePaths {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			data, errRead := os.ReadFile(p)
			if errRead == nil && len(data) > 0 {
				foundData = data
				foundPath = p
				break
			}
		}
	}

	if len(foundData) == 0 {
		return nil, fmt.Errorf("kiro token file not found in candidate paths")
	}

	var bundle KiroTokenBundle
	if err := json.Unmarshal(foundData, &bundle); err != nil {
		return nil, fmt.Errorf("failed to parse kiro token from %s: %w", foundPath, err)
	}

	if bundle.AccessToken == "" && bundle.RefreshToken == "" {
		return nil, fmt.Errorf("kiro token file %s contains no access or refresh tokens", foundPath)
	}

	// Try best-effort email discovery from kiro-cli whoami
	if bundle.Email == "" {
		bundle.Email = detectKiroEmail()
	}

	return &bundle, nil
}

func detectKiroEmail() string {
	cmd := exec.Command("kiro-cli", "whoami")
	out, err := cmd.Output()
	if err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "Email:") {
				return strings.TrimSpace(strings.TrimPrefix(line, "Email:"))
			}
		}
	}
	return ""
}

// CreateAuthRecord builds the shared CLI and management OAuth credential record for Kiro.
func CreateAuthRecord(bundle *KiroTokenBundle) (*coreauth.Auth, error) {
	if bundle == nil {
		return nil, fmt.Errorf("kiro token bundle is required")
	}

	email := strings.TrimSpace(bundle.Email)
	identifier := email
	if identifier == "" {
		identifier = bundle.Provider
	}
	if identifier == "" {
		h := sha256.Sum256([]byte(bundle.RefreshToken + bundle.AccessToken))
		identifier = fmt.Sprintf("user-%x", h[:8])
	}

	fileIdentifier := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' || r == '@' {
			return r
		}
		return '_'
	}, identifier)
	if fileIdentifier != identifier || len(fileIdentifier) > 160 {
		h := sha256.Sum256([]byte(identifier))
		fileIdentifier = fmt.Sprintf("user-%x", h[:8])
	}

	fileName := fmt.Sprintf("kiro-%s.json", fileIdentifier)
	label := fmt.Sprintf("Kiro (%s)", identifier)

	attributes := map[string]string{
		"api_key":       bundle.AccessToken,
		"access_token":  bundle.AccessToken,
		"refresh_token": bundle.RefreshToken,
		"profile_arn":   bundle.ProfileArn,
		"auth_kind":     "oauth",
	}
	if email != "" {
		attributes["email"] = email
	}

	metadata := map[string]any{
		"type":          "kiro",
		"auth_kind":     "oauth",
		"access_token":  bundle.AccessToken,
		"refresh_token": bundle.RefreshToken,
		"profile_arn":   bundle.ProfileArn,
		"expires_at":    bundle.ExpiresAt,
		"expired":       bundle.ExpiresAt,
	}
	if email != "" {
		metadata["email"] = email
	}
	if bundle.Provider != "" {
		metadata["provider"] = bundle.Provider
	}
	if bundle.AuthMethod != "" {
		metadata["auth_method"] = bundle.AuthMethod
	}

	storage := &TokenStorage{
		Type:         "kiro",
		AccessToken:  bundle.AccessToken,
		RefreshToken: bundle.RefreshToken,
		ProfileArn:   bundle.ProfileArn,
		ExpiresAt:    bundle.ExpiresAt,
		Expired:      bundle.ExpiresAt,
		Email:        email,
		Provider:     bundle.Provider,
		AuthMethod:   bundle.AuthMethod,
		AuthKind:     "oauth",
	}

	log.Infof("created kiro auth record for %s (id: %s)", identifier, fileName)

	return &coreauth.Auth{
		ID:         fileName,
		Provider:   "kiro",
		FileName:   fileName,
		Label:      label,
		Status:     coreauth.StatusActive,
		Storage:    storage,
		Attributes: attributes,
		Metadata:   metadata,
	}, nil
}
