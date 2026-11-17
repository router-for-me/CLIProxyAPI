package auth

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	logging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	executor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type receiptIdentityExecutor struct {
	started   chan struct{}
	release   chan struct{}
	status    int
	fallback  bool
	prepare   bool
	retry     bool
	bootstrap bool
	attempts  atomic.Int32
}

func (*receiptIdentityExecutor) Identifier() string { return "codex" }
func (e *receiptIdentityExecutor) respond(ctx context.Context, a *Auth) error {
	if e.retry && e.attempts.Add(1) == 1 {
		return &Error{HTTPStatus: 401, Message: "expired access token"}
	}
	close(e.started)
	<-e.release
	logging.SetResponseHeaders(ctx, http.Header{"X-Codex-Primary-Used-Percent": {"91"}, "X-Codex-Primary-Window-Minutes": {"10080"}, "X-Codex-Primary-Reset-At": {strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)}})
	if e.status == 429 {
		return &Error{HTTPStatus: 429, Message: `{"error":{"type":"usage_limit_reached"}}`}
	}
	return nil
}
func (e *receiptIdentityExecutor) Execute(ctx context.Context, a *Auth, _ executor.Request, _ executor.Options) (executor.Response, error) {
	err := e.respond(ctx, a)
	return executor.Response{Payload: []byte(`{"status":"completed","output":[]}`)}, err
}
func (e *receiptIdentityExecutor) CountTokens(ctx context.Context, a *Auth, r executor.Request, o executor.Options) (executor.Response, error) {
	return e.Execute(ctx, a, r, o)
}
func (e *receiptIdentityExecutor) ExecuteStream(ctx context.Context, a *Auth, _ executor.Request, _ executor.Options) (*executor.StreamResult, error) {
	if e.bootstrap && e.attempts.Add(1) == 1 {
		ch := make(chan executor.StreamChunk, 1)
		ch <- executor.StreamChunk{Err: &Error{HTTPStatus: 401, Message: "bootstrap expired access token"}}
		close(ch)
		return &executor.StreamResult{Chunks: ch}, nil
	}
	if e.fallback {
		return nil, &Error{HTTPStatus: 501, Message: "stream unsupported"}
	}
	if err := e.respond(ctx, a); err != nil {
		return nil, err
	}
	ch := make(chan executor.StreamChunk, 1)
	ch <- executor.StreamChunk{Payload: []byte("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")}
	close(ch)
	return &executor.StreamResult{Chunks: ch}, nil
}
func (*receiptIdentityExecutor) Refresh(_ context.Context, a *Auth) (*Auth, error) {
	a = a.Clone()
	a.Metadata["account_id"] = "refreshed-account"
	a.Metadata["access_token"] = "refreshed-bearer"
	return a, nil
}
func (*receiptIdentityExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("unused")
}

func TestQuotaReceiptDispatchedAccount(t *testing.T) {
	for _, mode := range []string{"execute", "execute_retry", "prepared", "stream", "stream_retry", "stream_bootstrap_retry", "verify", "verify_fallback", "count_tokens"} {
		for _, status := range []int{200, 429} {
			for _, replacement := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/status%d/replaced=%v", mode, status, replacement), func(t *testing.T) {
					prior := quotaCooldownDisabled.Load()
					quotaCooldownDisabled.Store(false)
					t.Cleanup(func() { quotaCooldownDisabled.Store(prior) })
					ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
					defer cancel()
					m := NewManager(nil, nil, nil)
					e := &receiptIdentityExecutor{started: make(chan struct{}), release: make(chan struct{}), status: status, fallback: mode == "verify_fallback", prepare: mode == "prepared", retry: mode == "execute_retry" || mode == "stream_retry", bootstrap: mode == "stream_bootstrap_retry"}
					m.RegisterExecutor(e)
					model := "gpt-receipt-identity"
					a, err := m.Register(ctx, &Auth{ID: t.Name(), Provider: "codex", Status: StatusActive, Metadata: map[string]any{"account_id": "A", "access_token": "same-bearer", "refresh_token": "refresh"}})
					if err != nil {
						t.Fatal(err)
					}
					registry.GetGlobalRegistry().RegisterClient(a.ID, "codex", []*registry.ModelInfo{{ID: model}})
					t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(a.ID) })
					done := make(chan error, 1)
					go func() {
						switch mode {
						case "execute", "execute_retry", "prepared":
							_, err = m.Execute(ctx, []string{"codex"}, executor.Request{Model: model}, executor.Options{})
						case "stream", "stream_retry", "stream_bootstrap_retry":
							var stream *executor.StreamResult
							stream, err = m.ExecuteStream(ctx, []string{"codex"}, executor.Request{Model: model}, executor.Options{Stream: true})
							if stream != nil {
								for range stream.Chunks {
								}
							}
						case "verify", "verify_fallback":
							_, err = m.VerifyInference(ctx, a.ID, model)
						case "count_tokens":
							_, err = m.ExecuteCount(ctx, []string{"codex"}, executor.Request{Model: model}, executor.Options{})
						}
						done <- err
					}()
					select {
					case <-e.started:
					case <-ctx.Done():
						t.Fatal("dispatch did not start")
					}
					updated, _ := m.GetByID(a.ID)
					if replacement {
						updated.Metadata["account_id"] = "C"
					} else {
						updated.Metadata["unrelated"] = "updated"
					}
					var errUpdate error
					if replacement {
						_, errUpdate = m.Update(ctx, updated)
					} else {
						_, errUpdate = m.UpdateRefreshedAuth(ctx, updated.Clone(), updated)
					}
					if errUpdate != nil {
						t.Fatal(errUpdate)
					}
					close(e.release)
					select {
					case <-done:
					case <-ctx.Done():
						t.Fatal("dispatch did not finish")
					}
					got, _ := m.GetByID(a.ID)
					observed := len(got.Quota.Signals) > 0 || !got.QuotaResetSchedule.WeeklyResetAt.IsZero()
					want := !replacement && mode != "count_tokens"
					if observed != want {
						t.Errorf("passive observation=%v want%v: late receipt must follow dispatched account", observed, want)
					}
					if state := got.ModelStates[model]; state != nil && len(state.Quota.Signals) > 0 && !want {
						t.Error("old-account model signals revived")
					}
					if status == 429 && (!got.Unavailable || got.LastError == nil || got.LastError.HTTPStatus != 429) {
						t.Error("identity fence dropped ordinary cooldown result")
					}
				})
			}
		}
	}
}

func TestQuotaReceiptManualObservationCompatibility(t *testing.T) {
	m := NewManager(nil, nil, nil)
	a, err := m.Register(context.Background(), &Auth{ID: t.Name(), Provider: "codex", Status: StatusActive})
	if err != nil {
		t.Fatal(err)
	}
	ctx := logging.WithResponseHeadersHolder(context.Background())
	logging.SetResponseHeaders(ctx, http.Header{"X-Codex-Primary-Window-Minutes": {"10080"}, "X-Codex-Primary-Reset-At": {strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)}})
	m.MarkResult(ctx, Result{AuthID: a.ID, Provider: a.Provider, Success: true})
	a, _ = m.GetByID(a.ID)
	if a.QuotaResetSchedule.WeeklyResetAt.IsZero() {
		t.Fatal("unstamped direct MarkResult observation was silently disabled")
	}
}

func (e *receiptIdentityExecutor) ShouldPrepareRequestAuth(a *Auth) bool {
	return e.prepare && a.Metadata["account_id"] == "A"
}
func (e *receiptIdentityExecutor) PrepareRequestAuth(_ context.Context, a *Auth) (*Auth, error) {
	a = a.Clone()
	a.Metadata["account_id"] = "prepared-account"
	return a, nil
}

func TestQuotaReceiptNewerAccountObservation(t *testing.T) {
	for _, mode := range []string{"execute", "stream"} {
		for _, status := range []int{200, 429} {
			t.Run(fmt.Sprintf("%s/status%d", mode, status), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				m := NewManager(nil, nil, nil)
				e := &receiptIdentityExecutor{started: make(chan struct{}), release: make(chan struct{}), status: status}
				m.RegisterExecutor(e)
				model := "gpt-newer-account"
				a, err := m.Register(ctx, &Auth{ID: t.Name(), Provider: "codex", Status: StatusActive, Metadata: map[string]any{"account_id": "A", "access_token": "same"}})
				if err != nil {
					t.Fatal(err)
				}
				registry.GetGlobalRegistry().RegisterClient(a.ID, "codex", []*registry.ModelInfo{{ID: model}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(a.ID) })
				done := make(chan struct{})
				go func() {
					defer close(done)
					if mode == "execute" {
						_, _ = m.Execute(ctx, []string{"codex"}, executor.Request{Model: model}, executor.Options{})
					} else {
						stream, _ := m.ExecuteStream(ctx, []string{"codex"}, executor.Request{Model: model}, executor.Options{Stream: true})
						if stream != nil {
							for range stream.Chunks {
							}
						}
					}
				}()
				select {
				case <-e.started:
				case <-ctx.Done():
					t.Fatal("old dispatch did not start")
				}
				updated := a.Clone()
				updated.Metadata["account_id"] = "B"
				if _, err = m.Update(ctx, updated); err != nil {
					t.Fatal(err)
				}
				bctx := logging.WithResponseHeadersHolder(ctx)
				reset := time.Now().Add(2 * time.Hour).Unix()
				logging.SetResponseHeaders(bctx, http.Header{"X-Codex-Primary-Used-Percent": {"20"}, "X-Codex-Primary-Window-Minutes": {"10080"}, "X-Codex-Primary-Reset-At": {strconv.FormatInt(reset, 10)}})
				m.MarkResult(bctx, Result{AuthID: a.ID, Provider: "codex", Model: model, Success: true})
				close(e.release)
				select {
				case <-done:
				case <-ctx.Done():
					t.Fatal("old dispatch did not finish")
				}
				got, _ := m.GetByID(a.ID)
				if got.Quota.Signals["X-Codex-Primary-Used-Percent"] != "20" || got.QuotaResetSchedule.WeeklyResetAt.Unix() != reset || got.ModelStates[model].Quota.Signals["X-Codex-Primary-Used-Percent"] != "20" {
					t.Fatal("old receipt replaced the new account's accepted observation")
				}
			})
		}
	}
}

func TestQuotaReceiptIdentityFallback(t *testing.T) {
	base := &Auth{Provider: "codex", Metadata: map[string]any{"access_token": "one", "refresh_token": "refresh"}}
	ctx := withQuotaObservationIdentity(context.Background(), base)
	if !quotaObservationIdentityMatches(ctx, base.Clone()) {
		t.Fatal("unchanged token identity rejected")
	}
	changed := base.Clone()
	changed.Metadata["access_token"] = "other"
	if quotaObservationIdentityMatches(ctx, changed) {
		t.Fatal("missing account identity ignored changed token fallback")
	}
	changed = base.Clone()
	changed.Provider = "claude"
	if quotaObservationIdentityMatches(ctx, changed) {
		t.Fatal("provider mismatch accepted")
	}
	explicit := base.Clone()
	explicit.Metadata["account_id"] = "same-account"
	ctx = withQuotaObservationIdentity(context.Background(), explicit)
	changed = explicit.Clone()
	changed.Metadata["access_token"] = "rotated"
	if !quotaObservationIdentityMatches(ctx, changed) {
		t.Fatal("same account rotation rejected by token fallback")
	}
	explicit.Metadata["account_id"] = "different"
	if quotaObservationIdentityMatches(ctx, explicit) {
		t.Fatal("dispatch context retained mutable identity instead of immutable values")
	}
}
