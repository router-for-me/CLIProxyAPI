package management

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/codex"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

type codexCLITokens struct {
	IDToken      string `json:"id_token"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	AccountID    string `json:"account_id,omitempty"`
}

type codexCLIOAuthFile struct {
	Tokens     codexCLITokens `json:"tokens"`
	AuthMethod string         `json:"auth_method,omitempty"`
}

func findAuthByNameOrID(manager *coreauth.Manager, name string) *coreauth.Auth {
	if manager == nil {
		return nil
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	if auth, ok := manager.GetByID(name); ok {
		return auth
	}
	auths := manager.List()
	for _, auth := range auths {
		if auth == nil {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(auth.FileName), name) || strings.EqualFold(strings.TrimSpace(auth.ID), name) {
			return auth
		}
	}
	return nil
}

func sanitizeDownloadName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "auth.json"
	}
	base := filepath.Base(name)
	base = strings.TrimSuffix(base, filepath.Ext(base))
	base = strings.TrimSpace(base)
	if base == "" || base == "." || base == ".." {
		return "auth.json"
	}
	return fmt.Sprintf("codex-cli-%s-auth.json", base)
}

// DownloadCodexCLIOAuthFile exports a Codex OAuth auth file in Codex CLI compatible auth.json format.
func (h *Handler) DownloadCodexCLIOAuthFile(c *gin.Context) {
	if h == nil || h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}

	name := strings.TrimSpace(c.Query("name"))
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}

	auth := findAuthByNameOrID(h.authManager, name)
	if auth == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "auth file not found"})
		return
	}
	if !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "auth file is not codex provider"})
		return
	}

	idToken := metadataString(auth.Metadata, "id_token")
	accessToken := metadataString(auth.Metadata, "access_token", "accessToken")
	refreshToken := metadataString(auth.Metadata, "refresh_token", "refreshToken")
	if idToken == "" || accessToken == "" || refreshToken == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "codex oauth token fields are incomplete"})
		return
	}

	accountID := metadataString(auth.Metadata, "account_id", "chatgpt_account_id", "chatgptAccountId")
	if accountID == "" {
		if claims, err := codex.ParseJWTToken(idToken); err == nil && claims != nil {
			accountID = strings.TrimSpace(claims.GetAccountID())
		}
	}

	payload := codexCLIOAuthFile{
		Tokens: codexCLITokens{
			IDToken:      idToken,
			AccessToken:  accessToken,
			RefreshToken: refreshToken,
			AccountID:    accountID,
		},
		AuthMethod: "chatgpt",
	}

	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to marshal codex cli auth payload"})
		return
	}
	raw = append(raw, '\n')

	fileName := sanitizeDownloadName(auth.FileName)
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", fileName))
	c.Data(http.StatusOK, "application/json", raw)
}
