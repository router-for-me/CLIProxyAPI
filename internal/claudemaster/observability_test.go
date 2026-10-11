package claudemaster

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// safeBuffer is a bytes.Buffer that is safe to write from the snapshot goroutine and read from a test.
type safeBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *safeBuffer) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func captureLogs(t *testing.T, level string) *safeBuffer {
	t.Helper()
	out := &safeBuffer{}
	closeFn, err := ConfigureLogging(LogOptions{Level: level, Out: out})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeFn(); activeLogger.Store(nil) })
	return out
}

func namedSelector(t *testing.T) (*backendSeriesSelector, []*coreauth.Auth, time.Time) {
	t.Helper()
	selector := &backendSeriesSelector{authIDs: []string{"profile-a", "profile-b"}, provider: "claude",
		names: map[string]string{"profile-a": "claude-connor", "profile-b": "claude-ejc3"}}
	t.Cleanup(selector.Stop)
	reset := time.Date(2026, time.October, 2, 0, 0, 0, 0, time.UTC)
	selector.now = func() time.Time { return reset.Add(-time.Hour) }
	return selector, []*coreauth.Auth{backendSeriesTestAuth("profile-a", "claude"), backendSeriesTestAuth("profile-b", "claude")}, reset
}

func plainRequest(session string) coreexecutor.Options {
	return coreexecutor.Options{
		Headers:         http.Header{"X-Claude-Code-Session-Id": []string{session}},
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"hello"}]}`),
	}
}

func TestInfoLogSaysWhenAConversationMovesToAnotherAccount(t *testing.T) {
	logs := captureLogs(t, "info")
	selector, auths, reset := namedSelector(t)
	selector.observeQuota("profile-a", backendWeeklyQuota{known: true, used: 0.5, resetsAt: reset})
	selector.observeQuota("profile-b", backendWeeklyQuota{known: true, used: 0.2, resetsAt: reset.Add(24 * time.Hour)})
	const rawSession = "RAW-SESSION-ID-do-not-log-1234"
	requireBackendSeriesPickWith(t, selector, plainRequest(rawSession), auths, "profile-a")

	selector.observeQuota("profile-a", backendWeeklyQuota{known: true, used: 0.93, resetsAt: reset})
	requireBackendSeriesPickWith(t, selector, plainRequest(rawSession), auths, "profile-b")

	out := logs.String()
	for _, want := range []string{
		"inference account switched", "from=claude-connor", "to=claude-ejc3", "reason=reserve_reached",
		"quota band changed", "profile=claude-connor", "from=ok", "to=reserve", "level=info",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the info log is missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, rawSession) {
		t.Fatalf("the raw conversation id reached the log:\n%s", out)
	}
	if !strings.Contains(out, "session=s-") {
		t.Fatalf("the conversation is not followable by its tag:\n%s", out)
	}
	if strings.Contains(out, "account chosen") {
		t.Fatal("per-request routing belongs to debug, not info")
	}
}

func requireBackendSeriesPickWith(t *testing.T, selector *backendSeriesSelector, opts coreexecutor.Options, auths []*coreauth.Auth, wantID string) {
	t.Helper()
	got, err := selector.Pick(t.Context(), "claude", "", opts, auths)
	if err != nil || got == nil || got.ID != wantID {
		t.Fatalf("Pick() = %#v, %v, want %q", got, err, wantID)
	}
}

func TestDebugAddsEveryRoutingDecision(t *testing.T) {
	logs := captureLogs(t, "debug")
	selector, auths, _ := namedSelector(t)
	requireBackendSeriesPickWith(t, selector, plainRequest("debug-session"), auths, "profile-a")
	out := logs.String()
	for _, want := range []string{"account chosen", "profile=claude-connor", "conversation bound", "took="} {
		if !strings.Contains(out, want) {
			t.Errorf("the debug log is missing %q:\n%s", want, out)
		}
	}
}

func TestARateLimitAndTheProfileComingBackAreBothLogged(t *testing.T) {
	logs := captureLogs(t, "info")
	selector, _, _ := namedSelector(t)
	now := time.Date(2026, time.October, 1, 12, 0, 0, 0, time.UTC)
	selector.now = func() time.Time { return now }
	retry := 90 * time.Second
	result := backendSeriesQuotaResult("profile-a")
	result.RetryAfter = &retry
	selector.OnResult(result)
	out := logs.String()
	for _, want := range []string{"profile rate limited", "profile=claude-connor", "status=429", "cooldown=1m30s", `until="2026-10-01T12:01:30Z"`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	now = now.Add(2 * time.Minute) // the cooldown is over
	selector.OnResult(coreauth.Result{AuthID: "profile-a", Provider: "claude", Success: true})
	if !strings.Contains(logs.String(), "profile available again") {
		t.Fatalf("recovery was not logged:\n%s", logs.String())
	}
}

func TestACredentialRejectionIsAWarningWithoutUpstreamText(t *testing.T) {
	logs := captureLogs(t, "info")
	selector, _, _ := namedSelector(t)
	selector.OnResult(coreauth.Result{AuthID: "profile-b", Provider: "claude", CredentialScope: true,
		Error: &coreauth.Error{HTTPStatus: http.StatusUnauthorized, Message: "token sk-ant-oat01-LEAKME-LEAKME was revoked for alice@example.com"}})
	out := logs.String()
	if !strings.Contains(out, "level=warning") || !strings.Contains(out, "profile=claude-ejc3") || !strings.Contains(out, "status=401") {
		t.Fatalf("a rejected login was not a warning:\n%s", out)
	}
	for _, secret := range []string{"LEAKME", "alice@example.com", "revoked"} {
		if strings.Contains(out, secret) {
			t.Fatalf("upstream error text reached the log (%q):\n%s", secret, out)
		}
	}
}

func TestTheBackupBeingUsedIsAWarningOncePerMinute(t *testing.T) {
	logs := captureLogs(t, "info")
	selector, auths, reset := backendAPIBackupTestSelector(t)
	selector.names = map[string]string{"subscription-a": "claude-connor", "subscription-b": "claude-ejc3"}
	for _, id := range []string{"subscription-a", "subscription-b"} {
		selector.observeQuota(id, backendWeeklyQuota{known: true, used: 1, resetsAt: reset})
	}
	for i := 0; i < 3; i++ {
		requireBackendAPIBackupPick(t, selector, backendAPIBackupTestOptions("exhausted-"+string(rune('a'+i)), false), auths, "api-backup")
	}
	if got := strings.Count(logs.String(), "using the paid API-key backup"); got != 1 {
		t.Fatalf("the backup warning appeared %d times, want once a minute:\n%s", got, logs.String())
	}
	// The first sight of an exhausted profile is "quota band" (there is no earlier band to change from).
	if got := strings.Count(logs.String(), "band=exhausted"); got != 2 {
		t.Fatalf("exhaustion of both profiles was not logged (found %d):\n%s", got, logs.String())
	}
}

func TestTheQuotaSnapshotNamesEveryProfileAndSummarisesRouting(t *testing.T) {
	logs := captureLogs(t, "info")
	selector, auths, reset := namedSelector(t)
	selector.observeQuota("profile-a", backendWeeklyQuota{known: true, used: 0.425, resetsAt: reset})
	requireBackendSeriesPickWith(t, selector, plainRequest("snap-1"), auths, "profile-a")
	requireBackendSeriesPickWith(t, selector, plainRequest("snap-2"), auths, "profile-a")
	selector.logSnapshot(5 * time.Minute)
	out := logs.String()
	for _, want := range []string{
		"msg=quota", "profile=claude-connor", "used_pct=42.5", "band=ok", "resets_in=1h0m0s",
		"profile=claude-ejc3", "used_pct=unknown", "msg=\"routing summary\"", "interval=5m0s", "requests_claude-connor=2",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("snapshot is missing %q:\n%s", want, out)
		}
	}
	logs.b.Reset()
	selector.logSnapshot(5 * time.Minute)
	if strings.Contains(logs.String(), "requests_claude-connor") {
		t.Fatalf("the summary did not reset between snapshots:\n%s", logs.String())
	}
}

func TestTheSnapshotRunsOnItsIntervalAndNegativeTurnsItOff(t *testing.T) {
	logs := captureLogs(t, "info")
	selector, _, _ := namedSelector(t)
	ctx, cancel := context.WithCancel(t.Context())
	done := startBackendObservation(ctx, selector, 20*time.Millisecond)
	deadline := time.After(3 * time.Second)
	for strings.Count(logs.String(), "routing summary") < 3 {
		select {
		case <-deadline:
			t.Fatalf("the snapshot did not repeat on its interval:\n%s", logs.String())
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	<-done
	before := strings.Count(logs.String(), "routing summary")
	off := make(chan struct{})
	go func() { <-startBackendObservation(t.Context(), selector, -1); close(off) }()
	select {
	case <-off:
	case <-time.After(2 * time.Second):
		t.Fatal("a negative interval did not turn the snapshot off")
	}
	if strings.Count(logs.String(), "routing summary") != before {
		t.Fatal("a snapshot ran although the interval was negative")
	}
}

func TestNothingSecretOrIdentifyingIsEverLogged(t *testing.T) {
	logs := captureLogs(t, "debug")
	selector, auths, reset := namedSelector(t)
	secrets := []string{"sk-ant-oat01-SECRETSECRETSECRET", "eyJhbGciOiJIUzI1NiJ9.payload.sig", "Bearer abcdefghijklmnop", "ejc3@example.com"}
	selector.observeQuota("profile-a", backendWeeklyQuota{known: true, used: 0.95, resetsAt: reset})
	opts := plainRequest("sk-ant-oat01-SECRETSECRETSECRET-as-a-session-id")
	opts.Headers.Set("Authorization", "Bearer abcdefghijklmnop")
	opts.OriginalRequest = []byte(`{"metadata":{"user_id":"user_x_account_11111111-2222-3333-4444-555555555555_session_y"},"messages":[{"role":"user","content":"my email is ejc3@example.com"}]}`)
	_, _ = selector.Pick(t.Context(), "claude", "", opts, auths)
	selector.OnResult(coreauth.Result{AuthID: "profile-a", Provider: "claude", CredentialScope: true,
		Error: &coreauth.Error{HTTPStatus: 429, Message: "Bearer abcdefghijklmnop ejc3@example.com"}})
	selector.logSnapshot(time.Minute)
	out := logs.String()
	for _, secret := range append(secrets, "11111111-2222-3333-4444-555555555555", "my email is") {
		if strings.Contains(out, secret) {
			t.Fatalf("%q reached the log:\n%s", secret, out)
		}
	}
	if out == "" {
		t.Fatal("the scenario logged nothing at all")
	}
}

func TestRedactionIsASecondLineOfDefence(t *testing.T) {
	logs := captureLogs(t, "info")
	lg().Info("test", "access_token", "whatever", "note", "see sk-ant-api03-ABCDEFGHIJ and eyJhbGciOiJIUzI1NiJ9.x.y", "profile", "claude-connor")
	out := logs.String()
	if strings.Contains(out, "whatever") || strings.Contains(out, "ABCDEFGHIJ") || strings.Contains(out, "eyJhbGci") {
		t.Fatalf("a secret-shaped attribute was not redacted:\n%s", out)
	}
	if !strings.Contains(out, "profile=claude-connor") || !strings.Contains(out, "[redacted]") {
		t.Fatalf("redaction removed too much or marked nothing:\n%s", out)
	}
}

func TestLevelsAndFormats(t *testing.T) {
	for _, tc := range []struct {
		level, sees, hides string
	}{
		{"debug", "d-line", ""}, {"info", "i-line", "d-line"}, {"warn", "w-line", "i-line"}, {"error", "e-line", "w-line"},
	} {
		logs := captureLogs(t, tc.level)
		lg().Debug("d-line")
		lg().Info("i-line")
		lg().Warn("w-line")
		lg().Error("e-line")
		out := logs.String()
		if !strings.Contains(out, tc.sees) || tc.hides != "" && strings.Contains(out, tc.hides) {
			t.Errorf("level %s: wanted %q and not %q:\n%s", tc.level, tc.sees, tc.hides, out)
		}
	}
	off := captureLogs(t, "off")
	lg().Error("nothing")
	if off.String() != "" {
		t.Fatal("level off still logged")
	}
	if _, err := ConfigureLogging(LogOptions{Level: "loud", Out: &safeBuffer{}}); err == nil {
		t.Fatal("an unknown level was accepted")
	}
	json1 := &safeBuffer{}
	closeFn, err := ConfigureLogging(LogOptions{Level: "info", Format: "json", Out: json1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeFn(); activeLogger.Store(nil) })
	lg().Info("hello", "profile", "claude-connor")
	var record map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(json1.String())), &record); err != nil || record["profile"] != "claude-connor" || record["msg"] != "hello" {
		t.Fatalf("json log = %q (%v)", json1.String(), err)
	}
}

func TestTheLogFileRotatesBySizeAndKeepsAFixedNumberOfFiles(t *testing.T) {
	dir := canonicalTestTempDir(t)
	path := filepath.Join(dir, "claude-master.log")
	closeFn, err := ConfigureLogging(LogOptions{Level: "info", File: path, MaxBytes: 400, Keep: 3})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeFn(); activeLogger.Store(nil) })
	for i := 0; i < 60; i++ {
		lg().Info("a line long enough to fill the file", "n", i, "padding", strings.Repeat("x", 30))
	}
	closeFn()
	var names []string
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		names = append(names, e.Name())
		info, _ := e.Info()
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s has mode %v, want 0600", e.Name(), info.Mode().Perm())
		}
		if info.Size() > 400+200 {
			t.Errorf("%s is %d bytes, far over its 400 byte limit", e.Name(), info.Size())
		}
	}
	want := []string{"claude-master.log", "claude-master.log.1", "claude-master.log.2", "claude-master.log.3"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("files = %v, want exactly %v (the oldest must be removed)", names, want)
	}
	newest, _ := os.ReadFile(path)
	if !strings.Contains(string(newest), "n=59") {
		t.Fatalf("the current file does not hold the newest lines:\n%s", newest)
	}
	oldest, _ := os.ReadFile(path + ".3")
	if strings.Contains(string(oldest), "n=59") || len(oldest) == 0 {
		t.Fatal("the oldest file holds the newest lines, or is empty")
	}
}

func TestReopeningTheLogFileContinuesItWithoutLosingLines(t *testing.T) {
	path := filepath.Join(canonicalTestTempDir(t), "c.log")
	for round := 0; round < 2; round++ {
		closeFn, err := ConfigureLogging(LogOptions{Level: "info", File: path})
		if err != nil {
			t.Fatal(err)
		}
		lg().Info("round", "n", round)
		closeFn()
	}
	activeLogger.Store(nil)
	data, _ := os.ReadFile(path)
	if strings.Count(string(data), "msg=round") != 2 {
		t.Fatalf("a restart lost or duplicated lines:\n%s", data)
	}
}

func TestSessionTagsAreStableShortAndNotTheId(t *testing.T) {
	a, b := sessionTag("one"), sessionTag("two")
	if a == b || a != sessionTag("one") || len(a) != 10 || strings.Contains(a, "one") {
		t.Fatalf("tags %q %q", a, b)
	}
	if sessionTag("") != "none" {
		t.Fatal("empty session")
	}
}

func TestTheLimiterAllowsOnePerGapPerKey(t *testing.T) {
	now := time.Unix(1000, 0)
	e := newEvery(time.Minute)
	e.now = func() time.Time { return now }
	if !e.allow("a") || e.allow("a") || !e.allow("b") {
		t.Fatal("first calls: a allowed, a again refused, b allowed")
	}
	now = now.Add(61 * time.Second)
	if !e.allow("a") {
		t.Fatal("a stays refused after the gap")
	}
}

// A credential file is named after the account's email (claude-<id>-<email>.json). Whatever names a
// profile in a log or a metric must never be derived from that.
func TestAProfileWithoutANameIsNeverNamedAfterItsEmailBearingFile(t *testing.T) {
	const fileName = "claude-54af6cbe-someone@example.com.json"
	name := safeProfileName("", fileName)
	if strings.Contains(name, "someone") || strings.Contains(name, "@") || strings.Contains(name, ".") || !strings.HasPrefix(name, "profile-") {
		t.Fatalf("fallback name %q leaks the file name", name)
	}
	if safeProfileName("", fileName) != name || name == safeProfileName("", "claude-other.json") {
		t.Fatal("the fallback must be stable and tell profiles apart")
	}
	if safeProfileName("claude-connor", fileName) != "claude-connor" {
		t.Fatal("a profile's own name must win")
	}
	if safeProfileName("not a name!", "claude-master-3-1") != "claude-master-3-1" {
		t.Fatal("an internal runtime id is a fine fallback")
	}
	logs := captureLogs(t, "debug")
	selector := &backendSeriesSelector{authIDs: []string{fileName}, provider: "claude"}
	t.Cleanup(selector.Stop)
	selector.observeQuota(fileName, backendWeeklyQuota{known: true, used: 0.95, resetsAt: time.Now().Add(time.Hour)})
	selector.logSnapshot(time.Minute)
	selector.OnResult(coreauth.Result{AuthID: fileName, Provider: "claude", CredentialScope: true, Error: &coreauth.Error{HTTPStatus: 401}})
	if out := logs.String(); strings.Contains(out, "someone") || strings.Contains(out, "example.com") || strings.Contains(out, "54af6cbe") {
		t.Fatalf("a credential file name reached the log:\n%s", out)
	}
	store := &backendStore{opts: BackendOptions{AuthID: fileName}}
	if strings.Contains(store.name(), "someone") {
		t.Fatalf("a store named itself %q", store.name())
	}
}
