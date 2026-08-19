package kiro

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type parsedImportToken struct {
	tokenData      *KiroTokenData
	rawKiroIDEJSON bool
}

func parseImportTokenPayload(data []byte) (*parsedImportToken, error) {
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, fmt.Errorf("invalid request body")
	}

	if tokenData, ok, err := parseRawKiroIDETokenJSON(data); ok || err != nil {
		return &parsedImportToken{tokenData: tokenData, rawKiroIDEJSON: ok}, err
	}

	var req ImportTokenRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, fmt.Errorf("invalid request body")
	}

	refreshToken := strings.TrimSpace(req.RefreshToken)
	if strings.HasPrefix(refreshToken, "{") {
		if tokenData, ok, err := parseRawKiroIDETokenJSON([]byte(refreshToken)); ok || err != nil {
			return &parsedImportToken{tokenData: tokenData, rawKiroIDEJSON: ok}, err
		}
	}

	return &parsedImportToken{
		tokenData: &KiroTokenData{RefreshToken: refreshToken},
	}, nil
}

func parseRawKiroIDETokenJSON(data []byte) (*KiroTokenData, bool, error) {
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, false, nil
	}

	accessToken := importStringField(fields, "accessToken", "access_token")
	refreshToken := importStringField(fields, "refreshToken", "refresh_token")
	hasKiroIDEField := accessToken != "" ||
		importStringField(fields, "profileArn", "profile_arn") != "" ||
		importStringField(fields, "clientIdHash", "client_id_hash") != "" ||
		importStringField(fields, "startUrl", "start_url") != "" ||
		importStringField(fields, "authMethod", "auth_method") != ""

	if !hasKiroIDEField {
		return nil, false, nil
	}
	if accessToken == "" {
		return nil, true, fmt.Errorf("accessToken is required when importing raw Kiro IDE token JSON")
	}
	if refreshToken == "" {
		return nil, true, fmt.Errorf("refreshToken is required when importing raw Kiro IDE token JSON")
	}

	authMethod := normalizeImportedKiroAuthMethod(fields)
	region := strings.TrimSpace(importStringField(fields, "region"))
	if region == "" {
		region = DefaultKiroRegion
	}

	email := strings.TrimSpace(importStringField(fields, "email"))
	if email == "" {
		email = ExtractEmailFromJWT(accessToken)
	}

	provider := strings.TrimSpace(importStringField(fields, "provider"))
	if provider == "" {
		provider = "imported"
	}

	tokenData := &KiroTokenData{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ProfileArn:   strings.TrimSpace(importStringField(fields, "profileArn", "profile_arn")),
		ExpiresAt:    strings.TrimSpace(importStringField(fields, "expiresAt", "expires_at")),
		AuthMethod:   authMethod,
		Provider:     provider,
		ClientID:     strings.TrimSpace(importStringField(fields, "clientId", "client_id")),
		ClientSecret: strings.TrimSpace(importStringField(fields, "clientSecret", "client_secret")),
		ClientIDHash: strings.TrimSpace(importStringField(fields, "clientIdHash", "client_id_hash")),
		Email:        email,
		StartURL:     strings.TrimSpace(importStringField(fields, "startUrl", "start_url")),
		Region:       region,
	}
	if err := prepareImportedKiroTokenData(tokenData); err != nil {
		return nil, true, err
	}
	return tokenData, true, nil
}

func normalizeImportedKiroAuthMethod(fields map[string]any) string {
	authMethod := strings.ToLower(strings.TrimSpace(importStringField(fields, "authMethod", "auth_method")))
	authMethod = strings.ReplaceAll(authMethod, "_", "-")
	switch authMethod {
	case "idc", "builder-id", "social", "google", "github", "imported":
		return authMethod
	case "builderid", "aws":
		return "builder-id"
	}

	if importStringField(fields, "clientIdHash", "client_id_hash") != "" ||
		importStringField(fields, "startUrl", "start_url") != "" {
		return "idc"
	}
	return "imported"
}

func prepareImportedKiroTokenData(tokenData *KiroTokenData) error {
	if tokenData == nil || tokenData.AuthMethod != "idc" {
		return nil
	}
	if tokenData.ClientIDHash != "" && (tokenData.ClientID == "" || tokenData.ClientSecret == "") {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("failed to load Kiro IDE device registration: %w", err)
		}
		if err := loadDeviceRegistration(homeDir, tokenData.ClientIDHash, tokenData); err != nil {
			return fmt.Errorf("failed to load Kiro IDE device registration for clientIdHash %q: %w", tokenData.ClientIDHash, err)
		}
	}
	if tokenData.ClientID == "" || tokenData.ClientSecret == "" {
		return fmt.Errorf("Kiro IDC token import requires clientId/clientSecret or a readable device registration file")
	}
	return nil
}

func importStringField(fields map[string]any, names ...string) string {
	for _, name := range names {
		raw, ok := fields[name]
		if !ok || raw == nil {
			continue
		}
		switch value := raw.(type) {
		case string:
			return value
		case fmt.Stringer:
			return value.String()
		default:
			return fmt.Sprint(value)
		}
	}
	return ""
}
