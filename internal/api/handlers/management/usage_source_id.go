package management

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
)

const usageExportSourceIDFilename = ".usage-export-source-id"

func (h *Handler) usageExportSourceID() string {
	if h == nil {
		return ""
	}

	baseDir := h.usageExportSourceBaseDir()
	if baseDir == "" {
		return ""
	}

	sourceIDPath := filepath.Join(baseDir, usageExportSourceIDFilename)
	if raw, err := os.ReadFile(sourceIDPath); err == nil {
		if sourceID := strings.TrimSpace(string(raw)); sourceID != "" {
			return sourceID
		}
	}

	sourceID := uuid.NewString()
	if err := os.MkdirAll(baseDir, 0o755); err == nil {
		tmpPath := sourceIDPath + ".tmp"
		if errWrite := os.WriteFile(tmpPath, []byte(sourceID+"\n"), 0o644); errWrite == nil {
			if errRename := os.Rename(tmpPath, sourceIDPath); errRename == nil {
				return sourceID
			}
			_ = os.Remove(tmpPath)
		}
	}

	return fallbackUsageExportSourceID(baseDir, h.configFilePath)
}

func (h *Handler) usageExportSourceBaseDir() string {
	if h == nil {
		return ""
	}
	if h.cfg != nil {
		if authDir, err := util.ResolveAuthDir(strings.TrimSpace(h.cfg.AuthDir)); err == nil && authDir != "" {
			return authDir
		}
	}
	configPath := strings.TrimSpace(h.configFilePath)
	if configPath != "" {
		return filepath.Dir(configPath)
	}
	return ""
}

func fallbackUsageExportSourceID(baseDir, configPath string) string {
	hostname, _ := os.Hostname()
	sum := sha256.Sum256([]byte(strings.TrimSpace(hostname) + "|" + filepath.Clean(baseDir) + "|" + filepath.Clean(strings.TrimSpace(configPath))))
	return "fallback-" + hex.EncodeToString(sum[:16])
}
