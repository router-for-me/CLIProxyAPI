//go:build windows

package claude

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	baseauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth"
)

func TestClaudeRefreshDefersOnWindowsStandbyThenRenews(t *testing.T) {
	resetClaudeRefreshState()
	t.Cleanup(resetClaudeRefreshState)

	previous := CheckClaudeWindowsReadiness
	t.Cleanup(func() { CheckClaudeWindowsReadiness = previous })

	deferred := true
	checks := 0
	CheckClaudeWindowsReadiness = func(context.Context) error {
		checks++
		if deferred {
			return fmt.Errorf("Claude renewal deferred until interactive Windows wake: %w", baseauth.ErrRefreshDeferred)
		}
		return nil
	}

	calls := 0
	auth := &ClaudeAuth{
		httpClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch req.URL.String() {
				case RefreshTokenURL:
					calls++
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     make(http.Header),
						Body:       io.NopCloser(strings.NewReader(`{"access_token":"claude-win-access","refresh_token":"claude-win-refresh","expires_in":28800}`)),
						Request:    req,
					}, nil
				case ProfileURL:
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     make(http.Header),
						Body:       io.NopCloser(strings.NewReader(`{"account":{"uuid":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","email":"user@example.com"},"organization":{"uuid":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","name":"Example Org"}}`)),
						Request:    req,
					}, nil
				default:
					t.Fatalf("unexpected OAuth request URL %s", req.URL)
					return nil, nil
				}
			}),
		},
	}

	token, errRefresh := auth.RefreshTokensWithRetry(context.Background(), "claude-win-sleep-refresh", 3)
	if token != nil || errRefresh == nil || calls != 0 || checks != 1 {
		t.Fatalf("standby wake: token=%v, err=%v, calls=%d, checks=%d; want deferral without HTTP calls or retries", token, errRefresh, calls, checks)
	}
	if !errors.Is(errRefresh, ErrClaudeRefreshDeferred) {
		t.Fatalf("standby wake error %v did not wrap ErrClaudeRefreshDeferred", errRefresh)
	}

	deferred = false
	token, errRefresh = auth.RefreshTokensWithRetry(context.Background(), "claude-win-sleep-refresh", 3)
	if errRefresh != nil || token == nil || token.AccessToken != "claude-win-access" || token.RefreshToken != "claude-win-refresh" || calls != 1 {
		t.Fatalf("interactive wake renewal failed: token=%+v, err=%v, calls=%d", token, errRefresh, calls)
	}
}
