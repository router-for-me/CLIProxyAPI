package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestClaudeCancelledLoginDoesNotStartOAuth(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	record, err := NewClaudeAuthenticator().Login(ctx, &config.Config{}, &LoginOptions{NoBrowser: true})
	if record != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled login was not rejected: %v", err)
	}
}
