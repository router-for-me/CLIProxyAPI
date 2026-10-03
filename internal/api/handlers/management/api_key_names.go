package management

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/redisqueue"
)

// Metadata never changes authentication. Fingerprints are SHA-256 of the full key.
// Empty entries are tombstones: old browser imports must not resurrect cleared names.
func (h *Handler) readAPIKeyNamesLocked() (map[string]string, error) {
	if strings.TrimSpace(h.configFilePath) == "" {
		return nil, errors.New("key name persistence requires a configuration file")
	}
	names := map[string]string{}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(h.configFilePath), "api-key-names.json"))
	if errors.Is(err, os.ErrNotExist) {
		return names, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(data, &names); err != nil || names == nil {
		return nil, errors.New("invalid key name metadata")
	}
	return names, nil
}

func (h *Handler) GetAPIKeyNames(c *gin.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()
	names, err := h.readAPIKeyNamesLocked()
	if err != nil {
		c.JSON(500, gin.H{"error": "key_names_read_failed"})
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(200, gin.H{"names": names})
}

func (h *Handler) PatchAPIKeyNames(c *gin.Context) {
	var request struct {
		Names        map[string]string `json:"names"`
		OnlyIfAbsent bool              `json:"only_if_absent"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1024*1024)
	if c.ShouldBindJSON(&request) != nil || request.Names == nil || len(request.Names) > 10000 {
		c.JSON(400, gin.H{"error": "invalid_key_names"})
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for fingerprint, value := range request.Names {
		digest, err := hex.DecodeString(fingerprint)
		if err != nil || len(digest) != 32 || fingerprint != strings.ToLower(fingerprint) || utf8.RuneCountInString(value) > 128 {
			c.JSON(400, gin.H{"error": "invalid_key_name"})
			return
		}
		for _, char := range value {
			if unicode.IsControl(char) || unicode.Is(unicode.Cf, char) {
				c.JSON(400, gin.H{"error": "invalid_key_name"})
				return
			}
		}
		if h.cfg == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "configuration_unavailable"})
			return
		}
		for _, key := range h.cfg.APIKeys {
			if key != "" && strings.Contains(value, key) {
				c.JSON(400, gin.H{"error": "name_contains_key"})
				return
			}
		}
	}
	names, err := h.readAPIKeyNamesLocked()
	if err != nil {
		c.JSON(500, gin.H{"error": "key_names_read_failed"})
		return
	}
	for key, name := range request.Names {
		if _, exists := names[key]; !exists || !request.OnlyIfAbsent {
			names[key] = strings.TrimSpace(name)
		}
	}
	if len(names) > 10000 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "too_many_key_names"})
		return
	}
	data, err := json.Marshal(names)
	if err != nil {
		c.JSON(500, gin.H{"error": "key_names_write_failed"})
		return
	}
	dir := filepath.Dir(h.configFilePath)
	file, err := os.CreateTemp(dir, ".key-names-*.json")
	if err != nil {
		c.JSON(500, gin.H{"error": "key_names_write_failed"})
		return
	}
	tmp := file.Name()
	defer os.Remove(tmp)
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tmp, filepath.Join(dir, "api-key-names.json"))
	}
	if err != nil {
		c.JSON(500, gin.H{"error": "key_names_write_failed"})
		return
	}
	redisqueue.NotifyUsageRefresh()
	c.Header("Cache-Control", "no-store")
	c.JSON(200, gin.H{"names": names})
}
