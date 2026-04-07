package auth

import (
	"net/http"
	"testing"
)

func TestErrorStatusCode_MapsAuthAvailabilityErrorsTo503(t *testing.T) {
	tests := []struct {
		name string
		code string
	}{
		{name: "auth_not_found", code: "auth_not_found"},
		{name: "auth_unavailable", code: "auth_unavailable"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &Error{Code: tt.code, Message: "no auth available"}
			if got := err.StatusCode(); got != http.StatusServiceUnavailable {
				t.Fatalf("StatusCode() = %d, want %d", got, http.StatusServiceUnavailable)
			}
		})
	}
}
