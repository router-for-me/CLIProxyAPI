package auth

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	executor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type issue6653TransitionError struct{ message, code string }

func (e issue6653TransitionError) Error() string     { return e.message }
func (e issue6653TransitionError) ErrorCode() string { return e.code }
func (e issue6653TransitionError) StatusCode() int   { return 400 }

type issue6653BoundExecutor struct {
	*issue6653Executor
	key string
}

func (e *issue6653BoundExecutor) Identifier() string { return e.key }

func issue6653AssertNoFragments(t *testing.T, text string) {
	t.Helper()
	for _, fragment := range []string{"QA_OVERLAP", "PRIVATE_SUFFIX", "qa-disposable-refresh"} {
		if strings.Contains(text, fragment) {
			t.Fatalf("credential fragment retained in diagnostic: %q", text)
		}
	}
}

func TestIssue6653TransitionBindingInvalidatesCommitPendingAndCache(t *testing.T) {
	for _, binding := range []string{"provider", "executor"} {
		for _, path := range []string{"inflight", "pending", "cache"} {
			t.Run(binding+"/"+path, func(t *testing.T) {
				s := &issue6653Store{}
				e := &issue6653Executor{}
				m, a := issue6653Manager(t, e, s)
				entered, release := make(chan struct{}), make(chan struct{})
				closeRelease := sync.OnceFunc(func() { close(release) })
				defer closeRelease()
				e.refreshFn = func(_ context.Context, b *Auth) (*Auth, error) {
					if path == "inflight" {
						close(entered)
						<-release
					}
					if path == "cache" {
						return nil, errors.New("qa old provider transient failure")
					}
					return issue6653Rotated(b), nil
				}
				done := make(chan error, 1)
				if path == "inflight" {
					go func() { _, err := m.EnsureAuthFresh(context.Background(), a); done <- err }()
					<-entered
				} else {
					s.fail = path == "pending"
					if _, err := m.EnsureAuthFresh(context.Background(), a); err == nil {
						t.Fatal("setup failure not retained")
					}
					s.fail = false
				}
				current, _ := m.GetByID(a.ID)
				if binding == "provider" {
					current.Provider = "qa-new-provider"
				} else {
					current.Attributes = map[string]string{"compat_name": "qa-new-executor", "provider_key": "qa-new-executor"}
				}
				current, err := m.Update(context.Background(), current)
				if err != nil {
					t.Fatal(err)
				}
				if current.CredentialVersion <= a.CredentialVersion {
					t.Fatal("binding change did not advance credential version")
				}
				before := s.saves
				if path == "inflight" {
					closeRelease()
					if err := <-done; err == nil {
						t.Fatal("stale refresh accepted")
					}
				}
				newExecutor := &issue6653Executor{refreshFn: func(context.Context, *Auth) (*Auth, error) { return nil, errors.New("qa NEW binding failure") }}
				m.RegisterExecutor(&issue6653BoundExecutor{issue6653Executor: newExecutor, key: executorKeyFromAuth(current)})
				if _, err := m.EnsureAuthFresh(context.Background(), current); err == nil || !strings.Contains(err.Error(), "qa NEW binding failure") {
					t.Fatalf("new binding did not discard old cached/pending outcome: %v", err)
				}
				if newExecutor.refreshes.Load() != 1 {
					t.Fatal("new binding never attempted its own refresh")
				}
				after, _ := m.GetByID(a.ID)
				if s.saves != before || authRefreshToken(after) != authRefreshToken(a) || !refreshIdentityMatches(current, after) || e.calls.Load() != 0 {
					t.Fatal("stale A rotation saved/mutated/dispatched after binding change")
				}
				value, _ := m.refreshLocks.Load(a.ID)
				lock := value.(*authRefreshLock)
				lock.mu.Lock()
				defer lock.mu.Unlock()
				if lock.pending != nil || lock.failureVersion != current.CredentialVersion {
					t.Fatal("binding change retained pending rotation or cached failure")
				}
			})
		}
	}
}

func TestIssue6653TransitionMarginLastErrorAfterExpiry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := &issue6653Executor{}
		m, a := issue6653Manager(t, e, nil)
		a.Metadata["expires_at"] = time.Now().Add(30 * time.Second).Format(time.RFC3339Nano)
		if _, err := m.Update(context.Background(), a); err != nil {
			t.Fatal(err)
		}
		e.refreshFn = func(_ context.Context, b *Auth) (*Auth, error) {
			return nil, issue6653TransitionError{authRefreshToken(b) + " denied", "invalid_grant"}
		}
		if _, err := m.EnsureAuthFresh(context.Background(), a); err == nil {
			t.Fatal("terminal margin refresh accepted")
		}
		c, _ := m.GetByID(a.ID)
		if !c.RequiresSignIn || c.RejectedRefreshToken != authRefreshToken(a) {
			t.Fatal("terminal margin rejection missing")
		}
		for _, d := range []*Error{c.RefreshError, c.LastError} {
			if d == nil {
				t.Fatal("stored diagnostic missing")
			}
			issue6653AssertNoFragments(t, d.Message+d.Code)
		}
		time.Sleep(31 * time.Second)
		for _, stream := range []bool{false, true} {
			var err error
			if stream {
				_, err = m.ExecuteStream(context.Background(), []string{e.Identifier()}, executor.Request{Model: "qa-model-6653"}, executor.Options{})
			} else {
				_, err = issue6653Execute(m)
			}
			if err == nil {
				t.Fatal("expired terminal credential dispatched")
			}
			issue6653AssertNoFragments(t, err.Error())
		}
		if e.refreshes.Load() != 1 || e.calls.Load() != 0 {
			t.Fatal("rejected grant reused or expired access dispatched")
		}
	})
}

func TestIssue6653TransitionOverlapFirstCachedAndStored(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(map[bool]string{false: "transient", true: "terminal"}[terminal], func(t *testing.T) {
			e := &issue6653Executor{}
			m, a := issue6653Manager(t, e, nil)
			a.Metadata["access_token"] = "QA_OVERLAP"
			a.Metadata["refresh_token"] = "QA_OVERLAP_PRIVATE_SUFFIX"
			if _, err := m.Update(context.Background(), a); err != nil {
				t.Fatal(err)
			}
			e.refreshFn = func(context.Context, *Auth) (*Auth, error) {
				code := "QA_OVERLAP_PRIVATE_SUFFIX"
				if terminal {
					code = "invalid_grant"
				}
				return nil, issue6653TransitionError{"QA_OVERLAP_PRIVATE_SUFFIX denied", code}
			}
			for i := 0; i < 2; i++ {
				_, err := m.EnsureAuthFresh(context.Background(), a)
				if err == nil {
					t.Fatal("failure admitted")
				}
				issue6653AssertNoFragments(t, err.Error())
				var coded interface{ ErrorCode() string }
				if errors.As(err, &coded) {
					issue6653AssertNoFragments(t, coded.ErrorCode())
				}
			}
			c, _ := m.GetByID(a.ID)
			for _, d := range []*Error{c.RefreshError, c.LastError} {
				if d == nil {
					if terminal {
						t.Fatal("stored terminal diagnostic missing")
					}
					continue
				}
				issue6653AssertNoFragments(t, d.Message+d.Code)
			}
			if e.refreshes.Load() != 1 || e.calls.Load() != 0 {
				t.Fatal("failure cache/grant exchange contract lost")
			}
		})
	}
}

func TestIssue6653TransitionScheduledRuntimeOnlyNoStoreException(t *testing.T) {
	e := &issue6653Executor{}
	m, a := issue6653Manager(t, e, nil)
	e.refreshFn = func(_ context.Context, b *Auth) (*Auth, error) {
		u := issue6653Rotated(b)
		u.RefreshPolicy = &RefreshPolicy{Mode: "scheduled"}
		u.Attributes = map[string]string{"runtime_only": "true"}
		return u, nil
	}
	fresh, err := m.EnsureAuthFresh(context.Background(), a)
	if err != nil || fresh == nil || fresh.RefreshPolicy.Mode != "scheduled" || authRefreshToken(fresh) != "qa-rotated-refresh" {
		t.Fatalf("no-store exception lost: %v", err)
	}
	if _, err := issue6653Execute(m); err != nil {
		t.Fatal(err)
	}
	if e.refreshes.Load() != 1 || e.calls.Load() != 1 {
		t.Fatal("no-store scheduled transition dispatch/exchange contract lost")
	}
}
