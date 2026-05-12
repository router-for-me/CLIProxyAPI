package helps

import (
	"strings"
	"testing"
)

func TestDeterministicFakeUserID_Stable(t *testing.T) {
	a := DeterministicFakeUserID("user_3DGta5gzamzAUr57ZYZEtmc6lHX")
	b := DeterministicFakeUserID("user_3DGta5gzamzAUr57ZYZEtmc6lHX")
	if a != b {
		t.Fatalf("expected stable output, got\n  a=%s\n  b=%s", a, b)
	}
}

func TestDeterministicFakeUserID_DifferentSeedsDiffer(t *testing.T) {
	a := DeterministicFakeUserID("alice")
	b := DeterministicFakeUserID("bob")
	if a == b {
		t.Fatalf("different seeds must produce different ids: %s", a)
	}
}

func TestDeterministicFakeUserID_PassesIsValidUserID(t *testing.T) {
	for _, seed := range []string{
		"user_3DGta5gzamzAUr57ZYZEtmc6lHX",
		"alice@example.com",
		"x",
		strings.Repeat("z", 256),
	} {
		got := DeterministicFakeUserID(seed)
		if !IsValidUserID(got) {
			t.Errorf("DeterministicFakeUserID(%q) = %q does not match userIDPattern", seed, got)
		}
	}
}

func TestDeterministicFakeUserID_EmptySeedReturnsEmpty(t *testing.T) {
	if got := DeterministicFakeUserID(""); got != "" {
		t.Fatalf("expected empty string for empty seed, got %q", got)
	}
}

func TestDeterministicFakeUserID_AccountAndSessionDiffer(t *testing.T) {
	// Same seed must derive distinct account_uuid and session_uuid to
	// match real Claude Code shape (the official CLI never repeats the
	// account UUID as the session UUID).
	const seed = "user_3DGta5gzamzAUr57ZYZEtmc6lHX"
	got := DeterministicFakeUserID(seed)
	parts := strings.Split(got, "_")
	// "user_<hex>_account_<uuid>_session_<uuid>" → fields after split on "_"
	// are: ["user","<hex>","account","<uuid>","session","<uuid>"]
	if len(parts) < 6 {
		t.Fatalf("unexpected shape: %q", got)
	}
	accountUUID := parts[3]
	sessionUUID := parts[5]
	if accountUUID == sessionUUID {
		t.Fatalf("account_uuid == session_uuid (%q) — must differ", accountUUID)
	}
}

func TestDeterministicFakeUserID_DistinctFromRandomFakeUserID(t *testing.T) {
	// Sanity: deterministic output and a random output for the same seed
	// must NOT collide — the deterministic path is intentionally on a
	// different derivation than crypto/rand.
	det := DeterministicFakeUserID("seed-x")
	rnd := GenerateFakeUserID()
	if det == rnd {
		t.Fatalf("deterministic id collided with a random one: %s", det)
	}
}
