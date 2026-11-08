package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	executor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestIssue6653ReviewSelectedPartition(t *testing.T) {
	for _, outer := range []bool{false, true} {
		for _, rejected := range []bool{false, true} {
			for _, ineffective := range []bool{false, true} {
				t.Run(fmt.Sprintf("outer=%v/rejected=%v/ineffective=%v", outer, rejected, ineffective), func(t *testing.T) {
					e := &issue6653Executor{}
					m, a := issue6653Manager(t, e, nil)
					future := time.Now().Add(2 * time.Hour).Format(time.RFC3339Nano)
					past := time.Now().Add(-time.Hour).Format(time.RFC3339Nano)
					a.Metadata["use_siwc"] = true
					a.Metadata["expires_at"] = future
					a.Metadata["siwc"] = map[string]any{"access_token": "qa-nested-access", "refresh_token": "qa-nested-refresh", "expires_at": future}
					nested := a.Metadata["siwc"].(map[string]any)
					if outer {
						a.Metadata["expires_at"] = past
						if rejected {
							a.Metadata["expires_at"] = future
							a.RejectedAccessToken = "qa-disposable-access"
						}
					} else {
						nested["expires_at"] = past
						if rejected {
							nested["expires_at"] = future
							a.RejectedSIWCAccessToken = "qa-nested-access"
						}
					}
					m.mu.Lock()
					m.auths[a.ID] = a.Clone()
					m.scheduler.upsertAuth(a.Clone())
					m.mu.Unlock()
					ctx := WithCodexClientRequest(context.Background(), outer)
					e.refreshFn = func(_ context.Context, base *Auth) (*Auth, error) {
						u := base.Clone()
						if !ineffective {
							if outer {
								u.Metadata["access_token"] = "qa-new-outer"
								u.Metadata["expires_at"] = future
							} else {
								u.Metadata["siwc"] = map[string]any{"access_token": "qa-new-nested", "refresh_token": "qa-new-nested-refresh", "expires_at": future}
							}
						}
						return u, nil
					}
					fresh, err := m.EnsureAuthFresh(ctx, a)
					if e.refreshes.Load() != 1 {
						t.Fatalf("selected expired/rejected partition refreshes=%d err=%v", e.refreshes.Load(), err)
					}
					if ineffective {
						if err == nil {
							t.Fatal("unchanged expired/rejected selected token admitted")
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					want := "qa-new-nested"
					if outer {
						want = "qa-new-outer"
					}
					if authAccessTokenForRequest(ctx, fresh) != want {
						t.Fatal("wrong selected rotated token")
					}
				})
			}
		}
	}
}

func TestIssue6653ReviewDirectHTTPHeaders(t *testing.T) {
	for _, method := range []string{"prepare", "new", "dispatch"} {
		t.Run(method, func(t *testing.T) {
			e := &issue6653Executor{}
			m, a := issue6653Manager(t, e, nil)
			m.RegisterExecutor(&issue6653HTTPExecutor{e})
			a.Metadata["use_siwc"] = true
			a.Metadata["siwc"] = map[string]any{"access_token": "qa-fresh-nested", "refresh_token": "qa-nested-grant", "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339Nano)}
			if _, err := m.Update(context.Background(), a); err != nil {
				t.Fatal(err)
			}
			calls := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Header.Get("Authorization") != "Bearer qa-rotated-access" {
					t.Error("header-selected direct HTTP token was not rotated")
				}
			}))
			defer upstream.Close()
			req, _ := http.NewRequest(http.MethodGet, upstream.URL, nil)
			req.Header.Set("User-Agent", "codex_cli_rs/1.0")
			switch method {
			case "prepare":
				if err := m.PrepareHttpRequest(context.Background(), a, req); err != nil {
					t.Fatal(err)
				}
			case "new":
				var err error
				req, err = m.NewHttpRequest(context.Background(), a, http.MethodGet, upstream.URL, nil, req.Header)
				if err != nil {
					t.Fatal(err)
				}
			case "dispatch":
				resp, err := m.HttpRequest(context.Background(), a, req)
				if err != nil {
					t.Fatal(err)
				}
				resp.Body.Close()
			}
			if method != "dispatch" && req.Header.Get("Authorization") != "Bearer qa-rotated-access" {
				t.Fatal("prepared HTTP header uses stale partition")
			}
			if e.refreshes.Load() != 1 || method == "dispatch" && calls != 1 {
				t.Fatalf("HTTP selected partition refresh=%d upstream=%d", e.refreshes.Load(), calls)
			}
		})
	}
}

func TestIssue6653ReviewPersistenceOwnership(t *testing.T) {
	for _, kind := range []string{"virtual", "runtime", "config"} {
		for _, hasStore := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/store=%v", kind, hasStore), func(t *testing.T) {
				e := &issue6653Executor{}
				s := &issue6653Store{}
				var store Store
				if hasStore {
					store = s
				}
				m, a := issue6653Manager(t, e, store)
				switch kind {
				case "virtual":
					MarkPluginVirtualAuth(a, "qa-multiple.json", 0)
				case "runtime":
					a.Attributes = map[string]string{"runtime_only": "true"}
				case "config":
					a.Attributes = map[string]string{"auth_kind": "apikey", "source": "config:qa[0]"}
				}
				if _, err := m.Update(context.Background(), a); err != nil {
					t.Fatal(err)
				}
				before := s.saves
				_, err := m.EnsureAuthFresh(context.Background(), a)
				if hasStore {
					if err == nil || e.refreshes.Load() != 0 || s.saves != before {
						t.Fatalf("unsupported ownership rotated/saved: err=%v refresh=%d saves=%d->%d", err, e.refreshes.Load(), before, s.saves)
					}
				} else if err != nil || e.refreshes.Load() != 1 {
					t.Fatalf("documented no-store exception err=%v refresh=%d", err, e.refreshes.Load())
				}
			})
		}
	}
}

func issue6653AssertRedacted(t *testing.T, err error, markers []string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected safe failure")
	}
	for depth := 0; err != nil && depth < 12; depth++ {
		for _, marker := range markers {
			if strings.Contains(err.Error(), marker) {
				t.Fatalf("dummy secret leaked in error chain: %s", err)
			}
		}
		err = errors.Unwrap(err)
	}
	if err != nil {
		t.Fatal("unexpected deep error chain")
	}
}

func TestIssue6653ReviewErrorContainment(t *testing.T) {
	markers := []string{"qa-disposable-access", "qa-disposable-refresh", "QA_BEARER_SECRET", "QA_URL_SECRET", "QA_CODE_SECRET", "qa-known-nested-access", "qa-known-nested-refresh", "qa-known-id-token"}
	raw := "opaque qa-disposable-access qa-disposable-refresh Bearer QA_BEARER_SECRET https://example.invalid/?secret=QA_URL_SECRET code=QA_CODE_SECRET qa-known-nested-access qa-known-nested-refresh qa-known-id-token"
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			e := &issue6653Executor{}
			m, a := issue6653Manager(t, e, nil)
			a.Metadata["siwc"] = map[string]any{"access_token": "qa-known-nested-access", "refresh_token": "qa-known-nested-refresh"}
			a.Metadata["id_token"] = "qa-known-id-token"
			if _, err := m.Update(context.Background(), a); err != nil {
				t.Fatal(err)
			}
			cause := &Error{Code: "qa_failure", HTTPStatus: 503, Message: raw}
			e.refreshFn = func(context.Context, *Auth) (*Auth, error) { return nil, cause }
			for range 2 {
				var err error
				if stream {
					_, err = m.ExecuteStream(context.Background(), []string{e.Identifier()}, executor.Request{Model: "qa-model-6653"}, executor.Options{})
				} else {
					_, err = issue6653Execute(m)
				}
				issue6653AssertRedacted(t, err, markers)
				if errors.Is(err, cause) {
					t.Fatal("raw cause retained")
				}
				var safe *Error
				if !errors.As(err, &safe) || safe.Code != "qa_failure" || safe.HTTPStatus != 503 {
					t.Fatalf("safe status/code lost: %v", err)
				}
			}
			if e.refreshes.Load() != 1 || e.calls.Load() != 0 {
				t.Fatal("cached redacted failure failed to suppress rotation/inference")
			}
		})
	}
	t.Run("persistence", func(t *testing.T) {
		s := &issue6653SecretStore{raw: raw}
		e := &issue6653Executor{}
		m, a := issue6653Manager(t, e, s)
		s.fail = true
		hook := setupTestLoggerHook(t)
		_, err := m.EnsureAuthFresh(context.Background(), a)
		issue6653AssertRedacted(t, err, append(markers, "qa-rotated-access", "qa-rotated-refresh"))
		var safe *Error
		if !errors.As(err, &safe) || safe.Code != "refresh_persistence_failed" || safe.HTTPStatus != 503 {
			t.Fatalf("persistence status/code lost: %v", err)
		}
		for _, entry := range hook.AllEntries() {
			for _, marker := range markers {
				if strings.Contains(fmt.Sprint(entry.Message, entry.Data), marker) {
					t.Fatal("dummy secret leaked in persistence log")
				}
			}
		}
	})
}

type issue6653SecretStore struct {
	issue6653Store
	raw string
}

func (s *issue6653SecretStore) Save(ctx context.Context, a *Auth) (string, error) {
	if s.fail {
		return "", errors.New(s.raw + " qa-rotated-access qa-rotated-refresh")
	}
	return s.issue6653Store.Save(ctx, a)
}

func TestIssue6653ReviewCancellationWhileWaiting(t *testing.T) {
	e := &issue6653Executor{}
	m, a := issue6653Manager(t, e, nil)
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	e.refreshFn = func(_ context.Context, b *Auth) (*Auth, error) {
		close(started)
		<-release
		return issue6653Rotated(b), nil
	}
	go func() { _, err := m.EnsureAuthFresh(context.Background(), a); done <- err }()
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	waiter := make(chan error, 1)
	entered := make(chan struct{})
	go func() { close(entered); _, err := m.EnsureAuthFresh(ctx, a); waiter <- err }()
	<-entered
	cancel()
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-waiter; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled concurrent caller=%v", err)
	}
	if e.refreshes.Load() != 1 {
		t.Fatal("cancelled concurrent caller rotated grant")
	}
}

func TestIssue6653ReviewRejectedOuterNoOpBlocksDispatch(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			e := &issue6653Executor{}
			m, a := issue6653Manager(t, e, nil)
			a.Metadata["expires_at"] = time.Now().Add(time.Hour).Format(time.RFC3339Nano)
			a.Metadata["use_siwc"] = true
			a.Metadata["siwc"] = map[string]any{"access_token": "qa-fresh-nested", "refresh_token": "qa-nested-grant", "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339Nano)}
			a.RejectedAccessToken = "qa-disposable-access"
			m.mu.Lock()
			m.auths[a.ID] = a.Clone()
			m.scheduler.upsertAuth(a.Clone())
			m.mu.Unlock()
			e.refreshFn = func(_ context.Context, a *Auth) (*Auth, error) { return a.Clone(), nil }
			ctx := WithCodexClientRequest(context.Background(), true)
			selected := ""
			e.executeFn = func(a *Auth) error { selected = authAccessTokenForRequest(ctx, a); return nil }
			var err error
			if stream {
				_, err = m.ExecuteStream(ctx, []string{e.Identifier()}, executor.Request{Model: "qa-model-6653"}, executor.Options{})
			} else {
				_, err = m.Execute(ctx, []string{e.Identifier()}, executor.Request{Model: "qa-model-6653"}, executor.Options{})
			}
			if err == nil || e.calls.Load() != 0 {
				t.Fatalf("unchanged rejected outer token dispatched: error=%v refresh=%d inference=%d selected=%s", err, e.refreshes.Load(), e.calls.Load(), selected)
			}
		})
	}
}
