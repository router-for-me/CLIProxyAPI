// Package codebuddy contains the small credential client needed to call the
// CodeBuddy desktop extension backend.
//
// CodeBuddy stores its session in a JSON .info file rather than exposing a
// normal API key.  The access token must be accompanied by the account and
// tenant headers below, and an expired token is refreshed through the same
// backend before the request is sent.
package codebuddy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// AuthType is the value used by OpenAI compatibility entries to opt into
	// CodeBuddy session-file authentication.
	AuthType = "codebuddy"

	// DefaultBackendBaseURL is the CodeBuddy OpenAI-compatible API base URL.
	DefaultBackendBaseURL = "https://copilot.tencent.com/v2"
	// DefaultDomain is sent when the session does not specify a domain.
	DefaultDomain = "www.codebuddy.cn"
	// UserAgent identifies native CLIProxyAPI traffic to the CodeBuddy backend.
	UserAgent = "codebuddy2openai/2.0"

	// AuthDirEnv allows callers to override the platform-specific session path.
	AuthDirEnv = "CODEBUDDY_AUTH_DIR"
)

// DefaultModels mirrors the model catalog exposed by codebuddy2api.  A
// configured OpenAI compatibility entry may override it with its own models.
var DefaultModels = []string{
	"glm-5.2",
	"glm-5.1",
	"glm-5v-turbo",
	"kimi-k2.7",
	"kimi-k2.6",
	"kimi-k2.5",
	"deepseek-v4-pro",
	"deepseek-v4-flash",
	"minimax-m3-pay",
	"hy3-preview-agent",
	"auto",
}

// ErrAuthFileNotFound indicates that CodeBuddy is not logged in (or its
// session directory does not exist).  It is intentionally not returned by
// FindAuthFile so callers can treat an absent desktop login as no provider.
var ErrAuthFileNotFound = errors.New("codebuddy auth file not found")

type fileState struct {
	modTime time.Time
	size    int64
}

// Manager reads and refreshes one CodeBuddy .info file.  Manager methods are
// safe for concurrent requests; a single refresh is serialized per file.
type Manager struct {
	path       string
	refreshURL string

	mu      sync.Mutex
	session map[string]any
	state   fileState
}

// NewCredentialManager creates a manager using CodeBuddy's public refresh
// endpoint derived from DefaultBackendBaseURL.
func NewCredentialManager(path string) (*Manager, error) {
	return NewCredentialManagerWithRefreshURL(path, RefreshURLForBaseURL(DefaultBackendBaseURL))
}

// NewCredentialManagerWithRefreshURL is primarily useful for tests and for
// deployments using a CodeBuddy-compatible gateway.
func NewCredentialManagerWithRefreshURL(path, refreshURL string) (*Manager, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, ErrAuthFileNotFound
	}
	refreshURL = strings.TrimSpace(refreshURL)
	if refreshURL == "" {
		refreshURL = RefreshURLForBaseURL(DefaultBackendBaseURL)
	}
	return &Manager{path: path, refreshURL: refreshURL}, nil
}

// Path returns the source .info path.
func (m *Manager) Path() string {
	if m == nil {
		return ""
	}
	return m.path
}

// GetHeaders returns a fresh set of headers for a backend request and refreshes
// the access token when it is within one minute of expiry.
func (m *Manager) GetHeaders(ctx context.Context, client *http.Client) (http.Header, error) {
	if m == nil {
		return nil, ErrAuthFileNotFound
	}
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.loadLocked(); err != nil {
		return nil, err
	}
	if needsRefresh(m.session) {
		if err := m.refreshLocked(ctx, client); err != nil {
			return nil, err
		}
	}
	return headersFromSession(m.session), nil
}

// Refresh forces a refresh of the current session and writes the new auth
// object back to the original .info file.
func (m *Manager) Refresh(ctx context.Context, client *http.Client) error {
	if m == nil {
		return ErrAuthFileNotFound
	}
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.loadLocked(); err != nil {
		return err
	}
	return m.refreshLocked(ctx, client)
}

// Summary contains non-secret account information suitable for logs or model
// registration.  Access and refresh tokens are never included.
type Summary struct {
	UID            string
	Nickname       string
	EnterpriseName string
	EnterpriseID   string
	TokenExpiresAt int64
	TokenExpired   bool
}

// Summary returns account metadata without exposing token values.
func (m *Manager) Summary() (Summary, error) {
	if m == nil {
		return Summary{}, ErrAuthFileNotFound
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.loadLocked(); err != nil {
		return Summary{}, err
	}
	account := objectValue(m.session["account"])
	auth := objectValue(m.session["auth"])
	expiresAt := integerValue(auth["expiresAt"])
	return Summary{
		UID:            stringValue(account["uid"]),
		Nickname:       stringValue(account["nickname"]),
		EnterpriseName: stringValue(account["enterpriseName"]),
		EnterpriseID:   stringValue(account["enterpriseId"]),
		TokenExpiresAt: expiresAt,
		TokenExpired:   needsRefresh(m.session),
	}, nil
}

func (m *Manager) loadLocked() error {
	info, errStat := os.Stat(m.path)
	if errStat != nil {
		if os.IsNotExist(errStat) {
			return ErrAuthFileNotFound
		}
		return fmt.Errorf("stat CodeBuddy auth file: %w", errStat)
	}
	if info.IsDir() {
		return fmt.Errorf("CodeBuddy auth path is a directory: %s", m.path)
	}
	state := fileState{modTime: info.ModTime(), size: info.Size()}
	if m.session != nil && state == m.state {
		return nil
	}
	raw, errRead := os.ReadFile(m.path)
	if errRead != nil {
		return fmt.Errorf("read CodeBuddy auth file: %w", errRead)
	}
	var session map[string]any
	if errUnmarshal := json.Unmarshal(raw, &session); errUnmarshal != nil {
		return fmt.Errorf("decode CodeBuddy auth file: %w", errUnmarshal)
	}
	if len(session) == 0 {
		return fmt.Errorf("CodeBuddy auth file is empty: %s", m.path)
	}
	m.session = session
	m.state = state
	return nil
}

func (m *Manager) refreshLocked(ctx context.Context, client *http.Client) error {
	if client == nil {
		client = http.DefaultClient
	}
	auth := objectValue(m.session["auth"])
	headers := headersFromSession(m.session)
	headers.Set("X-Refresh-Token", stringValue(auth["refreshToken"]))
	headers.Set("X-Auth-Refresh-Source", "plugin")

	body := strings.NewReader(`{}`)
	req, errRequest := http.NewRequestWithContext(ctx, http.MethodPost, m.refreshURL, body)
	if errRequest != nil {
		return fmt.Errorf("build CodeBuddy token refresh request: %w", errRequest)
	}
	req.Header = headers
	resp, errDo := client.Do(req)
	if errDo != nil {
		return fmt.Errorf("refresh CodeBuddy token: %w", errDo)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, errRead := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if errRead != nil {
		return fmt.Errorf("read CodeBuddy token refresh response: %w", errRead)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("refresh CodeBuddy token: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var envelope map[string]any
	if errUnmarshal := json.Unmarshal(raw, &envelope); errUnmarshal != nil {
		return fmt.Errorf("decode CodeBuddy token refresh response: %w", errUnmarshal)
	}
	if code := integerValue(envelope["code"]); code != 0 {
		return fmt.Errorf("refresh CodeBuddy token failed: %s", stringValue(envelope["msg"]))
	}
	newAuth := objectValue(envelope["data"])
	if len(newAuth) == 0 || stringValue(newAuth["accessToken"]) == "" {
		return fmt.Errorf("refresh CodeBuddy token failed: response did not contain accessToken")
	}
	if stringValue(newAuth["domain"]) == "" {
		newAuth["domain"] = stringValue(auth["domain"])
	}
	nowMillis := time.Now().UnixMilli()
	newAuth["lastRefreshTime"] = nowMillis
	if integerValue(newAuth["expiresAt"]) == 0 {
		if expiresIn := integerValue(newAuth["expiresIn"]); expiresIn > 0 {
			newAuth["expiresAt"] = nowMillis + expiresIn*1000
		}
	}
	if integerValue(newAuth["refreshExpiresAt"]) == 0 {
		if expiresIn := integerValue(newAuth["refreshExpiresIn"]); expiresIn > 0 {
			newAuth["refreshExpiresAt"] = nowMillis + expiresIn*1000
		}
	}
	m.session["auth"] = newAuth
	if errWrite := m.writeLocked(); errWrite != nil {
		return errWrite
	}
	return nil
}

func (m *Manager) writeLocked() error {
	raw, errMarshal := json.MarshalIndent(m.session, "", "  ")
	if errMarshal != nil {
		return fmt.Errorf("encode CodeBuddy auth file: %w", errMarshal)
	}
	tmpPath := m.path + ".tmp"
	if errWrite := os.WriteFile(tmpPath, raw, 0o600); errWrite != nil {
		return fmt.Errorf("write CodeBuddy auth temp file: %w", errWrite)
	}
	if errRename := os.Rename(tmpPath, m.path); errRename != nil {
		// os.Rename cannot replace an existing file on some Windows versions.
		// The fallback is limited to the exact credential file and is only used
		// after the complete replacement contents have been written.
		if runtime.GOOS == "windows" {
			if errRemove := os.Remove(m.path); errRemove == nil || os.IsNotExist(errRemove) {
				if errRetry := os.Rename(tmpPath, m.path); errRetry == nil {
					m.state = fileState{}
					return nil
				}
			}
		}
		_ = os.Remove(tmpPath)
		return fmt.Errorf("replace CodeBuddy auth file: %w", errRename)
	}
	m.state = fileState{}
	return nil
}

func needsRefresh(session map[string]any) bool {
	auth := objectValue(session["auth"])
	if stringValue(auth["accessToken"]) == "" {
		return true
	}
	expiresAt := integerValue(auth["expiresAt"])
	if expiresAt == 0 {
		return stringValue(auth["refreshToken"]) != ""
	}
	return time.Now().UnixMilli() >= expiresAt-60_000
}

func headersFromSession(session map[string]any) http.Header {
	auth := objectValue(session["auth"])
	account := objectValue(session["account"])
	domain := stringValue(auth["domain"])
	if domain == "" {
		domain = DefaultDomain
	}
	h := make(http.Header)
	h.Set("Content-Type", "application/json")
	h.Set("Accept", "application/json")
	h.Set("Authorization", "Bearer "+stringValue(auth["accessToken"]))
	h.Set("X-User-Id", stringValue(account["uid"]))
	h.Set("X-Enterprise-Id", stringValue(account["enterpriseId"]))
	h.Set("X-Tenant-Id", stringValue(account["enterpriseId"]))
	h.Set("X-Domain", domain)
	h.Set("User-Agent", UserAgent)
	return h
}

func objectValue(value any) map[string]any {
	if out, ok := value.(map[string]any); ok && out != nil {
		return out
	}
	return map[string]any{}
}

func stringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case json.Number:
		return typed.String()
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(typed), 'f', -1, 32)
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	default:
		return ""
	}
}

func integerValue(value any) int64 {
	switch typed := value.(type) {
	case int:
		return int64(typed)
	case int64:
		return typed
	case int32:
		return int64(typed)
	case float64:
		return int64(typed)
	case json.Number:
		if parsed, errParse := typed.Int64(); errParse == nil {
			return parsed
		}
	case string:
		if parsed, errParse := strconv.ParseInt(strings.TrimSpace(typed), 10, 64); errParse == nil {
			return parsed
		}
	}
	return 0
}

// FindAuthFile returns the configured .info file, or the first sorted .info
// file in CodeBuddy's platform-specific session directory.
func FindAuthFile(authDir, authFile string) (string, error) {
	if path := expandPath(authFile); path != "" {
		if info, errStat := os.Stat(path); errStat == nil && !info.IsDir() {
			return path, nil
		} else if errStat != nil && !os.IsNotExist(errStat) {
			return "", errStat
		}
		return "", nil
	}
	dir := expandPath(authDir)
	if dir == "" {
		dir = expandPath(os.Getenv(AuthDirEnv))
	}
	if dir == "" {
		dir = defaultAuthDir()
	}
	entries, errReadDir := os.ReadDir(dir)
	if errReadDir != nil {
		if os.IsNotExist(errReadDir) {
			return "", nil
		}
		return "", errReadDir
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".info") {
			continue
		}
		paths = append(paths, filepath.Join(dir, entry.Name()))
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		return "", nil
	}
	return paths[0], nil
}

func defaultAuthDir() string {
	home, errHome := os.UserHomeDir()
	if errHome != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	switch runtime.GOOS {
	case "windows":
		local := strings.TrimSpace(os.Getenv("LOCALAPPDATA"))
		if local == "" {
			local = filepath.Join(home, "AppData", "Local")
		}
		return filepath.Join(local, "CodeBuddyExtension", "Data", "Public", "auth")
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "CodeBuddyExtension", "Data", "Public", "auth")
	default:
		dataHome := strings.TrimSpace(os.Getenv("XDG_DATA_HOME"))
		if dataHome == "" {
			dataHome = filepath.Join(home, ".local", "share")
		}
		return filepath.Join(dataHome, "CodeBuddyExtension", "Data", "Public", "auth")
	}
}

// RefreshURLForBaseURL derives the plugin refresh endpoint from a configured
// CodeBuddy-compatible base URL.
func RefreshURLForBaseURL(baseURL string) string {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		baseURL = DefaultBackendBaseURL
	}
	u, errParse := url.Parse(baseURL)
	if errParse != nil || u.Scheme == "" || u.Host == "" {
		return strings.TrimRight(DefaultBackendBaseURL, "/") + "/plugin/auth/token/refresh"
	}
	path := strings.TrimRight(u.Path, "/")
	if path == "" || path == "/" {
		path = "/v2"
	}
	u.Path = path + "/plugin/auth/token/refresh"
	u.RawQuery = ""
	u.Fragment = ""
	return strings.TrimRight(u.String(), "/")
}

func expandPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	return filepath.Clean(os.ExpandEnv(path))
}
