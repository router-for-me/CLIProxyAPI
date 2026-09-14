package pluginhost

import (
	"errors"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
)

// Regression: a plugin calling host.model.execute must be able to recover the
// upstream HTTP status from the error when the executor sets BOTH StatusCode and
// Error (the common case for a 429). Before the fix, Error won and the status
// was silently dropped, leaving every status-based failover plugin blind.
func TestModelExecutionErrorKeepsStatusWhenErrorSet(t *testing.T) {
	upstream := errors.New(`{"type":"error","error":{"type":"rate_limit_error","message":"cooling down"}}`)
	err := modelExecutionError(&interfaces.ErrorMessage{StatusCode: 429, Error: upstream})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "429") {
		t.Fatalf("status code lost: %q", err.Error())
	}
	if !errors.Is(err, upstream) {
		t.Fatalf("upstream error not wrapped: %q", err.Error())
	}
}
