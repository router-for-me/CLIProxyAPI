package codex

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	ReadSIWCSystemState = func(context.Context) ([]byte, error) {
		return []byte("Current System Capabilities are: CPU Graphics Audio Network\nCurrent Power State: 4\n"), nil
	}
	os.Exit(m.Run())
}

func TestSIWCRefreshDefersDuringMaintenanceWakeThenRenews(t *testing.T) {
	for _, state := range []string{
		"Current System Capabilities are: CPU Network\nCurrent Power State: 2\n",
		"Current System Capabilities are: CPU Graphics Audio\n",
		"Graphics Network",
	} {
		t.Run(strings.TrimSpace(state), func(t *testing.T) {
			previous := ReadSIWCSystemState
			t.Cleanup(func() { ReadSIWCSystemState = previous })
			currentState := state
			checks := 0
			ReadSIWCSystemState = func(context.Context) ([]byte, error) {
				checks++
				return []byte(currentState), nil
			}
			calls := 0
			auth := &CodexAuth{httpClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"access_token":"new-access","refresh_token":"new-refresh","expires_in":3600,"scope":"chatgpt.tokens.use.direct"}`))}, nil
			})}}
			auth.SetRefreshJournal(&memoryRefreshJournal{})
			token, errRefresh := auth.RefreshSIWCTokensWithRetry(context.Background(), "sleep-refresh", "oaiapp_sleep", "", 3)
			if token != nil || errRefresh == nil || calls != 0 || checks != 1 {
				t.Fatalf("maintenance wake: token returned = %t, error = %v, requests = %d, checks = %d; want deferral without token requests or retries", token != nil, errRefresh, calls, checks)
			}
			if errors.Is(errRefresh, context.Canceled) {
				t.Fatalf("maintenance wake: refresh returned context.Canceled; want a dedicated deferral error: %v", errRefresh)
			}
			if strings.TrimSpace(state) == "Graphics Network" && !strings.Contains(errRefresh.Error(), "unexpected pmset output") {
				t.Fatalf("maintenance wake: error = %v; want parse diagnostics for unexpected pmset output", errRefresh)
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
	previous := ReadSIWCSystemState
	t.Cleanup(func() { ReadSIWCSystemState = previous })
	errProbe := errors.New("power-state probe unavailable")
	ReadSIWCSystemState = func(context.Context) ([]byte, error) { return nil, errProbe }
	auth := &CodexAuth{httpClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("token request sent without a known power state")
		return nil, nil
	})}}
	_, errRefresh := auth.RefreshSIWCTokens(context.Background(), "refresh", "oaiapp_probe", "")
	if !errors.Is(errRefresh, errProbe) {
		t.Fatalf("power-state error was lost: %v", errRefresh)
	}
}

func TestOrdinaryCodexRefreshDefersDuringMaintenanceWakeThenRenews(t *testing.T) {
	for _, tc := range []struct {
		name     string
		state    string
		probeErr error
	}{
		{name: "darkwake_missing_graphics", state: "Current System Capabilities are: CPU Network\nCurrent Power State: 2\n"},
		{name: "missing_network", state: "Current System Capabilities are: CPU Graphics Audio\n"},
		{name: "unexpected_pmset_output", state: "Graphics Network"},
		{name: "power_probe_unavailable", probeErr: errors.New("pmset unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetCodexRefreshGroupForTest()
			t.Cleanup(resetCodexRefreshGroupForTest)

			previous := ReadSIWCSystemState
			t.Cleanup(func() { ReadSIWCSystemState = previous })
			currentState := tc.state
			currentErr := tc.probeErr
			checks := 0
			ReadSIWCSystemState = func(context.Context) ([]byte, error) {
				checks++
				if currentErr != nil {
					return nil, currentErr
				}
				return []byte(currentState), nil
			}

			calls := 0
			auth := NewCodexAuth(nil)
			auth.SetRefreshJournal(&FileRefreshJournal{Dir: t.TempDir()})
			auth.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(`{"access_token":"ordinary-new-access","refresh_token":"ordinary-new-refresh","expires_in":3600}`)),
					Request:    req,
				}, nil
			})}

			token, errRefresh := auth.RefreshTokensWithRetry(context.Background(), "ordinary-sleep-refresh", 3)
			if token != nil || errRefresh == nil || calls != 0 || checks != 1 {
				t.Fatalf("maintenance wake: token=%v, err=%v, calls=%d, checks=%d; want deferral without HTTP calls or retries", token, errRefresh, calls, checks)
			}
			if !errors.Is(errRefresh, ErrSIWCRefreshDeferred) {
				t.Fatalf("maintenance wake error %v did not wrap ErrSIWCRefreshDeferred", errRefresh)
			}
			if tc.probeErr != nil && !errors.Is(errRefresh, tc.probeErr) {
				t.Fatalf("power probe error lost: %v", errRefresh)
			}

			currentErr = nil
			currentState = "Current System Capabilities are: CPU Graphics Audio Network\nCurrent Power State: 4\n"
			token, errRefresh = auth.RefreshTokensWithRetry(context.Background(), "ordinary-sleep-refresh", 3)
			if errRefresh != nil || token == nil || token.AccessToken != "ordinary-new-access" || token.RefreshToken != "ordinary-new-refresh" || calls != 1 {
				t.Fatalf("interactive wake renewal failed: token=%+v, err=%v, calls=%d", token, errRefresh, calls)
			}
		})
	}
}
