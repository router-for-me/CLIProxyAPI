package claude

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	baseauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth"
)

func TestMain(m *testing.M) {
	ReadClaudeWindowsPowerState = func(context.Context) (baseauth.WindowsPowerState, error) {
		return baseauth.WindowsPowerState{ModernStandby: true, DisplayKnown: true, DisplayOn: true}, nil
	}
	os.Exit(m.Run())
}

func TestClaudeRefreshDefersDuringModernStandbyThenRenews(t *testing.T) {
	resetClaudeRefreshState()
	t.Cleanup(resetClaudeRefreshState)

	previous := ReadClaudeWindowsPowerState
	t.Cleanup(func() { ReadClaudeWindowsPowerState = previous })
	displayOn := false
	checks := 0
	ReadClaudeWindowsPowerState = func(context.Context) (baseauth.WindowsPowerState, error) {
		checks++
		return baseauth.WindowsPowerState{ModernStandby: true, DisplayKnown: true, DisplayOn: displayOn}, nil
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
						Body:       io.NopCloser(strings.NewReader(`{"access_token":"claude-new-access","refresh_token":"claude-new-refresh","expires_in":28800}`)),
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

	token, errRefresh := auth.RefreshTokensWithRetry(context.Background(), "claude-standby-refresh", 3)
	if token != nil || errRefresh == nil || calls != 0 || checks != 1 {
		t.Fatalf("display off: token=%v, err=%v, calls=%d, checks=%d; want deferral without HTTP calls or retries", token, errRefresh, calls, checks)
	}
	if !errors.Is(errRefresh, ErrClaudeRefreshDeferred) {
		t.Fatalf("display off: error %v did not wrap ErrClaudeRefreshDeferred", errRefresh)
	}

	displayOn = true
	token, errRefresh = auth.RefreshTokensWithRetry(context.Background(), "claude-standby-refresh", 3)
	if errRefresh != nil || token == nil || token.AccessToken != "claude-new-access" || token.RefreshToken != "claude-new-refresh" || calls != 1 {
		t.Fatalf("display on: token=%+v, err=%v, calls=%d; want renewal", token, errRefresh, calls)
	}
}
