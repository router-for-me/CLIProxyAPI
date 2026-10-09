package auth

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type observationRefreshExecutor struct {
	countingRefreshExecutor
	err          error
	requestProxy string
	entered      chan struct{}
	release      chan struct{}
	mutate       func(*Auth)
}

func (e *observationRefreshExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	e.refreshCalls.Add(1)
	e.requestProxy = cliproxyexecutor.RequestProxyURL(ctx)
	if e.entered != nil {
		close(e.entered)
		<-e.release
	}
	if e.err != nil {
		return nil, e.err
	}
	auth.Metadata["access_token"] = "synthetic-rotated-token"
	auth.Metadata["refresh_token"] = "synthetic-rotated-refresh"
	auth.Metadata["expired"] = time.Now().Add(time.Hour).Format(time.RFC3339)
	// Even if an executor tries to recover routing, observation must not do so.
	auth.Unavailable = false
	auth.Status = StatusActive
	auth.LastError = nil
	auth.ModelStates = nil
	auth.Quota = QuotaState{}
	if e.mutate != nil {
		e.mutate(auth)
	}
	return auth, nil
}

func TestRefreshAuthForObservationPreservesRoutingAndDeduplicates(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	exec := &observationRefreshExecutor{countingRefreshExecutor: countingRefreshExecutor{id: "claude"}}
	manager.RegisterExecutor(exec)
	auth, errRegister := manager.Register(context.Background(), &Auth{
		ID: "observation", Provider: "claude", Status: StatusError, Unavailable: true,
		StatusMessage: "synthetic unauthorized", LastError: &Error{HTTPStatus: 401, Message: "synthetic unauthorized"},
		NextRetryAfter: time.Now().Add(time.Hour), NextRefreshAfter: time.Now().Add(time.Minute),
		RejectedAccessToken: "synthetic-expired-token",
		Quota:               QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: time.Now().Add(time.Hour), BackoffLevel: 2},
		ModelStates: map[string]*ModelState{"claude": {
			Status: StatusError, Unavailable: true, LastError: &Error{HTTPStatus: 401},
			NextRetryAfter: time.Now().Add(time.Hour),
		}},
		Metadata: map[string]any{"access_token": "synthetic-expired-token", "refresh_token": "synthetic-refresh", "expired": "2000-01-01T00:00:00Z"},
	})
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	ctx := cliproxyexecutor.WithRequestProxyURL(context.Background(), "http://execution-proxy.invalid")
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			if _, _, errRefresh := manager.RefreshAuthForObservation(ctx, auth); errRefresh != nil {
				t.Errorf("refresh observation: %v", errRefresh)
			}
		})
	}
	workers.Wait()
	current, _ := manager.GetByID(auth.ID)
	if exec.refreshCalls.Load() != 1 || exec.requestProxy != "" {
		t.Fatal("observation did not reuse the refresh lock or credential proxy semantics")
	}
	if authAccessToken(current) != "synthetic-rotated-token" || current.LastRefreshedAt.IsZero() {
		t.Fatal("rotated credential was not committed")
	}
	if current.Status != auth.Status || current.Unavailable != auth.Unavailable ||
		current.StatusMessage != auth.StatusMessage || !reflect.DeepEqual(current.LastError, auth.LastError) ||
		current.NextRetryAfter != auth.NextRetryAfter || current.NextRefreshAfter != auth.NextRefreshAfter ||
		!reflect.DeepEqual(current.Quota, auth.Quota) || !reflect.DeepEqual(current.ModelStates, auth.ModelStates) ||
		current.RejectedAccessToken != auth.RejectedAccessToken {
		t.Fatal("observation refresh modified routing/cooldown fields")
	}
}

func TestRefreshAuthForObservationFailureAndRegistrationIsolation(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	exec := &observationRefreshExecutor{
		countingRefreshExecutor: countingRefreshExecutor{id: "claude"},
		err:                     errors.New("synthetic refresh failure"),
	}
	manager.RegisterExecutor(exec)
	auth, errRegister := manager.Register(context.Background(), &Auth{
		ID: "observation", Provider: "claude", Metadata: map[string]any{"refresh_token": "synthetic-refresh"},
	})
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	if _, _, errRefresh := manager.RefreshAuthForObservation(nil, auth); errRefresh == nil {
		t.Fatal("expected refresh failure")
	}
	current, _ := manager.GetByID(auth.ID)
	if !reflect.DeepEqual(current, auth) {
		t.Fatal("failed observation changed the credential")
	}
	manager.Remove(context.Background(), auth.ID)
	if _, errReplace := manager.Register(context.Background(), auth.Clone()); errReplace != nil {
		t.Fatal(errReplace)
	}
	if _, _, errRefresh := manager.RefreshAuthForObservation(nil, auth); errRefresh == nil || exec.refreshCalls.Load() != 1 {
		t.Fatal("stale registration was refreshed")
	}
}

func TestRefreshAuthForObservationKeepsConcurrentCredentials(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	exec := &observationRefreshExecutor{
		countingRefreshExecutor: countingRefreshExecutor{id: "claude"},
		entered:                 make(chan struct{}), release: make(chan struct{}),
	}
	manager.RegisterExecutor(exec)
	auth, errRegister := manager.Register(context.Background(), &Auth{
		ID: "observation", Provider: "claude", Metadata: map[string]any{"refresh_token": "synthetic-refresh"},
	})
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	done := make(chan error, 1)
	go func() {
		_, rotated, errRefresh := manager.RefreshAuthForObservation(context.Background(), auth)
		if rotated {
			t.Error("concurrent replacement was reported as an observation token rotation")
		}
		done <- errRefresh
	}()
	<-exec.entered
	replacement := auth.Clone()
	replacement.Metadata["access_token"] = "synthetic-user-replacement"
	if _, errUpdate := manager.Update(context.Background(), replacement); errUpdate != nil {
		t.Fatal(errUpdate)
	}
	close(exec.release)
	if errRefresh := <-done; errRefresh != nil {
		t.Fatal(errRefresh)
	}
	current, _ := manager.GetByID(auth.ID)
	if authAccessToken(current) != "synthetic-user-replacement" {
		t.Fatal("in-flight observation overwrote newer credential material")
	}
}

func TestRefreshAuthForObservationRejectsCredentialScopeReplacement(t *testing.T) {
	for _, tc := range []struct {
		name    string
		replace func(*Auth)
	}{
		{"provider", func(auth *Auth) { auth.Provider = "codex" }},
		{"kind", func(auth *Auth) { auth.Attributes[AttributeAuthKind] = AuthKindAPIKey }},
		{"account", func(auth *Auth) { auth.Metadata["account_uuid"] = "account-b" }},
		{"organization", func(auth *Auth) { auth.Metadata["organization_uuid"] = "org-b" }},
		{"email fallback", func(auth *Auth) { auth.Metadata["email"] = "other@example.test" }},
	} {
		for _, stage := range []string{"before refresh", "during refresh", "refresh output"} {
			t.Run(tc.name+"/"+stage, func(t *testing.T) {
				manager := NewManager(nil, nil, nil)
				exec := &observationRefreshExecutor{countingRefreshExecutor: countingRefreshExecutor{id: "claude"}}
				otherExec := &observationRefreshExecutor{countingRefreshExecutor: countingRefreshExecutor{id: "codex"}}
				manager.RegisterExecutor(exec)
				manager.RegisterExecutor(otherExec)
				metadata := map[string]any{
					"refresh_token": "synthetic-refresh", "account_uuid": "account-a", "organization_uuid": "org-a",
				}
				if tc.name == "email fallback" {
					delete(metadata, "account_uuid")
					metadata["email"] = "original@example.test"
				}
				auth, errRegister := manager.Register(context.Background(), &Auth{
					ID: "observation", Provider: "claude", Metadata: metadata,
					Attributes: map[string]string{AttributeAuthKind: AuthKindOAuth},
				})
				if errRegister != nil {
					t.Fatal(errRegister)
				}
				replacement := auth.Clone()
				tc.replace(replacement)
				var errRefresh error
				switch stage {
				case "before refresh":
					if _, errUpdate := manager.Update(context.Background(), replacement); errUpdate != nil {
						t.Fatal(errUpdate)
					}
					_, _, errRefresh = manager.RefreshAuthForObservation(context.Background(), auth)
				case "during refresh":
					exec.entered, exec.release = make(chan struct{}), make(chan struct{})
					done := make(chan error, 1)
					go func() {
						_, _, err := manager.RefreshAuthForObservation(context.Background(), auth)
						done <- err
					}()
					<-exec.entered
					_, errUpdate := manager.Update(context.Background(), replacement)
					close(exec.release)
					errRefresh = <-done
					if errUpdate != nil {
						t.Fatal(errUpdate)
					}
				case "refresh output":
					exec.mutate = tc.replace
					_, _, errRefresh = manager.RefreshAuthForObservation(context.Background(), auth)
				}
				if errRefresh == nil {
					t.Fatal("observation accepted a different credential scope")
				}
				current, _ := manager.GetByID(auth.ID)
				expected := replacement
				if stage == "refresh output" {
					expected = auth
				}
				if current.Provider != expected.Provider || !reflect.DeepEqual(current.Metadata, expected.Metadata) ||
					!reflect.DeepEqual(current.Attributes, expected.Attributes) {
					t.Fatal("observation overwrote replacement credential material")
				}
				if otherExec.refreshCalls.Load() != 0 || (stage == "before refresh" && exec.refreshCalls.Load() != 0) {
					t.Fatal("observation refreshed the replacement credential")
				}
			})
		}
	}
}

type observationFailureStore struct{ persistFailureStore }

func (*observationFailureStore) Save(_ context.Context, auth *Auth) (string, error) {
	return "", errors.New("store echoed " + authAccessToken(auth) + " " + authRefreshToken(auth))
}

func TestRefreshAuthForObservationRedactsPersistenceFailure(t *testing.T) {
	hook := setupTestLoggerHook(t)
	manager := NewManager(&observationFailureStore{}, nil, nil)
	manager.RegisterExecutor(&observationRefreshExecutor{countingRefreshExecutor: countingRefreshExecutor{id: "claude"}})
	auth, errRegister := manager.Register(WithSkipPersist(context.Background()), &Auth{
		ID: "observation", Provider: "claude", Metadata: map[string]any{"refresh_token": "synthetic-refresh"},
	})
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	hook.Reset()
	if _, _, errRefresh := manager.RefreshAuthForObservation(context.Background(), auth); errRefresh != nil {
		t.Fatal(errRefresh)
	}
	if len(hook.AllEntries()) == 0 {
		t.Fatal("expected a safe persistence failure diagnostic")
	}
	for _, entry := range hook.AllEntries() {
		if strings.Contains(entry.Message, "synthetic-rotated-token") || strings.Contains(entry.Message, "synthetic-rotated-refresh") {
			t.Fatal("observation persistence failure leaked credential material")
		}
	}
}
