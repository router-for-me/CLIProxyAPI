package registry

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"

	log "github.com/sirupsen/logrus"
	"golang.org/x/net/http/httpguts"
)

//go:embed models/codex_fingerprint_profile.json
var embeddedCodexFingerprintProfileJSON []byte

// CodexFingerprintHeaders names the application identity headers used by the
// official Codex Responses HTTP and websocket transports.
type CodexFingerprintHeaders struct {
	InstallationID  string `json:"installation_id"`
	TurnState       string `json:"turn_state"`
	TurnMetadata    string `json:"turn_metadata"`
	ParentThreadID  string `json:"parent_thread_id"`
	WindowID        string `json:"window_id"`
	Subagent        string `json:"subagent"`
	TimingMetrics   string `json:"timing_metrics"`
	ClientRequestID string `json:"client_request_id"`
	SessionID       string `json:"session_id"`
	ThreadID        string `json:"thread_id"`
}

// CodexFingerprintMetadataKeys names the Codex-owned fields inside canonical
// turn metadata.
type CodexFingerprintMetadataKeys struct {
	InstallationID      string `json:"installation_id"`
	SessionID           string `json:"session_id"`
	ThreadID            string `json:"thread_id"`
	TurnID              string `json:"turn_id"`
	WindowID            string `json:"window_id"`
	RequestKind         string `json:"request_kind"`
	TurnStartedAtUnixMS string `json:"turn_started_at_unix_ms"`
	ParentThreadID      string `json:"parent_thread_id"`
	ParentTurnID        string `json:"parent_turn_id"`
	SubagentKind        string `json:"subagent_kind"`
}

// CodexFingerprintProfile is a validated snapshot of the official Codex
// application request contract.
type CodexFingerprintProfile struct {
	SchemaVersion     int                          `json:"schema_version"`
	SourceRevision    string                       `json:"source_revision"`
	Version           string                       `json:"version"`
	Originator        string                       `json:"originator"`
	UserAgentTemplate string                       `json:"user_agent_template"`
	WebsocketBeta     string                       `json:"websocket_beta"`
	Headers           CodexFingerprintHeaders      `json:"headers"`
	MetadataKeys      CodexFingerprintMetadataKeys `json:"metadata_keys"`
}

// UserAgent expands the profile's coherent originator and release version.
func (profile CodexFingerprintProfile) UserAgent() string {
	value := strings.ReplaceAll(profile.UserAgentTemplate, "{originator}", profile.Originator)
	return strings.ReplaceAll(value, "{version}", profile.Version)
}

type codexFingerprintProfileStore struct {
	mu       sync.RWMutex
	profile  CodexFingerprintProfile
	revision uint64
}

var codexFingerprintCatalogStore = &codexFingerprintProfileStore{}

func init() {
	var profile CodexFingerprintProfile
	if err := json.Unmarshal(embeddedCodexFingerprintProfileJSON, &profile); err != nil {
		log.Warnf("registry: failed to decode embedded Codex fingerprint profile: %v", err)
		return
	}
	if err := validateCodexFingerprintProfile(profile); err != nil {
		log.Warnf("registry: embedded Codex fingerprint profile is invalid: %v", err)
		return
	}
	codexFingerprintCatalogStore = newCodexFingerprintProfileStore(profile)
}

func newCodexFingerprintProfileStore(profile CodexFingerprintProfile) *codexFingerprintProfileStore {
	return &codexFingerprintProfileStore{profile: profile, revision: 1}
}

// GetCodexFingerprintProfile returns the current immutable profile snapshot.
func GetCodexFingerprintProfile() CodexFingerprintProfile {
	profile, _ := GetCodexFingerprintProfileSnapshot()
	return profile
}

// GetCodexFingerprintProfileSnapshot returns the current profile and revision.
func GetCodexFingerprintProfileSnapshot() (CodexFingerprintProfile, uint64) {
	return codexFingerprintCatalogStore.snapshot()
}

func (store *codexFingerprintProfileStore) snapshot() (CodexFingerprintProfile, uint64) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	return store.profile, store.revision
}

func loadCodexFingerprintProfileFromBytes(data []byte, source string) (bool, error) {
	var profile CodexFingerprintProfile
	if err := json.Unmarshal(data, &profile); err != nil {
		return false, fmt.Errorf("decode Codex fingerprint profile from %s: %w", source, err)
	}
	return codexFingerprintCatalogStore.update(profile)
}

func (store *codexFingerprintProfileStore) update(candidate CodexFingerprintProfile) (bool, error) {
	if err := validateCodexFingerprintProfile(candidate); err != nil {
		return false, err
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if store.revision > 0 {
		comparison, errCompare := compareCodexReleaseVersions(candidate.Version, store.profile.Version)
		if errCompare != nil {
			return false, errCompare
		}
		if comparison < 0 {
			return false, fmt.Errorf("Codex fingerprint version downgrade %s -> %s rejected", store.profile.Version, candidate.Version)
		}
	}

	currentJSON, _ := json.Marshal(store.profile)
	candidateJSON, _ := json.Marshal(candidate)
	if bytes.Equal(currentJSON, candidateJSON) {
		return false, nil
	}
	store.profile = candidate
	store.revision++
	return true, nil
}

func validateCodexFingerprintProfile(profile CodexFingerprintProfile) error {
	if profile.SchemaVersion != 1 {
		return fmt.Errorf("Codex fingerprint schema version %d is unsupported", profile.SchemaVersion)
	}
	if strings.TrimSpace(profile.SourceRevision) == "" {
		return fmt.Errorf("Codex fingerprint source revision is empty")
	}
	if _, err := parseCodexReleaseVersion(profile.Version); err != nil {
		return err
	}
	if strings.TrimSpace(profile.Originator) == "" || !httpguts.ValidHeaderFieldValue(profile.Originator) {
		return fmt.Errorf("Codex fingerprint originator is invalid")
	}
	if !strings.Contains(profile.UserAgentTemplate, "{originator}") || !strings.Contains(profile.UserAgentTemplate, "{version}") {
		return fmt.Errorf("Codex fingerprint User-Agent template must contain {originator} and {version}")
	}
	if userAgent := profile.UserAgent(); strings.TrimSpace(userAgent) == "" || !httpguts.ValidHeaderFieldValue(userAgent) {
		return fmt.Errorf("Codex fingerprint User-Agent is invalid")
	}
	if !strings.HasPrefix(strings.TrimSpace(profile.WebsocketBeta), "responses_websockets=") {
		return fmt.Errorf("Codex fingerprint websocket beta is invalid")
	}

	headers := []string{
		profile.Headers.InstallationID,
		profile.Headers.TurnState,
		profile.Headers.TurnMetadata,
		profile.Headers.ParentThreadID,
		profile.Headers.WindowID,
		profile.Headers.Subagent,
		profile.Headers.TimingMetrics,
		profile.Headers.ClientRequestID,
		profile.Headers.SessionID,
		profile.Headers.ThreadID,
	}
	seenHeaders := make(map[string]struct{}, len(headers))
	for _, header := range headers {
		header = strings.TrimSpace(header)
		if !httpguts.ValidHeaderFieldName(header) {
			return fmt.Errorf("Codex fingerprint header name %q is invalid", header)
		}
		normalized := strings.ToLower(header)
		if _, exists := seenHeaders[normalized]; exists {
			return fmt.Errorf("Codex fingerprint header name %q is duplicated", header)
		}
		seenHeaders[normalized] = struct{}{}
	}

	metadataKeys := []string{
		profile.MetadataKeys.InstallationID,
		profile.MetadataKeys.SessionID,
		profile.MetadataKeys.ThreadID,
		profile.MetadataKeys.TurnID,
		profile.MetadataKeys.WindowID,
		profile.MetadataKeys.RequestKind,
		profile.MetadataKeys.TurnStartedAtUnixMS,
		profile.MetadataKeys.ParentThreadID,
		profile.MetadataKeys.ParentTurnID,
		profile.MetadataKeys.SubagentKind,
	}
	seenMetadata := make(map[string]struct{}, len(metadataKeys))
	for _, key := range metadataKeys {
		key = strings.TrimSpace(key)
		if key == "" {
			return fmt.Errorf("Codex fingerprint metadata key is empty")
		}
		if _, exists := seenMetadata[key]; exists {
			return fmt.Errorf("Codex fingerprint metadata key %q is duplicated", key)
		}
		seenMetadata[key] = struct{}{}
	}
	return nil
}

func compareCodexReleaseVersions(left, right string) (int, error) {
	leftParts, errLeft := parseCodexReleaseVersion(left)
	if errLeft != nil {
		return 0, errLeft
	}
	rightParts, errRight := parseCodexReleaseVersion(right)
	if errRight != nil {
		return 0, errRight
	}
	for i := range leftParts {
		switch {
		case leftParts[i] < rightParts[i]:
			return -1, nil
		case leftParts[i] > rightParts[i]:
			return 1, nil
		}
	}
	return 0, nil
}

func parseCodexReleaseVersion(version string) ([3]int64, error) {
	var parsed [3]int64
	original := strings.TrimSpace(version)
	version = strings.TrimPrefix(original, "v")
	if separator := strings.IndexAny(version, "-+"); separator >= 0 {
		version = version[:separator]
	}
	parts := strings.Split(version, ".")
	if len(parts) != len(parsed) {
		return parsed, fmt.Errorf("Codex fingerprint version %q is invalid", original)
	}
	for i, part := range parts {
		if part == "" {
			return parsed, fmt.Errorf("Codex fingerprint version %q is invalid", original)
		}
		value, errParse := strconv.ParseInt(part, 10, 64)
		if errParse != nil || value < 0 {
			return parsed, fmt.Errorf("Codex fingerprint version %q is invalid", original)
		}
		parsed[i] = value
	}
	return parsed, nil
}
