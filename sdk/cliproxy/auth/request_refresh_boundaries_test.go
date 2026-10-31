package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type requestRefreshPreparer struct {
	*demandTestExecutor
	preparations atomic.Int32
}

func (e *requestRefreshPreparer) PrepareRequest(req *http.Request, auth *Auth) error {
	e.preparations.Add(1)
	req.Header.Set("Authorization", "Bearer "+authAccessToken(auth))
	return nil
}

func TestRequestRefreshInjectCredentialsFreshness(t *testing.T) {
	for _, mode := range []string{"expired", "fresh", "failure", "canceled", "opt-out", "missing"} {
		t.Run(mode, func(t *testing.T) {
			expiry := time.Now().Add(-time.Hour)
			if mode == "fresh" {
				expiry = time.Now().Add(time.Hour)
			}
			m, auth, _, executor := demandManager(t, expiry)
			preparer := &requestRefreshPreparer{demandTestExecutor: executor}
			m.RegisterExecutor(preparer)
			failure := errors.New("synthetic acquisition failure")
			executor.refresh = func(_ context.Context, next *Auth) (*Auth, error) {
				if mode == "failure" {
					return nil, failure
				}
				next.Metadata["access_token"] = "new"
				next.Metadata["refresh_token"] = "new-refresh"
				next.Metadata["expired"] = time.Now().Add(time.Hour).Format(time.RFC3339Nano)
				return next, nil
			}
			if mode == "opt-out" {
				auth.RefreshPolicy = pluginapi.RefreshDefault
				if _, err := m.Update(context.Background(), auth); err != nil {
					t.Fatal(err)
				}
			}
			var inference atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				inference.Add(1)
				want := "Bearer old"
				if mode == "expired" {
					want = "Bearer new"
				}
				if req.Header.Get("Authorization") != want {
					t.Errorf("upstream bearer=%q want %q", req.Header.Get("Authorization"), want)
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()
			ctx := context.Background()
			if mode == "canceled" {
				canceled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = canceled
			}
			req, err := http.NewRequestWithContext(ctx, "GET", server.URL, nil)
			if err != nil {
				t.Fatal(err)
			}
			id := auth.ID
			if mode == "missing" {
				id = "not-registered"
			}
			err = m.InjectCredentials(req, id)
			if mode == "failure" || mode == "canceled" {
				want := failure
				if mode == "canceled" {
					want = context.Canceled
				}
				if !errors.Is(err, want) || preparer.preparations.Load() != 0 || inference.Load() != 0 {
					t.Fatalf("failed acquisition dispatched: err=%v preparations=%d inference=%d", err, preparer.preparations.Load(), inference.Load())
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if mode == "missing" {
				if preparer.preparations.Load() != 0 || req.Header.Get("Authorization") != "" {
					t.Fatal("missing auth changed request")
				}
				return
			}
			resp, err := server.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			wantRefresh := int32(0)
			if mode == "expired" {
				wantRefresh = 1
			}
			if executor.refreshes.Load() != wantRefresh || preparer.preparations.Load() != 1 || inference.Load() != 1 {
				t.Fatalf("refreshes=%d preparations=%d inference=%d", executor.refreshes.Load(), preparer.preparations.Load(), inference.Load())
			}
		})
	}
}

func TestRequestRefreshExecutorRouteCoherence(t *testing.T) {
	for _, consumer := range []string{"buffered", "count", "stream"} {
		t.Run(consumer, func(t *testing.T) {
			for _, mode := range []string{"managed-change", "managed-stable", "opt-out-change"} {
				t.Run(mode, func(t *testing.T) {
					m, auth, _, executor := demandManager(t, time.Now().Add(time.Hour))
					if mode == "opt-out-change" {
						auth.RefreshPolicy = pluginapi.RefreshDefault
						if _, err := m.Update(context.Background(), auth); err != nil {
							t.Fatal(err)
						}
					}
					var intercepts atomic.Int32
					opts := cliproxyexecutor.Options{RequestAfterAuthInterceptor: func(_ context.Context, _ cliproxyexecutor.RequestAfterAuthInterceptRequest) cliproxyexecutor.RequestAfterAuthInterceptResponse {
						intercepts.Add(1)
						current, found := m.GetByID(auth.ID)
						if !found {
							t.Fatal("interceptor lost selected registration")
						}
						if current.Attributes == nil {
							current.Attributes = make(map[string]string)
						}
						if mode != "managed-stable" {
							current.Attributes["compat_name"] = "new-unregistered-executor"
						}
						if _, err := m.Update(context.Background(), current); err != nil {
							t.Fatal(err)
						}
						return cliproxyexecutor.RequestAfterAuthInterceptResponse{}
					}}
					req := cliproxyexecutor.Request{Model: "demand-model"}
					var err error
					switch consumer {
					case "buffered":
						_, err = m.Execute(context.Background(), []string{auth.Provider}, req, opts)
					case "count":
						_, err = m.ExecuteCount(context.Background(), []string{auth.Provider}, req, opts)
					case "stream":
						var stream *cliproxyexecutor.StreamResult
						stream, err = m.ExecuteStream(context.Background(), []string{auth.Provider}, req, opts)
						if stream != nil {
							for range stream.Chunks {
							}
						}
					}
					if intercepts.Load() != 1 || executor.refreshes.Load() != 0 {
						t.Fatalf("wrong lane: intercepts=%d refreshes=%d", intercepts.Load(), executor.refreshes.Load())
					}
					if mode == "managed-change" {
						if err == nil || executor.calls.Load() != 0 {
							t.Fatalf("captured executor dispatched changed route: err=%v calls=%d", err, executor.calls.Load())
						}
					} else if err != nil || executor.calls.Load() != 1 {
						t.Fatalf("valid or legacy route changed: err=%v calls=%d", err, executor.calls.Load())
					}
				})
			}
		})
	}
}

func TestRequestRefreshFailedForcePreservesFreshCredential(t *testing.T) {
	for _, mode := range []string{"fresh", "near-expiry", "expired", "rejected", "invalid-success", "save-failure"} {
		t.Run(mode, func(t *testing.T) {
			expiry := time.Now().Add(time.Hour)
			if mode == "near-expiry" {
				expiry = time.Now().Add(30 * time.Second)
			}
			if mode == "expired" {
				expiry = time.Now().Add(-time.Hour)
			}
			m, auth, store, executor := demandManager(t, expiry)
			failure := errors.New("synthetic force transient failure")
			if mode == "rejected" {
				m.markRejectedAccessToken(auth.ID, "old")
			}
			if mode == "save-failure" {
				store.mu.Lock()
				store.fail = failure
				store.mu.Unlock()
			} else {
				executor.refresh = func(context.Context, *Auth) (*Auth, error) {
					if mode == "invalid-success" {
						return nil, nil
					}
					return nil, failure
				}
			}
			_, err := m.ForceRefreshAuth(context.Background(), auth.ID)
			if mode == "invalid-success" {
				if err == nil {
					t.Fatal("force accepted invalid successful result")
				}
			} else if !errors.Is(err, failure) {
				t.Fatalf("force lost actual failure: %v", err)
			}
			fresh, err := m.EnsureRequestAuth(context.Background(), auth, "")
			if mode == "fresh" {
				if err != nil || fresh == nil || fresh.Metadata["access_token"] != "old" {
					t.Fatalf("voluntary force failure poisoned fresh credential: fresh=%v err=%v", fresh != nil, err)
				}
			} else if err == nil || fresh != nil {
				t.Fatal("due, rejected, or unsettled credential admitted after failed force")
			}
			_, err = m.Execute(context.Background(), []string{auth.Provider}, cliproxyexecutor.Request{Model: "demand-model"}, cliproxyexecutor.Options{})
			if mode == "fresh" {
				if err != nil || executor.calls.Load() != 1 {
					t.Fatalf("fresh old bearer did not execute: err=%v calls=%d", err, executor.calls.Load())
				}
			} else if err == nil || executor.calls.Load() != 0 {
				t.Fatalf("failed credential reached inference: err=%v calls=%d", err, executor.calls.Load())
			}
			if executor.refreshes.Load() != 1 {
				t.Fatalf("demand retried force during backoff: refreshes=%d", executor.refreshes.Load())
			}
		})
	}
}

func TestRequestRefreshInvalidRotationNeverReplaysGrant(t *testing.T) {
	for _, mode := range []string{"expired", "missing-expiry", "invalid-expiry", "nil", "unchanged-expired", "contradictory-bearer"} {
		t.Run(mode, func(t *testing.T) {
			m, auth, store, executor := demandManager(t, time.Now().Add(-time.Hour))
			executor.refresh = func(_ context.Context, next *Auth) (*Auth, error) {
				if mode == "nil" {
					return nil, nil
				}
				if mode == "unchanged-expired" {
					return next, nil
				}
				next.Metadata["access_token"] = "unusable-rotation"
				next.Metadata["refresh_token"] = "consumed-grant-rotated"
				switch mode {
				case "missing-expiry":
					delete(next.Metadata, "expired")
				case "invalid-expiry":
					next.Metadata["expired"] = "not-a-date"
				case "contradictory-bearer":
					next.Metadata["expired"] = time.Now().Add(time.Hour).Format(time.RFC3339Nano)
					next.Attributes = map[string]string{"api_key": "wrong-bearer"}
				}
				return next, nil
			}
			if _, err := m.EnsureRequestAuth(context.Background(), auth, ""); err == nil {
				t.Fatal("unusable successful rotation was accepted")
			}
			if _, err := m.ForceRefreshAuth(context.Background(), auth.ID); err == nil || executor.refreshes.Load() != 1 {
				t.Fatalf("force replayed consumed grant: err=%v refreshes=%d", err, executor.refreshes.Load())
			}
			state := m.authRefreshState(auth.ID)
			state.stateMu.Lock()
			if state.outcome != nil {
				state.outcome.retryAt = time.Now().Add(-time.Hour)
			}
			state.stateMu.Unlock()
			current, _ := m.GetByID(auth.ID)
			if _, err := m.EnsureRequestAuth(context.Background(), current, ""); err == nil || executor.refreshes.Load() != 1 {
				t.Fatalf("later demand replayed consumed grant: err=%v refreshes=%d", err, executor.refreshes.Load())
			}
			current.RefreshPolicy = pluginapi.RefreshDefault
			if _, err := m.Update(context.Background(), current); err != nil {
				t.Fatal(err)
			}
			if _, err := m.ForceRefreshAuth(context.Background(), auth.ID); err == nil || executor.refreshes.Load() != 1 {
				t.Fatalf("policy clear replayed consumed grant: err=%v refreshes=%d", err, executor.refreshes.Load())
			}
			current, _ = m.GetByID(auth.ID)
			if _, err := m.Register(context.Background(), current); err != nil {
				t.Fatal(err)
			}
			if _, err := m.ForceRefreshAuth(context.Background(), auth.ID); err == nil || executor.refreshes.Load() != 1 {
				t.Fatalf("same-pair registration replayed consumed grant: err=%v refreshes=%d", err, executor.refreshes.Load())
			}
			if err := m.Load(context.Background()); err != nil {
				t.Fatal(err)
			}
			current, _ = m.GetByID(auth.ID)
			if _, err := m.EnsureRequestAuth(context.Background(), current, ""); err == nil || executor.refreshes.Load() != 1 {
				t.Fatalf("reload waived invalid-rotation hold: err=%v refreshes=%d", err, executor.refreshes.Load())
			}
			_, err := m.Execute(context.Background(), []string{auth.Provider}, cliproxyexecutor.Request{Model: "demand-model"}, cliproxyexecutor.Options{})
			if err == nil || executor.calls.Load() != 0 {
				t.Fatal("quarantined credential reached inference")
			}
			current, _ = m.GetByID(auth.ID)
			current.RefreshPolicy = pluginapi.RefreshOnDemand
			current.Metadata["access_token"] = "distinct-login"
			current.Metadata["refresh_token"] = "distinct-login-refresh"
			current.Metadata["expired"] = time.Now().Add(time.Hour).Format(time.RFC3339Nano)
			login, err := m.Register(context.Background(), current)
			if err != nil {
				t.Fatal(err)
			}
			fresh, err := m.EnsureRequestAuth(context.Background(), login, "")
			if err != nil || fresh.Metadata["access_token"] != "distinct-login" || executor.refreshes.Load() != 1 {
				t.Fatalf("distinct login did not recover: err=%v refreshes=%d", err, executor.refreshes.Load())
			}
			store.mu.Lock()
			persisted := store.items[0].Clone()
			store.mu.Unlock()
			if persisted.Metadata["access_token"] != "distinct-login" {
				t.Fatal("distinct login was not saved")
			}
		})
	}
}
