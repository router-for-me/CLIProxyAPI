# Session Affinity Stickiness Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Make every session-affinity binding loss explainable from logs, and stop the two confirmed mass-wipe paths (auth re-render, selector swap) from dropping live session bindings.

**Architecture:** All changes live in the in-memory affinity layer: log readability fixes in `SessionAffinitySelector`, return-count + warn logging on invalidation, a cache handoff across selector swaps, and a short-lived pending-migration map on `Manager` that rebinds sessions when a re-rendered equivalent auth registers. No persistent state, no cache-key format change, no upstreamsync changes.

**Tech Stack:** Go 1.26, logrus, stdlib testing. Design doc: `docs/plans/2026-09-11-session-affinity-stickiness-design.md`.

**Repo conventions (from AGENTS.md):** `gofmt -w .` after every Go change; verify compile with `go build -o test-output ./cmd/server && rm test-output`; no `log.Fatal`; logrus structured logging; comments in English; commit messages end with `Co-Authored-By: Claude Code <noreply@anthropic.com>`.

---

### Task 1: Readable session IDs + `key=` hash in affinity logs

**Files:**
- Modify: `sdk/cliproxy/auth/selector.go` (function `truncateSessionID` ~line 771; log lines at ~723, ~733, ~742, ~756)
- Test: `sdk/cliproxy/auth/selector_test.go` (append; create if absent)

**Step 1: Write the failing test**

Append to `sdk/cliproxy/auth/selector_test.go`:

```go
func TestTruncateSessionIDDistinctDerivedSessions(t *testing.T) {
	first := truncateSessionID("derived:aaaaaaaa1234567890abcdef")
	second := truncateSessionID("derived:bbbbbbbb1234567890abcdef")
	if first == second {
		t.Fatalf("distinct derived sessions render identically: %q", first)
	}
	if got := truncateSessionID("derived:short"); got != "short" {
		t.Fatalf("short id = %q, want short", got)
	}
	if got := truncateSessionID("derived:0123456789abcdef0123456789abcdef"); got != "0123456789abcdef..." {
		t.Fatalf("long id = %q, want 16 chars + ellipsis", got)
	}
}

func TestAffinityKeyHashStable(t *testing.T) {
	key := "mixed::derived:abc::glm-5"
	if affinityKeyHash(key) != affinityKeyHash(key) {
		t.Fatal("affinityKeyHash not deterministic")
	}
	if len(affinityKeyHash(key)) != 8 {
		t.Fatalf("affinityKeyHash length = %d, want 8", len(affinityKeyHash(key)))
	}
	if affinityKeyHash(key) == affinityKeyHash("mixed::derived:xyz::glm-5") {
		t.Fatal("different keys produced identical hash")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./sdk/cliproxy/auth/ -run 'TestTruncateSessionIDDistinct|TestAffinityKeyHash' -v`
Expected: FAIL — `affinityKeyHash` undefined, and `truncateSessionID` returns 8-char truncation (so the first assertion fails on distinctness... actually with 8 chars the two derived IDs differ already — the distinctness assertion alone does NOT fail; the 16-char assertion fails: got `0123456789...` (10 chars), want `0123456789abcdef...`). Expected: compile error on `affinityKeyHash` + FAIL on truncation length.

**Step 3: Write minimal implementation**

In `sdk/cliproxy/auth/selector.go`, replace `truncateSessionID` and add the hash helper:

```go
// sessionLogPrefixes are the source prefixes extractSessionIDs prepends to
// session IDs. They carry no discriminating value in logs — every derived
// session starts with "derived:" — so truncateSessionID strips them before
// truncating, otherwise all sessions of one source class log identically.
var sessionLogPrefixes = []string{"derived:", "conv:", "pck:", "session:", "user:", "execution:", "msg:"}

// truncateSessionID shortens a session ID for logging: strip the known source
// prefix, then keep the first 16 characters of the unique part so distinct
// sessions remain distinguishable across log lines.
func truncateSessionID(id string) string {
	for _, prefix := range sessionLogPrefixes {
		if strings.HasPrefix(id, prefix) {
			id = id[len(prefix):]
			break
		}
	}
	if len(id) <= 16 {
		return id
	}
	return id[:16] + "..."
}

// affinityKeyHash returns an 8-hex fingerprint of a full affinity cache key so
// log lines can be grouped per logical session+provider+model binding without
// logging the raw session ID in full.
func affinityKeyHash(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:4])
}
```

Add `"crypto/sha256"` and `"encoding/hex"` to the imports of `selector.go`.

Then add `key=` to the four binding log lines so operators can group them. Compute once at the top of `Pick` after `cacheKey` is built:

```go
keyHash := affinityKeyHash(cacheKey)
```

and append `, key=`+keyHash to the `entry.Infof`/`entry.Debugf` messages for: cache hit (~line 723), cache hit but auth unavailable, reselected (~733), fallback cache hit (~742), cache miss new binding (~754–756).

**Step 4: Run test to verify it passes**

Run: `go test ./sdk/cliproxy/auth/ -run 'TestTruncateSessionIDDistinct|TestAffinityKeyHash' -v`
Expected: PASS

**Step 5: Format, verify compile, commit**

```bash
gofmt -w sdk/cliproxy/auth/
go build -o test-output ./cmd/server && rm test-output
git add sdk/cliproxy/auth/selector.go sdk/cliproxy/auth/selector_test.go
git commit -m "feat(auth): readable session IDs and key fingerprints in affinity logs

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 2: `InvalidateAuth` returns count; invalidation logs

**Files:**
- Modify: `sdk/cliproxy/auth/session_cache.go` (`InvalidateAuth` ~line 339)
- Modify: `sdk/cliproxy/auth/conductor_lifecycle.go` (`invalidateSessionAffinity` ~line 206)
- Modify: `sdk/cliproxy/auth/selector.go` (`SessionAffinitySelector.InvalidateAuth` ~line 799 — signature follows the cache)
- Test: `sdk/cliproxy/auth/session_cache_test.go` (append)

**Step 1: Write the failing test**

Append to `sdk/cliproxy/auth/session_cache_test.go`:

```go
func TestInvalidateAuthReturnsDeletedCount(t *testing.T) {
	c := NewSessionCache(time.Hour)
	defer c.Stop()
	c.Set("mixed::s1::m", "auth-a")
	c.Set("mixed::s2::m", "auth-a")
	c.Set("mixed::s3::m", "auth-b")
	if got := c.InvalidateAuth("auth-a"); got != 2 {
		t.Fatalf("InvalidateAuth = %d, want 2", got)
	}
	if got := c.InvalidateAuth("auth-a"); got != 0 {
		t.Fatalf("second InvalidateAuth = %d, want 0", got)
	}
	if _, ok := c.Get("mixed::s3::m"); !ok {
		t.Fatal("unrelated binding was removed")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./sdk/cliproxy/auth/ -run TestInvalidateAuthReturnsDeletedCount -v`
Expected: FAIL — `c.InvalidateAuth(...)` used as value in a void-call context (compile error: "EvalDiscardStmt" / multiple-value context).

**Step 3: Write minimal implementation**

`session_cache.go` — count deletions:

```go
// InvalidateAuth removes all sessions bound to a specific auth ID and returns
// how many cache keys were deleted. Used when an auth becomes unavailable or
// is removed; the count lets callers report the blast radius.
func (c *SessionCache) InvalidateAuth(authID string) int {
	if authID == "" {
		return 0
	}
	c.mu.Lock()
	deleted := 0
	for sid, entry := range c.entries {
		if entry.authID == authID {
			delete(c.entries, sid)
			deleted++
		}
	}
	c.mu.Unlock()
	return deleted
}
```

`selector.go` — propagate the return:

```go
func (s *SessionAffinitySelector) InvalidateAuth(authID string) int {
	if s == nil || s.cache == nil {
		return 0
	}
	return s.cache.InvalidateAuth(authID)
}
```

`conductor_lifecycle.go` — log the count:

```go
func (m *Manager) invalidateSessionAffinity(authID string) {
	if m == nil || authID == "" {
		return
	}
	if invalidator, ok := m.selector.(interface{ InvalidateAuth(string) int }); ok && invalidator != nil {
		if removed := invalidator.InvalidateAuth(authID); removed > 0 {
			log.Warnf("session-affinity: invalidated %d binding(s) for auth %s", removed, authID)
		}
	}
}
```

If the anonymous-interface assertion no longer compiles elsewhere (grep for `InvalidateAuth(string)` usages under `sdk/cliproxy/`), update those call sites to ignore the returned int with `_ =`.

**Step 4: Run tests to verify they pass**

Run: `go test ./sdk/cliproxy/auth/ -v -count=1`
Expected: PASS (full auth package — catches any missed call site at compile time)

**Step 5: Format, verify compile, commit**

```bash
gofmt -w sdk/cliproxy/auth/
go build -o test-output ./cmd/server && rm test-output
git add sdk/cliproxy/auth/
git commit -m "feat(auth): report and log the blast radius of affinity invalidation

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 3: Selector swap — log bindings lost, hand off cache to next affinity selector

**Files:**
- Modify: `sdk/cliproxy/auth/conductor_selection.go` (`SetSelector` ~line 238)
- Modify: `sdk/cliproxy/auth/session_cache.go` (add `Adopt`)
- Test: `sdk/cliproxy/auth/session_cache_test.go`, `sdk/cliproxy/auth/selector_test.go` (append)

**Step 1: Write the failing tests**

Append to `sdk/cliproxy/auth/session_cache_test.go`:

```go
func TestSessionCacheAdoptTransfersUnexpiredEntries(t *testing.T) {
	src := NewSessionCache(time.Hour)
	dst := NewSessionCache(time.Hour)
	defer src.Stop()
	defer dst.Stop()
	src.Set("mixed::s1::m", "auth-a")
	expired := &sessionEntry{authID: "auth-b", expiresAt: time.Now().Add(-time.Minute)}
	src.mu.Lock()
	src.entries["mixed::dead::m"] = *expired
	src.mu.Unlock()

	adopted := dst.Adopt(src)
	if adopted != 1 {
		t.Fatalf("Adopt = %d, want 1 (expired entry skipped)", adopted)
	}
	if id, ok := dst.Get("mixed::s1::m"); !ok || id != "auth-a" {
		t.Fatalf("adopted binding missing or wrong: %q %v", id, ok)
	}

	// Adopting again does not clobber a live destination entry.
	dst.Set("mixed::s1::m", "auth-live")
	if got := dst.Adopt(src); got != 0 {
		t.Fatalf("second Adopt = %d, want 0 (live destination entries kept)", got)
	}
	if id, _ := dst.Get("mixed::s1::m"); id != "auth-live" {
		t.Fatalf("destination entry clobbered: %q", id)
	}
}
```

Append to `sdk/cliproxy/auth/selector_test.go` (adjust the manager-construction helper to whatever `set_selector_stop_test.go` already uses — mirror its setup):

```go
func TestSetSelectorHandsOffAffinityCache(t *testing.T) {
	m := newTestManager(t) // reuse the helper pattern from set_selector_stop_test.go
	old := NewSessionAffinitySelector(&RoundRobinSelector{})
	m.SetSelector(old)
	// Seed a binding through the cache directly (Pick needs live auths).
	old.cache.Set("mixed::sess::model", "auth-1")

	fresh := NewSessionAffinitySelector(&RoundRobinSelector{})
	m.SetSelector(fresh)

	if id, ok := fresh.cache.Get("mixed::sess::model"); !ok || id != "auth-1" {
		t.Fatalf("binding not handed off: %q %v", id, ok)
	}
}
```

**Step 2: Run tests to verify they fail**

Run: `go test ./sdk/cliproxy/auth/ -run 'TestSessionCacheAdopt|TestSetSelectorHandsOff' -v`
Expected: FAIL — `Adopt` undefined (compile error); the SetSelector test also fails because no handoff exists yet.

**Step 3: Write minimal implementation**

`session_cache.go`:

```go
// Adopt copies every unexpired entry from src into c, skipping keys that
// already have a live destination entry. Used when a routing-config update
// replaces one SessionAffinitySelector with another: the old cache would
// otherwise be dropped with all its session bindings. The source cache is not
// modified or stopped — the caller owns its lifecycle.
func (c *SessionCache) Adopt(src *SessionCache) int {
	if src == nil || src == c {
		return 0
	}
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	src.mu.RLock()
	adopted := 0
	for key, entry := range src.entries {
		if !now.Before(entry.expiresAt) {
			continue
		}
		if current, ok := c.entries[key]; ok && now.Before(current.expiresAt) {
			continue
		}
		c.entries[key] = entry
		adopted++
	}
	src.mu.RUnlock()
	return adopted
}
```

`conductor_selection.go` — extend the existing outgoing branch of `SetSelector` (current code stops the outgoing selector; add handoff/logging before `Stop()`):

```go
if outgoingAffinity, ok := outgoing.(*SessionAffinitySelector); ok && outgoingAffinity != selector {
	if incomingAffinity, okIncoming := selector.(*SessionAffinitySelector); okIncoming {
		if moved := incomingAffinity.cache.Adopt(outgoingAffinity.cache); moved > 0 {
			log.Infof("session-affinity: handed off %d binding(s) across selector swap", moved)
		}
	} else {
		log.Infof("session-affinity: dropping %d binding(s) — selector swapped to %T", len(outgoingAffinity.Snapshot()), selector)
	}
	outgoingAffinity.Stop()
}
```

`log` is already imported in `conductor_selection.go`.

**Step 4: Run tests to verify they pass**

Run: `go test ./sdk/cliproxy/auth/ -v -count=1`
Expected: PASS

**Step 5: Format, verify compile, commit**

```bash
gofmt -w sdk/cliproxy/auth/
go build -o test-output ./cmd/server && rm test-output
git add sdk/cliproxy/auth/
git commit -m "feat(auth): hand off affinity bindings across selector swaps

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 4: Migrate bindings when a re-rendered equivalent auth replaces a removed one

**Background for the implementer.** OpenAI-compat auth IDs are deterministic hashes of `kind + API key + base URL + proxy URL` (`internal/watcher/synthesizer/helpers.go:29`, used at `internal/watcher/synthesizer/config.go:393`). Editing the API key therefore produces Delete(old ID) + Add(new ID) for the *same logical credential*. Today `Manager.Remove` → `invalidateSessionAffinity` wipes every session bound to the old ID. This task makes `Manager` remember the removed auth's bindings for 30 seconds and rebind them if an auth with the same equivalence key (`base_url` attribute + `compat_name` attribute) registers.

**Files:**
- Modify: `sdk/cliproxy/auth/conductor_lifecycle.go` (`Remove` ~line 155, `Register` ~line 68; add pending-migration state + helpers)
- Modify: `sdk/cliproxy/auth/conductor.go` (add fields to the `Manager` struct)
- Modify: `sdk/cliproxy/auth/session_cache.go` (add `SnapshotForAuth`, `SetWithExpiry`)
- Test: `sdk/cliproxy/auth/session_cache_test.go`, new file `sdk/cliproxy/auth/affinity_migration_test.go`

**Step 1: Write the failing cache-level tests**

Append to `sdk/cliproxy/auth/session_cache_test.go`:

```go
func TestSnapshotForAuthAndSetWithExpiry(t *testing.T) {
	c := NewSessionCache(time.Hour)
	defer c.Stop()
	c.Set("mixed::s1::glm-5", "auth-a")
	c.Set("mixed::s2::glm-5", "auth-a")
	c.Set("mixed::s3::glm-5", "auth-b")

	bindings := c.SnapshotForAuth("auth-a")
	if len(bindings) != 2 {
		t.Fatalf("SnapshotForAuth = %d entries, want 2", len(bindings))
	}
	expiry := time.Now().Add(30 * time.Minute)
	c.SetWithExpiry("mixed::s1::glm-5", "auth-new", expiry)
	got, ok := c.Get("mixed::s1::glm-5")
	if !ok || got != "auth-new" {
		t.Fatalf("after SetWithExpiry: %q %v", got, ok)
	}
	c.mu.RLock()
	entry := c.entries["mixed::s1::glm-5"]
	c.mu.RUnlock()
	if !entry.expiresAt.Equal(expiry) {
		t.Fatalf("expiresAt = %v, want preserved %v", entry.expiresAt, expiry)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./sdk/cliproxy/auth/ -run TestSnapshotForAuthAndSetWithExpiry -v`
Expected: FAIL — both methods undefined (compile error).

**Step 3: Implement the two cache methods**

`session_cache.go`:

```go
// SnapshotForAuth returns the live bindings currently pointing at authID.
// Used to carry bindings across an auth re-registration (identity change
// without a logical credential change).
func (c *SessionCache) SnapshotForAuth(authID string) []SessionAffinityBinding {
	if c == nil || authID == "" {
		return nil
	}
	all := c.Snapshot()
	out := make([]SessionAffinityBinding, 0, len(all))
	for _, binding := range all {
		if binding.AuthID == authID {
			out = append(out, binding)
		}
	}
	return out
}

// SetWithExpiry binds a session to an auth ID with an explicit expiry instead
// of a fresh TTL window. Used by binding migration to preserve the remaining
// lifetime of a moved binding.
func (c *SessionCache) SetWithExpiry(sessionID, authID string, expiresAt time.Time) {
	if authID == "" || sessionID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !time.Now().Before(expiresAt) {
		return
	}
	entry := sessionEntry{authID: authID, expiresAt: expiresAt, lastTouched: time.Now()}
	if previous, ok := c.entries[sessionID]; ok && time.Now().Before(previous.expiresAt) {
		entry.aliases = mergeSessionAliases(previous.aliases, sessionID)
	}
	c.entries[sessionID] = entry
}
```

**Step 4: Run the cache test to verify it passes**

Run: `go test ./sdk/cliproxy/auth/ -run TestSnapshotForAuthAndSetWithExpiry -v`
Expected: PASS

**Step 5: Write the failing migration test**

Create `sdk/cliproxy/auth/affinity_migration_test.go`. Reuse the Manager-construction pattern from `set_selector_stop_test.go` (same-store test manager; call it `newTestManager(t)` there — mirror it, do not export it). The test seeds an affinity selector on the manager, creates a binding through the selector's cache directly, then simulates re-render: `Remove(old)` then `Register(new)` with matching `base_url`/`compat_name` attributes, and asserts the binding survived with the NEW auth id; a control auth with a different `compat_name` must not be rebound.

```go
package auth

import (
	"testing"
	"time"
)

func TestAffinityBindingMigratesAcrossReRender(t *testing.T) {
	m := newTestManager(t)
	sel := NewSessionAffinitySelector(&RoundRobinSelector{})
	m.SetSelector(sel)
	old := &Auth{ID: "openai-compatibility:p1:old", Provider: "openai-compatibility:p1", Status: StatusActive,
		Attributes: map[string]string{"base_url": "https://up.example", "compat_name": "Up Example"}}
	_ = m.RegisterForTest(old)
	sel.cache.Set("mixed::sess1::glm-5", old.ID)

	// Removal stashes the binding for 30s instead of losing it.
	m.Remove(t.Context(), old.ID)
	if id, ok := sel.cache.Get("mixed::sess1::glm-5"); ok {
		t.Fatalf("binding should be invalidated on Remove, still bound to %q", id)
	}

	replacement := &Auth{ID: "openai-compatibility:p1:new", Provider: "openai-compatibility:p1", Status: StatusActive,
		Attributes: map[string]string{"base_url": "https://up.example", "compat_name": "Up Example"}}
	_ = m.RegisterForTest(replacement)

	if id, ok := sel.cache.Get("mixed::sess1::glm-5"); !ok || id != replacement.ID {
		t.Fatalf("binding did not migrate: %q %v", id, ok)
	}
}

func TestAffinityBindingNotMigratedToDifferentCredential(t *testing.T) {
	m := newTestManager(t)
	sel := NewSessionAffinitySelector(&RoundRobinSelector{})
	m.SetSelector(sel)
	old := &Auth{ID: "openai-compatibility:p1:old", Provider: "openai-compatibility:p1", Status: StatusActive,
		Attributes: map[string]string{"base_url": "https://up.example", "compat_name": "Up Example"}}
	_ = m.RegisterForTest(old)
	sel.cache.Set("mixed::sess1::glm-5", old.ID)

	m.Remove(t.Context(), old.ID)
	other := &Auth{ID: "openai-compatibility:p2:new", Provider: "openai-compatibility:p2", Status: StatusActive,
		Attributes: map[string]string{"base_url": "https://other.example", "compat_name": "Other"}}
	_ = m.RegisterForTest(other)

	if _, ok := sel.cache.Get("mixed::sess1::glm-5"); ok {
		t.Fatal("binding must not migrate to a different credential")
	}
}
```

Notes for the implementer:
- If `Manager.Register` requires executor/provider plumbing that makes direct registration heavy, look at how `set_selector_stop_test.go` or `session_affinity_priority_test.go` builds its manager and follow that exactly; adapt the helper name (`newTestManager`) to what already exists rather than duplicating it. If a `RegisterForTest` shim is needed, make it a tiny unexported wrapper in the test file that calls `m.Register`.
- The migration window and matching logic live in `Manager`, so the test exercises the real Remove/Register path.

**Step 6: Run the migration tests to verify they fail**

Run: `go test ./sdk/cliproxy/auth/ -run TestAffinityBinding -v`
Expected: FAIL — first test fails at "binding did not migrate" (no migration exists); second may pass vacuously (that is fine; it guards the negative case once migration lands).

**Step 7: Implement migration in Manager**

`conductor.go` — add fields to the `Manager` struct (guarded by the existing `m.mu`):

```go
	// pendingAffinityMigrations carries recently removed auths' session
	// bindings so a re-render that changes an auth's identity (same logical
	// credential, new ID) can rebind them. Entries expire after the window
	// below; the map stays tiny and in-memory only.
	pendingAffinityMigrations map[string]pendingAffinityMigration
```

And in the same file (or `conductor_lifecycle.go`), the support types:

```go
const pendingAffinityMigrationTTL = 30 * time.Second

type pendingAffinityMigration struct {
	bindings  []SessionAffinityBinding
	equivalenceKey string
	expiresAt time.Time
}

// affinityEquivalenceKey identifies the logical credential behind an auth for
// binding-migration purposes: the upstream base URL plus the compat entry
// name. Auths without both attributes (built-in OAuth channels, plugin
// executors) never migrate — their removals keep the plain invalidation
// behavior. Empty return means "do not migrate".
func affinityEquivalenceKey(a *Auth) string {
	if a == nil || a.Attributes == nil {
		return ""
	}
	baseURL := strings.TrimSpace(a.Attributes["base_url"])
	compatName := strings.TrimSpace(a.Attributes["compat_name"])
	if baseURL == "" || compatName == "" {
		return ""
	}
	return strings.ToLower(baseURL) + "|" + strings.ToLower(compatName)
}
```

`conductor_lifecycle.go` — in `Remove`, before `m.invalidateSessionAffinity(id)` (inside the same function, after the lock is released), capture and stash:

```go
	m.stashPendingAffinityMigrations(existing, id)
	m.invalidateSessionAffinity(id)
```

(the existing `existing` variable holds the pre-removal clone; verify it is still in scope at that point — it is captured at the top of `Remove`.)

Helper on `Manager`:

```go
// stashPendingAffinityMigrations snapshots the removed auth's live affinity
// bindings so a re-rendered equivalent auth can rebind them. Genuinely
// deleted credentials are unaffected: nothing rebinds unless a Register
// arrives with the same equivalence key inside the TTL window.
func (m *Manager) stashPendingAffinityMigrations(existing *Auth, id string) {
	key := affinityEquivalenceKey(existing)
	if key == "" || m.selector == nil {
		return
	}
	view, ok := m.selector.(interface{ BindingsForAuth(string) []SessionAffinityBinding })
	if !ok {
		return
	}
	bindings := view.BindingsForAuth(id)
	if len(bindings) == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pendingAffinityMigrations == nil {
		m.pendingAffinityMigrations = make(map[string]pendingAffinityMigration)
	}
	// Drop expired stashes opportunistically.
	now := time.Now()
	for k, pending := range m.pendingAffinityMigrations {
		if now.After(pending.expiresAt) {
			delete(m.pendingAffinityMigrations, k)
		}
	}
	m.pendingAffinityMigrations[key] = pendingAffinityMigration{
		bindings:       bindings,
		equivalenceKey: key,
		expiresAt:      now.Add(pendingAffinityMigrationTTL),
	}
}
```

`conductor_lifecycle.go` — at the end of `Register` (after the auth is stored and the scheduler upserted), call:

```go
	m.applyPendingAffinityMigrations(ctx, auth)
```

with:

```go
// applyPendingAffinityMigrations rebinds sessions stashed by a recent Remove
// when the newly registered auth is the same logical credential under a new
// ID. Only exact equivalence-key matches inside the 30s window migrate; the
// stash is always consumed so a late lookalike cannot resurrect stale pins.
func (m *Manager) applyPendingAffinityMigrations(ctx context.Context, auth *Auth) {
	key := affinityEquivalenceKey(auth)
	if key == "" {
		return
	}
	m.mu.Lock()
	pending, ok := m.pendingAffinityMigrations[key]
	if ok {
		delete(m.pendingAffinityMigrations, key)
	}
	m.mu.Unlock()
	if !ok || time.Now().After(pending.expiresAt) {
		return
	}
	m.mu.RLock()
	view, hasView := m.selector.(interface {
		RebindBindings([]SessionAffinityBinding, string)
	})
	m.mu.RUnlock()
	if !hasView {
		return
	}
	view.RebindBindings(pending.bindings, auth.ID)
	log.WithField("auth_id", auth.ID).Infof("session-affinity: migrated %d binding(s) onto re-registered auth", len(pending.bindings))
}
```

`selector.go` — the two small selector methods the interfaces above require:

```go
// BindingsForAuth exposes the live bindings pointing at one auth (used by the
// manager to carry bindings across an auth identity change).
func (s *SessionAffinitySelector) BindingsForAuth(authID string) []SessionAffinityBinding {
	if s == nil || s.cache == nil {
		return nil
	}
	return s.cache.SnapshotForAuth(authID)
}

// RebindBindings moves previously stashed bindings onto newAuthID, preserving
// each binding's original expiry.
func (s *SessionAffinitySelector) RebindBindings(bindings []SessionAffinityBinding, newAuthID string) {
	if s == nil || s.cache == nil || newAuthID == "" {
		return
	}
	for _, binding := range bindings {
		s.cache.SetWithExpiry(binding.Provider+"::"+binding.SessionID+"::"+binding.Model, newAuthID, binding.ExpiresAt)
	}
}
```

**Step 8: Run the full auth package**

Run: `go test ./sdk/cliproxy/auth/ -v -count=1`
Expected: PASS, including both migration tests and all pre-existing affinity/cache tests.

**Step 9: Format, verify compile, commit**

```bash
gofmt -w sdk/cliproxy/auth/
go build -o test-output ./cmd/server && rm test-output
git add sdk/cliproxy/auth/
git commit -m "feat(auth): migrate affinity bindings across upstream re-renders

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 5: Full regression + repo-level verification

**Files:** none modified.

**Step 1: Run the complete test suite**

Run: `go test ./...`
Expected: PASS. If pre-existing failures appear, confirm they fail on the base commit too (`git stash && go test ./<pkg>` …) before attributing them to this change.

**Step 2: Verify compile cleanly (repo-required)**

Run: `go build -o test-output ./cmd/server && rm test-output`
Expected: no output, exit 0.

**Step 3: Final gofmt check**

Run: `gofmt -l .`
Expected: empty output.

**Step 4: Commit any residual formatting (if step 3 was non-empty)**

```bash
gofmt -w .
git add -u
git commit -m "chore: gofmt

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

## Out of scope (do not do in this plan)

- Derived session identity changes (`sdk/cliproxy/session/identity.go`) — wait for Phase-1 logs.
- Per-model route strategy vs. established bindings semantics.
- Any change to `internal/translator/` (repo rule).
- Persisting affinity bindings anywhere.
