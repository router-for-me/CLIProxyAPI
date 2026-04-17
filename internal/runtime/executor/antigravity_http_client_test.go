package executor

import (
	"context"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor/helps"
)

func TestNewAntigravityHTTPClientDoesNotMutateSharedProxyAwareClient(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{}
	sharedClient := helps.NewProxyAwareHTTPClient(context.Background(), cfg, nil, 0)

	sharedTransport, ok := sharedClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("shared transport type = %T, want *http.Transport", sharedClient.Transport)
	}

	client := newAntigravityHTTPClient(context.Background(), cfg, nil, 0)

	antigravityTransport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("antigravity transport type = %T, want *http.Transport", client.Transport)
	}
	if client == sharedClient {
		t.Fatal("expected antigravity client to copy shared client settings")
	}
	if antigravityTransport == sharedTransport {
		t.Fatal("expected antigravity transport to be cloned from shared transport")
	}
	if !sharedTransport.ForceAttemptHTTP2 {
		t.Fatal("expected shared proxy-aware transport to keep HTTP/2 enabled")
	}
	if antigravityTransport.ForceAttemptHTTP2 {
		t.Fatal("expected antigravity transport to force HTTP/1.1")
	}
}
