package helps

import (
	"testing"

	"github.com/MoeForge/moego/regexp"
	"github.com/google/uuid"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestDerivedSessionProviderMappings(t *testing.T) {
	t.Parallel()

	metadata := map[string]any{cliproxyexecutor.DerivedSessionIDMetadataKey: "ctx:v1:test-root"}
	codexID := DerivedSessionUUID("codex", metadata)
	xaiID := DerivedSessionUUID("xai", metadata)
	if _, errParse := uuid.Parse(codexID); errParse != nil {
		t.Fatalf("Codex mapping %q is not a UUID: %v", codexID, errParse)
	}
	if _, errParse := uuid.Parse(xaiID); errParse != nil {
		t.Fatalf("xAI mapping %q is not a UUID: %v", xaiID, errParse)
	}
	if codexID == xaiID {
		t.Fatalf("provider namespaces produced the same UUID: %q", codexID)
	}
	if repeated := DerivedSessionUUID("codex", metadata); repeated != codexID {
		t.Fatalf("Codex mapping is not stable: first=%q repeated=%q", codexID, repeated)
	}

	antigravityID := DerivedAntigravitySessionID(metadata)
	if matched := regexp.MustCompile(`^-[0-9]+$`).MatchString(antigravityID); !matched {
		t.Fatalf("Antigravity mapping = %q, want negative decimal", antigravityID)
	}
	if repeated := DerivedAntigravitySessionID(metadata); repeated != antigravityID {
		t.Fatalf("Antigravity mapping is not stable: first=%q repeated=%q", antigravityID, repeated)
	}
}

func TestProviderSessionUUIDPrefersExecutionSession(t *testing.T) {
	t.Parallel()

	first := map[string]any{
		cliproxyexecutor.ExecutionSessionMetadataKey: "connection-1",
		cliproxyexecutor.DerivedSessionIDMetadataKey: "ctx:v1:first-root",
	}
	second := map[string]any{
		cliproxyexecutor.ExecutionSessionMetadataKey: "connection-1",
		cliproxyexecutor.DerivedSessionIDMetadataKey: "ctx:v1:second-root",
	}
	firstID := ProviderSessionUUID("codex", first)
	secondID := ProviderSessionUUID("codex", second)
	if firstID == "" || firstID != secondID {
		t.Fatalf("execution session did not stabilize provider UUID: first=%q second=%q", firstID, secondID)
	}
	if firstID == DerivedSessionUUID("codex", first) {
		t.Fatalf("provider UUID did not prefer execution session: %q", firstID)
	}
}

func TestDerivedSessionProviderMappingsRequireIdentity(t *testing.T) {
	t.Parallel()

	if got := DerivedSessionUUID("codex", nil); got != "" {
		t.Fatalf("DerivedSessionUUID() = %q, want empty", got)
	}
	if got := DerivedAntigravitySessionID(nil); got != "" {
		t.Fatalf("DerivedAntigravitySessionID() = %q, want empty", got)
	}
	if got := AntigravitySessionIDForAuth("auth-a", nil); got != "" {
		t.Fatalf("AntigravitySessionIDForAuth() = %q, want empty", got)
	}
}

func TestAntigravitySessionIDForAuth(t *testing.T) {
	t.Parallel()

	negativeDecimal := regexp.MustCompile(`^-[0-9]+$`)

	canonicalMeta := map[string]any{
		cliproxyexecutor.CanonicalSessionIDMetadataKey: "claude:sess-7706",
		cliproxyexecutor.ExecutionSessionMetadataKey:   "exec-1",
		cliproxyexecutor.DerivedSessionIDMetadataKey:   "ctx:v1:derived-1",
	}
	authAFirst := AntigravitySessionIDForAuth("auth-a", canonicalMeta)
	if !negativeDecimal.MatchString(authAFirst) {
		t.Fatalf("AntigravitySessionIDForAuth(auth-a) = %q, want negative decimal", authAFirst)
	}
	if repeated := AntigravitySessionIDForAuth("  auth-a  ", canonicalMeta); repeated != authAFirst {
		t.Fatalf("AntigravitySessionIDForAuth not stable across whitespace-trimmed authID: first=%q repeated=%q", authAFirst, repeated)
	}

	authB := AntigravitySessionIDForAuth("auth-b", canonicalMeta)
	if !negativeDecimal.MatchString(authB) {
		t.Fatalf("AntigravitySessionIDForAuth(auth-b) = %q, want negative decimal", authB)
	}
	if authB == authAFirst {
		t.Fatalf("different credentials produced identical Antigravity session ID: %q", authB)
	}

	// CanonicalSessionID must outrank ExecutionSession and DerivedSession even when placed in a later metadata map.
	optsMeta := map[string]any{
		cliproxyexecutor.DerivedSessionIDMetadataKey: "ctx:v1:derived-1",
	}
	reqMeta := map[string]any{
		cliproxyexecutor.CanonicalSessionIDMetadataKey: "claude:sess-7706",
	}
	if got := AntigravitySessionIDForAuth("auth-a", optsMeta, reqMeta); got != authAFirst {
		t.Fatalf("canonical session in req.Metadata did not outrank derived session in opts.Metadata: got=%q want=%q", got, authAFirst)
	}

	execMeta := map[string]any{
		cliproxyexecutor.ExecutionSessionMetadataKey: "exec-1",
		cliproxyexecutor.DerivedSessionIDMetadataKey: "ctx:v1:derived-1",
	}
	execID := AntigravitySessionIDForAuth("auth-a", execMeta)
	derivedID := AntigravitySessionIDForAuth("auth-a", optsMeta)
	if execID == "" || derivedID == "" || execID == authAFirst || execID == derivedID {
		t.Fatalf("session kinds did not produce distinct non-empty IDs: canonical=%q exec=%q derived=%q", authAFirst, execID, derivedID)
	}
}
