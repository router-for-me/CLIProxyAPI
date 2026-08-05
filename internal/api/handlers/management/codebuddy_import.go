package management

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codebuddy"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
)

// ImportCodeBuddyAuth imports a WorkBuddy/CodeBuddy desktop session into the
// native codebuddy compatibility entry.  The endpoint accepts either a JSON
// auth-file path or a multipart .info upload, matching the Vertex JSON import
// workflow without exposing session tokens in the response.
func (h *Handler) ImportCodeBuddyAuth(c *gin.Context) {
	if h == nil || h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config unavailable"})
		return
	}

	var (
		authPath   string
		sessionRaw []byte
		uploaded   bool
	)
	if strings.HasPrefix(strings.ToLower(c.GetHeader("Content-Type")), "multipart/form-data") {
		fileHeader, errFile := c.FormFile("file")
		if errFile != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "file required"})
			return
		}
		if !strings.EqualFold(filepath.Ext(fileHeader.Filename), ".info") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "WorkBuddy session file must use the .info extension"})
			return
		}
		file, errOpen := fileHeader.Open()
		if errOpen != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("failed to read file: %v", errOpen)})
			return
		}
		sessionRaw, errOpen = io.ReadAll(file)
		_ = file.Close()
		if errOpen != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("failed to read file: %v", errOpen)})
			return
		}
		authDir, errDir := util.ResolveAuthDir(h.cfg.AuthDir)
		if errDir != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("resolve auth directory: %v", errDir)})
			return
		}
		if errMkdir := os.MkdirAll(authDir, 0o700); errMkdir != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("create auth directory: %v", errMkdir)})
			return
		}
		fileName := "workbuddy-" + filepath.Base(fileHeader.Filename)
		authPath = filepath.Join(authDir, fileName)
		uploaded = true
	} else {
		var body struct {
			AuthFile string `json:"auth-file"`
		}
		if errJSON := c.ShouldBindJSON(&body); errJSON != nil && errJSON != io.EOF {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
			return
		}
		authPath = strings.TrimSpace(body.AuthFile)
		if authPath == "" {
			authPath = strings.TrimSpace(c.Query("auth-file"))
		}
		if authPath == "" {
			authPath = strings.TrimSpace(c.PostForm("auth-file"))
		}
		authPath = expandCodeBuddyImportPath(authPath)
	}

	if authPath == "" || !strings.EqualFold(filepath.Ext(authPath), ".info") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "auth-file must point to a .info session file"})
		return
	}

	if !uploaded {
		var errRead error
		sessionRaw, errRead = os.ReadFile(authPath)
		if errRead != nil {
			status := http.StatusBadRequest
			if !os.IsNotExist(errRead) {
				status = http.StatusInternalServerError
			}
			c.JSON(status, gin.H{"error": fmt.Sprintf("read auth-file: %v", errRead)})
			return
		}
	}

	if errValidate := validateCodeBuddySession(sessionRaw); errValidate != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errValidate.Error()})
		return
	}

	if uploaded {
		if errWrite := writeCodeBuddyImportFile(authPath, sessionRaw); errWrite != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("save auth-file: %v", errWrite)})
			return
		}
	}

	h.mu.Lock()
	entryIndex := -1
	for i := range h.cfg.OpenAICompatibility {
		entry := &h.cfg.OpenAICompatibility[i]
		if strings.EqualFold(strings.TrimSpace(entry.AuthType), codebuddy.AuthType) ||
			strings.EqualFold(strings.TrimSpace(entry.Name), codebuddy.AuthType) ||
			strings.EqualFold(strings.TrimSpace(entry.Name), "workbuddy") {
			entryIndex = i
			break
		}
	}
	if entryIndex == -1 {
		h.cfg.OpenAICompatibility = append(h.cfg.OpenAICompatibility, config.OpenAICompatibility{
			Name:        "WorkBuddy",
			AuthType:    codebuddy.AuthType,
			BaseURL:     codebuddy.DefaultBackendBaseURL,
			Desensitize: true,
		})
		entryIndex = len(h.cfg.OpenAICompatibility) - 1
	}
	entry := &h.cfg.OpenAICompatibility[entryIndex]
	if strings.TrimSpace(entry.Name) == "" || strings.EqualFold(strings.TrimSpace(entry.Name), codebuddy.AuthType) {
		entry.Name = "WorkBuddy"
	}
	entry.AuthType = codebuddy.AuthType
	entry.AuthFile = authPath
	entry.AuthDir = ""
	if strings.TrimSpace(entry.BaseURL) == "" {
		entry.BaseURL = codebuddy.DefaultBackendBaseURL
	}
	h.cfg.SanitizeOpenAICompatibility()
	snapshot, ok := h.saveConfigAndSnapshotLocked(c)
	h.mu.Unlock()
	if !ok {
		if uploaded {
			_ = os.Remove(authPath)
		}
		return
	}
	h.reloadConfigAfterManagementSaveAsync(context.Background(), snapshot)

	manager, _ := codebuddy.NewCredentialManager(authPath)
	summary, _ := manager.Summary()
	c.JSON(http.StatusOK, gin.H{
		"status":           "ok",
		"provider":         "workbuddy",
		"auth-file":        authPath,
		"nickname":         summary.Nickname,
		"enterprise":       summary.EnterpriseName,
		"token-expired":    summary.TokenExpired,
		"token-expires-at": summary.TokenExpiresAt,
	})
}

func validateCodeBuddySession(data []byte) error {
	var session map[string]any
	if err := json.Unmarshal(data, &session); err != nil {
		return fmt.Errorf("invalid WorkBuddy .info JSON: %v", err)
	}
	auth, ok := session["auth"].(map[string]any)
	if !ok {
		return fmt.Errorf("invalid WorkBuddy session: missing auth object")
	}
	accessToken, _ := auth["accessToken"].(string)
	refreshToken, _ := auth["refreshToken"].(string)
	if strings.TrimSpace(accessToken) == "" && strings.TrimSpace(refreshToken) == "" {
		return fmt.Errorf("invalid WorkBuddy session: accessToken/refreshToken missing")
	}
	return nil
}

func expandCodeBuddyImportPath(path string) string {
	path = strings.TrimSpace(os.ExpandEnv(path))
	if strings.HasPrefix(path, "~") {
		if home, errHome := os.UserHomeDir(); errHome == nil {
			path = filepath.Join(home, strings.TrimLeft(path[1:], `/\\`))
		}
	}
	if path == "" {
		return ""
	}
	if abs, errAbs := filepath.Abs(path); errAbs == nil {
		path = abs
	}
	return filepath.Clean(path)
}

func writeCodeBuddyImportFile(path string, data []byte) error {
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}
