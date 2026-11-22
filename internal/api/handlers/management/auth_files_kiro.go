package management

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	log "github.com/sirupsen/logrus"
)

// ImportKiroCredential handles importing a Kiro token JSON (from kiro-cli ~/.aws/sso/cache/kiro-auth-token.json or payload)
func (h *Handler) ImportKiroCredential(c *gin.Context) {
	if h == nil || h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config unavailable"})
		return
	}
	if h.cfg.AuthDir == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "auth directory not configured"})
		return
	}

	var data []byte
	var err error

	// Check if uploaded as multipart file
	fileHeader, errFile := c.FormFile("file")
	if errFile == nil && fileHeader != nil {
		f, errOpen := fileHeader.Open()
		if errOpen != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("failed to read uploaded file: %v", errOpen)})
			return
		}
		defer f.Close()
		data, err = io.ReadAll(f)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("failed to read uploaded file: %v", err)})
			return
		}
	} else if c.Request.Body != nil {
		// Read raw JSON body or JSON field
		bodyBytes, errRead := io.ReadAll(c.Request.Body)
		if errRead != nil {
			c.JSON(clienterror.HTTPStatusFromErrorOr(errRead, http.StatusBadRequest), gin.H{"error": "invalid request body"})
			return
		}
		bodyBytes = []byte(strings.TrimSpace(string(bodyBytes)))
		if len(bodyBytes) > 0 {
			var bodyMap map[string]any
			if errUnmarshal := json.Unmarshal(bodyBytes, &bodyMap); errUnmarshal == nil {
				// Check if JSON has a nested "json" or "token" field with stringified JSON
				if rawStr, ok := bodyMap["json"].(string); ok && strings.TrimSpace(rawStr) != "" {
					data = []byte(strings.TrimSpace(rawStr))
				} else if rawStr, ok := bodyMap["token"].(string); ok && strings.TrimSpace(rawStr) != "" {
					data = []byte(strings.TrimSpace(rawStr))
				} else {
					data = bodyBytes
				}
			} else {
				data = bodyBytes
			}
		}
	}

	if len(data) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "token JSON is required (either as raw body, form field 'json', or file upload)"})
		return
	}

	var bundle kiro.KiroTokenBundle
	if errParse := json.Unmarshal(data, &bundle); errParse != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid JSON payload: %v", errParse)})
		return
	}

	// Validate essential token fields
	if strings.TrimSpace(bundle.AccessToken) == "" && strings.TrimSpace(bundle.RefreshToken) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid kiro token: accessToken or refreshToken is required"})
		return
	}

	// Allow overriding email or provider from form query if missing
	if bundle.Email == "" {
		if emailQuery := strings.TrimSpace(c.Query("email")); emailQuery != "" {
			bundle.Email = emailQuery
		} else if emailPost := strings.TrimSpace(c.PostForm("email")); emailPost != "" {
			bundle.Email = emailPost
		}
	}

	record, errRecord := kiro.CreateAuthRecord(&bundle)
	if errRecord != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("failed to create kiro auth record: %v", errRecord)})
		return
	}

	// Save record to auth directory
	ctx := context.Background()
	if reqCtx := c.Request.Context(); reqCtx != nil {
		ctx = reqCtx
	}

	savedPath, errSave := h.saveTokenRecord(ctx, record)
	if errSave != nil {
		log.Errorf("failed to save kiro auth record: %v", errSave)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "save_failed", "message": errSave.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":    "ok",
		"provider":  "kiro",
		"auth-file": savedPath,
		"id":        record.ID,
		"label":     record.Label,
		"email":     bundle.Email,
	})
}
