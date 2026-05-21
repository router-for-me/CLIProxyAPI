package management

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	kiroauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/kiro"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

const maxKiroSSOTokenImportBytes = 2 << 20

// ImportKiroSSOToken imports an already acquired Kiro SSO token into the auth store.
func (h *Handler) ImportKiroSSOToken(c *gin.Context) {
	if h == nil || h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config unavailable"})
		return
	}
	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}

	payload, err := decodeKiroSSOImportPayload(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json", "message": err.Error()})
		return
	}

	name := firstKiroImportString(c.Query("name"), payload.Name, payload.FileName)
	if name != "" {
		if isUnsafeAuthFileName(name) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid name"})
			return
		}
		if !strings.HasSuffix(strings.ToLower(name), ".json") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "name must end with .json"})
			return
		}
	}

	tokenData := kiroauth.TokenDataFromMetadata(payload.Token)
	if tokenData == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "token is required"})
		return
	}

	shouldRefresh := payload.Refresh || strings.TrimSpace(tokenData.AccessToken) == ""
	if shouldRefresh {
		if strings.TrimSpace(tokenData.RefreshToken) == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "refresh_token is required when access_token is empty or refresh=true"})
			return
		}
		refreshed, errRefresh := kiroauth.NewKiroAuth(h.cfg).Refresh(c.Request.Context(), tokenData)
		if errRefresh != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "refresh_failed", "message": "failed to refresh Kiro token"})
			return
		}
		tokenData = refreshed
	}

	if strings.TrimSpace(tokenData.AccessToken) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "access_token is required"})
		return
	}
	if strings.TrimSpace(tokenData.ProfileARN) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "profile_arn is required"})
		return
	}

	now := time.Now().UTC()
	if name == "" {
		name = kiroauth.CredentialFileName(tokenData)
	}
	label := firstKiroImportString(payload.Label, tokenData.Email, tokenData.ProfileARN, "Kiro")
	metadata := kiroauth.MetadataFromTokenData(tokenData)
	metadata["last_import"] = now.Format(time.RFC3339)
	if payload.Note != "" {
		metadata["note"] = payload.Note
	}
	if payload.Priority != nil {
		metadata["priority"] = *payload.Priority
	}
	if payload.Prefix != "" {
		metadata["prefix"] = payload.Prefix
	}

	status := coreauth.StatusActive
	if payload.Disabled {
		status = coreauth.StatusDisabled
	}
	record := &coreauth.Auth{
		ID:        name,
		Provider:  kiroauth.Provider,
		FileName:  name,
		Label:     label,
		Prefix:    payload.Prefix,
		Disabled:  payload.Disabled,
		Status:    status,
		CreatedAt: now,
		UpdatedAt: now,
		Storage: &kiroauth.TokenStorage{
			Data:     *tokenData,
			Metadata: metadata,
		},
		Metadata: metadata,
		Attributes: map[string]string{
			"auth_kind":   "oauth",
			"profile_arn": tokenData.ProfileARN,
			"region":      tokenData.APIRegion,
		},
		NextRefreshAfter: nextKiroImportRefreshAfter(tokenData.ExpiresAt, now),
	}
	if payload.Note != "" {
		record.Attributes["note"] = payload.Note
	}
	if payload.Priority != nil {
		record.Attributes["priority"] = fmt.Sprintf("%d", *payload.Priority)
	}

	ctx := context.Background()
	if reqCtx := c.Request.Context(); reqCtx != nil {
		ctx = reqCtx
	}
	ctx = PopulateAuthContext(ctx, c)
	savedPath, errSave := h.saveTokenRecord(ctx, record)
	if errSave != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "save_failed", "message": errSave.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":    "ok",
		"provider":  kiroauth.Provider,
		"auth_id":   record.ID,
		"name":      record.FileName,
		"auth-file": savedPath,
		"label":     record.Label,
		"disabled":  record.Disabled,
	})
}

type kiroSSOImportPayload struct {
	Token    map[string]any
	Name     string
	FileName string
	Label    string
	Prefix   string
	Note     string
	Priority *int
	Disabled bool
	Refresh  bool
}

func decodeKiroSSOImportPayload(reader io.Reader) (*kiroSSOImportPayload, error) {
	if reader == nil {
		return nil, fmt.Errorf("request body is empty")
	}
	limited := io.LimitReader(reader, maxKiroSSOTokenImportBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read request body: %w", err)
	}
	if len(body) == 0 {
		return nil, fmt.Errorf("request body is empty")
	}
	if len(body) > maxKiroSSOTokenImportBytes {
		return nil, fmt.Errorf("request body too large")
	}

	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var raw map[string]any
	if err = decoder.Decode(&raw); err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("token is required")
	}

	payload := &kiroSSOImportPayload{
		Name:     firstKiroImportString(mapString(raw, "name")),
		FileName: firstKiroImportString(mapString(raw, "file_name"), mapString(raw, "fileName")),
		Label:    firstKiroImportString(mapString(raw, "label")),
		Prefix:   firstKiroImportString(mapString(raw, "prefix")),
		Note:     firstKiroImportString(mapString(raw, "note")),
		Disabled: mapBool(raw, "disabled"),
		Refresh:  mapBool(raw, "refresh"),
	}
	if priority, ok := mapInt(raw, "priority"); ok {
		payload.Priority = &priority
	}

	if token, ok := raw["token"].(map[string]any); ok {
		payload.Token = token
	} else {
		payload.Token = raw
	}
	return payload, nil
}

func firstKiroImportString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func mapString(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := m[key].(string); ok {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

func mapBool(m map[string]any, key string) bool {
	value, ok := m[key]
	if !ok {
		return false
	}
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "1", "true", "yes", "on":
			return true
		}
	}
	return false
}

func mapInt(m map[string]any, key string) (int, bool) {
	value, ok := m[key]
	if !ok {
		return 0, false
	}
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	case json.Number:
		n, err := typed.Int64()
		return int(n), err == nil
	case string:
		var n int
		_, err := fmt.Sscanf(strings.TrimSpace(typed), "%d", &n)
		return n, err == nil
	default:
		return 0, false
	}
}

func nextKiroImportRefreshAfter(expiry string, fallback time.Time) time.Time {
	for _, layout := range []string{time.RFC3339, time.RFC3339Nano} {
		if ts, err := time.Parse(layout, strings.TrimSpace(expiry)); err == nil {
			return ts.Add(-20 * time.Minute)
		}
	}
	return fallback.Add(40 * time.Minute)
}
