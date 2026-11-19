package auth

import (
	"errors"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestSameAuthRefreshOnlyRecoversSelectedAccount(t *testing.T) {
	for _, method := range []string{"execute", "count", "stream_open", "stream_bootstrap"} {
		t.Run(method, func(t *testing.T) {
			f := newSameAuthRefreshFixture(t, method, "success")
			visible, err := f.run(t)
			if err != nil || !visible {
				t.Error("same auth refresh response")
			}
			f.assertCounts(t, 2, 1)
			current, ok := f.manager.GetByID(f.primary.ID)
			if !ok || current == nil || current.RegistrationEpoch != f.primary.RegistrationEpoch || authAccessToken(current) != "synthetic-rotated" {
				t.Error("same auth refresh committed")
			}
		})
	}
}

func TestSameAuthRefreshOnlyNeverSelectsAlternates(t *testing.T) {
	for _, method := range []string{"execute", "count", "stream_open", "stream_bootstrap"} {
		for _, mode := range []string{"refresh_failed", "retry_401", "retry_503"} {
			t.Run(method+"/"+mode, func(t *testing.T) {
				f := newSameAuthRefreshFixture(t, method, mode)
				visible, err := f.run(t)
				var scoped cliproxyexecutor.RequestScopedError
				var status cliproxyexecutor.StatusError
				wantStatus, calls := 401, 2
				if mode == "refresh_failed" {
					calls = 1
				}
				if mode == "retry_503" {
					wantStatus = 503
				}
				if visible || err == nil || !errors.As(err, &scoped) || !scoped.IsRequestScoped() || !errors.As(err, &status) || status.StatusCode() != wantStatus {
					t.Error("same auth refresh terminal")
				}
				f.assertCounts(t, calls, 1)
			})
		}
	}
}

func TestSameAuthRefreshOnlyKeepsExistingRefusals(t *testing.T) {
	for _, mode := range []string{"marker_false", "unmarked", "non_401", "no_grant", "configured_rule"} {
		t.Run(mode, func(t *testing.T) {
			f := newSameAuthRefreshFixture(t, "execute", mode)
			visible, err := f.run(t)
			var scoped cliproxyexecutor.RequestScopedError
			if visible || err == nil || !errors.As(err, &scoped) || !scoped.IsRequestScoped() {
				t.Error("same auth refresh refusal")
			}
			f.assertCounts(t, 1, 0)
		})
	}
}

func TestSameAuthRefreshOnlyNeverReplaysVisibleStream(t *testing.T) {
	f := newSameAuthRefreshFixture(t, "stream_bootstrap", "visible")
	visible, err := f.run(t)
	if !visible || err == nil {
		t.Error("same auth refresh visible stream")
	}
	f.assertCounts(t, 1, 0)
}

func TestSameAuthRefreshOnlyDoesNotChangeOrdinaryFallback(t *testing.T) {
	f := newSameAuthRefreshFixture(t, "execute", "ordinary_failed")
	visible, err := f.run(t)
	f.mu.Lock()
	defer f.mu.Unlock()
	if err != nil || !visible || f.primaryCalls != 1 || f.otherCalls != 1 || f.refreshes != 1 || f.invalidRefresh != 0 {
		t.Error("same auth refresh ordinary fallback")
	}
}
