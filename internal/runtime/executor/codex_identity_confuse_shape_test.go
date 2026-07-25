package executor

import (
	"testing"

	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

// realCodexTurnMetadata is a verbatim x-codex-turn-metadata header from a real client
// (Codex Desktop 0.144.0-alpha.4), captured 2026-07-17. It is the ground truth for every
// assertion below. Note what the real client does:
//   - installation_id lives ONLY here; the client sends no client_metadata at all
//   - session_id == thread_id, and window_id is that same id + ":0"
//   - session/thread/turn ids are UUID version 7; installation_id is version 4
//   - turn_started_at_unix_ms is minted off the same clock as turn_id, 17 ms apart
const realCodexTurnMetadata = `{"installation_id":"6ae1cc61-7e89-4a8c-8da2-982ddde8d432","session_id":"019f5a8a-cc68-7b92-89aa-17602176af46","thread_id":"019f5a8a-cc68-7b92-89aa-17602176af46","turn_id":"019f5aaa-1ca3-71f0-b3e4-de5b34ec347a","window_id":"019f5a8a-cc68-7b92-89aa-17602176af46:0","request_kind":"turn","thread_source":"user","sandbox":"none","turn_started_at_unix_ms":1783932525748,"workspace_kind":"projectless"}`

const (
	realCodexSessionID      = "019f5a8a-cc68-7b92-89aa-17602176af46"
	realCodexTurnID         = "019f5aaa-1ca3-71f0-b3e4-de5b34ec347a"
	realCodexInstallationID = "6ae1cc61-7e89-4a8c-8da2-982ddde8d432"
)

func confuseStateFor(authID string) *codexIdentityConfuseState {
	return &codexIdentityConfuseState{
		enabled:                true,
		authID:                 authID,
		originalPromptCacheKey: realCodexSessionID,
		promptCacheKey:         codexIdentityConfuseUUIDLike(authID, "prompt-cache", realCodexSessionID),
	}
}

func millisOf(t *testing.T, id string) int64 {
	t.Helper()
	parsed, err := uuid.Parse(id)
	if err != nil {
		t.Fatalf("parse %q: %v", id, err)
	}
	return codexUUIDMillis(parsed)
}

// installation_id is stable for the life of a Codex install, so it survives every other
// per-account remap: leaving it raw lets the backend group every credential a single
// client is routed across. The existing remap only covers
// client_metadata.x-codex-installation-id, which a real client never populates.
func TestIdentityConfuse_RemapsTurnMetadataInstallationID(t *testing.T) {
	got := gjson.Get(applyCodexTurnMetadataIdentityConfuse(realCodexTurnMetadata, confuseStateFor("auth-a")), "installation_id").String()

	if got == realCodexInstallationID {
		t.Fatalf("raw installation_id reached the wire: %q", got)
	}
	if got == "" {
		t.Fatalf("installation_id was dropped; a real client always sends one")
	}
}

// The decisive property: two credentials serving one client must not share an
// installation_id, or the id is a correlation anchor and identity-confuse is defeated.
func TestIdentityConfuse_InstallationIDDiffersPerAuth(t *testing.T) {
	a := gjson.Get(applyCodexTurnMetadataIdentityConfuse(realCodexTurnMetadata, confuseStateFor("auth-a")), "installation_id").String()
	b := gjson.Get(applyCodexTurnMetadataIdentityConfuse(realCodexTurnMetadata, confuseStateFor("auth-b")), "installation_id").String()

	if a == b {
		t.Fatalf("two auths emitted the same installation_id %q", a)
	}
}

// A blank authID would hash identically for every credential: confused-looking, but still
// a perfect anchor. Leave the id alone instead.
func TestIdentityConfuse_BlankAuthIDLeavesInstallationIDAlone(t *testing.T) {
	state := &codexIdentityConfuseState{enabled: true, originalPromptCacheKey: realCodexSessionID, promptCacheKey: "confused"}
	if got := gjson.Get(applyCodexTurnMetadataIdentityConfuse(realCodexTurnMetadata, state), "installation_id").String(); got != realCodexInstallationID {
		t.Fatalf("installation_id = %q with a blank authID, want it untouched", got)
	}
}

// uuid.NewSHA1 always yields version 5, which no real client emits. A confused id that
// stops the original from leaking while stamping "5" in the version nibble of every
// request just trades one tell for another.
func TestIdentityConfuseUUIDLike_MirrorsOriginalVersion(t *testing.T) {
	if got := realCodexSessionID[14]; got != '7' {
		t.Fatalf("fixture drift: captured session id is version %c, want 7", got)
	}
	if got := realCodexInstallationID[14]; got != '4' {
		t.Fatalf("fixture drift: captured installation id is version %c, want 4", got)
	}

	for name, tc := range map[string]struct{ original, kind string }{
		"session (v7)":      {realCodexSessionID, "prompt-cache"},
		"turn (v7)":         {realCodexTurnID, "turn"},
		"installation (v4)": {realCodexInstallationID, "installation"},
	} {
		got := codexIdentityConfuseUUIDLike("auth-a", tc.kind, tc.original)
		if len(got) != 36 {
			t.Errorf("%s: got %q, want a 36-char hyphenated UUID", name, got)
		}
		if got[14] != tc.original[14] {
			t.Errorf("%s: got version %c, want the original's %c", name, got[14], tc.original[14])
		}
		if parsed, err := uuid.Parse(got); err != nil {
			t.Errorf("%s: %v", name, err)
		} else if parsed.Variant() != uuid.RFC4122 {
			t.Errorf("%s: variant = %v, want RFC4122", name, parsed.Variant())
		}
	}
}

// A version-7 id's leading 48 bits are a real millisecond timestamp, so a confused one has
// to decode to a plausible instant. Reading a version-5 hash as a v7 lands in the year
// 6335 — visible to anyone who parses the id.
func TestIdentityConfuseUUIDLike_V7TimestampStaysPlausible(t *testing.T) {
	original := millisOf(t, realCodexSessionID)
	got := millisOf(t, codexIdentityConfuseUUIDLike("auth-a", "prompt-cache", realCodexSessionID))

	if got > original {
		t.Errorf("confused id is %d ms in the future of the original; a session cannot start after itself", got-original)
	}
	if original-got > codexIdentityConfuseMaxSkewMillis {
		t.Errorf("confused id is %d ms before the original, beyond the %d ms bound", original-got, codexIdentityConfuseMaxSkewMillis)
	}
}

// One shift per auth, applied to every id: a real client mints a turn after the session
// that contains it, and that gap has to survive.
func TestIdentityConfuseUUIDLike_PreservesSessionToTurnGap(t *testing.T) {
	session := millisOf(t, codexIdentityConfuseUUIDLike("auth-a", "prompt-cache", realCodexSessionID))
	turn := millisOf(t, codexIdentityConfuseUUIDLike("auth-a", "turn", realCodexTurnID))

	if turn <= session {
		t.Fatalf("turn (%d) is not after session (%d)", turn, session)
	}
	if want := millisOf(t, realCodexTurnID) - millisOf(t, realCodexSessionID); turn-session != want {
		t.Errorf("session-to-turn gap = %d ms, want the original %d ms", turn-session, want)
	}
}

// The shift must differ per auth, or every credential reports the identical millisecond
// and the timestamp becomes the anchor the id no longer is.
func TestIdentityConfuseUUIDLike_V7TimestampDiffersPerAuth(t *testing.T) {
	a := millisOf(t, codexIdentityConfuseUUIDLike("auth-a", "prompt-cache", realCodexSessionID))
	b := millisOf(t, codexIdentityConfuseUUIDLike("auth-b", "prompt-cache", realCodexSessionID))

	if a == b {
		t.Fatalf("two auths reported the same session millisecond (%d)", a)
	}
}

// A real id never changes, so the remap has to be deterministic; one that drifted between
// requests would be its own tell.
func TestIdentityConfuseUUIDLike_IsDeterministic(t *testing.T) {
	for _, original := range []string{realCodexSessionID, realCodexInstallationID} {
		first := codexIdentityConfuseUUIDLike("auth-a", "prompt-cache", original)
		if second := codexIdentityConfuseUUIDLike("auth-a", "prompt-cache", original); first != second {
			t.Fatalf("%q remapped to %q then %q", original, first, second)
		}
	}
}

// An original that is not a UUID has no shape to mirror; keep the plain hash rather than
// invent a version the caller never had.
func TestIdentityConfuseUUIDLike_NonUUIDOriginalKeepsHash(t *testing.T) {
	got := codexIdentityConfuseUUIDLike("auth-a", "prompt-cache", "not-a-uuid")
	if want := codexIdentityConfuseUUID("auth-a", "prompt-cache", "not-a-uuid"); got != want {
		t.Fatalf("got %q, want the plain hash %q", got, want)
	}
}

// turn_started_at_unix_ms comes off the same clock as turn_id — 17 ms apart in the
// capture, because a real client mints them together. Shifting the ids while leaving the
// plaintext sibling raw puts a request's own two timestamps ~40 s apart, which no real
// client can produce.
func TestIdentityConfuse_TurnStartedAtTracksTurnID(t *testing.T) {
	realStartedAt := gjson.Get(realCodexTurnMetadata, "turn_started_at_unix_ms").Int()
	realDelta := realStartedAt - millisOf(t, realCodexTurnID)
	if realDelta < 0 || realDelta > 1000 {
		t.Fatalf("fixture drift: captured turn_started_at is %d ms from turn_id", realDelta)
	}

	for _, authID := range []string{"auth-a", "auth-b"} {
		confused := applyCodexTurnMetadataIdentityConfuse(realCodexTurnMetadata, confuseStateFor(authID))
		startedAt := gjson.Get(confused, "turn_started_at_unix_ms").Int()

		if got := startedAt - millisOf(t, gjson.Get(confused, "turn_id").String()); got != realDelta {
			t.Errorf("%s: turn_started_at is %d ms from turn_id, want the captured %d ms", authID, got, realDelta)
		}
		if startedAt >= realStartedAt {
			t.Errorf("%s: turn_started_at %d was not shifted (raw %d)", authID, startedAt, realStartedAt)
		}
	}
}

// The identity graph a real client emits must survive the reshape:
// session_id == thread_id and window_id == that id + ":0".
func TestIdentityConfuse_TurnMetadataGraphStaysCoherent(t *testing.T) {
	state := confuseStateFor("auth-a")
	confused := applyCodexTurnMetadataIdentityConfuse(realCodexTurnMetadata, state)

	sessionID := gjson.Get(confused, "session_id").String()
	if sessionID == realCodexSessionID {
		t.Fatalf("raw session id reached the wire")
	}
	if got := gjson.Get(confused, "thread_id").String(); got != sessionID {
		t.Errorf("thread_id = %q, want it to equal session_id %q", got, sessionID)
	}
	if got, want := gjson.Get(confused, "window_id").String(), sessionID+":0"; got != want {
		t.Errorf("window_id = %q, want %q", got, want)
	}
}
