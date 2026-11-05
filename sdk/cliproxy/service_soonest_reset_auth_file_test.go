package cliproxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// soonestResetAuthFileUpstream is a local Anthropic stand-in: each access token answers
// with its own weekly reset, or without one when its reset is zero.
type soonestResetAuthFileUpstream struct {
	mu     sync.Mutex
	resets map[string]time.Time
	calls  []string
}

func (u *soonestResetAuthFileUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	_, _ = io.Copy(io.Discard, r.Body)
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if token == "" {
		token = r.Header.Get("X-Api-Key")
	}
	u.mu.Lock()
	u.calls = append(u.calls, token)
	reset, known := u.resets[token]
	u.mu.Unlock()
	if !known {
		http.Error(w, "unknown token", http.StatusUnauthorized)
		return
	}
	if !reset.IsZero() {
		w.Header().Set("Anthropic-Ratelimit-Unified-7d-Reset", strconv.FormatInt(reset.Unix(), 10))
		w.Header().Set("Anthropic-Ratelimit-Unified-7d-Status", "allowed")
	}
	w.Header().Set("Anthropic-Ratelimit-Unified-5h-Status", "allowed")
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"id":"msg","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
}

func (u *soonestResetAuthFileUpstream) callsSince(start int) []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.calls[start:]...)
}

func (u *soonestResetAuthFileUpstream) callCount() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.calls)
}

// TestSoonestResetSurvivesAuthFileRewrite rewrites Claude OAuth auth files the way token
// refresh (new access_token, last_refresh) and an external weight script do, and applies
// each rewrite through Service.handleAuthUpdates, the entry point the file watcher feeds
// (a tmp+rename of an existing file is published as a Modify; see
// internal/watcher/auth_file_rewrite_update_test.go). The rewritten credentials must keep
// their quota observation and their spent probe, so routing is unchanged and no second
// probe is sent. A real Remove followed by a re-add is a new credential and is probed once.
func TestSoonestResetSurvivesAuthFileRewrite(t *testing.T) {
	for _, affinity := range []bool{false, true} {
		for _, removeAndAdd := range []bool{false, true} {
			name := "modify"
			if removeAndAdd {
				name = "remove-and-add"
			}
			if affinity {
				name += "/session-affinity"
			}
			t.Run(name, func(t *testing.T) {
				testSoonestResetSurvivesAuthFileRewrite(t, affinity, removeAndAdd)
			})
		}
	}
}

func testSoonestResetSurvivesAuthFileRewrite(t *testing.T, affinity, removeAndAdd bool) {
	now := time.Now()
	upstream := &soonestResetAuthFileUpstream{resets: map[string]time.Time{
		"token-a-1": now.Add(72 * time.Hour),
		"token-b-1": now.Add(24 * time.Hour),
		"token-b-2": now.Add(24 * time.Hour),
		// c answers without a weekly reset: probed, then ranked after the known resets.
		"token-c-1": {},
		"token-c-2": {},
	}}
	server := httptest.NewServer(upstream)
	defer server.Close()

	dir := t.TempDir()
	var selector coreauth.Selector = &coreauth.SoonestResetSelector{}
	if affinity {
		affinitySelector := coreauth.NewSessionAffinitySelector(selector)
		t.Cleanup(affinitySelector.Stop)
		selector = affinitySelector
	}
	manager := coreauth.NewManager(nil, selector, nil)
	manager.SetRetryConfig(0, 0, 0)
	service, errBuild := NewBuilder().WithConfig(&internalconfig.Config{AuthDir: dir}).WithConfigPath(filepath.Join(dir, "config.yaml")).WithCoreAuthManager(manager).Build()
	if errBuild != nil {
		t.Fatal(errBuild)
	}
	ctx := coreauth.WithSkipPersist(context.Background())
	const model = "claude-soonest-reset-auth-file"
	expired := now.Add(48 * time.Hour).UTC().Format(time.RFC3339)

	// writeAuth rewrites an auth file with tmp+rename and returns the snapshot the watcher
	// would publish for it.
	writeAuth := func(name, token string, weight int) *coreauth.Auth {
		t.Helper()
		path := filepath.Join(dir, name)
		raw := `{"type":"claude","email":"` + name + `","access_token":"` + token + `","expired":"` + expired + `","last_refresh":"` + time.Now().UTC().Format(time.RFC3339Nano) + `","weight":` + strconv.Itoa(weight) + `}`
		tmp := path + ".tmp"
		if errWrite := os.WriteFile(tmp, []byte(raw), 0o600); errWrite != nil {
			t.Fatal(errWrite)
		}
		if errRename := os.Rename(tmp, path); errRename != nil {
			t.Fatal(errRename)
		}
		auths, errSynth := synthesizer.SynthesizeAuthFile(&synthesizer.SynthesisContext{
			Config: &internalconfig.Config{AuthDir: dir}, AuthDir: dir, Now: time.Now(), IDGenerator: synthesizer.NewStableIDGenerator(),
		}, path, []byte(raw))
		if errSynth != nil || len(auths) != 1 {
			t.Fatalf("synthesize %s: %v %v", name, errSynth, auths)
		}
		// Claude OAuth files cannot name an upstream, so the snapshot is pointed at the
		// local stand-in; nothing here may reach api.anthropic.com.
		auths[0].Attributes["base_url"] = server.URL
		return auths[0]
	}
	apply := func(updates ...watcher.AuthUpdate) {
		t.Helper()
		service.handleAuthUpdates(ctx, updates)
		for _, update := range updates {
			if update.Action == watcher.AuthUpdateActionDelete {
				continue
			}
			current, ok := manager.GetByID(update.ID)
			if !ok || current == nil || current.Attributes["base_url"] != server.URL {
				t.Fatalf("auth %s not applied with the local upstream: %+v", update.ID, current)
			}
			registry.GetGlobalRegistry().RegisterClient(update.ID, "claude", []*registry.ModelInfo{{ID: model}})
		}
	}
	names := []string{"claude-a.json", "claude-b.json", "claude-c.json"}
	for _, name := range names {
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(name) })
	}
	apply(
		watcher.AuthUpdate{Action: watcher.AuthUpdateActionAdd, ID: names[0], Auth: writeAuth(names[0], "token-a-1", 1)},
		watcher.AuthUpdate{Action: watcher.AuthUpdateActionAdd, ID: names[1], Auth: writeAuth(names[1], "token-b-1", 1)},
		watcher.AuthUpdate{Action: watcher.AuthUpdateActionAdd, ID: names[2], Auth: writeAuth(names[2], "token-c-1", 1)},
	)

	session := 0
	send := func(n int) []string {
		t.Helper()
		start := upstream.callCount()
		for i := 0; i < n; i++ {
			session++
			opts := cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FormatClaude,
				Headers:      http.Header{"X-Claude-Code-Session-Id": []string{"session-" + strconv.Itoa(session)}},
			}
			req := cliproxyexecutor.Request{Model: model, Payload: []byte(`{"model":"` + model + `","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`)}
			if _, errExecute := manager.Execute(ctx, []string{"claude"}, req, opts); errExecute != nil {
				t.Fatalf("Execute() error = %v", errExecute)
			}
		}
		return upstream.callsSince(start)
	}
	assertCalls := func(label string, got []string, want ...string) {
		t.Helper()
		if strings.Join(got, " ") != strings.Join(want, " ") {
			t.Fatalf("%s: upstream calls = %v, want %v", label, got, want)
		}
	}

	assertCalls("probes, then the earliest reset", send(5), "token-a-1", "token-b-1", "token-c-1", "token-b-1", "token-b-1")
	before, _ := manager.GetByID(names[1])
	if before == nil || before.Quota.ObservedAt.IsZero() {
		t.Fatalf("auth b has no quota observation before the rewrite: %+v", before)
	}
	wantReset := before.Quota.Signals["Anthropic-Ratelimit-Unified-7d-Reset"]

	// Token refresh plus weight script: new access_token, last_refresh and weight on b and c.
	rewrittenB := writeAuth(names[1], "token-b-2", 5)
	rewrittenC := writeAuth(names[2], "token-c-2", 5)
	if removeAndAdd {
		// A watcher that really saw the files disappear publishes a Delete, then an Add.
		apply(
			watcher.AuthUpdate{Action: watcher.AuthUpdateActionDelete, ID: names[1]},
			watcher.AuthUpdate{Action: watcher.AuthUpdateActionDelete, ID: names[2]},
		)
		apply(
			watcher.AuthUpdate{Action: watcher.AuthUpdateActionAdd, ID: names[1], Auth: rewrittenB},
			watcher.AuthUpdate{Action: watcher.AuthUpdateActionAdd, ID: names[2], Auth: rewrittenC},
		)
	} else {
		apply(
			watcher.AuthUpdate{Action: watcher.AuthUpdateActionModify, ID: names[1], Auth: rewrittenB},
			watcher.AuthUpdate{Action: watcher.AuthUpdateActionModify, ID: names[2], Auth: rewrittenC},
		)
	}

	after, _ := manager.GetByID(names[1])
	if after == nil || after.Metadata["access_token"] != "token-b-2" || after.Attributes[coreauth.AttributeWeight] != "5" {
		t.Fatalf("rewrite not applied to b: %+v", after)
	}
	if removeAndAdd {
		// A removed credential is new to the manager: its observation and probe are gone,
		// so each re-added credential is probed once, then routing recovers. The two probes
		// tie and take turns from b, the last pick, so c goes first.
		if !after.Quota.ObservedAt.IsZero() {
			t.Fatalf("re-added auth b kept a quota observation: %+v", after.Quota)
		}
		assertCalls("re-added credentials are probed once each", send(4), "token-c-2", "token-b-2", "token-b-2", "token-b-2")
		return
	}
	if !after.Quota.ObservedAt.Equal(before.Quota.ObservedAt) || after.Quota.Signals["Anthropic-Ratelimit-Unified-7d-Reset"] != wantReset {
		t.Fatalf("rewrite lost b's quota observation: before=%+v after=%+v", before.Quota, after.Quota)
	}
	// b is still known with its old reset and c's probe is still spent: no probe, no
	// change of routing.
	assertCalls("routing unchanged after the rewrite", send(4), "token-b-2", "token-b-2", "token-b-2", "token-b-2")
}
