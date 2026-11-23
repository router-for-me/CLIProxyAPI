package helps

import (
	"fmt"
	"strconv"
	"testing"

	"github.com/google/uuid"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func assertSignedInt64SessionID(t *testing.T, label, value string) int64 {
	t.Helper()
	parsed, errParse := strconv.ParseInt(value, 10, 64)
	if errParse != nil || strconv.FormatInt(parsed, 10) != value {
		t.Fatalf("%s = %q, want canonical signed int64 decimal (err=%v)", label, value, errParse)
	}
	return parsed
}

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
	assertSignedInt64SessionID(t, "Antigravity mapping", antigravityID)
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

	canonicalMeta := map[string]any{
		cliproxyexecutor.CanonicalSessionIDMetadataKey: "claude:sess-7706",
		cliproxyexecutor.ExecutionSessionMetadataKey:   "exec-1",
		cliproxyexecutor.DerivedSessionIDMetadataKey:   "ctx:v1:derived-1",
	}
	authAFirst := AntigravitySessionIDForAuth("auth-a", canonicalMeta)
	assertSignedInt64SessionID(t, "AntigravitySessionIDForAuth(auth-a)", authAFirst)
	if repeated := AntigravitySessionIDForAuth("  auth-a  ", canonicalMeta); repeated != authAFirst {
		t.Fatalf("AntigravitySessionIDForAuth not stable across whitespace-trimmed authID: first=%q repeated=%q", authAFirst, repeated)
	}

	authB := AntigravitySessionIDForAuth("auth-b", canonicalMeta)
	assertSignedInt64SessionID(t, "AntigravitySessionIDForAuth(auth-b)", authB)
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

func TestScopeAntigravitySessionIDForAuth(t *testing.T) {
	t.Parallel()

	if got := ScopeAntigravitySessionIDForAuth("auth-a", "   "); got != "" {
		t.Fatalf("ScopeAntigravitySessionIDForAuth(empty) = %q, want empty", got)
	}
	if got := ScopeAntigravitySessionIDForAuth("", "  -987654321  "); got != "-987654321" {
		t.Fatalf("ScopeAntigravitySessionIDForAuth(empty authID) = %q, want -987654321", got)
	}

	authA := ScopeAntigravitySessionIDForAuth("auth-a", "-987654321")
	assertSignedInt64SessionID(t, "ScopeAntigravitySessionIDForAuth(auth-a)", authA)
	if authA == "-987654321" {
		t.Fatalf("ScopeAntigravitySessionIDForAuth(auth-a) = %q, want scoped value distinct from raw input", authA)
	}
	if repeated := ScopeAntigravitySessionIDForAuth("  auth-a  ", " -987654321 "); repeated != authA {
		t.Fatalf("ScopeAntigravitySessionIDForAuth not stable across whitespace: first=%q repeated=%q", authA, repeated)
	}

	authB := ScopeAntigravitySessionIDForAuth("auth-b", "-987654321")
	assertSignedInt64SessionID(t, "ScopeAntigravitySessionIDForAuth(auth-b)", authB)
	if authB == "-987654321" || authB == authA {
		t.Fatalf("ScopeAntigravitySessionIDForAuth(auth-b) = %q, want distinct scoped signed int64 decimal (authA=%q)", authB, authA)
	}
}

func TestHashAntigravitySessionIDMatchesFNV1a64AndIncludesBothSigns(t *testing.T) {
	t.Parallel()

	referenceFNV1a64 := func(input []byte) string {
		var h uint64 = 0xcbf29ce484222325
		for _, b := range input {
			h = (h ^ uint64(b)) * 0x100000001b3
		}
		return strconv.FormatInt(int64(h), 10)
	}

	samples := []string{"", "a", "hello", "session-1", "session-2", "cli-proxy-api\x00antigravity\x00canonical-session\x00auth-a\x00sess-1"}
	for _, sample := range samples {
		got := HashAntigravitySessionID([]byte(sample))
		want := referenceFNV1a64([]byte(sample))
		if got != want {
			t.Fatalf("HashAntigravitySessionID(%q) = %q, want %q", sample, got, want)
		}
		assertSignedInt64SessionID(t, fmt.Sprintf("HashAntigravitySessionID(%q)", sample), got)
	}

	var derivedPos, derivedNeg int
	var authPos, authNeg int
	var scopePos, scopeNeg int
	for i := 0; i < 64; i++ {
		sid := fmt.Sprintf("session-%d", i)

		derivedVal := assertSignedInt64SessionID(t, "DerivedAntigravitySessionID", DerivedAntigravitySessionID(map[string]any{
			cliproxyexecutor.DerivedSessionIDMetadataKey: "ctx:v1:" + sid,
		}))
		if derivedVal > 0 {
			derivedPos++
		} else if derivedVal < 0 {
			derivedNeg++
		}

		authVal := assertSignedInt64SessionID(t, "AntigravitySessionIDForAuth", AntigravitySessionIDForAuth("auth-a", map[string]any{
			cliproxyexecutor.CanonicalSessionIDMetadataKey: "claude:" + sid,
		}))
		if authVal > 0 {
			authPos++
		} else if authVal < 0 {
			authNeg++
		}

		scopeVal := assertSignedInt64SessionID(t, "ScopeAntigravitySessionIDForAuth", ScopeAntigravitySessionIDForAuth("auth-a", sid))
		if scopeVal > 0 {
			scopePos++
		} else if scopeVal < 0 {
			scopeNeg++
		}
	}

	if derivedPos == 0 || derivedNeg == 0 {
		t.Fatalf("DerivedAntigravitySessionID produced skewed signs: pos=%d neg=%d", derivedPos, derivedNeg)
	}
	if authPos == 0 || authNeg == 0 {
		t.Fatalf("AntigravitySessionIDForAuth produced skewed signs: pos=%d neg=%d", authPos, authNeg)
	}
	if scopePos == 0 || scopeNeg == 0 {
		t.Fatalf("ScopeAntigravitySessionIDForAuth produced skewed signs: pos=%d neg=%d", scopePos, scopeNeg)
	}
}
