package pluginstore

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	internalpluginstore "github.com/router-for-me/CLIProxyAPI/v7/internal/pluginstore"
)

type networkScopeDoer func(*http.Request) (*http.Response, error)

func (f networkScopeDoer) Do(req *http.Request) (*http.Response, error) { return f(req) }

func TestClientWithNetworkScope(t *testing.T) {
	for name, client := range map[string]Client{
		"plain":           NewClient(nil, ""),
		"auth":            NewClientWithAuth(nil, "", nil),
		"resolved":        NewClientWithResolvedAuth(nil, "", nil),
		"resolved expiry": NewClientWithResolvedAuthExpiry(nil, "", nil, time.Time{}),
	} {
		t.Run(name, func(t *testing.T) {
			scoped := client.WithNetworkScope("  http://proxy.example:8080  ")
			if scoped.inner.NetworkScope != "http://proxy.example:8080" || client.inner.NetworkScope != "" {
				t.Fatal("WithNetworkScope must normalize the scope without modifying the original client")
			}
			if scoped.WithNetworkScope("").inner.NetworkScope != "" {
				t.Fatal("empty scope must restore the direct-network identity")
			}
		})
	}
}

func TestClientNetworkScopeIsolatesAndSharesCooldowns(t *testing.T) {
	t.Parallel()
	limiter := &internalpluginstore.GitHubRateLimiter{}
	plugin := Plugin{Repository: "https://github.com/test/network-scope"}
	calls := 0
	client := NewClient(networkScopeDoer(func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return &http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": {"3600"}}, Body: io.NopCloser(strings.NewReader("limited"))}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"tag_name":"v1.0.0"}`))}, nil
	}), "")
	client.inner.RateLimiter = limiter
	proxyA := client.WithNetworkScope("http://proxy-a.example")
	_, errRelease := proxyA.FetchLatestRelease(context.Background(), plugin)
	var rateLimit *internalpluginstore.RateLimitError
	if !errors.As(errRelease, &rateLimit) || calls != 1 {
		t.Fatalf("first lookup: calls=%d error=%v", calls, errRelease)
	}
	for _, scope := range []string{"http://proxy-b.example", ""} {
		if _, errRelease = client.WithNetworkScope(scope).FetchLatestRelease(context.Background(), plugin); errRelease != nil {
			t.Fatalf("independent scope %q was blocked: %v", scope, errRelease)
		}
	}
	if calls != 3 {
		t.Fatalf("requests=%d, want 3", calls)
	}
	plugin.Repository = "https://github.com/test/another-repository"
	_, errRelease = client.WithNetworkScope(" http://proxy-a.example ").FetchLatestRelease(context.Background(), plugin)
	if !errors.As(errRelease, &rateLimit) || calls != 3 {
		t.Fatalf("same scope must share cooldown across clients and repositories: calls=%d error=%v", calls, errRelease)
	}
}
