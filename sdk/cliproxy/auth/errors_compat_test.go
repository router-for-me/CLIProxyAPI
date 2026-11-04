package auth_test

import (
	"net/http"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestErrorLegacyUnkeyedLiteralCompatibility(t *testing.T) {
	err := cliproxyauth.Error{"code", "message", false, http.StatusRequestTimeout}

	if err.Code != "code" || err.Message != "message" || err.Retryable || err.HTTPStatus != http.StatusRequestTimeout {
		t.Fatalf("unexpected error fields: %#v", err)
	}
}
