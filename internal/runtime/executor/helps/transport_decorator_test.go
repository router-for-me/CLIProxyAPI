package helps

import (
	"context"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/httptransport"
	"net/http"
	"testing"
)

type observingTransport struct{ http.RoundTripper }

func TestClientDecoratorsPreserveProtectedAndProxyTransports(t *testing.T) {
	for _, factory := range []string{"plain", "utls"} {
		for _, proxy := range []string{"", "http://localhost:12345"} {
			t.Run(factory+proxy, func(t *testing.T) {
				calls := 0
				var selected http.RoundTripper
				ctx := httptransport.WithDecorator(context.Background(), func(next http.RoundTripper) http.RoundTripper {
					calls++
					selected = next
					return &observingTransport{next}
				})
				auth := &coreauth.Auth{ProxyURL: proxy}
				var c *http.Client
				if factory == "utls" {
					c = NewUtlsHTTPClient(ctx, &config.Config{}, auth, 0)
					rt, ok := selected.(*fallbackRoundTripper)
					if !ok || rt.anthropic == nil || rt.chrome == nil || rt.fallback == nil {
						t.Fatal("lost TLS profiles")
					}
				} else {
					c = NewProxyAwareHTTPClient(ctx, &config.Config{}, auth, 0)
				}
				if calls != 1 || c.Transport.(*observingTransport).RoundTripper != selected {
					t.Fatal("decoration replaced transport or ran twice")
				}
			})
		}
	}
}
