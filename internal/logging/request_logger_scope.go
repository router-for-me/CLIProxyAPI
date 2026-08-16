package logging

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"unicode"
)

type requestLogScopeState struct {
	runtimeMu sync.RWMutex
	runtime   requestLogRuntimeSettings
	namesMu   sync.RWMutex
	names     map[string]string
	noLogMu   sync.RWMutex
	noLog     map[string]struct{}
}

type requestLogRuntimeSettings struct {
	enabled           bool
	homeEnabled       bool
	errorLogsMaxFiles int
}

func newRequestLogScopeState(settings requestLogRuntimeSettings) *requestLogScopeState {
	return &requestLogScopeState{
		runtime: settings,
		names:   make(map[string]string),
		noLog:   make(map[string]struct{}),
	}
}

func (l *FileRequestLogger) ensureRequestLogScope() *requestLogScopeState {
	if l == nil {
		return nil
	}
	l.scopeOnce.Do(func() {
		if l.scope == nil {
			l.scope = newRequestLogScopeState(requestLogRuntimeSettings{})
		}
	})
	return l.scope
}

func (l *FileRequestLogger) runtimeSettingsSnapshot() requestLogRuntimeSettings {
	if l == nil {
		return requestLogRuntimeSettings{}
	}
	return l.ensureRequestLogScope().runtimeSettingsSnapshot()
}

func (s *requestLogScopeState) runtimeSettingsSnapshot() requestLogRuntimeSettings {
	if s == nil {
		return requestLogRuntimeSettings{}
	}
	s.runtimeMu.RLock()
	settings := s.runtime
	s.runtimeMu.RUnlock()
	return settings
}

func (s *requestLogScopeState) setEnabled(enabled bool) {
	if s == nil {
		return
	}
	s.runtimeMu.Lock()
	s.runtime.enabled = enabled
	s.runtimeMu.Unlock()
}

func (s *requestLogScopeState) setHomeEnabled(enabled bool) {
	if s == nil {
		return
	}
	s.runtimeMu.Lock()
	s.runtime.homeEnabled = enabled
	s.runtimeMu.Unlock()
}

func (s *requestLogScopeState) setErrorLogsMaxFiles(maxFiles int) {
	if s == nil {
		return
	}
	s.runtimeMu.Lock()
	s.runtime.errorLogsMaxFiles = maxFiles
	s.runtimeMu.Unlock()
}

// SetAPIKeyNames replaces the display-name mapping used for per-key log directories.
func (l *FileRequestLogger) SetAPIKeyNames(keys, names []string) {
	if l == nil {
		return
	}
	next := make(map[string]string)
	for index, key := range keys {
		if index >= len(names) {
			break
		}
		name := SanitizeAPIKeyName(names[index])
		if strings.TrimSpace(key) != "" && name != "" {
			next[key] = name
		}
	}
	scope := l.ensureRequestLogScope()
	scope.namesMu.Lock()
	scope.names = next
	scope.namesMu.Unlock()
}

// SetNoLogAPIKeys replaces the set of API keys whose requests should skip logging.
func (l *FileRequestLogger) SetNoLogAPIKeys(keys []string) {
	if l == nil {
		return
	}
	next := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if trimmed := strings.TrimSpace(key); trimmed != "" {
			next[trimmed] = struct{}{}
		}
	}
	scope := l.ensureRequestLogScope()
	scope.noLogMu.Lock()
	scope.noLog = next
	scope.noLogMu.Unlock()
}

// ShouldSkipLog reports whether the given API key should bypass request logging.
func (l *FileRequestLogger) ShouldSkipLog(apiKey string) bool {
	apiKey = strings.TrimSpace(apiKey)
	if l == nil || apiKey == "" {
		return false
	}
	scope := l.ensureRequestLogScope()
	scope.noLogMu.RLock()
	_, found := scope.noLog[apiKey]
	scope.noLogMu.RUnlock()
	return found
}

// ForAPIKey returns a request logger scoped to a safe per-key directory.
func (l *FileRequestLogger) ForAPIKey(apiKey string) RequestLogger {
	if l == nil {
		return l
	}
	scope := l.ensureRequestLogScope()
	directory := ""
	scope.namesMu.RLock()
	directory = scope.names[apiKey]
	scope.namesMu.RUnlock()
	if directory == "" {
		directory = APIKeyLogDirectory(apiKey)
	}
	return &FileRequestLogger{
		logsDir: filepath.Join(l.logsDir, "keys", directory),
		scope:   scope,
	}
}

// ForKeyLabel scopes logs by a stable non-secret label such as a plugin key ID.
func (l *FileRequestLogger) ForKeyLabel(label string) RequestLogger {
	if l == nil {
		return l
	}
	directory := SanitizeAPIKeyName(label)
	if directory == "" {
		directory = "unauthenticated"
	}
	return &FileRequestLogger{
		logsDir: filepath.Join(l.logsDir, "keys", directory),
		scope:   l.ensureRequestLogScope(),
	}
}

// SanitizeAPIKeyName returns a safe configured directory label.
func SanitizeAPIKeyName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	var builder strings.Builder
	for _, r := range name {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), r == '-', r == '_':
			builder.WriteRune(r)
		case unicode.IsSpace(r):
			builder.WriteByte('-')
		default:
			builder.WriteByte('-')
		}
	}
	result := strings.Trim(builder.String(), "-_")
	for strings.Contains(result, "--") {
		result = strings.ReplaceAll(result, "--", "-")
	}
	runes := []rune(result)
	if len(runes) > 48 {
		result = string(runes[:48])
	}
	return result
}

// APIKeyLogDirectory returns a stable directory without exposing a raw credential.
func APIKeyLogDirectory(apiKey string) string {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return "unauthenticated"
	}
	if strings.HasPrefix(apiKey, "cpa_") {
		remainder := strings.TrimPrefix(apiKey, "cpa_")
		alias, secret, found := strings.Cut(remainder, "_")
		if found && len(secret) >= 16 && isSafeAPIKeyAlias(alias) {
			return alias
		}
	}
	digest := sha256.Sum256([]byte(apiKey))
	return fmt.Sprintf("key-%x", digest[:6])
}

func isSafeAPIKeyAlias(alias string) bool {
	if alias == "" || len(alias) > 48 {
		return false
	}
	for _, r := range alias {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	return true
}
