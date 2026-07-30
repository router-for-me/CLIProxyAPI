package executor

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const codexApplicationWindowTTL = time.Hour

type codexApplicationIdentity struct {
	enabled          bool
	profile          registry.CodexFingerprintProfile
	installationID   string
	sessionID        string
	threadID         string
	turnID           string
	windowID         string
	requestKind      string
	turnStartedAtMS  int64
	parentThreadID   string
	parentTurnID     string
	subagentKind     string
	turnMetadataJSON string
	websocket        bool
}

type codexApplicationWindowEntry struct {
	id        string
	expiresAt time.Time
}

var codexApplicationWindows = struct {
	sync.Mutex
	entries map[string]codexApplicationWindowEntry
}{entries: make(map[string]codexApplicationWindowEntry)}

func applyCodexOfficialApplicationIdentity(
	cfg *config.Config,
	auth *cliproxyauth.Auth,
	requestURL string,
	body []byte,
) ([]byte, codexApplicationIdentity) {
	if !codexOfficialFingerprintScope(cfg, auth, requestURL) || len(body) == 0 {
		return body, codexApplicationIdentity{}
	}

	profile := registry.GetCodexFingerprintProfile()
	authIdentity := strings.TrimSpace(auth.ID)
	turnMetadata := parseCodexTurnMetadata(body, profile)

	sessionSeed := clientMetadataString(body, profile.MetadataKeys.SessionID)
	if sessionSeed == "" {
		sessionSeed = strings.TrimSpace(gjson.GetBytes(body, "prompt_cache_key").String())
	}
	sessionID := stableCodexApplicationUUID(authIdentity, "session", sessionSeed)

	threadSeed := clientMetadataString(body, profile.MetadataKeys.ThreadID)
	if threadSeed == "" {
		threadSeed = sessionSeed
	}
	threadID := stableCodexApplicationUUID(authIdentity, "thread", threadSeed)

	installationSeed := clientMetadataString(body, profile.Headers.InstallationID)
	if installationSeed == "" {
		installationSeed = authIdentity
	}
	installationID := stableCodexApplicationUUID(authIdentity, "installation", installationSeed)

	windowID := clientMetadataString(body, profile.Headers.WindowID)
	if parsed, errParse := uuid.Parse(windowID); errParse != nil || parsed.Version() != 7 {
		windowID = cachedCodexApplicationWindow(authIdentity, sessionID)
	}

	turnID := clientMetadataString(body, profile.MetadataKeys.TurnID)
	if turnID == "" {
		turnID, _ = turnMetadata[profile.MetadataKeys.TurnID].(string)
	}
	if parsed, errParse := uuid.Parse(turnID); errParse != nil || parsed.Version() != 7 {
		turnID = newCodexUUIDv7()
	}

	parentThreadID := clientMetadataString(body, profile.Headers.ParentThreadID)
	if parentThreadID == "" {
		parentThreadID, _ = turnMetadata[profile.MetadataKeys.ParentThreadID].(string)
	}
	parentTurnID := clientMetadataString(body, profile.MetadataKeys.ParentTurnID)
	if parentTurnID == "" {
		parentTurnID, _ = turnMetadata[profile.MetadataKeys.ParentTurnID].(string)
	}
	subagentKind := clientMetadataString(body, profile.Headers.Subagent)
	if subagentKind == "" {
		subagentKind, _ = turnMetadata[profile.MetadataKeys.SubagentKind].(string)
	}

	requestKind := codexApplicationRequestKind(requestURL)
	turnStartedAtMS := time.Now().UnixMilli()
	if existing, ok := turnMetadata[profile.MetadataKeys.TurnStartedAtUnixMS].(float64); ok && existing > 0 {
		turnStartedAtMS = int64(existing)
	}

	turnMetadata[profile.MetadataKeys.InstallationID] = installationID
	turnMetadata[profile.MetadataKeys.SessionID] = sessionID
	turnMetadata[profile.MetadataKeys.ThreadID] = threadID
	turnMetadata[profile.MetadataKeys.TurnID] = turnID
	turnMetadata[profile.MetadataKeys.WindowID] = windowID
	turnMetadata[profile.MetadataKeys.RequestKind] = requestKind
	turnMetadata[profile.MetadataKeys.TurnStartedAtUnixMS] = turnStartedAtMS
	if parentThreadID != "" {
		turnMetadata[profile.MetadataKeys.ParentThreadID] = parentThreadID
	}
	if parentTurnID != "" {
		turnMetadata[profile.MetadataKeys.ParentTurnID] = parentTurnID
	}
	if subagentKind != "" {
		turnMetadata[profile.MetadataKeys.SubagentKind] = subagentKind
	}
	turnMetadataJSONBytes, _ := json.Marshal(turnMetadata)
	turnMetadataJSON := string(turnMetadataJSONBytes)

	body = setCodexClientMetadataString(body, profile.Headers.InstallationID, installationID)
	body = setCodexClientMetadataString(body, profile.MetadataKeys.SessionID, sessionID)
	body = setCodexClientMetadataString(body, profile.MetadataKeys.ThreadID, threadID)
	body = setCodexClientMetadataString(body, profile.MetadataKeys.TurnID, turnID)
	body = setCodexClientMetadataString(body, profile.Headers.WindowID, windowID)
	body = setCodexClientMetadataString(body, profile.Headers.TurnMetadata, turnMetadataJSON)
	if parentThreadID != "" {
		body = setCodexClientMetadataString(body, profile.Headers.ParentThreadID, parentThreadID)
	}
	if parentTurnID != "" {
		body = setCodexClientMetadataString(body, profile.MetadataKeys.ParentTurnID, parentTurnID)
	}
	if subagentKind != "" {
		body = setCodexClientMetadataString(body, profile.Headers.Subagent, subagentKind)
	}

	return body, codexApplicationIdentity{
		enabled:          true,
		profile:          profile,
		installationID:   installationID,
		sessionID:        sessionID,
		threadID:         threadID,
		turnID:           turnID,
		windowID:         windowID,
		requestKind:      requestKind,
		turnStartedAtMS:  turnStartedAtMS,
		parentThreadID:   parentThreadID,
		parentTurnID:     parentTurnID,
		subagentKind:     subagentKind,
		turnMetadataJSON: turnMetadataJSON,
		websocket:        codexApplicationIsWebsocket(requestURL),
	}
}

func applyCodexOfficialApplicationIdentityHeaders(headers http.Header, identity *codexApplicationIdentity) {
	if headers == nil || identity == nil || !identity.enabled {
		return
	}
	profile := identity.profile
	headers.Set("User-Agent", profile.UserAgent())
	headers.Set("Originator", profile.Originator)
	headers.Set("Version", profile.Version)
	if identity.websocket {
		headers.Set("OpenAI-Beta", profile.WebsocketBeta)
	}
	setCodexSessionHeaderCasePreserved(headers, profile.Headers.SessionID, identity.sessionID)
	setHeaderCasePreserved(headers, profile.Headers.ThreadID, identity.threadID)
	if identity.websocket {
		headers.Set(profile.Headers.ClientRequestID, identity.threadID)
	}
	headers.Set(profile.Headers.WindowID, identity.windowID)
	headers.Set(profile.Headers.TurnMetadata, identity.turnMetadataJSON)
	if identity.requestKind == "compaction" {
		headers.Set(profile.Headers.InstallationID, identity.installationID)
	}
	if identity.parentThreadID != "" {
		headers.Set(profile.Headers.ParentThreadID, identity.parentThreadID)
	}
	if identity.subagentKind != "" {
		headers.Set(profile.Headers.Subagent, identity.subagentKind)
	}
}

func codexApplicationIsWebsocket(requestURL string) bool {
	parsed, errParse := url.Parse(strings.TrimSpace(requestURL))
	return errParse == nil && strings.EqualFold(parsed.Scheme, "wss")
}

func codexOfficialFingerprintScope(cfg *config.Config, auth *cliproxyauth.Auth, requestURL string) bool {
	if auth == nil || strings.TrimSpace(auth.ID) == "" {
		return false
	}
	if cfg != nil && cfg.Codex.DisableCodexCloaking {
		return false
	}
	if auth.Attributes != nil {
		if strings.TrimSpace(auth.Attributes["api_key"]) != "" ||
			strings.TrimSpace(auth.Attributes["base_url"]) != "" {
			return false
		}
	}
	parsed, errParse := url.Parse(strings.TrimSpace(requestURL))
	if errParse != nil || !strings.EqualFold(parsed.Hostname(), "chatgpt.com") {
		return false
	}
	path := strings.TrimSuffix(parsed.Path, "/")
	return path == "/backend-api/codex/responses" ||
		path == "/backend-api/codex/responses/compact"
}

func codexApplicationRequestKind(requestURL string) string {
	parsed, _ := url.Parse(strings.TrimSpace(requestURL))
	if strings.HasSuffix(strings.TrimSuffix(parsed.Path, "/"), "/responses/compact") {
		return "compaction"
	}
	return "turn"
}

func stableCodexApplicationUUID(authIdentity, kind, value string) string {
	value = strings.TrimSpace(value)
	if parsed, errParse := uuid.Parse(value); errParse == nil {
		return parsed.String()
	}
	name := strings.Join([]string{"cli-proxy-api", "codex", "application-identity", kind, authIdentity, value}, "\x00")
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(name)).String()
}

func cachedCodexApplicationWindow(authIdentity, sessionID string) string {
	key := authIdentity + "\x00" + sessionID
	now := time.Now()
	codexApplicationWindows.Lock()
	defer codexApplicationWindows.Unlock()
	for existingKey, entry := range codexApplicationWindows.entries {
		if !entry.expiresAt.After(now) {
			delete(codexApplicationWindows.entries, existingKey)
		}
	}
	if entry, ok := codexApplicationWindows.entries[key]; ok && entry.expiresAt.After(now) {
		entry.expiresAt = now.Add(codexApplicationWindowTTL)
		codexApplicationWindows.entries[key] = entry
		return entry.id
	}
	windowID := newCodexUUIDv7()
	codexApplicationWindows.entries[key] = codexApplicationWindowEntry{
		id:        windowID,
		expiresAt: now.Add(codexApplicationWindowTTL),
	}
	return windowID
}

func newCodexUUIDv7() string {
	value, errNew := uuid.NewV7()
	if errNew != nil {
		return uuid.NewString()
	}
	return value.String()
}

func parseCodexTurnMetadata(body []byte, profile registry.CodexFingerprintProfile) map[string]any {
	metadata := make(map[string]any)
	raw := clientMetadataString(body, profile.Headers.TurnMetadata)
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &metadata)
	}
	return metadata
}

func clientMetadataString(body []byte, key string) string {
	return strings.TrimSpace(gjson.GetBytes(body, codexClientMetadataPath(key)).String())
}

func setCodexClientMetadataString(body []byte, key, value string) []byte {
	updated, errSet := sjson.SetBytes(body, codexClientMetadataPath(key), value)
	if errSet != nil {
		return body
	}
	return updated
}

func codexClientMetadataPath(key string) string {
	escaped := strings.ReplaceAll(strings.TrimSpace(key), "\\", "\\\\")
	escaped = strings.ReplaceAll(escaped, ".", "\\.")
	return "client_metadata." + escaped
}
