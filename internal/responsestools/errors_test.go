package responsestools

import (
	"errors"
	"net/http"
	"testing"
)

func TestToolCompatibilityErrorImplementsRuntimeErrorContracts(t *testing.T) {
	tests := []struct {
		name   string
		err    *ToolCompatibilityError
		status int
	}{
		{name: "syntax", err: syntaxError(errors.New("invalid JSON")), status: http.StatusBadRequest},
		{name: "unprocessable", err: unprocessableError(ReasonUnsupportedProtocol, errors.New("unsupported")), status: http.StatusUnprocessableEntity},
		{name: "budget", err: budgetError(ReasonAttemptBudget, errors.New("too large")), status: http.StatusRequestEntityTooLarge},
		{name: "capacity", err: capacityError(errors.New("full")), status: http.StatusTooManyRequests},
		{name: "upstream", err: upstreamError(ReasonUpstreamContract, errors.New("invalid stream")), status: http.StatusBadGateway},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var statusErr interface{ StatusCode() int }
			if !errors.As(test.err, &statusErr) {
				t.Fatalf("%T does not implement StatusCode()", test.err)
			}
			if got := statusErr.StatusCode(); got != test.status {
				t.Fatalf("StatusCode() = %d, want %d", got, test.status)
			}
			var requestErr interface{ IsRequestScoped() bool }
			if !errors.As(test.err, &requestErr) || !requestErr.IsRequestScoped() {
				t.Fatalf("%T does not implement IsRequestScoped() == true", test.err)
			}
			var neutralErr interface{ AvailabilityNeutral() bool }
			if !errors.As(test.err, &neutralErr) || !neutralErr.AvailabilityNeutral() {
				t.Fatalf("%T does not implement AvailabilityNeutral() == true", test.err)
			}
		})
	}
}
