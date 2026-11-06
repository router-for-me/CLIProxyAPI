package codex

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestSIWCRefreshDefersDuringMaintenanceWakeThenRenews(t *testing.T) {
	for _, state := range []string{
		"Current System Capabilities are: CPU Network\nCurrent Power State: 2\n",
		"Current System Capabilities are: CPU Graphics Audio\n",
		"Graphics Network",
	} {
		t.Run(strings.TrimSpace(state), func(t *testing.T) {
			previous := siwcReadSystemState
			t.Cleanup(func() { siwcReadSystemState = previous })
			currentState := state
			checks := 0
			siwcReadSystemState = func(context.Context) ([]byte, error) {
				checks++
				return []byte(currentState), nil
			}
			calls := 0
			auth := &CodexAuth{httpClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"access_token":"new-access","refresh_token":"new-refresh","expires_in":3600,"scope":"chatgpt.tokens.use.direct"}`))}, nil
			})}}
			token, errRefresh := auth.RefreshSIWCTokensWithRetry(context.Background(), "sleep-refresh", "oaiapp_sleep", "", 3)
			if token != nil || !errors.Is(errRefresh, context.Canceled) || calls != 0 || checks != 1 {
				t.Fatalf("maintenance wake: token returned = %t, error = %v, requests = %d, checks = %d; want cancellation without token requests or retries", token != nil, errRefresh, calls, checks)
			}
			var partition *SIWCRefreshError
			if !errors.As(errRefresh, &partition) {
				t.Fatalf("deferral lost the SIWC error partition: %v", errRefresh)
			}
			currentState = "Current System Capabilities are: CPU Graphics Audio Network\nCurrent Power State: 4\n"
			token, errRefresh = auth.RefreshSIWCTokensWithRetry(context.Background(), "sleep-refresh", "oaiapp_sleep", "", 3)
			if errRefresh != nil || token == nil || token.RefreshToken != "new-refresh" || calls != 1 {
				t.Fatalf("interactive wake: token returned = %t, error = %v, requests = %d; want successful renewal", token != nil, errRefresh, calls)
			}
		})
	}
}

func TestSIWCRefreshDoesNotConsumeGrantWhenPowerStateUnavailable(t *testing.T) {
	previous := siwcReadSystemState
	t.Cleanup(func() { siwcReadSystemState = previous })
	errProbe := errors.New("power-state probe unavailable")
	siwcReadSystemState = func(context.Context) ([]byte, error) { return nil, errProbe }
	auth := &CodexAuth{httpClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("token request sent without a known power state")
		return nil, nil
	})}}
	_, errRefresh := auth.RefreshSIWCTokens(context.Background(), "refresh", "oaiapp_probe", "")
	if !errors.Is(errRefresh, errProbe) {
		t.Fatalf("power-state error was lost: %v", errRefresh)
	}
}
