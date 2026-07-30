package registry

import (
	"strings"
	"testing"
)

func TestCodexFingerprintProfileEmbeddedFallback(t *testing.T) {
	profile, revision := GetCodexFingerprintProfileSnapshot()
	if revision == 0 {
		t.Fatal("embedded profile revision = 0, want initialized store")
	}
	if profile.SchemaVersion != 1 {
		t.Fatalf("schema version = %d, want 1", profile.SchemaVersion)
	}
	if profile.Version == "" || profile.Originator == "" || profile.WebsocketBeta == "" {
		t.Fatalf("embedded profile is incomplete: %+v", profile)
	}
	if profile.Headers.InstallationID != "x-codex-installation-id" {
		t.Fatalf("installation header = %q", profile.Headers.InstallationID)
	}
	if profile.Headers.SessionID != "session-id" || profile.Headers.ThreadID != "thread-id" {
		t.Fatalf("session/thread headers = %q/%q", profile.Headers.SessionID, profile.Headers.ThreadID)
	}
	if profile.MetadataKeys.RequestKind != "request_kind" {
		t.Fatalf("request kind metadata key = %q", profile.MetadataKeys.RequestKind)
	}
}

func TestCodexFingerprintProfileSnapshotIsCloned(t *testing.T) {
	first, firstRevision := GetCodexFingerprintProfileSnapshot()
	first.Version = "99.99.99"
	first.Headers.WindowID = "x-mutated-window"

	second, secondRevision := GetCodexFingerprintProfileSnapshot()
	if secondRevision != firstRevision {
		t.Fatalf("revision changed after caller mutation: %d -> %d", firstRevision, secondRevision)
	}
	if second.Version == first.Version || second.Headers.WindowID == first.Headers.WindowID {
		t.Fatalf("caller mutation leaked into store: %+v", second)
	}
}

func TestCodexFingerprintProfileUserAgent(t *testing.T) {
	profile := validTestCodexFingerprintProfile()
	profile.Version = "1.2.3"
	profile.Originator = "codex_cli_rs"

	got := profile.UserAgent()
	if !strings.Contains(got, "codex_cli_rs/1.2.3") {
		t.Fatalf("UserAgent() = %q, want originator/version prefix", got)
	}
	if strings.Contains(got, "{originator}") || strings.Contains(got, "{version}") {
		t.Fatalf("UserAgent() left template variables unresolved: %q", got)
	}
}

func TestCodexFingerprintProfileValidationRejectsInvalidHeader(t *testing.T) {
	profile := validTestCodexFingerprintProfile()
	profile.Headers.WindowID = "x-codex-window-id\ninvalid"
	if err := validateCodexFingerprintProfile(profile); err == nil {
		t.Fatal("validateCodexFingerprintProfile() error = nil, want invalid header error")
	}
}

func TestCodexFingerprintProfileValidationRejectsInvalidTemplate(t *testing.T) {
	profile := validTestCodexFingerprintProfile()
	profile.UserAgentTemplate = "codex_cli_rs/1.2.3"
	if err := validateCodexFingerprintProfile(profile); err == nil {
		t.Fatal("validateCodexFingerprintProfile() error = nil, want missing placeholder error")
	}
}

func TestCodexFingerprintProfileStoreRejectsDowngrade(t *testing.T) {
	store := newCodexFingerprintProfileStore(validTestCodexFingerprintProfile())
	downgrade := validTestCodexFingerprintProfile()
	downgrade.Version = "0.145.0"
	if changed, err := store.update(downgrade); err == nil || changed {
		t.Fatalf("store.update(downgrade) = (%v, %v), want false and error", changed, err)
	}
}

func validTestCodexFingerprintProfile() CodexFingerprintProfile {
	return CodexFingerprintProfile{
		SchemaVersion:     1,
		SourceRevision:    "test-revision",
		Version:           "0.146.0",
		Originator:        "codex_cli_rs",
		UserAgentTemplate: "{originator}/{version} (Mac OS 26.5.2; arm64) Apple_Terminal/470 (codex-tui; {version})",
		WebsocketBeta:     "responses_websockets=2026-02-06",
		Headers: CodexFingerprintHeaders{
			InstallationID:  "x-codex-installation-id",
			TurnState:       "x-codex-turn-state",
			TurnMetadata:    "x-codex-turn-metadata",
			ParentThreadID:  "x-codex-parent-thread-id",
			WindowID:        "x-codex-window-id",
			Subagent:        "x-openai-subagent",
			TimingMetrics:   "x-responsesapi-include-timing-metrics",
			ClientRequestID: "x-client-request-id",
			SessionID:       "session-id",
			ThreadID:        "thread-id",
		},
		MetadataKeys: CodexFingerprintMetadataKeys{
			InstallationID:      "installation_id",
			SessionID:           "session_id",
			ThreadID:            "thread_id",
			TurnID:              "turn_id",
			WindowID:            "window_id",
			RequestKind:         "request_kind",
			TurnStartedAtUnixMS: "turn_started_at_unix_ms",
			ParentThreadID:      "parent_thread_id",
			ParentTurnID:        "parent_turn_id",
			SubagentKind:        "subagent_kind",
		},
	}
}
