package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	executor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestIssue6653FinalPolicyTransitionSelectedToken(t *testing.T) {
	for _, operator := range []bool{false, true} {
		for _, outer := range []bool{false, true} {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("operator=%v/outer=%v/stream=%v", operator, outer, stream), func(t *testing.T) {
					e := &issue6653Executor{}
					m, a := issue6653Manager(t, e, nil)
					future := time.Now().Add(time.Hour).Format(time.RFC3339Nano)
					past := time.Now().Add(-time.Hour).Format(time.RFC3339Nano)
					a.Metadata["use_siwc"] = true
					a.Metadata["expires_at"] = future
					a.Metadata["siwc"] = map[string]any{"access_token": "qa-nested-access", "refresh_token": "qa-nested-grant", "expires_at": future}
					if outer {
						a.Metadata["expires_at"] = past
					} else {
						a.Metadata["siwc"].(map[string]any)["expires_at"] = past
					}
					if _, err := m.Update(context.Background(), a); err != nil {
						t.Fatal(err)
					}
					started := make(chan struct{})
					release := make(chan struct{})
					done := make(chan error, 1)
					e.refreshFn = func(_ context.Context, b *Auth) (*Auth, error) {
						close(started)
						<-release
						u := b.Clone()
						if !operator {
							u.RefreshPolicy = &RefreshPolicy{Mode: pluginapi.AuthRefreshScheduled}
						}
						return u, nil
					}
					go func() {
						ctx := WithCodexClientRequest(context.Background(), outer)
						var err error
						if stream {
							_, err = m.ExecuteStream(ctx, []string{e.Identifier()}, executor.Request{Model: "qa-model-6653"}, executor.Options{})
						} else {
							_, err = m.Execute(ctx, []string{e.Identifier()}, executor.Request{Model: "qa-model-6653"}, executor.Options{})
						}
						done <- err
					}()
					<-started
					if operator {
						current, _ := m.GetByID(a.ID)
						current.RefreshPolicy = &RefreshPolicy{Mode: pluginapi.AuthRefreshScheduled}
						if _, err := m.Update(context.Background(), current); err != nil {
							t.Fatal(err)
						}
					}
					close(release)
					err := <-done
					if err == nil || e.calls.Load() != 0 || e.refreshes.Load() != 1 {
						t.Fatalf("scheduled transition admitted expired selected partition: err=%v inference=%d refresh=%d", err, e.calls.Load(), e.refreshes.Load())
					}
					current, _ := m.GetByID(a.ID)
					if current.RefreshPolicy.Mode != pluginapi.AuthRefreshScheduled {
						t.Fatal("scheduled policy lost")
					}
				})
			}
		}
	}
}

func TestIssue6653FinalRejectedOuterNestedOnlyRotation(t *testing.T) {
	for _, scheduled := range []bool{false, true} {
		t.Run(fmt.Sprintf("scheduled=%v", scheduled), func(t *testing.T) {
			e := &issue6653Executor{}
			m, a := issue6653Manager(t, e, nil)
			future := time.Now().Add(time.Hour).Format(time.RFC3339Nano)
			a.Metadata["expires_at"] = future
			a.Metadata["use_siwc"] = true
			a.Metadata["siwc"] = map[string]any{"access_token": "qa-nested-access", "refresh_token": "qa-nested-grant", "expires_at": future}
			a.RejectedAccessToken = "qa-disposable-access"
			m.mu.Lock()
			m.auths[a.ID] = a.Clone()
			m.scheduler.upsertAuth(a.Clone())
			m.mu.Unlock()
			e.refreshFn = func(_ context.Context, b *Auth) (*Auth, error) {
				u := b.Clone()
				u.Metadata["siwc"] = map[string]any{"access_token": "qa-new-nested", "refresh_token": "qa-new-nested-grant", "expiresAt": future}
				if scheduled {
					u.RefreshPolicy = &RefreshPolicy{Mode: pluginapi.AuthRefreshScheduled}
				}
				return u, nil
			}
			_, err := m.Execute(WithCodexClientRequest(context.Background(), true), []string{e.Identifier()}, executor.Request{Model: "qa-model-6653"}, executor.Options{})
			current, _ := m.GetByID(a.ID)
			if err == nil || e.calls.Load() != 0 || current.RejectedAccessToken != "qa-disposable-access" {
				t.Fatalf("nested-only rotation cleared unchanged rejected outer: err=%v inference=%d rejected=%v", err, e.calls.Load(), current.RejectedAccessToken != "")
			}
		})
	}
}

func TestIssue6653FinalRedactionBeforeTruncation(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(fmt.Sprintf("terminal=%v", terminal), func(t *testing.T) {
			e := &issue6653Executor{}
			m, a := issue6653Manager(t, e, nil)
			secret := "QA_BOUNDARY_GRANT_0123456789"
			a.Metadata["refresh_token"] = secret
			if _, err := m.Update(context.Background(), a); err != nil {
				t.Fatal(err)
			}
			code := "qa_refresh_503"
			status := 503
			if terminal {
				code = "invalid_grant"
				status = 400
			}
			e.refreshFn = func(context.Context, *Auth) (*Auth, error) {
				return nil, &Error{Code: code, HTTPStatus: status, Message: strings.Repeat("界", 290) + " " + secret + " renewal denied"}
			}
			for range 2 {
				_, err := m.EnsureAuthFresh(context.Background(), a)
				issue6653AssertRedacted(t, err, []string{secret, secret[:8]})
			}
			current, _ := m.GetByID(a.ID)
			for _, stored := range []*Error{current.LastError, current.RefreshError} {
				if stored != nil && strings.Contains(stored.Message, secret[:8]) {
					t.Fatal("dummy token prefix survived stored terminal diagnostic")
				}
			}
			if e.refreshes.Load() != 1 {
				t.Fatal("redacted first/cached failure rotated again")
			}
		})
	}
}

type issue6653SnapshotContext struct {
	context.Context
	entered, proceed chan struct{}
	once             sync.Once
}

func (c *issue6653SnapshotContext) Value(key any) any {
	if _, ok := key.(forceRefreshContextKey); ok {
		c.once.Do(func() { close(c.entered); <-c.proceed })
	}
	return c.Context.Value(key)
}

func TestIssue6653FinalWaitingErrorUsesAttemptedSnapshot(t *testing.T) {
	e := &issue6653Executor{}
	m, a := issue6653Manager(t, e, nil)
	ctx := &issue6653SnapshotContext{Context: context.Background(), entered: make(chan struct{}), proceed: make(chan struct{})}
	lock := &authRefreshLock{}
	m.refreshLocks.Store(a.ID, lock)
	lock.mu.Lock()
	result := make(chan error, 1)
	go func() { _, err := m.EnsureAuthFresh(ctx, a); result <- err }()
	<-ctx.entered
	v2 := a.Clone()
	v2.Metadata["access_token"] = "QA_V2_ACCESS_DISTINCT"
	v2.Metadata["refresh_token"] = "QA_V2_GRANT_DISTINCT"
	if _, err := m.Update(context.Background(), v2); err != nil {
		t.Fatal(err)
	}
	e.refreshFn = func(_ context.Context, attempt *Auth) (*Auth, error) {
		if authRefreshToken(attempt) != "QA_V2_GRANT_DISTINCT" {
			t.Error("refresh did not reload V2 under lock")
		}
		return nil, &Error{Code: "qa_refresh_503", HTTPStatus: 503, Message: "renewal denied QA_V2_GRANT_DISTINCT"}
	}
	close(ctx.proceed)
	lock.mu.Unlock()
	first := <-result
	issue6653AssertRedacted(t, first, []string{"QA_V2_GRANT_DISTINCT"})
	_, cached := m.EnsureAuthFresh(context.Background(), a)
	issue6653AssertRedacted(t, cached, []string{"QA_V2_GRANT_DISTINCT"})
	if e.refreshes.Load() != 1 {
		t.Fatal("first V2 failure did not coalesce/cache")
	}
	var safe *Error
	if !errors.As(first, &safe) || safe.Code != "qa_refresh_503" || safe.HTTPStatus != 503 {
		t.Fatalf("sanitized V2 status/code lost: %v", first)
	}
}

func TestIssue6653FinalScheduledTransitionModelCooldown(t *testing.T) {
	for _, operator := range []bool{false, true} {
		t.Run(fmt.Sprintf("operator=%v", operator), func(t *testing.T) {
			e := &issue6653Executor{}
			m, a := issue6653Manager(t, e, nil)
			started := make(chan struct{})
			release := make(chan struct{})
			done := make(chan error, 1)
			e.refreshFn = func(_ context.Context, b *Auth) (*Auth, error) {
				close(started)
				<-release
				u := issue6653Rotated(b)
				if !operator {
					u.RefreshPolicy = &RefreshPolicy{Mode: "scheduled"}
				}
				return u, nil
			}
			go func() { _, err := issue6653Execute(m); done <- err }()
			<-started
			current, _ := m.GetByID(a.ID)
			current.ModelStates = map[string]*ModelState{"qa-model-6653": {Unavailable: true, NextRetryAfter: time.Now().Add(time.Hour)}}
			if operator {
				current.RefreshPolicy = &RefreshPolicy{Mode: "scheduled"}
			}
			if _, err := m.Update(context.Background(), current); err != nil {
				t.Fatal(err)
			}
			close(release)
			if err := <-done; err == nil || e.calls.Load() != 0 {
				t.Fatalf("scheduled result skipped post-refresh model gate: err=%v inference=%d", err, e.calls.Load())
			}
		})
	}
}
