package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

var regionPattern = regexp.MustCompile(`^[a-z]{2}(?:-gov)?-[a-z]+-\d+$`)

type kiroCredential struct {
	Type      string `json:"type"`
	APIKey    string `json:"api_key,omitempty"`
	APIKeyEnv string `json:"api_key_env,omitempty"`
	Region    string `json:"region,omitempty"`
	Label     string `json:"label,omitempty"`
	Prefix    string `json:"prefix,omitempty"`
	ProxyURL  string `json:"proxy_url,omitempty"`
	Disabled  bool   `json:"disabled,omitempty"`
}

func parseAuth(raw []byte) ([]byte, error) {
	var request pluginapi.AuthParseRequest
	if errUnmarshal := json.Unmarshal(raw, &request); errUnmarshal != nil {
		return nil, fmt.Errorf("decode auth parse request: %w", errUnmarshal)
	}
	credential, handled, errCredential := decodeCredential(request.RawJSON)
	if errCredential != nil {
		return errorEnvelope("invalid_auth", errCredential.Error(), 0, false), nil
	}
	if !handled {
		return okEnvelope(pluginapi.AuthParseResponse{Handled: false})
	}
	storage, errMarshal := json.Marshal(credential)
	if errMarshal != nil {
		return nil, errMarshal
	}
	label := firstNonEmpty(credential.Label, strings.TrimSuffix(request.FileName, filepath.Ext(request.FileName)), keyFingerprint(credential))
	authID := strings.TrimSpace(request.FileName)
	if authID == "" {
		authID = "kiro-" + keyFingerprint(credential)
	}
	return okEnvelope(pluginapi.AuthParseResponse{
		Handled: true,
		Auth: pluginapi.AuthData{
			Provider:    pluginIdentifier,
			ID:          authID,
			FileName:    request.FileName,
			Label:       label,
			Prefix:      credential.Prefix,
			ProxyURL:    credential.ProxyURL,
			Disabled:    credential.Disabled,
			StorageJSON: storage,
			Metadata: map[string]any{
				"type":        pluginIdentifier,
				"auth_kind":   "api_key",
				"region":      credential.Region,
				"key_source":  keySource(credential),
				"key_present": credential.APIKey != "" || os.Getenv(credential.APIKeyEnv) != "",
			},
			Attributes: map[string]string{
				"auth_kind": "api_key",
				"region":    credential.Region,
			},
		},
	})
}

func refreshAuth(raw []byte) ([]byte, error) {
	var request pluginapi.AuthRefreshRequest
	if errUnmarshal := json.Unmarshal(raw, &request); errUnmarshal != nil {
		return nil, fmt.Errorf("decode auth refresh request: %w", errUnmarshal)
	}
	credential, handled, errCredential := decodeCredential(request.StorageJSON)
	if errCredential != nil {
		return errorEnvelope("invalid_auth", errCredential.Error(), 0, false), nil
	}
	if !handled {
		return errorEnvelope("invalid_auth", "Kiro auth storage is not recognized", 0, false), nil
	}
	return okEnvelope(pluginapi.AuthRefreshResponse{Auth: pluginapi.AuthData{
		Provider:    pluginIdentifier,
		ID:          request.AuthID,
		StorageJSON: request.StorageJSON,
		Metadata:    request.Metadata,
		Attributes:  request.Attributes,
		Label:       credential.Label,
		Prefix:      credential.Prefix,
		ProxyURL:    credential.ProxyURL,
		Disabled:    credential.Disabled,
	}})
}

func decodeCredential(raw []byte) (kiroCredential, bool, error) {
	var credential kiroCredential
	if len(raw) == 0 {
		return credential, false, nil
	}
	if errUnmarshal := json.Unmarshal(raw, &credential); errUnmarshal != nil {
		return credential, false, nil
	}
	credential.Type = strings.ToLower(strings.TrimSpace(credential.Type))
	if credential.Type != pluginIdentifier {
		return credential, false, nil
	}
	credential.APIKey = strings.TrimSpace(credential.APIKey)
	credential.APIKeyEnv = strings.TrimSpace(credential.APIKeyEnv)
	credential.Region = firstNonEmpty(strings.ToLower(strings.TrimSpace(credential.Region)), "us-east-1")
	credential.Label = strings.TrimSpace(credential.Label)
	credential.Prefix = strings.TrimSpace(credential.Prefix)
	credential.ProxyURL = strings.TrimSpace(credential.ProxyURL)
	if credential.APIKey == "" && credential.APIKeyEnv == "" {
		return credential, true, fmt.Errorf("either api_key or api_key_env is required")
	}
	if credential.APIKeyEnv != "" && strings.ContainsRune(credential.APIKeyEnv, '=') {
		return credential, true, fmt.Errorf("api_key_env must be an environment variable name")
	}
	if !regionPattern.MatchString(credential.Region) {
		return credential, true, fmt.Errorf("invalid Kiro region %q", credential.Region)
	}
	return credential, true, nil
}

func resolveAPIKey(credential kiroCredential) (string, error) {
	if credential.APIKey != "" {
		return credential.APIKey, nil
	}
	key := strings.TrimSpace(os.Getenv(credential.APIKeyEnv))
	if key == "" {
		return "", fmt.Errorf("environment variable %s is empty", credential.APIKeyEnv)
	}
	return key, nil
}

func keySource(credential kiroCredential) string {
	if credential.APIKey != "" {
		return "auth_file"
	}
	return "env:" + credential.APIKeyEnv
}

func keyFingerprint(credential kiroCredential) string {
	seed := credential.APIKey
	if seed == "" {
		seed = "env:" + credential.APIKeyEnv
	}
	sum := sha256.Sum256([]byte("KiroAPIKey/" + seed))
	return hex.EncodeToString(sum[:6])
}

func machineID(key string) string {
	sum := sha256.Sum256([]byte("KiroAPIKey/" + key))
	return hex.EncodeToString(sum[:])
}
