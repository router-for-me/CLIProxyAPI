package pluginhost

import (
	"errors"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
)

// A host call that failed with an upstream status must carry that status back to
// the plugin. Most producers of ErrorMessage put the status on StatusCode and a
// plain error in Error, so reading only the inner error loses it.
func TestModelExecutionErrorCarriesUpstreamStatus(t *testing.T) {
	for _, tc := range []struct {
		name   string
		errMsg *interfaces.ErrorMessage
		want   int
	}{
		{
			name:   "plain inner error keeps the reported status",
			errMsg: &interfaces.ErrorMessage{StatusCode: http.StatusTooManyRequests, Error: errors.New("rate limited")},
			want:   http.StatusTooManyRequests,
		},
		{
			name:   "no inner error keeps the reported status",
			errMsg: &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway},
			want:   http.StatusBadGateway,
		},
		{
			name:   "inner error that reports its own status wins",
			errMsg: &interfaces.ErrorMessage{StatusCode: http.StatusTooManyRequests, Error: statusError{code: http.StatusForbidden}},
			want:   http.StatusForbidden,
		},
		{
			name:   "no status anywhere stays unknown",
			errMsg: &interfaces.ErrorMessage{Error: errors.New("opaque")},
			want:   0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			errCall := modelExecutionError(tc.errMsg)
			if errCall == nil {
				t.Fatal("modelExecutionError returned nil")
			}
			if got := clienterror.HTTPStatusFromError(errCall); got != tc.want {
				t.Fatalf("status = %d, want %d", got, tc.want)
			}
			// The envelope the plugin actually decodes must carry it too.
			raw := marshalRPCError("host_call_failed", errCall.Error(), clienterror.HTTPStatusFromError(errCall))
			_, errDecode := decodeRPCEnvelope[rpcEmptyResponse](raw)
			if errDecode == nil {
				t.Fatal("decodeRPCEnvelope returned nil error")
			}
			provider, ok := errDecode.(interface{ StatusCode() int })
			if !ok {
				t.Fatalf("decoded error %T does not expose StatusCode", errDecode)
			}
			if got := provider.StatusCode(); got != tc.want {
				t.Fatalf("decoded status = %d, want %d", got, tc.want)
			}
		})
	}
}

// Wrapping must not break errors.Is for callers that inspect the inner error.
func TestModelExecutionErrorRemainsUnwrappable(t *testing.T) {
	sentinel := errors.New("sentinel")
	errCall := modelExecutionError(&interfaces.ErrorMessage{StatusCode: http.StatusTooManyRequests, Error: sentinel})
	if !errors.Is(errCall, sentinel) {
		t.Fatalf("errors.Is lost the inner error: %v", errCall)
	}
	if errCall.Error() != sentinel.Error() {
		t.Fatalf("message = %q, want %q", errCall.Error(), sentinel.Error())
	}
}

type statusError struct{ code int }

func (e statusError) Error() string   { return "status error" }
func (e statusError) StatusCode() int { return e.code }
