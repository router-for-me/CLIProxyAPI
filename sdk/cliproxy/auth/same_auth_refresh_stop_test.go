package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// These controls use the public selected-auth refresh API and compile before
// the optional marker is recognized.
func TestSameAuthRefreshPermissionCannotOverrideStopOrCancellation(t *testing.T) {
	for _, mode := range []string{"request_stop", "terminated", "canceled_context", "expired_context", "joined_cancel", "joined_deadline", "message_only_401", "non_401_message"} {
		t.Run(mode, func(t *testing.T) {
			f := newSameAuthRefreshFixture(t, "execute", "success")
			ctx := t.Context()
			var failure error = sameAuthRefreshError{status: 401, allow: true}
			switch mode {
			case "request_stop":
				failure = wrapRequestStopError(failure)
			case "terminated":
				failure = errors.Join(failure, &coreexecutor.RequestTerminatedError{HTTPStatus: 401})
			case "canceled_context":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "expired_context":
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer cancel()
			case "joined_cancel":
				failure = errors.Join(failure, context.Canceled)
			case "joined_deadline":
				failure = errors.Join(failure, context.DeadlineExceeded)
			case "message_only_401":
				failure = sameAuthRefreshMessageError{}
			case "non_401_message":
				failure = sameAuthRefreshMessageError{status: 503}
			}
			result, refreshed := f.manager.RefreshAfterUnauthorized(ctx, f.primary, failure, false)
			current, found := f.manager.GetByID(f.primary.ID)
			if refreshed || result != f.primary || !found || current == nil ||
				authAccessToken(current) != "synthetic-old" || current.RejectedAccessToken != "" {
				t.Error("same auth refresh stop changed state")
			}
			f.assertCounts(t, 0, 0)
		})
	}
}

// Existing unscoped recognition may use a textual status. The new opt-in
// exception requires an actual HTTP 401 and does not inherit that heuristic.
type sameAuthRefreshMessageError struct{ status int }

func (sameAuthRefreshMessageError) Error() string              { return "synthetic status 401" }
func (e sameAuthRefreshMessageError) StatusCode() int          { return e.status }
func (sameAuthRefreshMessageError) IsRequestScoped() bool      { return true }
func (sameAuthRefreshMessageError) AllowSameAuthRefresh() bool { return true }
