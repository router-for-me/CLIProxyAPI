package executor

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/tidwall/gjson"
)

func extractUserID(t *testing.T, payload []byte) string {
	t.Helper()
	return gjson.GetBytes(payload, "metadata.user_id").String()
}

func TestInjectFakeUserID_PreservesAlreadyValidCC(t *testing.T) {
	// Build a valid CC-shape id via the helper; the fast path returns the
	// payload unchanged.
	valid := helps.DeterministicFakeUserID("alice")
	payload := []byte(`{"metadata":{"user_id":"` + valid + `"}}`)

	out := injectFakeUserID(payload, "api-key", false)
	if got := extractUserID(t, out); got != valid {
		t.Fatalf("expected valid id to be preserved, got %q", got)
	}
}

func TestInjectFakeUserID_DerivesDeterministicFromNonCCSeed(t *testing.T) {
	// Capy-shape inbound user_id (matches no CC regex). Two calls with
	// the same seed must produce the same outbound id and the result
	// must be CC-shape.
	in := `{"metadata":{"user_id":"user_3DGta5gzamzAUr57ZYZEtmc6lHX"}}`

	out1 := injectFakeUserID([]byte(in), "api-key-1", false)
	out2 := injectFakeUserID([]byte(in), "api-key-2", false)

	id1 := extractUserID(t, out1)
	id2 := extractUserID(t, out2)
	if id1 != id2 {
		t.Fatalf("expected deterministic output across api-key changes, got\n  %s\n  %s", id1, id2)
	}
	if !helps.IsValidUserID(id1) {
		t.Fatalf("derived id is not CC-shape: %s", id1)
	}
	expected := helps.DeterministicFakeUserID("user_3DGta5gzamzAUr57ZYZEtmc6lHX")
	if id1 != expected {
		t.Fatalf("derived id %q does not match DeterministicFakeUserID seed result %q", id1, expected)
	}
}

func TestInjectFakeUserID_EmptyUserIDFallsBackToCache(t *testing.T) {
	// metadata exists but user_id is empty: with useCache=true we expect
	// the per-API-key cached random id, stable for the same api key.
	payload := []byte(`{"metadata":{"user_id":""}}`)

	out1 := injectFakeUserID(payload, "api-key-cache-1", true)
	out2 := injectFakeUserID(payload, "api-key-cache-1", true)

	id1 := extractUserID(t, out1)
	id2 := extractUserID(t, out2)
	if id1 == "" || !helps.IsValidUserID(id1) {
		t.Fatalf("expected CC-shape id from cache, got %q", id1)
	}
	if id1 != id2 {
		t.Fatalf("expected same cached id within TTL, got %q vs %q", id1, id2)
	}
}

func TestInjectFakeUserID_NoMetadataInjectsFresh(t *testing.T) {
	// payload lacks metadata entirely: a fresh CC-shape id is injected.
	payload := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)

	out := injectFakeUserID(payload, "api-key-fresh", false)
	id := extractUserID(t, out)
	if id == "" || !helps.IsValidUserID(id) {
		t.Fatalf("expected CC-shape id, got %q", id)
	}
}
