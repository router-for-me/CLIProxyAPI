package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type demandVerificationExecutor struct {
	*demandTestExecutor
	streamCalls   atomic.Int32
	bufferedCalls atomic.Int32
	check         func(*Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) error
	beforeStream  func()
	streamErr     error
	incomplete    bool
}

func (e *demandVerificationExecutor) ExecuteStream(_ context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.streamCalls.Add(1)
	if e.check != nil {
		if err := e.check(auth, req, opts); err != nil {
			return nil, err
		}
	}
	if e.beforeStream != nil {
		e.beforeStream()
	}
	if e.streamErr != nil {
		return nil, e.streamErr
	}
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte(`{"type":"response.completed","response":{"status":"completed"}}`), Incomplete: e.incomplete}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *demandVerificationExecutor) Execute(_ context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.bufferedCalls.Add(1)
	if e.check != nil {
		if err := e.check(auth, req, opts); err != nil {
			return cliproxyexecutor.Response{}, err
		}
	}
	return cliproxyexecutor.Response{Payload: []byte(`{"status":"completed","output":[{"type":"message"}]}`), Incomplete: e.incomplete}, nil
}

func demandVerificationCheck(t *testing.T, id string, store *demandTestStore, managed bool) func(*Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) error {
	t.Helper()
	return func(auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) error {
		if auth.ID != id {
			return errors.New("verification changed account")
		}
		if managed && requestRefreshDue(auth, time.Now()) {
			return errors.New("verification dispatched without managed freshness")
		}
		store.mu.Lock()
		saved := store.items[0].Clone()
		store.mu.Unlock()
		if managed && authAccessToken(saved) != authAccessToken(auth) {
			return errors.New("verification dispatched before Save")
		}
		var payload map[string]any
		if err := json.Unmarshal(req.Payload, &payload); err != nil {
			return err
		}
		if payload["max_output_tokens"] != float64(1) || payload["store"] != false || !strings.Contains(string(req.Payload), `"text":"ping"`) || payload["model"] != req.Model || !strings.HasPrefix(opts.Headers.Get("X-Session-Id"), "verify-") {
			return errors.New("verification request lost minimal isolated payload")
		}
		return nil
	}
}

func TestRequestRefreshVerificationFreshness(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		for _, boundary := range []string{"expired", "near", "fresh"} {
			t.Run(boundary+map[bool]string{false: "/stream", true: "/buffered"}[fallback], func(t *testing.T) {
				expiry := time.Now().Add(time.Hour)
				if boundary == "expired" {
					expiry = time.Now().Add(-time.Hour)
				}
				if boundary == "near" {
					expiry = time.Now().Add(30 * time.Second)
				}
				m, a, store, e := demandManager(t, expiry)
				verify := &demandVerificationExecutor{demandTestExecutor: e, check: demandVerificationCheck(t, a.ID, store, true)}
				if fallback {
					verify.streamErr = &Error{HTTPStatus: http.StatusNotImplemented, Message: "synthetic stream unsupported"}
				}
				m.RegisterExecutor(verify)
				current, err := m.VerifyInference(context.Background(), a.ID, "demand-model")
				wantRefresh := int32(1)
				if boundary == "fresh" {
					wantRefresh = 0
				}
				wantBuffered := int32(0)
				if fallback {
					wantBuffered = 1
				}
				if err != nil || current == nil || !current.InferenceVerified || verify.streamCalls.Load() != 1 || verify.bufferedCalls.Load() != wantBuffered || e.refreshes.Load() != wantRefresh {
					t.Fatalf("verification freshness: err=%v refresh=%d stream=%d buffered=%d", err, e.refreshes.Load(), verify.streamCalls.Load(), verify.bufferedCalls.Load())
				}
				if boundary != "fresh" && (!current.RenewalVerified || current.CredentialVersion <= a.CredentialVersion || authAccessToken(current) != "new") {
					t.Fatal("verification did not stamp committed rotation")
				}
			})
		}
	}
}

func TestRequestRefreshVerificationFallbackRechecksCurrent(t *testing.T) {
	for _, mutation := range []string{"expiry", "route", "disabled", "quota", "model", "login", "registration", "sign-in"} {
		t.Run(mutation, func(t *testing.T) {
			m, a, store, e := demandManager(t, time.Now().Add(time.Hour))
			verify := &demandVerificationExecutor{demandTestExecutor: e, check: demandVerificationCheck(t, a.ID, store, true), streamErr: &Error{HTTPStatus: 501, Message: "synthetic stream unsupported"}}
			verify.beforeStream = func() {
				current, _ := m.GetByID(a.ID)
				switch mutation {
				case "expiry":
					current.Metadata["expired"] = time.Now().Add(30 * time.Second).Format(time.RFC3339Nano)
				case "route":
					current.Attributes = map[string]string{"compat_name": "changed-route", "provider_key": "changed-route"}
				case "disabled":
					current.Disabled = true
				case "quota":
					current.Quota = QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: time.Now().Add(time.Hour)}
					current.Unavailable = true
				case "model":
					registry.GetGlobalRegistry().RegisterClient(a.ID, a.Provider, []*registry.ModelInfo{{ID: "different-model"}})
				case "login", "registration":
					current.Metadata["access_token"] = "fallback-login-access"
					current.Metadata["refresh_token"] = "fallback-login-refresh"
				case "sign-in":
					m.mu.Lock()
					m.auths[a.ID].RequiresSignIn = true
					m.mu.Unlock()
					return
				}
				var err error
				if mutation == "registration" {
					_, err = m.Register(context.Background(), current)
				} else {
					_, err = m.Update(context.Background(), current)
				}
				if err != nil {
					t.Error(err)
				}
			}
			m.RegisterExecutor(verify)
			current, err := m.VerifyInference(context.Background(), a.ID, "demand-model")
			allow := mutation == "expiry" || mutation == "login"
			if allow {
				want := int32(0)
				if mutation == "expiry" {
					want = 1
				}
				if err != nil || current == nil || !current.InferenceVerified || verify.bufferedCalls.Load() != 1 || e.refreshes.Load() != want {
					t.Fatalf("fallback did not use current snapshot: err=%v buffered=%d refresh=%d", err, verify.bufferedCalls.Load(), e.refreshes.Load())
				}
				if mutation == "login" && authAccessToken(current) != "fallback-login-access" {
					t.Fatal("fallback verified stale login")
				}
			} else if err == nil || verify.bufferedCalls.Load() != 0 || e.refreshes.Load() != 0 {
				t.Fatalf("fallback dispatched obsolete credential/model: err=%v buffered=%d refresh=%d", err, verify.bufferedCalls.Load(), e.refreshes.Load())
			}
		})
	}
}

func TestRequestRefreshVerificationAcquisitionFailureStopsDispatch(t *testing.T) {
	for _, failSave := range []bool{false, true} {
		t.Run(map[bool]string{false: "provider", true: "save"}[failSave], func(t *testing.T) {
			m, a, store, e := demandManager(t, time.Now().Add(30*time.Second))
			failure := &Error{HTTPStatus: 503, Message: "synthetic acquisition failure"}
			if failSave {
				store.mu.Lock()
				store.fail = failure
				store.mu.Unlock()
			} else {
				e.refresh = func(context.Context, *Auth) (*Auth, error) { return nil, failure }
			}
			verify := &demandVerificationExecutor{demandTestExecutor: e}
			m.RegisterExecutor(verify)
			for i := 0; i < 2; i++ {
				current, err := m.VerifyInference(context.Background(), a.ID, "demand-model")
				if !errors.Is(err, failure) || current == nil || current.InferenceVerified || current.RenewalVerified || verify.streamCalls.Load() != 0 || verify.bufferedCalls.Load() != 0 || e.refreshes.Load() != 1 {
					t.Fatalf("acquisition did not stop verifier: err=%v refresh=%d stream=%d buffered=%d", err, e.refreshes.Load(), verify.streamCalls.Load(), verify.bufferedCalls.Load())
				}
			}
		})
	}
}

func TestRequestRefreshVerificationPreservesEarlyGatesAndOptOut(t *testing.T) {
	for _, mode := range []string{"disabled", "sign-in", "opt-out", "incomplete-stream", "incomplete-buffered", "non-501"} {
		t.Run(mode, func(t *testing.T) {
			m, a, store, e := demandManager(t, time.Now().Add(30*time.Second))
			if mode == "disabled" {
				a.Disabled = true
				_, _ = m.Update(context.Background(), a)
			}
			if mode == "sign-in" {
				m.mu.Lock()
				m.auths[a.ID].RequiresSignIn = true
				m.mu.Unlock()
			}
			if mode == "opt-out" {
				a.RefreshPolicy = pluginapi.RefreshDefault
				_, _ = m.Update(context.Background(), a)
			}
			verify := &demandVerificationExecutor{demandTestExecutor: e, check: demandVerificationCheck(t, a.ID, store, mode != "opt-out")}
			if strings.HasPrefix(mode, "incomplete") {
				verify.incomplete = true
			}
			if mode == "incomplete-buffered" {
				verify.streamErr = &Error{HTTPStatus: 501}
			}
			if mode == "non-501" {
				verify.streamErr = &Error{HTTPStatus: 503, Message: "synthetic stream failure"}
			}
			m.RegisterExecutor(verify)
			current, err := m.VerifyInference(context.Background(), a.ID, "demand-model")
			if mode == "disabled" || mode == "sign-in" {
				if statusCodeFromError(err) != 409 || e.refreshes.Load() != 0 || verify.streamCalls.Load() != 0 {
					t.Fatalf("early gate changed: %v", err)
				}
			} else if mode == "opt-out" {
				if err != nil || current == nil || !current.InferenceVerified || e.refreshes.Load() != 0 {
					t.Fatalf("opt-out verification changed: %v", err)
				}
			} else if err == nil || current == nil || current.InferenceVerified || mode == "non-501" && verify.bufferedCalls.Load() != 0 {
				t.Fatalf("verification accepted incomplete/failure: %v", err)
			}
		})
	}
}

func TestRequestRefreshVerificationSharesOwnerAndCancellation(t *testing.T) {
	m, a, store, e := demandManager(t, time.Now().Add(-time.Hour))
	started, release := make(chan struct{}), make(chan struct{})
	e.refresh = func(_ context.Context, auth *Auth) (*Auth, error) {
		close(started)
		<-release
		auth.Metadata["access_token"] = "new"
		auth.Metadata["refresh_token"] = "new-refresh"
		auth.Metadata["expired"] = time.Now().Add(time.Hour).Format(time.RFC3339Nano)
		return auth, nil
	}
	verifyCheck := demandVerificationCheck(t, a.ID, store, true)
	verify := &demandVerificationExecutor{demandTestExecutor: e, check: func(auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) error {
		if strings.HasPrefix(opts.Headers.Get("X-Session-Id"), "verify-") {
			return verifyCheck(auth, req, opts)
		}
		if auth.ID != a.ID || authAccessToken(auth) != "new" || requestRefreshDue(auth, time.Now()) {
			return errors.New("ordinary demand did not share committed freshness")
		}
		return nil
	}}
	m.RegisterExecutor(verify)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := make(chan error, 1)
	go func() { _, err := m.VerifyInference(ctx, a.ID, "demand-model"); first <- err }()
	<-started
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) || verify.streamCalls.Load() != 0 {
		t.Fatalf("cancelled verifier dispatched: %v", err)
	}
	waiter := &demandWaitContext{Context: context.Background(), waiting: make(chan struct{})}
	second := make(chan error, 1)
	go func() { _, err := m.VerifyInference(waiter, a.ID, "demand-model"); second <- err }()
	<-waiter.waiting
	ordinaryWaiter := &demandWaitContext{Context: context.Background(), waiting: make(chan struct{})}
	ordinary := make(chan error, 1)
	go func() {
		_, err := m.Execute(ordinaryWaiter, []string{a.Provider}, cliproxyexecutor.Request{Model: "demand-model"}, cliproxyexecutor.Options{})
		ordinary <- err
	}()
	<-ordinaryWaiter.waiting
	if verify.streamCalls.Load() != 0 || verify.bufferedCalls.Load() != 0 {
		t.Fatal("authenticated demand dispatched before rotation completed")
	}
	close(release)
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	if err := <-ordinary; err != nil {
		t.Fatal(err)
	}
	if e.refreshes.Load() != 1 || verify.streamCalls.Load() != 1 || verify.bufferedCalls.Load() != 1 {
		t.Fatalf("verification did not share owner: refresh=%d stream=%d buffered=%d", e.refreshes.Load(), verify.streamCalls.Load(), verify.bufferedCalls.Load())
	}
}

func TestRequestRefreshVerificationRevalidatesAcquisition(t *testing.T) {
	for _, mutation := range []string{"disabled", "quota", "model", "prefix", "route", "login", "registration"} {
		t.Run(mutation, func(t *testing.T) {
			m, a, store, e := demandManager(t, time.Now().Add(-time.Hour))
			started, release := make(chan struct{}), make(chan struct{})
			e.refresh = func(_ context.Context, auth *Auth) (*Auth, error) {
				close(started)
				<-release
				auth.Metadata["access_token"] = "new"
				auth.Metadata["refresh_token"] = "new-refresh"
				auth.Metadata["expired"] = time.Now().Add(time.Hour).Format(time.RFC3339Nano)
				return auth, nil
			}
			verify := &demandVerificationExecutor{demandTestExecutor: e, check: demandVerificationCheck(t, a.ID, store, true)}
			m.RegisterExecutor(verify)
			result := make(chan error, 1)
			go func() { _, err := m.VerifyInference(context.Background(), a.ID, "demand-model"); result <- err }()
			<-started
			current, _ := m.GetByID(a.ID)
			switch mutation {
			case "disabled":
				current.Disabled = true
			case "quota":
				current.Quota = QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: time.Now().Add(time.Hour)}
				current.Unavailable = true
			case "model":
				registry.GetGlobalRegistry().RegisterClient(a.ID, a.Provider, []*registry.ModelInfo{{ID: "different-model"}})
			case "prefix":
				current.Prefix = "different-prefix"
			case "route":
				current.Attributes = map[string]string{"compat_name": "changed-route", "provider_key": "changed-route"}
			case "login", "registration":
				current.Metadata["access_token"] = "replacement-access"
				current.Metadata["refresh_token"] = "replacement-refresh"
				current.Metadata["expired"] = time.Now().Add(time.Hour).Format(time.RFC3339Nano)
			}
			var err error
			if mutation == "registration" {
				_, err = m.Register(context.Background(), current)
			} else {
				_, err = m.Update(context.Background(), current)
			}
			if err != nil {
				close(release)
				t.Fatal(err)
			}
			close(release)
			err = <-result
			if mutation == "prefix" {
				latest, _ := m.GetByID(a.ID)
				if err != nil || verify.streamCalls.Load() != 1 || latest.Prefix != "different-prefix" || !latest.InferenceVerified {
					t.Fatalf("verification did not use current prefix: %v", err)
				}
			} else if err == nil || verify.streamCalls.Load() != 0 || verify.bufferedCalls.Load() != 0 || e.refreshes.Load() != 1 {
				t.Fatalf("acquisition dispatched stale credential/model: err=%v stream=%d buffered=%d refresh=%d", err, verify.streamCalls.Load(), verify.bufferedCalls.Load(), e.refreshes.Load())
			}
		})
	}
}
