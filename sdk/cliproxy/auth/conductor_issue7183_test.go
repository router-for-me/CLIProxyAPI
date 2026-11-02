package auth

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestDisabledCodexRejectedRefreshTokenDoesNotSpin(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	// Access token still valid for 66h, i.e. inside the 96h CodexOuterRefreshLead window.
	exp := now.Add(66 * time.Hour)
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, exp.Unix())))
	accessToken := "eyJhbGciOiJub25lIn0." + payload + ".sig"

	a := &Auth{
		ID: "codex-disabled.json", FileName: "codex-disabled.json", Provider: "codex",
		Status: StatusActive, Disabled: true,
		Metadata: map[string]any{"type": "codex", "access_token": accessToken, "refresh_token": "rt", "disabled": true},
	}
	ex := &unauthorizedRefreshExecutor{id: "codex", refreshErr: errors.New(
		`token refresh failed with status 401: {"error":{"message":"Could not validate your refresh token. Please try signing in again.","type":"invalid_request_error","code":"invalid_refresh_token"}}`)}
	m := NewManager(nil, &FillFirstSelector{}, nil)
	m.RegisterExecutor(ex)
	if _, err := m.Register(ctx, a); err != nil {
		t.Fatal(err)
	}

	// First (real) refresh attempt fails terminally; disabled branch sets NextRefreshAfter = now+5m.
	m.refreshAuth(ctx, a.ID)
	// Simulate the 5m refreshFailureBackoff having elapsed.
	m.mu.Lock()
	m.auths[a.ID].NextRefreshAfter = now.Add(-time.Second)
	gen0 := m.auths[a.ID].Generation
	m.mu.Unlock()

	m.StartAutoRefresh(ctx, refreshCheckInterval)
	time.Sleep(500 * time.Millisecond)
	m.StopAutoRefresh()

	got, _ := m.GetByID(a.ID)
	bumps := got.Generation - gen0
	t.Logf("upstream Refresh calls=%d, Generation bumps in 500ms=%d", ex.RefreshCalls(), bumps)
	if bumps > 10 {
		t.Fatalf("auto-refresh loop is spinning: %d Generation bumps in 500ms with %d upstream refresh calls", bumps, ex.RefreshCalls())
	}
}
