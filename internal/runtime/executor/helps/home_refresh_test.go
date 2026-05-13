package helps

import (
	"net/http"
	"testing"
)

func TestStatusFromHomeErrorCodeMapsAuthenticationErrorToUnauthorized(t *testing.T) {
	if got := statusFromHomeErrorCode("authentication_error"); got != http.StatusUnauthorized {
		t.Fatalf("statusFromHomeErrorCode(authentication_error) = %d, want %d", got, http.StatusUnauthorized)
	}
	if got := statusFromHomeErrorCode("unauthorized"); got != http.StatusUnauthorized {
		t.Fatalf("statusFromHomeErrorCode(unauthorized) = %d, want %d", got, http.StatusUnauthorized)
	}
}
