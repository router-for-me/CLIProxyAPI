//go:build windows

package codex

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

func TestCodexRefreshDefersOnWindowsStandbyThenRenews(t *testing.T) {
	resetCodexRefreshGroupForTest()
	t.Cleanup(resetCodexRefreshGroupForTest)

	previous := CheckCodexWindowsReadiness
	t.Cleanup(func() { CheckCodexWindowsReadiness = previous })

	deferred := true
	checks := 0
	CheckCodexWindowsReadiness = func(_ context.Context, label string) error {
		checks++
		if deferred {
			return fmt.Errorf("%s renewal deferred until interactive Windows wake: %w", label, baseauth.ErrRefreshDeferred)
		}
		return nil
	}

	calls := 0
	auth := NewCodexAuth(nil)
	auth.SetRefreshJournal(&FileRefreshJournal{Dir: t.TempDir()})
	auth.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"access_token":"codex-win-access","refresh_token":"codex-win-refresh","expires_in":3600}`)),
			Request:    req,
		}, nil
	})}

	token, errRefresh := auth.RefreshTokensWithRetry(context.Background(), "codex-win-sleep-refresh", 3)
	if token != nil || errRefresh == nil || calls != 0 || checks != 1 {
		t.Fatalf("standby wake: token=%v, err=%v, calls=%d, checks=%d; want deferral without HTTP calls or retries", token, errRefresh, calls, checks)
	}
	if !errors.Is(errRefresh, ErrSIWCRefreshDeferred) || !errors.Is(errRefresh, baseauth.ErrRefreshDeferred) {
		t.Fatalf("standby wake error %v did not wrap both ErrSIWCRefreshDeferred and ErrRefreshDeferred", errRefresh)
	}

	deferred = false
	token, errRefresh = auth.RefreshTokensWithRetry(context.Background(), "codex-win-sleep-refresh", 3)
	if errRefresh != nil || token == nil || token.AccessToken != "codex-win-access" || token.RefreshToken != "codex-win-refresh" || calls != 1 {
		t.Fatalf("interactive wake renewal failed: token=%+v, err=%v, calls=%d", token, errRefresh, calls)
	}
}

func TestSIWCRefreshDefersOnWindowsStandbyThenRenews(t *testing.T) {
	previous := CheckCodexWindowsReadiness
	t.Cleanup(func() { CheckCodexWindowsReadiness = previous })

	deferred := true
	checks := 0
	CheckCodexWindowsReadiness = func(_ context.Context, label string) error {
		checks++
		if deferred {
			return fmt.Errorf("%s renewal deferred until interactive Windows wake: %w", label, baseauth.ErrRefreshDeferred)
		}
		return nil
	}

	calls := 0
	auth := &CodexAuth{httpClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"access_token":"siwc-win-access","refresh_token":"siwc-win-refresh","expires_in":3600,"scope":"chatgpt.tokens.use.direct"}`)),
		}, nil
	})}}
	auth.SetRefreshJournal(&memoryRefreshJournal{})

	token, errRefresh := auth.RefreshSIWCTokensWithRetry(context.Background(), "siwc-win-sleep-refresh", "oaiapp_win", "", 3)
	if token != nil || errRefresh == nil || calls != 0 || checks != 1 {
		t.Fatalf("standby wake: token=%v, err=%v, calls=%d, checks=%d; want deferral without HTTP calls or retries", token, errRefresh, calls, checks)
	}
	if !errors.Is(errRefresh, ErrSIWCRefreshDeferred) || !errors.Is(errRefresh, baseauth.ErrRefreshDeferred) {
		t.Fatalf("standby wake error %v did not wrap both ErrSIWCRefreshDeferred and ErrRefreshDeferred", errRefresh)
	}

	deferred = false
	token, errRefresh = auth.RefreshSIWCTokensWithRetry(context.Background(), "siwc-win-sleep-refresh", "oaiapp_win", "", 3)
	if errRefresh != nil || token == nil || token.RefreshToken != "siwc-win-refresh" || calls != 1 {
		t.Fatalf("interactive wake renewal failed: token=%+v, err=%v, calls=%d", token, errRefresh, calls)
	}
}
