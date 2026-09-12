package executor

import (
	"context"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/httptransport"
	"net/http"
	"testing"
)

type auditTestTransport struct{ http.RoundTripper }

func TestAntigravityDecoratorRunsAfterCredentialHTTP11PoolSelection(t *testing.T) {
	for _, proxy := range []string{"", "http://localhost:12345"} {
		calls := 0
		var selected http.RoundTripper
		ctx := httptransport.WithDecorator(context.Background(), func(next http.RoundTripper) http.RoundTripper {
			calls++
			selected = next
			tr, ok := next.(*http.Transport)
			if !ok || tr.ForceAttemptHTTP2 {
				t.Fatal("decorator preceded H1 selection")
			}
			return &auditTestTransport{next}
		})
		auth := &coreauth.Auth{ID: "decorator-fixture", ProxyURL: proxy}
		c := newAntigravityHTTPClient(ctx, &config.Config{}, auth, 0)
		plain := newAntigravityHTTPClient(context.Background(), &config.Config{}, auth, 0)
		if calls != 1 || c.Transport.(*auditTestTransport).RoundTripper != selected || plain.Transport != selected {
			t.Fatal("pool replaced or helper decorated twice")
		}
	}
}
