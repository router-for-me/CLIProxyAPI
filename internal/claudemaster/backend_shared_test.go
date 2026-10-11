package claudemaster

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// sharedProcess is one claude-master process's view of a profile: its own store, its own in-memory
// credential, its own executor, all over the same credential file.
type sharedProcess struct {
	store *backendStore
	auth  *coreauth.Auth
	exec  *sharedClaudeExecutor
}

func sharedProfile(t *testing.T, processes int) (BackendOptions, []*sharedProcess) {
	t.Helper()
	opts := writeSyntheticBackendCredential(t, t.TempDir(), "selected.json", map[string]any{
		"type": "claude", "access_token": "access-1", "refresh_token": "refresh-1",
		"account_uuid": "11111111-1111-4111-8111-111111111111", "claude_device_ids": []string{"dev"},
		"last_refresh": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
	})
	opts.Provider, opts.Model, opts.UseRequestModel = "claude", "", true
	out := make([]*sharedProcess, processes)
	for i := range out {
		store, auth, err := loadBackendCredential(t.Context(), opts)
		if err != nil {
			t.Fatal(err)
		}
		exec := newSharedClaudeExecutor(runtimeexecutor.NewClaudeExecutor(backendConfig(opts.AuthDir)), []*backendStore{store})
		out[i] = &sharedProcess{store: store, auth: auth, exec: exec}
	}
	return opts, out
}

// fakeRotation stands in for Anthropic: every call mints the next token pair.
type fakeRotation struct {
	calls atomic.Int32
	delay time.Duration
}

func (f *fakeRotation) rotate(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	n := f.calls.Add(1)
	time.Sleep(f.delay)
	out := auth.Clone()
	out.Metadata["access_token"] = "access-rotated-" + string(rune('a'+n))
	out.Metadata["refresh_token"] = "refresh-rotated-" + string(rune('a'+n))
	// Strictly increasing, like real rotations: last_refresh has one-second resolution, so
	// two fakes minted in the same second would otherwise tie.
	out.Metadata["last_refresh"] = time.Now().Add(time.Duration(n) * time.Minute).UTC().Format(time.RFC3339)
	return out, nil
}

func diskTokens(t *testing.T, opts BackendOptions) (access, refresh string, whole map[string]any) {
	t.Helper()
	whole, err := readRefreshSnapshot(filepath.Join(opts.AuthDir, opts.AuthID))
	if err != nil {
		t.Fatal(err)
	}
	return credentialString(whole, "access_token"), credentialString(whole, "refresh_token"), whole
}

func TestSharedRefreshAdoptsAnotherProcessRotationWithoutCallingAnthropic(t *testing.T) {
	opts, p := sharedProfile(t, 2)
	a, b := p[0], p[1]
	rotation := &fakeRotation{}
	a.exec.rotate = rotation.rotate
	b.exec.rotate = func(context.Context, *coreauth.Auth) (*coreauth.Auth, error) {
		t.Fatal("the second process called Anthropic although the first had already rotated")
		return nil, nil
	}
	if _, err := a.exec.Refresh(t.Context(), a.auth); err != nil {
		t.Fatal(err)
	}
	got, err := b.exec.Refresh(t.Context(), b.auth) // b still holds access-1/refresh-1, now dead
	if err != nil {
		t.Fatal(err)
	}
	access, refresh, _ := diskTokens(t, opts)
	if credentialString(got.Metadata, "refresh_token") != refresh || credentialString(got.Metadata, "access_token") != access || refresh == "refresh-1" {
		t.Fatalf("b did not adopt a's rotation: got %v / %v, disk %v / %v", got.Metadata["access_token"], got.Metadata["refresh_token"], access, refresh)
	}
	if rotation.calls.Load() != 1 {
		t.Fatalf("rotations = %d, want exactly 1", rotation.calls.Load())
	}
}

func TestSharedRequestsUseTheNewestSavedCredentialBeforeTheirOwnRefresh(t *testing.T) {
	_, p := sharedProfile(t, 2)
	a, b := p[0], p[1]
	if same := b.exec.current(b.auth); same != b.auth {
		t.Fatal("an unchanged credential must be used as is")
	}
	rotation := &fakeRotation{}
	a.exec.rotate = rotation.rotate
	if _, err := a.exec.Refresh(t.Context(), a.auth); err != nil {
		t.Fatal(err)
	}
	fresh := b.exec.current(b.auth)
	if credentialString(fresh.Metadata, "refresh_token") != "refresh-rotated-b" || credentialString(b.auth.Metadata, "refresh_token") != "refresh-1" {
		t.Fatalf("request did not pick up the rotation without mutating the shared copy: %v", fresh.Metadata["refresh_token"])
	}
}

func TestSaveNeverRollsBackAnotherProcessRotation(t *testing.T) {
	opts, p := sharedProfile(t, 2)
	a, b := p[0], p[1]
	a.exec.rotate = (&fakeRotation{}).rotate
	if _, err := a.exec.Refresh(t.Context(), a.auth); err != nil {
		t.Fatal(err)
	}
	stale := b.auth.Clone() // still refresh-1
	stale.Metadata["claude_account_profile_checked_at"] = "2026-10-01T00:00:00Z"
	if _, err := b.store.Save(t.Context(), stale); err != nil {
		t.Fatal(err)
	}
	_, refresh, whole := diskTokens(t, opts)
	if refresh != "refresh-rotated-b" {
		t.Fatalf("a stale save rolled the login back to %q", refresh)
	}
	if whole["claude_account_profile_checked_at"] != "2026-10-01T00:00:00Z" {
		t.Fatal("the stale process's own metadata was not merged in")
	}
}

func TestUnauthorizedAdoptsARotationOrRotatesOncePerCooldown(t *testing.T) {
	opts, p := sharedProfile(t, 2)
	a, b := p[0], p[1]
	rotation := &fakeRotation{}
	a.exec.rotate, b.exec.rotate = rotation.rotate, rotation.rotate
	if _, err := a.exec.Refresh(t.Context(), a.auth); err != nil {
		t.Fatal(err)
	}
	// b's request was rejected with the token a has since replaced: adopt, no extra rotation.
	next, ok := b.exec.recoverUnauthorized(t.Context(), b.auth)
	if !ok || credentialString(next.Metadata, "refresh_token") != "refresh-rotated-b" || rotation.calls.Load() != 1 {
		t.Fatalf("a 401 on a replaced token must adopt the saved one: ok=%v rotations=%d", ok, rotation.calls.Load())
	}
	// A 401 on the token that IS current means it really lapsed: rotate, but only once per cooldown.
	if _, ok := b.exec.recoverUnauthorized(t.Context(), next); !ok || rotation.calls.Load() != 2 {
		t.Fatalf("a 401 on the current token must rotate: rotations=%d", rotation.calls.Load())
	}
	_, refresh, _ := diskTokens(t, opts)
	rejected := b.exec.current(next)
	if _, ok := b.exec.recoverUnauthorized(t.Context(), rejected); ok || rotation.calls.Load() != 2 || refresh == "" {
		t.Fatalf("a repeated 401 must not become a refresh storm: rotations=%d", rotation.calls.Load())
	}
}

func TestConcurrentProcessesRotateExactlyOnce(t *testing.T) {
	const processes = 8
	opts, p := sharedProfile(t, processes)
	rotation := &fakeRotation{delay: 80 * time.Millisecond}
	for _, process := range p {
		process.exec.rotate = rotation.rotate
	}
	var wg sync.WaitGroup
	results := make([]*coreauth.Auth, processes)
	errs := make([]error, processes)
	for i, process := range p {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = process.exec.Refresh(t.Context(), process.auth)
		}()
	}
	wg.Wait()
	_, refresh, _ := diskTokens(t, opts)
	for i := range results {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
		if credentialString(results[i].Metadata, "refresh_token") != refresh {
			t.Fatalf("process %d ended on %q, the file holds %q", i, results[i].Metadata["refresh_token"], refresh)
		}
	}
	if rotation.calls.Load() != 1 {
		t.Fatalf("%d processes refreshed at once and Anthropic was called %d times; only one may rotate", processes, rotation.calls.Load())
	}
}

func TestRefreshLockIsReleasedWhenItsHolderCloses(t *testing.T) {
	opts, _ := sharedProfile(t, 1)
	unlock, err := lockRefresh(t.Context(), opts.AuthDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if again, err := lockRefresh(ctx, opts.AuthDir); err == nil {
		again()
		t.Fatal("two holders of the refresh lock")
	}
	unlock()
	again, err := lockRefresh(t.Context(), opts.AuthDir)
	if err != nil {
		t.Fatalf("the lock was not released: %v", err)
	}
	again()
}

func TestSharedUsagePollsAreServedFromAFreshCache(t *testing.T) {
	opts, p := sharedProfile(t, 1)
	var upstream atomic.Int32
	next := func(_ context.Context, _ *coreauth.Auth, req *http.Request) (*http.Response, error) {
		upstream.Add(1)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"seven_day":{"utilization":12}}`)), Request: req}, nil
	}
	do := sharedUsageRequest(next, []*backendStore{p[0].store})
	poll := func() string {
		req, _ := http.NewRequest(http.MethodGet, ClaudeOAuthUsageEndpoint, nil)
		resp, err := do(t.Context(), p[0].auth, req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return string(body)
	}
	if poll() != `{"seven_day":{"utilization":12}}` || poll() != `{"seven_day":{"utilization":12}}` || upstream.Load() != 1 {
		t.Fatalf("a second poll inside the TTL must not reach Anthropic: upstream calls = %d", upstream.Load())
	}
	old := time.Now().Add(-2 * usageCacheTTL)
	if err := os.Chtimes(filepath.Clean(opts.AuthDir)+".usage", old, old); err != nil {
		t.Fatal(err)
	}
	poll()
	if upstream.Load() != 2 {
		t.Fatalf("a stale cache must be refetched: upstream calls = %d", upstream.Load())
	}
}

// Rotations stamp whole-second times elsewhere; two token pairs with the SAME revision are a conflict
// and the saved file must win, or a stale save could overwrite a pair that was just rotated.
func TestSaveKeepsTheSavedPairWhenRevisionsTie(t *testing.T) {
	opts, p := sharedProfile(t, 1)
	store := p[0].store
	same := time.Now().Add(time.Hour).UTC().Format(time.RFC3339) // one second, whole-second resolution
	rotated := p[0].auth.Clone()
	rotated.Metadata["access_token"], rotated.Metadata["refresh_token"] = "access-rotated-x", "refresh-rotated-x"
	rotated.Metadata["last_refresh"] = same
	if _, err := store.Save(t.Context(), rotated); err != nil {
		t.Fatal(err)
	}
	// Another process, still holding the previous pair, saves in that same second.
	stale := p[0].auth.Clone()
	stale.Metadata["access_token"], stale.Metadata["refresh_token"] = "access-1", "refresh-1"
	stale.Metadata["last_refresh"] = same
	if _, err := store.Save(t.Context(), stale); err != nil {
		t.Fatal(err)
	}
	if _, refresh, _ := diskTokens(t, opts); refresh != "refresh-rotated-x" {
		t.Fatalf("a stale pair with an equal timestamp overwrote the rotated one: %q", refresh)
	}
}

func TestRotationsAreStampedBelowOneSecond(t *testing.T) {
	_, p := sharedProfile(t, 1)
	p[0].exec.rotate = (&fakeRotation{}).rotate
	got, err := p[0].exec.Refresh(t.Context(), p[0].auth)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(credentialString(got.Metadata, "last_refresh"), ".") {
		t.Fatalf("last_refresh %q has no sub-second part", got.Metadata["last_refresh"])
	}
}
