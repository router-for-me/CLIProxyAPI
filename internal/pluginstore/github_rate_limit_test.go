package pluginstore

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func githubTestHeaders(values map[string]string) http.Header {
	headers := make(http.Header)
	for name, value := range values {
		headers.Set(name, value)
	}
	return headers
}

func TestGitHubRateLimitHeaders(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	reset := strconv.FormatInt(now.Add(time.Hour).Unix(), 10)
	tests := []struct {
		name    string
		status  int
		headers map[string]string
		body    string
		wait    time.Duration
	}{
		{name: "primary forbidden", status: 403, headers: map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": reset}, wait: time.Hour},
		{name: "retry seconds", status: 429, headers: map[string]string{"Retry-After": "120"}, wait: 2 * time.Minute},
		{name: "retry date", status: 429, headers: map[string]string{"Retry-After": now.Add(3 * time.Minute).Format(http.TimeFormat)}, wait: 3 * time.Minute},
		{name: "reset wins", status: 403, headers: map[string]string{"Retry-After": "120", "X-RateLimit-Remaining": "0", "X-RateLimit-Reset": reset}, wait: time.Hour},
		{name: "retry wins", status: 429, headers: map[string]string{"Retry-After": "7200", "X-RateLimit-Remaining": "0", "X-RateLimit-Reset": reset}, wait: 2 * time.Hour},
		{name: "secondary message", status: 403, body: `{"message":"You have exceeded a secondary rate limit."}`, wait: time.Minute},
		{name: "abuse message", status: 403, body: `{"message":"You have triggered an abuse detection mechanism."}`, wait: time.Minute},
		{name: "retry forbidden", status: 403, headers: map[string]string{"Retry-After": "60"}, wait: time.Minute},
		{name: "headerless too many requests", status: 429, wait: time.Minute},
		{name: "invalid retry", status: 429, headers: map[string]string{"Retry-After": "invalid"}, wait: time.Minute},
		{name: "overflow retry", status: 429, headers: map[string]string{"Retry-After": "9223372036854775807"}, wait: time.Minute},
		{name: "negative retry", status: 429, headers: map[string]string{"Retry-After": "-10"}, wait: time.Minute},
		{name: "past reset", status: 403, headers: map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": "1"}, wait: time.Minute},
		{name: "invalid reset", status: 403, headers: map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": "invalid"}, wait: time.Minute},
		{name: "permission denied", status: 403, body: `{"message":"Resource not accessible by integration"}`},
		{name: "invalid body", status: 403, body: "forbidden"},
		{name: "not found", status: 404},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			limiter := &GitHubRateLimiter{nowFunc: func() time.Time { return now }}
			errLimit := limiter.observe("quota", tt.status, githubTestHeaders(tt.headers), []byte(tt.body))
			if tt.wait == 0 {
				if errLimit != nil || limiter.check("quota") != nil {
					t.Fatalf("non-rate-limit response started cooldown: %v", errLimit)
				}
				return
			}
			var rateLimit *RateLimitError
			if !errors.As(errLimit, &rateLimit) || rateLimit.StatusCode != tt.status || !rateLimit.RetryAt.Equal(now.Add(tt.wait)) {
				t.Fatalf("error=%v, want status=%d retry=%s", errLimit, tt.status, now.Add(tt.wait))
			}
			if errCheck := limiter.check("quota"); !errors.As(errCheck, &rateLimit) {
				t.Fatalf("check=%v, want cooldown", errCheck)
			}
		})
	}
}

func TestGitHubRateLimitKeyCanonicalHost(t *testing.T) {
	t.Parallel()
	want := githubRateLimitKey("https://api.github.com/repos/owner/repo", "", nil, false)
	for _, requestURL := range []string{"https://api.github.com:443/repos/other/repo", "https://API.GITHUB.COM/releases"} {
		if got := githubRateLimitKey(requestURL, "", nil, false); got != want || got == "" {
			t.Fatalf("key for %s = %q, want %q", requestURL, got, want)
		}
	}
	for _, requestURL := range []string{"https://api.github.com:444/repos/owner/repo", "http://api.github.com/", "https://api.github.com.example/", "https://raw.githubusercontent.com/"} {
		if got := githubRateLimitKey(requestURL, "", nil, false); got != "" {
			t.Fatalf("unrelated URL %s has GitHub quota key %q", requestURL, got)
		}
	}
}

func TestGitHubRateLimitPrunesInactiveIdentities(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	limiter := &GitHubRateLimiter{nowFunc: func() time.Time { return now }}
	_ = limiter.observe("old-token", 429, nil, nil)
	_ = limiter.observe("active-token", 429, githubTestHeaders(map[string]string{"Retry-After": "7200"}), nil)
	now = now.Add(time.Hour + time.Minute)
	_ = limiter.check("new-token")
	if _, exists := limiter.entries["old-token"]; exists {
		t.Fatal("inactive credential cooldown was not pruned")
	}
	if errCheck := limiter.check("active-token"); errCheck == nil {
		t.Fatal("active cooldown was pruned")
	}
}

func TestGitHubRateLimitBackoffAndRecovery(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	limiter := &GitHubRateLimiter{nowFunc: func() time.Time { return now }}
	for _, delay := range []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 16 * time.Minute, 32 * time.Minute, time.Hour, time.Hour} {
		errLimit := limiter.observe("quota", 429, nil, nil)
		var rateLimit *RateLimitError
		if !errors.As(errLimit, &rateLimit) || !rateLimit.RetryAt.Equal(now.Add(delay)) {
			t.Fatalf("backoff=%v, want %s", errLimit, delay)
		}
		for range 5 {
			if limiter.check("quota") == nil {
				t.Fatal("local check lost cooldown")
			}
		}
		now = now.Add(delay)
		if errCheck := limiter.check("quota"); errCheck != nil {
			t.Fatalf("expired cooldown: %v", errCheck)
		}
	}
	_ = limiter.observe("quota", 200, nil, nil)
	var rateLimit *RateLimitError
	if errLimit := limiter.observe("quota", 429, nil, nil); !errors.As(errLimit, &rateLimit) || !rateLimit.RetryAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("success did not reset backoff: %v", errLimit)
	}
}

func TestGitHubRateLimitSuccessfulExhaustionAndLateResponses(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	limiter := &GitHubRateLimiter{nowFunc: func() time.Time { return now }}
	headers := githubTestHeaders(map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": strconv.FormatInt(now.Add(time.Hour).Unix(), 10)})
	if errObserve := limiter.observe("quota", 200, headers, nil); errObserve != nil {
		t.Fatalf("final successful request failed: %v", errObserve)
	}
	_ = limiter.observe("quota", 200, nil, nil)
	_ = limiter.observe("quota", 429, githubTestHeaders(map[string]string{"Retry-After": "60"}), nil)
	var rateLimit *RateLimitError
	if errCheck := limiter.check("quota"); !errors.As(errCheck, &rateLimit) || !rateLimit.RetryAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("late response shortened cooldown: %v", errCheck)
	}
}

func TestGitHubCooldownSharedAcrossClientsRepositoriesAndAssets(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	limiter := &GitHubRateLimiter{nowFunc: func() time.Time { return now }}
	calls := 0
	doer := pluginIdentityDoerFunc(func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return &http.Response{StatusCode: 429, Header: githubTestHeaders(map[string]string{"Retry-After": "60"}), Body: io.NopCloser(strings.NewReader("limited"))}, nil
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"tag_name":"v1.0.0"}`))}, nil
	})
	first := Client{HTTPClient: doer, RateLimiter: limiter}
	second := Client{HTTPClient: doer, RateLimiter: limiter}
	ctx := context.Background()
	_, errFirst := first.FetchLatestRelease(ctx, Plugin{Repository: "https://github.com/owner/first"})
	_, errSecond := second.FetchReleaseByTag(ctx, Plugin{Repository: "https://github.com/other/second"}, "v1.0.0")
	_, errAsset := second.DownloadAsset(ctx, ReleaseAsset{APIURL: "https://api.github.com:443/repos/other/second/releases/assets/1"})
	for _, errRequest := range []error{errFirst, errSecond, errAsset} {
		var rateLimit *RateLimitError
		if !errors.As(errRequest, &rateLimit) {
			t.Fatalf("request error=%v, want rate limit", errRequest)
		}
	}
	if calls != 1 {
		t.Fatalf("calls=%d, want 1", calls)
	}
	if _, errRaw := second.get(ctx, DefaultRegistryURL, "application/json", RequestKindRegistry, 0); errRaw != nil {
		t.Fatalf("API cooldown blocked raw registry: %v", errRaw)
	}
	now = now.Add(time.Minute)
	if _, errRecovered := second.FetchLatestRelease(ctx, Plugin{Repository: "https://github.com/other/second"}); errRecovered != nil || calls != 3 {
		t.Fatalf("recovery error=%v calls=%d", errRecovered, calls)
	}
}

func TestGitHubCooldownSeparatesCredentialsAndNetworkScope(t *testing.T) {
	t.Parallel()
	limiter := &GitHubRateLimiter{}
	calls := 0
	client := Client{RateLimiter: limiter, HTTPClient: pluginIdentityDoerFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 429, Header: githubTestHeaders(map[string]string{"Retry-After": "3600"}), Body: io.NopCloser(strings.NewReader("limited"))}, nil
	})}
	plugin := Plugin{Repository: "https://github.com/owner/repo"}
	for _, token := range []string{"", "token-a", "token-b", "token-a", ""} {
		client.ResolvedAuth = nil
		if token != "" {
			client.ResolvedAuth = []ResolvedAuthConfig{{Match: "https://api.github.com/", Type: AuthTypeGitHubToken, Token: Secret(token)}}
		}
		_, _ = client.FetchLatestRelease(context.Background(), plugin)
	}
	if calls != 3 {
		t.Fatalf("credential scopes made %d calls, want 3", calls)
	}
	client.NetworkScope = "other-egress"
	_, _ = client.FetchLatestRelease(context.Background(), plugin)
	if calls != 4 {
		t.Fatalf("egress scopes made %d calls, want 4", calls)
	}
}

func TestGitHubRateLimitAuthenticatedBodyIsNotExposed(t *testing.T) {
	t.Parallel()
	client := Client{
		RateLimiter:  &GitHubRateLimiter{},
		ResolvedAuth: []ResolvedAuthConfig{{Match: "https://api.github.com/", Type: AuthTypeGitHubToken, Token: Secret("secret-token")}},
		HTTPClient: pluginIdentityDoerFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 403, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"message":"secondary rate limit: secret-token"}`))}, nil
		}),
	}
	_, errRelease := client.FetchLatestRelease(context.Background(), Plugin{Repository: "https://github.com/owner/repo"})
	var rateLimit *RateLimitError
	if !errors.As(errRelease, &rateLimit) || strings.Contains(errRelease.Error(), "secret-token") {
		t.Fatalf("unsafe or unclassified error: %v", errRelease)
	}
}

func TestGitHubDefaultCooldownBlocksConcurrentNewClients(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	client := Client{NetworkScope: t.Name(), HTTPClient: pluginIdentityDoerFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: 429, Header: githubTestHeaders(map[string]string{"Retry-After": "3600"}), Body: io.NopCloser(strings.NewReader("limited"))}, nil
	})}
	key := githubRateLimitKey("https://api.github.com/", client.NetworkScope, nil, false)
	t.Cleanup(func() {
		defaultGitHubRateLimiter.mu.Lock()
		delete(defaultGitHubRateLimiter.entries, key)
		defaultGitHubRateLimiter.mu.Unlock()
	})
	_, _ = client.FetchLatestRelease(context.Background(), Plugin{Repository: "https://github.com/owner/first"})
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			copyClient := Client{NetworkScope: client.NetworkScope, HTTPClient: client.HTTPClient}
			_, errRelease := copyClient.FetchLatestRelease(context.Background(), Plugin{Repository: "https://github.com/other/second"})
			var rateLimit *RateLimitError
			if !errors.As(errRelease, &rateLimit) {
				t.Errorf("request error = %v", errRelease)
			}
		})
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("calls=%d, want 1", calls.Load())
	}
}

type githubHeaderOnlyBody struct {
	reads  int
	closed bool
}

func (body *githubHeaderOnlyBody) Read([]byte) (int, error) {
	body.reads++
	return 0, errors.New("body must not be read")
}

func (body *githubHeaderOnlyBody) Close() error {
	body.closed = true
	return nil
}

func TestGitHubHeaderCooldownClosesBodyWithoutReading(t *testing.T) {
	t.Parallel()
	body := &githubHeaderOnlyBody{}
	client := Client{RateLimiter: &GitHubRateLimiter{}, HTTPClient: pluginIdentityDoerFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 429, Header: githubTestHeaders(map[string]string{"Retry-After": "60"}), Body: body}, nil
	})}
	_, errRelease := client.FetchLatestRelease(context.Background(), Plugin{Repository: "https://github.com/owner/repo"})
	var rateLimit *RateLimitError
	if !errors.As(errRelease, &rateLimit) || body.reads != 0 || !body.closed {
		t.Fatalf("error=%v reads=%d closed=%v", errRelease, body.reads, body.closed)
	}
}

func TestGitHubSuccessfulFinalRequestReturnsRelease(t *testing.T) {
	t.Parallel()
	calls := 0
	client := Client{RateLimiter: &GitHubRateLimiter{}, HTTPClient: pluginIdentityDoerFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{
			StatusCode: 200,
			Header:     githubTestHeaders(map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)}),
			Body:       io.NopCloser(strings.NewReader(`{"tag_name":"v1.0.0"}`)),
		}, nil
	})}
	release, errRelease := client.FetchLatestRelease(context.Background(), Plugin{Repository: "https://github.com/owner/repo"})
	if errRelease != nil || release.TagName != "v1.0.0" {
		t.Fatalf("release=%#v error=%v", release, errRelease)
	}
	_, errNext := client.FetchLatestRelease(context.Background(), Plugin{Repository: "https://github.com/owner/other"})
	var rateLimit *RateLimitError
	if !errors.As(errNext, &rateLimit) || calls != 1 {
		t.Fatalf("next error=%v calls=%d", errNext, calls)
	}
}

func TestNonGitHubRateLimitDoesNotCoolGitHub(t *testing.T) {
	t.Parallel()
	calls := 0
	client := Client{RateLimiter: &GitHubRateLimiter{}, HTTPClient: pluginIdentityDoerFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.Host != "api.github.com" {
			return &http.Response{StatusCode: 429, Header: githubTestHeaders(map[string]string{"Retry-After": "3600"}), Body: io.NopCloser(strings.NewReader("limited"))}, nil
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"tag_name":"v1.0.0"}`))}, nil
	})}
	_, errRegistry := client.FetchRegistry(context.Background())
	var rateLimit *RateLimitError
	if errRegistry == nil || errors.As(errRegistry, &rateLimit) {
		t.Fatalf("non-API error=%v, want ordinary HTTP error", errRegistry)
	}
	_, errRelease := client.FetchLatestRelease(context.Background(), Plugin{Repository: "https://github.com/owner/repo"})
	if errRelease != nil || calls != 2 {
		t.Fatalf("release error=%v calls=%d", errRelease, calls)
	}
}

func TestRateLimitRetryAfterRoundsUp(t *testing.T) {
	t.Parallel()
	now := time.Unix(100, 0)
	for _, tt := range []struct {
		delay time.Duration
		want  int64
	}{{-time.Second, 0}, {0, 0}, {time.Nanosecond, 1}, {time.Second, 1}, {time.Second + time.Nanosecond, 2}} {
		errLimit := &RateLimitError{RetryAt: now.Add(tt.delay)}
		if got := errLimit.RetryAfterSeconds(now); got != tt.want {
			t.Fatalf("RetryAfterSeconds(%s)=%d, want %d", tt.delay, got, tt.want)
		}
	}
}
