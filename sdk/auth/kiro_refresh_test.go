package auth

import (
	"context"
	"strings"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestKiroRefresh_IDCMissingClientCredentialsReturnsActionableError(t *testing.T) {
	a := NewKiroAuthenticator()
	auth := &coreauth.Auth{
		Provider: "kiro",
		Metadata: map[string]interface{}{
			"refresh_token": "rtok",
			"auth_method":   "idc",
		},
	}

	_, err := a.Refresh(context.Background(), nil, auth)
	if err == nil {
		t.Fatal("expected error for idc refresh without client credentials")
	}
	msg := err.Error()
	if !strings.Contains(msg, "missing idc client credentials") {
		t.Fatalf("expected actionable idc credential hint, got %q", msg)
	}
	if !strings.Contains(msg, "--kiro-aws-login") {
		t.Fatalf("expected remediation hint in message, got %q", msg)
	}
}
