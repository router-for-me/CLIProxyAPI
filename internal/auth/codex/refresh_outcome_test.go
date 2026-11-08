package codex

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestRefreshOutcomeSurvivesLaterCallAndNewClient(t *testing.T) {
	resetCodexRefreshGroupForTest()
	t.Cleanup(resetCodexRefreshGroupForTest)
	cfg := &config.Config{AuthDir: t.TempDir()}
	calls := 0
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if errParse := req.ParseForm(); errParse != nil {
			t.Fatal(errParse)
		}
		if req.Form.Get("refresh_token") == "fresh-authorization" {
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"access_token":"fresh-access","refresh_token":"rotated-fresh","expires_in":3600}`)), Request: req}, nil
		}
		return nil, errors.New("response lost after possible acceptance")
	})
	for attempt := 0; attempt < 2; attempt++ {
		auth := NewCodexAuth(cfg)
		auth.httpClient.Transport = transport
		_, errRefresh := auth.RefreshTokensWithRetry(context.Background(), "unresolved-grant", 1)
		if errRefresh == nil {
			t.Fatal("expected unresolved refresh error")
		}
	}
	if calls != 1 {
		t.Fatalf("unresolved grant replayed by later caller: calls = %d, want 1", calls)
	}
	auth := NewCodexAuth(cfg)
	auth.httpClient.Transport = transport
	tokens, errRefresh := auth.RefreshTokens(context.Background(), "fresh-authorization")
	if errRefresh != nil || tokens == nil || tokens.AccessToken != "fresh-access" {
		t.Fatalf("new authorization must recover: tokens = %v, error = %v", tokens, errRefresh)
	}
	if calls != 2 {
		t.Fatalf("new grant calls = %d, want 2", calls)
	}
}

func TestRefreshJournalSurvivesProcessRestart(t *testing.T) {
	if dir := os.Getenv("CLIPROXY_TEST_REFRESH_JOURNAL"); dir != "" {
		auth := NewCodexAuth(&config.Config{AuthDir: dir})
		auth.httpClient.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("unresolved grant replayed after restart")
			return nil, nil
		})
		_, errRefresh := auth.RefreshTokens(context.Background(), "restart-grant")
		var outcome *RefreshOutcomeUnknownError
		if !errors.As(errRefresh, &outcome) || outcome.Stage != "transport" {
			t.Fatalf("lost stored outcome: %v", errRefresh)
		}
		return
	}
	dir := t.TempDir()
	auth := NewCodexAuth(&config.Config{AuthDir: dir})
	auth.httpClient.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, io.ErrUnexpectedEOF })
	if _, errRefresh := auth.RefreshTokens(context.Background(), "restart-grant"); !IsRefreshOutcomeUnknown(errRefresh) {
		t.Fatal(errRefresh)
	}
	binary, errExecutable := os.Executable()
	if errExecutable != nil {
		t.Fatal(errExecutable)
	}
	cmd := exec.Command(binary, "-test.run=^TestRefreshJournalSurvivesProcessRestart$")
	cmd.Env = append(os.Environ(), "CLIPROXY_TEST_REFRESH_JOURNAL="+dir)
	if output, errRun := cmd.CombinedOutput(); errRun != nil {
		t.Fatalf("restart: %v\n%s", errRun, output)
	}
}

func TestRefreshReceiptFollowsLaterRotation(t *testing.T) {
	auth := NewCodexAuth(&config.Config{AuthDir: t.TempDir()})
	calls := 0
	auth.httpClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if errParse := req.ParseForm(); errParse != nil {
			t.Fatal(errParse)
		}
		body := `{"access_token":"access-second","refresh_token":"third","expires_in":3600}`
		if req.Form.Get("refresh_token") == "first" {
			body = `{"access_token":"access-first","refresh_token":"second","expires_in":3600}`
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})
	for _, token := range []string{"first", "second"} {
		if _, errRefresh := auth.RefreshTokens(context.Background(), token); errRefresh != nil {
			t.Fatal(errRefresh)
		}
	}
	result, errRefresh := auth.RefreshTokens(context.Background(), "first")
	if errRefresh != nil || result == nil || result.RefreshToken != "third" || calls != 2 {
		t.Fatalf("late caller received stale rotation: %+v, %v, calls=%d", result, errRefresh, calls)
	}
}

func TestSIWCUnknownOutcomeMustNotReplay(t *testing.T) {
	resetCodexRefreshGroupForTest()
	t.Cleanup(resetCodexRefreshGroupForTest)
	calls := 0
	auth := &CodexAuth{httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: &qualificationLostBody{}, Request: req}, nil
	})}}
	for i := 0; i < 2; i++ {
		_, errRefresh := auth.RefreshSIWCTokensWithRetry(context.Background(), "unresolved-siwc", "issued-client", "device", 3)
		if errRefresh == nil {
			t.Fatal("expected unknown SIWC outcome")
		}
	}
	if calls != 1 {
		t.Fatalf("SIWC unresolved grant calls = %d, want 1", calls)
	}
}

func TestRefreshRedirectCannotReplayGrant(t *testing.T) {
	resetCodexRefreshGroupForTest()
	t.Cleanup(resetCodexRefreshGroupForTest)
	calls := 0
	auth := &CodexAuth{httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return &http.Response{StatusCode: 307, Header: http.Header{"Location": []string{"https://auth.openai.com/redirected"}}, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"access_token":"new","refresh_token":"new-refresh","expires_in":3600}`)), Request: req}, nil
	})}}
	_, errRefresh := auth.RefreshTokensWithRetry(context.Background(), "redirect-grant", 3)
	if !IsRefreshOutcomeUnknown(errRefresh) || calls != 1 {
		t.Fatalf("redirect replayed grant: calls=%d, error=%v", calls, errRefresh)
	}
}

func TestRefreshHTTPFailureDoesNotProveReplaySafe(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			resetCodexRefreshGroupForTest()
			t.Cleanup(resetCodexRefreshGroupForTest)
			calls := 0
			auth := &CodexAuth{httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":"unavailable"}`)), Request: req}, nil
			})}}
			_, errRefresh := auth.RefreshTokensWithRetry(context.Background(), "unknown-http-outcome", 2)
			if errRefresh == nil || calls != 1 {
				t.Fatalf("status %d cannot authorize replay: calls = %d, error = %v", status, calls, errRefresh)
			}
		})
	}
}
