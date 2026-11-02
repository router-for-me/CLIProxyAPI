package management

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

var errCredentialPriorityConflict = errors.New("credential priority changed")

// PatchCredentialPriority conditionally updates one file credential's priority.
// The precondition runs inside Manager.Modify's per-credential mutation gate,
// so another writer cannot race a controller's ownership check and update.
// Null removes the value; zero is an explicit value rather than a delete sentinel.
func (h *Handler) PatchCredentialPriority(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}
	var request struct {
		Name     string          `json:"name"`
		Priority json.RawMessage `json:"priority"`
		Expected json.RawMessage `json:"expected_priority"`
	}
	if err := c.ShouldBindJSON(&request); err != nil || strings.TrimSpace(request.Name) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name, priority and expected_priority are required"})
		return
	}
	desired, errDesired := decodeCredentialPriority(request.Priority)
	expected, errExpected := decodeCredentialPriority(request.Expected)
	if errDesired != nil || errExpected != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "priority and expected_priority must be safe integers or null"})
		return
	}
	auth, _ := h.lookupAuthFile(strings.TrimSpace(request.Name), "")
	if auth == nil || auth.AuthSourceKind() == coreauth.AuthSourceConfig {
		c.JSON(http.StatusNotFound, gin.H{"error": "auth file not found"})
		return
	}
	if coreauth.IsPluginVirtualAuth(auth) {
		c.JSON(http.StatusConflict, gin.H{"error": errPluginVirtualAuth.Error()})
		return
	}
	updated, committed, errModify := h.authManager.ModifyPersisted(c.Request.Context(), auth.ID, auth.RegistrationEpoch, func(current *coreauth.Auth) error {
		actual := credentialPriority(current)
		if !sameCredentialPriority(actual, expected) {
			return errCredentialPriorityConflict
		}
		if current.Metadata == nil {
			current.Metadata = make(map[string]any)
		}
		if desired == nil {
			delete(current.Metadata, "priority")
		} else {
			current.Metadata["priority"] = *desired
		}
		syncAuthFilePriorityAttribute(current)
		return nil
	})
	if errors.Is(errModify, errCredentialPriorityConflict) {
		c.JSON(http.StatusConflict, gin.H{"error": "credential priority changed; refresh before retrying"})
		return
	}
	if errModify != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to persist credential priority"})
		return
	}
	if !committed || updated == nil {
		c.JSON(http.StatusConflict, gin.H{"error": "credential changed during update; refresh before retrying"})
		return
	}
	if h.postAuthPersistHook != nil {
		if errHook := h.postAuthPersistHook(c.Request.Context(), updated); errHook != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to synchronize credential priority"})
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "priority": credentialPriority(updated)})
}

func decodeCredentialPriority(raw json.RawMessage) (*int, error) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	var value int
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil || int64(value) > 9007199254740991 || int64(value) < -9007199254740991 {
		return nil, errors.New("invalid priority")
	}
	return &value, nil
}

func credentialPriority(auth *coreauth.Auth) *int {
	if auth == nil {
		return nil
	}
	if raw := strings.TrimSpace(auth.Attributes["priority"]); raw != "" {
		if value, err := strconv.Atoi(raw); err == nil {
			return &value
		}
	}
	if value, ok := authFileIntValue(auth.Metadata["priority"]); ok {
		return &value
	}
	return nil
}

func sameCredentialPriority(actual, expected *int) bool {
	return actual == nil && expected == nil || actual != nil && expected != nil && *actual == *expected
}
