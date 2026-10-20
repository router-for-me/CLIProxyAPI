package auth

import (
	"context"
	"fmt"
	execution "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// Ported from audit overlays: assert repaired behavior, not reproduction PASS.
func TestDelegatedLaunchEstablishedSecondTurn(t *testing.T) {
	f := newLaunchFixture(t)
	f.add(t, "a", "account")
	r := f.pool.Prepare(context.Background(), "client", launchRequest("long-child"))
	if err := f.execute("client", r, r.Model, r.SessionID, false); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(31 * time.Second)
	if err := f.execute("client", r, r.Model, r.SessionID, false); err != nil {
		t.Fatal(err)
	}
	if f.executor.starts.Load() != 2 {
		t.Fatal("second normal turn did not run")
	}
	state := f.pool.Inspect("client", r.BindingID, r.SessionID)
	if state.State != "established" || state.ExecutorAttempts != 2 {
		t.Fatal(state)
	}
	f.now = f.now.Add(launchIdleTTL)
	requireLaunchRefusal(t, f.execute("client", r, r.Model, r.SessionID, false), "binding_invalid")
	if len(f.pool.entries) != 0 || len(f.pool.bindings) != 0 {
		t.Fatal("idle records retained")
	}
}

func TestDelegatedLaunchPublishedUsageShapes(t *testing.T) {
	for _, kind := range []string{"published", "single-window", "nullable", "ordinary-false", "reached", "spend", "reached-type", "unknown-type", "unknown-extra", "missing-reached", "no-windows", "identity-mismatch"} {
		t.Run(kind, func(t *testing.T) {
			f := newLaunchFixture(t)
			f.add(t, "a", "account")
			body := strings.Replace(string(launchUsage("account", true, 12, f.now)), `"account_id":"account",`, `"plan_type":"pro",`, 1)
			var want = "ready"
			switch kind {
			case "single-window":
				body = strings.Replace(body, fmt.Sprintf(`,"secondary_window":{"used_percent":12,"reset_at":%d,"limit_window_seconds":604800}`, f.now.Add(24*time.Hour).Unix()), "", 1)
			case "nullable":
				body = strings.Replace(body, fmt.Sprintf(`"secondary_window":{"used_percent":12,"reset_at":%d,"limit_window_seconds":604800}`, f.now.Add(24*time.Hour).Unix()), `"secondary_window":null`, 1)
			case "ordinary-false":
				body = strings.Replace(body, `"allowed":true`, `"allowed":false`, 1)
				want = "waiting"
			case "reached":
				body = strings.Replace(body, `"limit_reached":false`, `"limit_reached":true`, 1)
				want = "waiting"
			case "spend":
				body = strings.Replace(body, `"plan_type":`, `"spend_control":{"reached":true},"plan_type":`, 1)
				want = "waiting"
			case "reached-type":
				body = strings.Replace(body, `"plan_type":`, `"rate_limit_reached_type":{"type":"workspace_member_usage_limit_reached"},"plan_type":`, 1)
				want = "waiting"
			case "unknown-type":
				body = strings.Replace(body, `"plan_type":`, `"rate_limit_reached_type":{"type":"future"},"plan_type":`, 1)
				want = "unverified"
			case "unknown-extra":
				body = strings.Replace(body, `"plan_type":`, `"additional_rate_limits":[{"limit_name":"future","metered_feature":"future","rate_limit":{"allowed":false,"limit_reached":true}}],"plan_type":`, 1)
				want = "unverified"
			case "missing-reached":
				body = strings.Replace(body, `"limit_reached":false,`, "", 1)
				want = "unverified"
			case "no-windows":
				body = `{"rate_limit":{"allowed":true,"limit_reached":false,"primary_window":null,"secondary_window":null}}`
				want = "unverified"
			case "identity-mismatch":
				body = strings.Replace(body, `"plan_type":`, `"account_id":"other","plan_type":`, 1)
				want = "unverified"
			}
			f.handler = func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Chatgpt-Account-Id") != "account" {
					t.Error("wrong authenticated account")
				}
				_, _ = w.Write([]byte(body))
			}
			r := f.pool.Prepare(context.Background(), "client", launchRequest("source-shape"))
			if r.Status != want || f.executor.starts.Load() != 0 {
				t.Fatalf("want %s got %+v", want, r)
			}
		})
	}
}

func TestDelegatedLaunchReleaseDuringDispatchProbe(t *testing.T) {
	f := newLaunchFixture(t)
	f.add(t, "a", "account")
	r := f.pool.Prepare(context.Background(), "client", launchRequest("release"))
	entered, release := make(chan struct{}), make(chan struct{})
	f.handler = func(w http.ResponseWriter, q *http.Request) {
		close(entered)
		<-release
		_, _ = w.Write(launchUsage("account", true, 12, f.now))
	}
	result := make(chan error, 1)
	go func() { result <- f.execute("client", r, r.Model, r.SessionID, false) }()
	<-entered
	f.pool.Release("attacker", r.BindingID, r.SessionID)
	if f.pool.Inspect("client", r.BindingID, r.SessionID).State != "pending" {
		t.Fatal("attacker released pin")
	}
	f.pool.Release("client", r.BindingID, r.SessionID)
	f.pool.Release("client", r.BindingID, r.SessionID)
	close(release)
	requireLaunchRefusal(t, <-result, "binding_changed")
	if f.executor.starts.Load() != 0 {
		t.Fatal("released request generated")
	}
	if got := f.pool.Prepare(context.Background(), "client", launchRequest("release")); got.Code != "binding_released" {
		t.Fatal(got)
	}
}

func TestDelegatedLaunchAuthReplacementDuringDispatchProbe(t *testing.T) {
	f := newLaunchFixture(t)
	f.add(t, "a", "account")
	r := f.pool.Prepare(context.Background(), "client", launchRequest("race"))
	entered, release := make(chan struct{}), make(chan struct{})
	f.handler = func(w http.ResponseWriter, q *http.Request) {
		close(entered)
		<-release
		_, _ = w.Write(launchUsage("account", true, 12, f.now))
	}
	result := make(chan error, 1)
	go func() { result <- f.execute("client", r, r.Model, r.SessionID, false) }()
	<-entered
	a, _ := f.manager.GetByID(t.Name() + "-a")
	a.Metadata["access_token"] = "new-synthetic-only"
	if _, err := f.manager.Update(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	close(release)
	requireLaunchRefusal(t, <-result, "binding_changed")
	if f.executor.starts.Load() != 0 {
		t.Fatal("replacement generated")
	}
}

func TestDelegatedLaunchCancelPreparation(t *testing.T) {
	f := newLaunchFixture(t)
	f.add(t, "a", "account")
	entered := make(chan struct{})
	f.handler = func(w http.ResponseWriter, r *http.Request) { close(entered); <-r.Context().Done() }
	result := make(chan LaunchResult, 1)
	go func() { result <- f.pool.Prepare(context.Background(), "client", launchRequest("cancel")) }()
	<-entered
	f.pool.CancelPrepare("client", "cancel")
	if r := <-result; r.Status != "unverified" {
		t.Fatal(r)
	}
	f.now = f.now.Add(launchTTL)
	f.pool.mu.Lock()
	f.pool.pruneLocked()
	n := len(f.pool.entries)
	f.pool.mu.Unlock()
	if n != 0 {
		t.Fatal("cancelled prepare retained")
	}
}

func TestDelegatedLaunchStatusAloneIsNotExhaustion(t *testing.T) {
	f := newLaunchFixture(t)
	f.add(t, "a", "account")
	r := f.pool.Prepare(context.Background(), "client", launchRequest("throttle"))
	f.executor.err = &Error{Code: "rate_limit_exceeded", HTTPStatus: 429}
	err := f.execute("client", r, r.Model, r.SessionID, false)
	if _, ok := err.(*LaunchRefusal); ok {
		t.Fatal("invented account exhaustion")
	}
}

// The executor is blocked until cancellation. Idle pruning may never evict it.
type launchBlockingExecutor struct {
	*launchTestExecutor
	entered chan struct{}
}

func (e *launchBlockingExecutor) ExecuteDelegatedLaunch(ctx context.Context, a *Auth, r execution.Request, o execution.Options) (execution.Response, *execution.StreamResult, error) {
	close(e.entered)
	<-ctx.Done()
	return execution.Response{}, nil, ctx.Err()
}
func TestDelegatedLaunchActiveReleaseAndBounds(t *testing.T) {
	f := newLaunchFixture(t)
	f.add(t, "a", "account")
	ex := &launchBlockingExecutor{f.executor, make(chan struct{})}
	f.manager.RegisterExecutor(ex)
	r := f.pool.Prepare(context.Background(), "client", launchRequest("active"))
	result := make(chan error, 1)
	go func() { result <- f.execute("client", r, r.Model, r.SessionID, false) }()
	<-ex.entered
	if f.pool.Resume("client", r.BindingID, r.SessionID, 1) {
		t.Fatal("resume authorized during active execution")
	}
	f.now = f.now.Add(2 * launchIdleTTL)
	f.pool.mu.Lock()
	f.pool.pruneLocked()
	entry := f.pool.bindings[r.BindingID]
	f.pool.mu.Unlock()
	if entry == nil {
		t.Fatal("evicted in-flight request")
	}
	requireLaunchRefusal(t, f.execute("client", r, r.Model, r.SessionID, false), "binding_busy")
	f.pool.Release("client", r.BindingID, r.SessionID)
	if err := <-result; err != context.Canceled {
		t.Fatal(err)
	}
	f.now = f.now.Add(launchTTL)
	f.pool.mu.Lock()
	f.pool.pruneLocked()
	n := len(f.pool.entries)
	f.pool.mu.Unlock()
	if n != 0 {
		t.Fatal("released completed pin retained")
	}
}

func TestDelegatedLaunchExplicitResumeKeepsPin(t *testing.T) {
	f := newLaunchFixture(t)
	f.add(t, "a", "account")
	r := f.pool.Prepare(context.Background(), "client", launchRequest("resume"))
	f.executor.err = launchTestFailure{}
	requireLaunchRefusal(t, f.execute("client", r, r.Model, r.SessionID, false), "usage_limit")
	requireLaunchRefusal(t, f.execute("client", r, r.Model, r.SessionID, false), "usage_limit")
	if f.executor.starts.Load() != 1 {
		t.Fatal("native retry duplicated generation")
	}
	if f.pool.Resume("attacker", r.BindingID, r.SessionID, 1) || f.pool.Resume("client", r.BindingID, r.SessionID, 0) {
		t.Fatal("invalid resume accepted")
	}
	f.executor.err = nil
	if !f.pool.Resume("client", r.BindingID, r.SessionID, 1) {
		t.Fatal("acknowledged resume denied")
	}
	if err := f.execute("client", r, r.Model, r.SessionID, false); err != nil {
		t.Fatal(err)
	}
	if f.executor.starts.Load() != 2 || f.executor.ids[0] != f.executor.ids[1] {
		t.Fatal("resume rebound")
	}
	f.executor.err = launchTestFailure{}
	requireLaunchRefusal(t, f.execute("client", r, r.Model, r.SessionID, false), "usage_limit")
	state := f.pool.Inspect("client", r.BindingID, r.SessionID)
	a, _ := f.manager.GetByID(t.Name() + "-a")
	a.Disabled = true
	if _, err := f.manager.Update(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if !f.pool.Resume("client", r.BindingID, r.SessionID, state.RefusalVersion) {
		t.Fatal("matching acknowledgement denied")
	}
	f.executor.err = nil
	requireLaunchRefusal(t, f.execute("client", r, r.Model, r.SessionID, false), "binding_changed")
	if f.executor.starts.Load() != 3 {
		t.Fatal("matching refusal version bypassed disabled auth")
	}
}

func TestDelegatedLaunchRetainedGeneratedContractFixture(t *testing.T) {
	body, err := os.ReadFile("testdata/delegated-usage-pro.json")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	r := decodeLaunchUsage(body, "validated-workspace", now)
	if r.Status != "ready" || len(r.Windows) != 1 || r.QuotaDomains[0] != "codex:credential:validated-workspace" {
		t.Fatal(r)
	}
}

func TestDelegatedLaunchEstablishedAuthRevoked(t *testing.T) {
	f := newLaunchFixture(t)
	f.add(t, "a", "account")
	r := f.pool.Prepare(context.Background(), "client", launchRequest("revoked"))
	if err := f.execute("client", r, r.Model, r.SessionID, false); err != nil {
		t.Fatal(err)
	}
	a, _ := f.manager.GetByID(t.Name() + "-a")
	a.Disabled = true
	if _, err := f.manager.Update(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	requireLaunchRefusal(t, f.execute("client", r, r.Model, r.SessionID, false), "binding_changed")
	if f.executor.starts.Load() != 1 {
		t.Fatal("revoked established pin dispatched")
	}
}

type launchStreamingExecutor struct {
	*launchTestExecutor
	finished chan struct{}
}

func (e *launchStreamingExecutor) ExecuteDelegatedLaunch(ctx context.Context, a *Auth, r execution.Request, o execution.Options) (execution.Response, *execution.StreamResult, error) {
	chunks := make(chan execution.StreamChunk)
	go func() { defer close(e.finished); defer close(chunks); <-ctx.Done() }()
	return execution.Response{}, &execution.StreamResult{Chunks: chunks}, nil
}
func TestDelegatedLaunchStreamingCancel(t *testing.T) {
	f := newLaunchFixture(t)
	f.add(t, "a", "account")
	ex := &launchStreamingExecutor{f.executor, make(chan struct{})}
	f.manager.RegisterExecutor(ex)
	r := f.pool.Prepare(context.Background(), "client", launchRequest("stream"))
	_, stream, err := f.pool.ExecuteBound(context.Background(), "client", r.BindingID, r.SessionID, execution.Request{Model: r.Model, Payload: []byte(`{"model":"gpt-6.1-sol"}`)}, execution.Options{Stream: true})
	if err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(2 * launchIdleTTL)
	if state := f.pool.Inspect("client", r.BindingID, r.SessionID); !state.Active {
		t.Fatal("stream evicted")
	}
	f.pool.Release("client", r.BindingID, r.SessionID)
	for range stream.Chunks {
	}
	<-ex.finished
	requireLaunchRefusal(t, f.execute("client", r, r.Model, r.SessionID, false), "binding_invalid")
}

func TestDelegatedLaunchExplicitHeartbeat(t *testing.T) {
	f := newLaunchFixture(t)
	f.add(t, "a", "account")
	r := f.pool.Prepare(context.Background(), "client", launchRequest("heartbeat"))
	if f.pool.Touch("client", r.BindingID, r.SessionID) {
		t.Fatal("heartbeat renewed unused admission")
	}
	if err := f.execute("client", r, r.Model, r.SessionID, false); err != nil {
		t.Fatal(err)
	}
	if f.pool.Touch("attacker", r.BindingID, r.SessionID) || f.pool.Touch("client", r.BindingID, "other") {
		t.Fatal("heartbeat crossed principal/session")
	}
	for i := 0; i < 3; i++ {
		f.now = f.now.Add(20 * time.Minute)
		if !f.pool.Touch("client", r.BindingID, r.SessionID) {
			t.Fatal("explicit established heartbeat failed")
		}
	}
	if err := f.execute("client", r, r.Model, r.SessionID, false); err != nil {
		t.Fatal(err)
	}
	f.pool.Release("client", r.BindingID, r.SessionID)
	if f.pool.Touch("client", r.BindingID, r.SessionID) {
		t.Fatal("heartbeat revived released binding")
	}
}

func TestReviewEstablishedSurvivesOrdinaryTraffic(t *testing.T) {
	f := newLaunchFixture(t)
	f.add(t, "a", "account")
	r := f.pool.Prepare(context.Background(), "client", launchRequest("shared-pool"))
	if err := f.execute("client", r, r.Model, r.SessionID, false); err != nil {
		t.Fatal(err)
	}
	id := t.Name() + "-a"
	before, _ := f.manager.GetByID(id)
	f.manager.MarkResult(context.Background(), Result{AuthID: id, Provider: "codex", Model: r.Model, Success: true})
	after, _ := f.manager.GetByID(id)
	t.Logf("ordinary success generation %d -> %d; credential version %d -> %d; account unchanged=%t token unchanged=%t", before.Generation, after.Generation, before.CredentialVersion, after.CredentialVersion, before.Metadata["account_id"] == after.Metadata["account_id"], before.Metadata["access_token"] == after.Metadata["access_token"])
	if err := f.execute("client", r, r.Model, r.SessionID, false); err != nil {
		t.Fatalf("healthy same-pin second turn after ordinary traffic: %v", err)
	}
}

func TestReviewResumeRejectsStaleProbeRefusalAcknowledgement(t *testing.T) {
	f := newLaunchFixture(t)
	f.add(t, "a", "account")
	r := f.pool.Prepare(context.Background(), "client", launchRequest("refusal-epoch"))
	f.handler = func(w http.ResponseWriter, q *http.Request) { _, _ = w.Write(launchUsage("account", false, 12, f.now)) }
	requireLaunchRefusal(t, f.execute("client", r, r.Model, r.SessionID, false), "usage_limit")
	first := f.pool.Inspect("client", r.BindingID, r.SessionID)
	if !f.pool.Resume("client", r.BindingID, r.SessionID, first.RefusalVersion) {
		t.Fatal("first acknowledgement denied")
	}
	requireLaunchRefusal(t, f.execute("client", r, r.Model, r.SessionID, false), "usage_limit")
	newer := f.pool.Inspect("client", r.BindingID, r.SessionID)
	t.Logf("first attempts=%d newer attempts=%d executor starts=%d; refusal versions %d -> %d", first.ExecutorAttempts, newer.ExecutorAttempts, f.executor.starts.Load(), first.RefusalVersion, newer.RefusalVersion)
	if f.pool.Resume("client", r.BindingID, r.SessionID, first.RefusalVersion) {
		t.Fatal("stale acknowledgement cleared newer pre-executor refusal")
	}
	if first.RefusalVersion == 0 || newer.RefusalVersion <= first.RefusalVersion || newer.ExecutorAttempts != 0 || f.executor.starts.Load() != 0 {
		t.Fatal("pre-executor refusals need distinct versions without execution")
	}
	if !f.pool.Resume("client", r.BindingID, r.SessionID, newer.RefusalVersion) || f.pool.Resume("client", r.BindingID, r.SessionID, newer.RefusalVersion) {
		t.Fatal("fresh acknowledgement must be consumed exactly once")
	}
	f.handler = func(w http.ResponseWriter, q *http.Request) { _, _ = w.Write(launchUsage("account", true, 12, f.now)) }
	if err := f.execute("client", r, r.Model, r.SessionID, false); err != nil {
		t.Fatal(err)
	}
}

func TestDelegatedLaunchEstablishedSurvivesBookkeeping(t *testing.T) {
	f := newLaunchFixture(t)
	f.add(t, "a", "account")
	r := f.pool.Prepare(context.Background(), "client", launchRequest("bookkeeping"))
	execute := func() {
		t.Helper()
		if err := f.execute("client", r, r.Model, r.SessionID, false); err != nil {
			t.Fatal(err)
		}
	}
	execute()
	id := t.Name() + "-a"
	before, _ := f.manager.GetByID(id)
	f.manager.recordAvailabilityNeutralResult(context.Background(), Result{AuthID: id, Provider: "codex", Model: r.Model, Success: false})
	execute()
	job := f.manager.markRefreshPending(nil, id, before.RegistrationEpoch, f.now)
	if job == nil {
		t.Fatal("refresh was not scheduled")
	}
	execute()
	f.manager.finishRefreshJob(job, time.Time{}, false)
	execute()
	a, _ := f.manager.GetByID(id)
	a.Status = StatusError // transient availability, not explicit disable
	a.Metadata["last_refresh"] = f.now.Format(time.RFC3339)
	a.Metadata["expired"] = f.now.Add(time.Hour).Format(time.RFC3339)
	a.Metadata["plan_type"] = "pro"
	a.Metadata["email"] = "synthetic@example.invalid"
	a.Attributes = map[string]string{"plan_type": "pro"}
	after, err := f.manager.Update(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	if after.Generation <= before.Generation || after.CredentialVersion != before.CredentialVersion {
		t.Fatal("fixture must change bookkeeping without replacing credentials")
	}
	execute()
	if f.executor.starts.Load() != 5 {
		t.Fatal("same-pin bookkeeping interrupted execution")
	}
}

func TestDelegatedLaunchEstablishedSecurityChanges(t *testing.T) {
	mutations := map[string]func(*Auth){
		"access-token":    func(a *Auth) { a.Metadata["access_token"] = "synthetic-replacement" },
		"refresh-token":   func(a *Auth) { a.Metadata["refresh_token"] = "synthetic-refresh" },
		"id-token":        func(a *Auth) { a.Metadata["id_token"] = "synthetic-identity" },
		"workspace":       func(a *Auth) { a.Metadata["account_id"] = "other-workspace" },
		"provider":        func(a *Auth) { a.Provider = "other" },
		"auth-mode":       func(a *Auth) { a.Metadata["use_siwc"] = true },
		"endpoint":        func(a *Auth) { a.Attributes["base_url"] = "https://example.invalid" },
		"header":          func(a *Auth) { a.Attributes["header:Authorization"] = "Bearer synthetic-other" },
		"proxy":           func(a *Auth) { a.ProxyURL = "http://example.invalid:8080" },
		"disable":         func(a *Auth) { a.Disabled = true },
		"disabled-status": func(a *Auth) { a.Status = StatusDisabled },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			f := newLaunchFixture(t)
			f.add(t, "a", "account")
			r := f.pool.Prepare(context.Background(), "client", launchRequest("security"))
			if err := f.execute("client", r, r.Model, r.SessionID, false); err != nil {
				t.Fatal(err)
			}
			a, _ := f.manager.GetByID(t.Name() + "-a")
			if a.Attributes == nil {
				a.Attributes = make(map[string]string)
			}
			mutate(a)
			if _, err := f.manager.Update(context.Background(), a); err != nil {
				t.Fatal(err)
			}
			requireLaunchRefusal(t, f.execute("client", r, r.Model, r.SessionID, false), "binding_changed")
			state := f.pool.Inspect("client", r.BindingID, r.SessionID)
			if !f.pool.Resume("client", r.BindingID, r.SessionID, state.RefusalVersion) {
				t.Fatal("fresh refusal acknowledgement denied")
			}
			requireLaunchRefusal(t, f.execute("client", r, r.Model, r.SessionID, false), "binding_changed")
			if f.executor.starts.Load() != 1 {
				t.Fatal("acknowledgement bypassed changed security identity")
			}
		})
	}
}

func TestDelegatedLaunchRemoveReregisterInvalidatesPin(t *testing.T) {
	f := newLaunchFixture(t)
	f.add(t, "a", "account")
	r := f.pool.Prepare(context.Background(), "client", launchRequest("reregister"))
	if err := f.execute("client", r, r.Model, r.SessionID, false); err != nil {
		t.Fatal(err)
	}
	id := t.Name() + "-a"
	f.manager.Remove(context.Background(), id)
	f.add(t, "a", "account")
	requireLaunchRefusal(t, f.execute("client", r, r.Model, r.SessionID, false), "binding_changed")
	if f.executor.starts.Load() != 1 {
		t.Fatal("re-registration restored old pin")
	}
}
