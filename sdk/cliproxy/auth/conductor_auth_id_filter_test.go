package auth

import (
	"context"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestAuthSelectionEligibility_AllowedAndExcludedIDs(t *testing.T) {
	ctx := WithAllowedAuthIDs(context.Background(), []string{"keep", "also"})
	ctx = WithExcludedAuthIDs(ctx, []string{"also"})
	e := authSelectionEligibilityForRequest(ctx, cliproxyexecutor.Options{})
	if !e.allows(&Auth{ID: "keep"}) {
		t.Fatal("keep should be allowed")
	}
	if e.allows(&Auth{ID: "also"}) {
		t.Fatal("also is excluded")
	}
	if e.allows(&Auth{ID: "other"}) {
		t.Fatal("other not in allow list")
	}
}
