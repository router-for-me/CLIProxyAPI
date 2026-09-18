package pluginhost

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
)

func TestModelExecutionErrorStatus(t *testing.T) {
	if modelExecutionError(nil) != nil {
		t.Fatal("nil input must return nil")
	}
	plain := errors.New("  opaque failure\n")
	typed := &rpcError{message: "opaque failure", statusCode: 429}
	for _, tc := range []struct {
		name         string
		cause        error
		status, want int
	}{
		{"503", plain, 503, 503},
		{"429", plain, 429, 429},
		{"400", plain, 400, 400},
		{"401", plain, 401, 401},
		{"422", plain, 422, 422},
		{"upper boundary", plain, 599, 599},
		{"wrapped plain", fmt.Errorf("outer: %w", plain), 503, 503},
		{"typed precedence", typed, 503, 429},
		{"wrapped typed precedence", fmt.Errorf("outer: %w", typed), 503, 429},
		{"invalid typed fallback", &rpcError{message: "opaque", statusCode: 600}, 401, 401},
		{"overflow typed fallback", &rpcError{message: "opaque", statusCode: int(^uint(0) >> 1)}, 422, 422},
		{"invalid typed default", &rpcError{message: "opaque", statusCode: -1}, 0, 500},
		{"default", plain, 0, 500},
		{"negative", plain, -1, 500},
		{"success is not error", plain, 200, 500},
		{"below boundary", plain, 399, 500},
		{"above boundary", plain, 600, 500},
		{"too large", plain, int(^uint(0) >> 1), 500},
		{"cancel", context.Canceled, 499, 499},
		{"wrapped cancel", fmt.Errorf("outer: %w", context.Canceled), 0, 500},
		{"status only", nil, 503, 503},
		{"empty", nil, 0, 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := modelExecutionError(&interfaces.ErrorMessage{StatusCode: tc.status, Error: tc.cause})
			var statusErr interface{ StatusCode() int }
			if !errors.As(err, &statusErr) || statusErr.StatusCode() != tc.want {
				t.Errorf("error %v must preserve status %d", err, tc.want)
			}
			if tc.cause != nil {
				if err.Error() != tc.cause.Error() || !errors.Is(err, tc.cause) {
					t.Fatal("message or error identity lost")
				}
				var original *rpcError
				if errors.As(tc.cause, &original) {
					var recovered *rpcError
					if !errors.As(err, &recovered) || recovered != original {
						t.Fatal("errors.As identity lost")
					}
				}
			} else {
				want := "model execution failed"
				if tc.status > 0 {
					want = fmt.Sprintf("model execution failed with status %d", tc.status)
				}
				if err.Error() != want {
					t.Fatalf("message = %q, want %q", err.Error(), want)
				}
			}
		})
	}
}
