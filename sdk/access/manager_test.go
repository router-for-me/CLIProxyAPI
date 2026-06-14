package access

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestManagerAuthenticateFailClosedWithoutProviders(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	_, authErr := NewManager().Authenticate(context.Background(), req)
	if !IsAuthErrorCode(authErr, AuthErrorCodeNoCredentials) {
		t.Fatalf("Authenticate() error = %v, want no credentials", authErr)
	}
}
