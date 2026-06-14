//go:build darwin

package cursorcomposer

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

type macCursorAPISettings struct {
	BackendBaseURL     string `json:"backendBaseURL"`
	LocalAgentEndpoint string `json:"localAgentEndpoint"`
	ClientVersion      string `json:"clientVersion"`
}

var (
	macSettingsOnce sync.Once
	macSettings     macCursorAPISettings
	macSettingsOK   bool
)

func loadMacCursorAPISettings() (macCursorAPISettings, bool) {
	macSettingsOnce.Do(func() {
		for _, domain := range []string{
			"ai.standardagents.apiforcursor",
			"ai.standardagents.cursorapi",
		} {
			if settings, ok := readMacCursorAPISettings(domain); ok {
				macSettings = settings
				macSettingsOK = true
				return
			}
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return
		}
		for _, name := range []string{
			"ai.standardagents.apiforcursor.plist",
			"ai.standardagents.cursorapi.plist",
		} {
			path := filepath.Join(home, "Library", "Preferences", name)
			if settings, ok := readMacCursorAPISettingsPlist(path); ok {
				macSettings = settings
				macSettingsOK = true
				return
			}
		}
	})
	return macSettings, macSettingsOK
}

func readMacCursorAPISettings(domain string) (macCursorAPISettings, bool) {
	cmd := exec.Command("defaults", "read", domain, "CursorAPI.settings.v1")
	out, err := cmd.Output()
	if err != nil {
		return macCursorAPISettings{}, false
	}
	return decodeMacCursorAPISettings(out)
}

func readMacCursorAPISettingsPlist(path string) (macCursorAPISettings, bool) {
	cmd := exec.Command("plutil", "-extract", "CursorAPI.settings.v1", "raw", "-o", "-", path)
	out, err := cmd.Output()
	if err != nil {
		return macCursorAPISettings{}, false
	}
	return decodeMacCursorAPISettings(out)
}

func decodeMacCursorAPISettings(raw []byte) (macCursorAPISettings, bool) {
	trimmed := bytesTrimSpace(raw)
	if len(trimmed) == 0 {
		return macCursorAPISettings{}, false
	}
	var settings macCursorAPISettings
	if err := json.Unmarshal(trimmed, &settings); err != nil {
		return macCursorAPISettings{}, false
	}
	if strings.TrimSpace(settings.BackendBaseURL) == "" &&
		strings.TrimSpace(settings.LocalAgentEndpoint) == "" &&
		strings.TrimSpace(settings.ClientVersion) == "" {
		return macCursorAPISettings{}, false
	}
	return settings, true
}

func bytesTrimSpace(b []byte) []byte {
	return []byte(strings.TrimSpace(string(b)))
}

func macCursorAPIBackendBase() string {
	settings, ok := loadMacCursorAPISettings()
	if !ok {
		return ""
	}
	return strings.TrimSpace(settings.BackendBaseURL)
}

func macCursorAPIChatEndpoint() string {
	// API for Cursor persists SDK routing in localAgentEndpoint; Composer connect-proto uses DefaultChatEndpoint.
	return ""
}

func macCursorAPIClientVersion() string {
	settings, ok := loadMacCursorAPISettings()
	if !ok {
		return ""
	}
	return strings.TrimSpace(settings.ClientVersion)
}
