package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestRequestRefreshNewLoginWaiterIgnoresObsoleteFailure(t *testing.T) {
	for _, mutation := range []string{"update", "register"} {
		for _, rejection := range []bool{false, true} {
			t.Run(mutation+map[bool]string{false: "/transient", true: "/invalid-grant"}[rejection], func(t *testing.T) {
				m, original, _, e := demandManager(t, time.Now().Add(-time.Hour))
				var failure error = errors.New("obsolete in-flight refresh failure")
				if rejection {
					failure = &Error{HTTPStatus: 401, Message: "obsolete in-flight invalid_grant"}
				}
				started, release := make(chan struct{}), make(chan struct{})
				e.refresh = func(context.Context, *Auth) (*Auth, error) {
					close(started)
					<-release
					return nil, failure
				}
				oldResult := make(chan error, 1)
				go func() {
					_, err := m.EnsureRequestAuth(context.Background(), original, "demand-model")
					oldResult <- err
				}()
				<-started
				login, _ := m.GetByID(original.ID)
				login.Metadata["access_token"] = "request-refresh-new-login"
				login.Metadata["refresh_token"] = "request-refresh-new-login-refresh"
				login.Metadata["expired"] = "2099-01-01T00:00:00Z"
				var err error
				if mutation == "update" {
					_, err = m.Update(context.Background(), login)
				} else {
					_, err = m.Register(context.Background(), login)
				}
				if err != nil {
					t.Fatal(err)
				}
				newResult := make(chan error, 1)
				waiter := &demandWaitContext{Context: context.Background(), waiting: make(chan struct{})}
				go func() {
					_, err := m.Execute(waiter, []string{login.Provider}, cliproxyexecutor.Request{Model: "demand-model"}, cliproxyexecutor.Options{})
					newResult <- err
				}()
				select {
				case <-waiter.waiting:
					close(release)
					err = <-newResult
				case err = <-newResult:
					close(release)
				}
				if oldErr := <-oldResult; !errors.Is(oldErr, failure) {
					t.Fatalf("original failure was lost: %v", oldErr)
				}
				current, _ := m.GetByID(original.ID)
				t.Logf("mutation=%s rejection=%v old-epoch=%d current-epoch=%d current-token=%v new-request=%v rotations=%d inference=%d", mutation, rejection, original.RegistrationEpoch, current.RegistrationEpoch, current.Metadata["access_token"], err, e.refreshes.Load(), e.calls.Load())
				if err != nil || e.calls.Load() != 1 || e.refreshes.Load() != 1 || current.Unavailable || current.LastError != nil {
					t.Fatalf("fresh replacement demand inherited obsolete refresh failure: error=%v refresh=%d inference=%d current-status=%s", err, e.refreshes.Load(), e.calls.Load(), current.Status)
				}
			})
		}
	}
}
