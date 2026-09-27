package auth

import (
	"crypto/sha256"
	"encoding/binary"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestModelPoolOffsetStickyPerSession(t *testing.T) {
	manager := &Manager{}
	const key = "auth-1|opencode|auto"
	const size = 8

	opts := cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.CanonicalSessionIDMetadataKey: "claude:sess-123",
	}}

	wantSum := sha256.Sum256([]byte(key + "|" + "claude:sess-123"))
	want := int(binary.BigEndian.Uint32(wantSum[:4]) % uint32(size))

	for i := 0; i < 16; i++ {
		if got := manager.modelPoolOffset(key, size, opts); got != want {
			t.Fatalf("call %d: modelPoolOffset() = %d, want stable %d", i, got, want)
		}
	}
}

func TestModelPoolOffsetFallsBackToRoundRobinWithoutSession(t *testing.T) {
	manager := &Manager{}
	const key = "auth-1|opencode|auto"
	const size = 4

	seen := make(map[int]bool)
	for i := 0; i < size; i++ {
		seen[manager.modelPoolOffset(key, size, cliproxyexecutor.Options{})] = true
	}
	if len(seen) != size {
		t.Fatalf("expected round-robin to visit %d distinct offsets, got %d", size, len(seen))
	}
}

func TestModelPoolOffsetPrefersCanonicalSessionID(t *testing.T) {
	manager := &Manager{}
	const key = "auth-1|opencode|auto"
	const size = 8

	canonicalOpts := cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.CanonicalSessionIDMetadataKey:   "session:canonical",
		cliproxyexecutor.DerivedSessionIDMetadataKey:     "session:derived",
		cliproxyexecutor.ExecutionSessionMetadataKey:     "session:execution",
		cliproxyexecutor.LCPAffinitySessionIDMetadataKey: "session:lcp",
	}}
	first := manager.modelPoolOffset(key, size, canonicalOpts)

	sum := sha256.Sum256([]byte(key + "|" + "session:canonical"))
	if want := int(binary.BigEndian.Uint32(sum[:4]) % uint32(size)); first != want {
		t.Fatalf("modelPoolOffset() = %d, want canonical-derived %d", first, want)
	}

	derivedOnly := cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.DerivedSessionIDMetadataKey: "session:derived",
	}}
	if got := manager.modelPoolOffset(key, size, derivedOnly); got == first {
		t.Logf("derived-only offset matches canonical offset by chance (%d); acceptable hash collision", got)
	}
}
