package claude

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
	ReadClaudeSystemState = func(context.Context) ([]byte, error) {
		return []byte("Current System Capabilities are: CPU Graphics Audio Network\nCurrent Power State: 4\n"), nil
	}
	os.Exit(m.Run())
}

func TestClaudeRefreshDefersDuringMaintenanceWakeThenRenews(t *testing.T) {
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
			resetClaudeRefreshState()
			t.Cleanup(resetClaudeRefreshState)

			previous := ReadClaudeSystemState
			t.Cleanup(func() { ReadClaudeSystemState = previous })
			currentState := tc.state
			currentErr := tc.probeErr
			checks := 0
			ReadClaudeSystemState = func(context.Context) ([]byte, error) {
				checks++
				if currentErr != nil {
					return nil, currentErr
				}
				return []byte(currentState), nil
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

			token, errRefresh := auth.RefreshTokensWithRetry(context.Background(), "claude-sleep-refresh", 3)
			if token != nil || errRefresh == nil || calls != 0 || checks != 1 {
				t.Fatalf("maintenance wake: token=%v, err=%v, calls=%d, checks=%d; want deferral without HTTP calls or retries", token, errRefresh, calls, checks)
			}
			if errors.Is(errRefresh, context.Canceled) {
				t.Fatalf("maintenance wake returned context.Canceled; want dedicated deferral error: %v", errRefresh)
			}
			if !errors.Is(errRefresh, ErrClaudeRefreshDeferred) {
				t.Fatalf("maintenance wake error %v did not wrap ErrClaudeRefreshDeferred", errRefresh)
			}
			if strings.TrimSpace(tc.state) == "Graphics Network" && !strings.Contains(errRefresh.Error(), "unexpected pmset output") {
				t.Fatalf("maintenance wake: error = %v; want parse diagnostics for unexpected pmset output", errRefresh)
			}
			if tc.probeErr != nil && !errors.Is(errRefresh, tc.probeErr) {
				t.Fatalf("power probe error lost: %v", errRefresh)
			}

			currentErr = nil
			currentState = "Current System Capabilities are: CPU Graphics Audio Network\nCurrent Power State: 4\n"
			token, errRefresh = auth.RefreshTokensWithRetry(context.Background(), "claude-sleep-refresh", 3)
			if errRefresh != nil || token == nil || token.AccessToken != "claude-new-access" || token.RefreshToken != "claude-new-refresh" || calls != 1 {
				t.Fatalf("interactive wake renewal failed: token=%+v, err=%v, calls=%d", token, errRefresh, calls)
			}
		})
	}
}
