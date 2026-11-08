package auth

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	executor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func issue6653ScheduledPending(t *testing.T, e *issue6653Executor, s *issue6653Store) (*Manager, *Auth) {
	t.Helper()
	m, a := issue6653Manager(t, e, s)
	a.Metadata["expires_at"] = time.Now().Add(30 * time.Second).Format(time.RFC3339Nano)
	var err error
	a, err = m.Update(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	s.fail = true
	if _, err := m.EnsureAuthFresh(context.Background(), a); err == nil {
		t.Fatal("initial pending save did not fail")
	}
	s.fail = false
	c, _ := m.GetByID(a.ID)
	c.RefreshPolicy = &RefreshPolicy{Mode: "scheduled"}
	c, err = m.Update(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	s.fail = true
	return m, c
}

func TestIssue6653PendingScheduledInvalidation(t *testing.T) {
	for _, change := range []string{"replace", "remove-reregister", "provider", "executor"} {
		t.Run(change, func(t *testing.T) {
			s := &issue6653Store{}
			e := &issue6653Executor{}
			m, c := issue6653ScheduledPending(t, e, s)
			s.fail = false
			if change == "remove-reregister" {
				m.Remove(context.Background(), c.ID)
				if _, err := m.EnsureAuthFresh(context.Background(), c); err == nil {
					t.Fatal("removed pending auth admitted")
				}
				c.RegistrationEpoch = 0
				c.Metadata["access_token"] = "qa-operator-access"
				c.Metadata["refresh_token"] = "qa-operator-grant"
				var err error
				c, err = m.Register(context.Background(), c)
				if err != nil {
					t.Fatal(err)
				}
			} else {
				switch change {
				case "replace":
					c.Metadata["access_token"] = "qa-operator-access"
					c.Metadata["refresh_token"] = "qa-operator-grant"
				case "provider":
					c.Provider = "qa-provider-new"
				case "executor":
					c.Attributes = map[string]string{"compat_name": "qa-executor-new", "provider_key": "qa-executor-new"}
				}
				var err error
				c, err = m.Update(context.Background(), c)
				if err != nil {
					t.Fatal(err)
				}
			}
			if change == "provider" || change == "executor" {
				m.RegisterExecutor(&issue6653BoundExecutor{issue6653Executor: &issue6653Executor{}, key: executorKeyFromAuth(c)})
			}
			saves := s.saves
			fresh, entered, err := m.EnsureAuthFreshForOperation(context.Background(), c)
			if err != nil || fresh == nil || !entered {
				t.Fatalf("scheduled pending invalidation boundary failed: entered=%v err=%v", entered, err)
			}
			if authRefreshToken(fresh) != authRefreshToken(c) || s.saves != saves || e.refreshes.Load() != 1 || m.HasLazyRefreshWork(c.ID) {
				t.Fatal("obsolete pending rotation saved/reused or work marker not cleared")
			}
			if authRefreshToken(s.last) != authRefreshToken(c) {
				t.Fatal("operator's persisted pair replaced by pending rotation")
			}
		})
	}
}

func TestIssue6653PendingScheduledSelectedTokenValidation(t *testing.T) {
	for _, outer := range []bool{false, true} {
		t.Run(fmt.Sprintf("outer=%v", outer), func(t *testing.T) {
			s := &issue6653Store{}
			e := &issue6653Executor{}
			m, a := issue6653Manager(t, e, s)
			future := time.Now().Add(time.Hour).Format(time.RFC3339Nano)
			margin := time.Now().Add(30 * time.Second).Format(time.RFC3339Nano)
			a.Metadata["use_siwc"] = true
			a.Metadata["expires_at"] = future
			a.Metadata["siwc"] = map[string]any{"access_token": "qa-nested", "refresh_token": "qa-nested-grant", "expires_at": future}
			if outer {
				a.Metadata["expires_at"] = margin
			} else {
				a.Metadata["siwc"].(map[string]any)["expires_at"] = margin
			}
			var err error
			a, err = m.Update(context.Background(), a)
			if err != nil {
				t.Fatal(err)
			}
			e.refreshFn = func(_ context.Context, b *Auth) (*Auth, error) {
				u := b.Clone()
				if outer {
					u.Metadata["access_token"] = "qa-new-outer"
					u.Metadata["refresh_token"] = "qa-new-grant"
					u.Metadata["expires_at"] = "2000-01-01T00:00:00Z"
				} else {
					u.Metadata["siwc"] = map[string]any{"access_token": "qa-new-nested", "refresh_token": "qa-new-nested-grant", "expires_at": "2000-01-01T00:00:00Z"}
				}
				return u, nil
			}
			ctx := WithCodexClientRequest(context.Background(), outer)
			s.fail = true
			if _, err := m.EnsureAuthFresh(ctx, a); err == nil {
				t.Fatal("initial save did not fail")
			}
			s.fail = false
			c, _ := m.GetByID(a.ID)
			c.RefreshPolicy = &RefreshPolicy{Mode: "scheduled"}
			c, err = m.Update(context.Background(), c)
			if err != nil {
				t.Fatal(err)
			}
			_, entered, err := m.EnsureAuthFreshForOperation(ctx, c)
			if err == nil || !entered || e.refreshes.Load() != 1 || e.calls.Load() != 0 {
				t.Fatalf("scheduled pending retry bypassed selected expired token: entered=%v err=%v", entered, err)
			}
		})
	}
}

func TestIssue6653PendingScheduledModelRecheckAfterWait(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			s := &issue6653Store{}
			e := &issue6653Executor{}
			m, c := issue6653ScheduledPending(t, e, s)
			ctx := &issue6653SnapshotContext{Context: context.Background(), entered: make(chan struct{}), proceed: make(chan struct{})}
			done := make(chan error, 1)
			go func() {
				var err error
				if stream {
					_, err = m.ExecuteStream(ctx, []string{c.Provider}, executor.Request{Model: "qa-model-6653"}, executor.Options{})
				} else {
					_, err = m.Execute(ctx, []string{c.Provider}, executor.Request{Model: "qa-model-6653"}, executor.Options{})
				}
				done <- err
			}()
			<-ctx.entered
			c, _ = m.GetByID(c.ID)
			c.ModelStates = map[string]*ModelState{"qa-model-6653": {Unavailable: true, NextRetryAfter: time.Now().Add(time.Hour)}}
			s.fail = false
			if _, err := m.Update(context.Background(), c); err != nil {
				t.Fatal(err)
			}
			close(ctx.proceed)
			if err := <-done; err == nil {
				t.Fatal("model became blocked during pending wait but dispatched")
			}
			if e.calls.Load() != 0 || e.refreshes.Load() != 1 || authRefreshToken(s.last) != "qa-rotated-refresh" {
				t.Fatal("model recheck did not preserve durable retry and zero dispatch")
			}
		})
	}
}

func TestIssue6653PendingOrdinaryScheduledDoesNotWait(t *testing.T) {
	for _, background := range []bool{false, true} {
		t.Run(fmt.Sprintf("background=%v", background), func(t *testing.T) {
			e := &issue6653Executor{}
			m, a := issue6653Manager(t, e, nil)
			a.RefreshPolicy = &RefreshPolicy{Mode: "scheduled"}
			a.Metadata["expires_at"] = time.Now().Add(time.Hour).Format(time.RFC3339Nano)
			var err error
			a, err = m.Update(context.Background(), a)
			if err != nil {
				t.Fatal(err)
			}
			entered, release := make(chan struct{}), make(chan struct{})
			closeRelease := sync.OnceFunc(func() { close(release) })
			defer closeRelease()
			e.refreshFn = func(_ context.Context, b *Auth) (*Auth, error) {
				close(entered)
				<-release
				return issue6653Rotated(b), nil
			}
			refreshed := make(chan error, 1)
			go func() {
				var err error
				if background {
					_, err = m.refreshAuthWithTrigger(context.Background(), a.ID, "", a.RegistrationEpoch, refreshTriggerBackground)
				} else {
					_, err = m.ForceRefreshAuth(context.Background(), a.ID)
				}
				refreshed <- err
			}()
			<-entered
			if m.HasLazyRefreshWork(a.ID) {
				t.Fatal("ordinary scheduled refresh published lazy work")
			}
			operation := make(chan error, 1)
			go func() {
				fresh, required, err := m.EnsureAuthFreshForOperation(context.Background(), a)
				if err == nil && (required || fresh != a) {
					err = fmt.Errorf("ordinary scheduled snapshot/required flag changed")
				}
				if err == nil {
					_, err = issue6653Execute(m)
				}
				operation <- err
			}()
			select {
			case err := <-operation:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("ordinary scheduled request waited behind refresh")
			}
			if e.calls.Load() != 1 {
				t.Fatal("ordinary scheduled dispatch failed")
			}
			closeRelease()
			if err := <-refreshed; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestIssue6653PendingActiveLazyExchangeScheduledRequestWaits(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			s := &issue6653Store{}
			e := &issue6653Executor{}
			m, a := issue6653Manager(t, e, s)
			a.Metadata["expires_at"] = time.Now().Add(30 * time.Second).Format(time.RFC3339Nano)
			var err error
			a, err = m.Update(context.Background(), a)
			if err != nil {
				t.Fatal(err)
			}
			entered, release := make(chan struct{}), make(chan struct{})
			closeRelease := sync.OnceFunc(func() { close(release) })
			defer closeRelease()
			e.refreshFn = func(_ context.Context, b *Auth) (*Auth, error) {
				close(entered)
				<-release
				return issue6653Rotated(b), nil
			}
			first := make(chan error, 1)
			go func() { _, err := m.EnsureAuthFresh(context.Background(), a); first <- err }()
			<-entered
			c, _ := m.GetByID(a.ID)
			c.RefreshPolicy = &RefreshPolicy{Mode: "scheduled"}
			if _, err := m.Update(context.Background(), c); err != nil {
				t.Fatal(err)
			}
			if !m.HasLazyRefreshWork(a.ID) {
				t.Fatal("active lazy exchange not visible after scheduled switch")
			}
			ctx := &issue6653SnapshotContext{Context: context.Background(), entered: make(chan struct{}), proceed: make(chan struct{})}
			close(ctx.proceed)
			second := make(chan error, 1)
			go func() {
				var err error
				if stream {
					_, err = m.ExecuteStream(ctx, []string{a.Provider}, executor.Request{Model: "qa-model-6653"}, executor.Options{})
				} else {
					_, err = m.Execute(ctx, []string{a.Provider}, executor.Request{Model: "qa-model-6653"}, executor.Options{})
				}
				second <- err
			}()
			select {
			case <-ctx.entered:
			case err := <-second:
				t.Fatalf("scheduled request skipped active lazy boundary: %v", err)
			case <-time.After(5 * time.Second):
				t.Fatal("scheduled request did not reach coordinated boundary")
			}
			if e.calls.Load() != 0 {
				t.Fatal("scheduled request dispatched before active exchange finished")
			}
			s.fail = true
			closeRelease()
			if err := <-first; err == nil {
				t.Fatal("failed exchange save admitted")
			}
			if err := <-second; err == nil {
				t.Fatal("waiting scheduled request bypassed failed save")
			}
			if e.refreshes.Load() != 1 || e.calls.Load() != 0 {
				t.Fatal("active waiting request reused grant or dispatched")
			}
			s.fail = false
			if _, err := issue6653Execute(m); err != nil {
				t.Fatal(err)
			}
			if e.refreshes.Load() != 1 || e.calls.Load() != 1 {
				t.Fatal("active pending recovery did not retain grant")
			}
		})
	}
}
