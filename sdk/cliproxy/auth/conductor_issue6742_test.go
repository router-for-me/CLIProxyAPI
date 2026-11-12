package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func makeIssue6742JWT(exp time.Time) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payloadBytes, _ := json.Marshal(map[string]any{
		"exp": exp.Unix(),
	})
	payload := base64.RawURLEncoding.EncodeToString(payloadBytes)
	return header + "." + payload + ".sig"
}

func TestIssue6742_CodexProactiveRefreshBeforeSevenDayInvalidation(t *testing.T) {
	issuedAt := time.Date(2026, 9, 26, 14, 39, 9, 0, time.FixedZone("UTC+8", 8*3600))
	jwtExpiry := issuedAt.Add(10 * 24 * time.Hour) // Oct 6 14:39:09
	jwtToken := makeIssue6742JWT(jwtExpiry)

	auth := &Auth{
		ID:              "codex-issue6742-a.json",
		Provider:        "codex",
		Status:          StatusActive,
		LastRefreshedAt: issuedAt,
		Metadata: map[string]any{
			"type":          "codex",
			"access_token":  jwtToken,
			"refresh_token": "codex-refresh-a",
			"last_refresh":  issuedAt.Format(time.RFC3339),
			"expired":       jwtExpiry.Format(time.RFC3339),
		},
	}

	mgr := NewManager(nil, nil, nil)

	// At 6 days + 1 hour after issuance (still ~3.9 days before 10-day JWT expiry,
	// and before the ~7-day upstream invalidation on Oct 3 16:33:55), proactive refresh must be due.
	checkAt := issuedAt.Add(6*24*time.Hour + time.Hour)
	dueAt, hasOuter, needOuter := CodexOuterNeedsRefresh(auth, checkAt)
	if !hasOuter {
		t.Fatal("CodexOuterNeedsRefresh hasRefresh = false, want true")
	}
	if !needOuter {
		t.Fatalf("CodexOuterNeedsRefresh needsRefresh = false at %v (dueAt=%v), want true before 7-day upstream invalidation", checkAt, dueAt)
	}
	if !mgr.shouldRefresh(auth, checkAt) {
		t.Fatalf("mgr.shouldRefresh = false at %v, want true before 7-day upstream invalidation", checkAt)
	}
	nextAt, okSchedule := nextRefreshCheckAt(issuedAt, auth, 5*time.Second)
	if !okSchedule {
		t.Fatal("nextRefreshCheckAt ok = false, want true")
	}
	sevenDayInvalidation := time.Date(2026, 10, 3, 16, 33, 55, 0, time.FixedZone("UTC+8", 8*3600))
	if !nextAt.Before(sevenDayInvalidation) {
		t.Fatalf("nextRefreshCheckAt = %v, want before 7-day invalidation at %v", nextAt, sevenDayInvalidation)
	}
}

func TestIssue6742_RejectedUnexpiredJWTRecoversViaBackgroundRefreshAfterTransientFailure(t *testing.T) {
	now := time.Now()
	// Use 8 days remaining (> 96h proactive refresh lead) so shouldRefresh is false
	// before rejection and can only become true via RejectedAccessToken tracking.
	staleJWTToken := makeIssue6742JWT(now.Add(8 * 24 * time.Hour))
	freshJWTToken := makeIssue6742JWT(now.Add(10 * 24 * time.Hour))

	model := "gpt-6-astra"
	primary := &Auth{
		ID:       "aa-codex-jwt-primary",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{
			"type":          "codex",
			"access_token":  staleJWTToken,
			"refresh_token": "primary-refresh-token",
			"expired":       now.Add(8 * 24 * time.Hour).Format(time.RFC3339),
		},
	}

	executor := &unauthorizedRefreshExecutor{
		id: "codex",
		tokenInvalid: map[string]struct{}{
			staleJWTToken: {},
		},
		refreshErr: errors.New("token refresh request failed: upstream 503 Service Unavailable"),
		refreshTokens: map[string]string{
			primary.ID: freshJWTToken,
		},
	}

	m := NewManager(nil, &FillFirstSelector{}, nil)
	m.RegisterExecutor(executor)

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(primary.ID, "codex", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(primary.ID)
	})

	if _, errRegister := m.Register(context.Background(), primary); errRegister != nil {
		t.Fatalf("register primary: %v", errRegister)
	}
	if m.shouldRefresh(primary, now) {
		t.Fatal("precondition failed: shouldRefresh = true before 401 with 8 days remaining, want false")
	}

	// 1. Execute hits 401 with invalidated JWT token; on-demand refresh fails transiently.
	_, errExec := m.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExec == nil {
		t.Fatal("expected initial Execute to fail when on-demand refresh fails transiently")
	}

	afterFail, ok := m.GetByID(primary.ID)
	if !ok || afterFail == nil {
		t.Fatal("primary auth missing after failure")
	}
	if afterFail.RejectedAccessToken != staleJWTToken {
		t.Fatalf("RejectedAccessToken = %q, want %q for rejected unexpired JWT", afterFail.RejectedAccessToken, staleJWTToken)
	}
	if afterFail.HasValidAccessToken(now) {
		t.Fatal("HasValidAccessToken = true, want false for rejected unexpired JWT")
	}
	if afterFail.NextRefreshAfter.IsZero() {
		t.Fatal("NextRefreshAfter is zero, want scheduled retry backoff")
	}

	// 2. When the retry backoff elapses, shouldRefresh must return true even though
	// the rejected JWT's nominal exp claim is still 3 days in the future.
	retryTime := afterFail.NextRefreshAfter.Add(time.Second)
	if !m.shouldRefresh(afterFail, retryTime) {
		t.Fatal("shouldRefresh = false at retryTime, want true for rejected unexpired JWT")
	}

	// 3. Transient network issue resolves; background auto-refresh loop processes the due auth.
	executor.mu.Lock()
	executor.refreshErr = nil
	executor.mu.Unlock()

	loop := newAuthAutoRefreshLoop(m, 5*time.Second, 1)
	loop.handleDueAuth(context.Background(), retryTime, primary.ID)
	select {
	case job := <-loop.jobs:
		if !m.beginRefreshJob(context.Background(), job) {
			t.Fatal("beginRefreshJob returned false")
		}
		if _, errRefresh := m.refreshAuthForRequestAtEpoch(context.Background(), job.id, "", job.registrationEpoch); errRefresh != nil {
			t.Fatalf("background refreshAuthForRequestAtEpoch error = %v", errRefresh)
		}
		m.finishRefreshJob(job, time.Time{}, false)
	default:
		t.Fatal("expected handleDueAuth to enqueue a refresh job for rejected unexpired JWT")
	}

	recovered, ok := m.GetByID(primary.ID)
	if !ok || recovered == nil {
		t.Fatal("primary auth missing after background refresh")
	}
	if got := authAccessToken(recovered); got != freshJWTToken {
		t.Fatalf("recovered access_token = %q, want %q", got, freshJWTToken)
	}
	if recovered.Unavailable || recovered.Status != StatusActive {
		t.Fatalf("recovered state: Unavailable=%v Status=%s, want false/StatusActive", recovered.Unavailable, recovered.Status)
	}
	if state := recovered.ModelStates[model]; state != nil && state.Unavailable {
		t.Fatalf("model state remained Unavailable after background refresh: %+v", state)
	}

	// 4. Subsequent Execute succeeds automatically without manual ForceRefreshAll.
	resp, errExec2 := m.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExec2 != nil {
		t.Fatalf("subsequent Execute error = %v, want success", errExec2)
	}
	if got := string(resp.Payload); got != fmt.Sprintf("%s:%s", primary.ID, freshJWTToken) {
		t.Fatalf("subsequent Execute payload = %q, want %q", got, fmt.Sprintf("%s:%s", primary.ID, freshJWTToken))
	}
}

type inStreamUnauthorizedExecutor struct {
	unauthorizedRefreshExecutor
}

func (e *inStreamUnauthorizedExecutor) ExecuteStream(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.mu.Lock()
	e.streamCalls = append(e.streamCalls, auth.ID)
	token := authAccessToken(auth)
	_, invalid := e.tokenInvalid[token]
	e.mu.Unlock()

	ch := make(chan cliproxyexecutor.StreamChunk, 2)
	// Emit an initial handshake chunk so readStreamBootstrap commits the stream,
	// and then deliver the 401 error in-stream via wrapStreamResult -> MarkResult.
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte(`data: {"type":"response.created"}` + "\n\n")}
	if invalid {
		ch <- cliproxyexecutor.StreamChunk{Err: &Error{
			HTTPStatus: http.StatusUnauthorized,
			Code:       "auth_unavailable",
			Message:    `{"error":{"message":"Encountered invalidated oauth token for user, failing request","type":"authentication_error","code":"auth_unavailable"}}`,
		}}
	} else {
		ch <- cliproxyexecutor.StreamChunk{Payload: []byte(auth.ID + ":" + token)}
	}
	close(ch)
	return &cliproxyexecutor.StreamResult{Headers: http.Header{"X-Auth": {auth.ID}}, Chunks: ch}, nil
}

func TestIssue6742_InStreamUnauthorizedMarksRejectedTokenAndSchedulesRefresh(t *testing.T) {
	now := time.Now()
	// Use 8 days remaining (> 96h proactive refresh lead) so shouldRefresh is false
	// before the in-stream 401 and only becomes true because MarkResult marks RejectedAccessToken.
	staleJWTToken := makeIssue6742JWT(now.Add(8 * 24 * time.Hour))
	freshJWTToken := makeIssue6742JWT(now.Add(10 * 24 * time.Hour))
	model := "gpt-5.6-luna"

	primary := &Auth{
		ID:       "codex-instream-401",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{
			"type":          "codex",
			"access_token":  staleJWTToken,
			"refresh_token": "primary-refresh-token",
			"expired":       now.Add(8 * 24 * time.Hour).Format(time.RFC3339),
		},
	}

	executor := &inStreamUnauthorizedExecutor{
		unauthorizedRefreshExecutor: unauthorizedRefreshExecutor{
			id: "codex",
			tokenInvalid: map[string]struct{}{
				staleJWTToken: {},
			},
			refreshTokens: map[string]string{
				primary.ID: freshJWTToken,
			},
		},
	}

	m := NewManager(nil, &FillFirstSelector{}, nil)
	m.RegisterExecutor(executor)

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(primary.ID, "codex", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(primary.ID)
	})

	if _, errRegister := m.Register(context.Background(), primary); errRegister != nil {
		t.Fatalf("register primary: %v", errRegister)
	}
	if m.shouldRefresh(primary, now) {
		t.Fatal("precondition failed: shouldRefresh = true before in-stream 401 with 8 days remaining, want false")
	}

	stream, errStream := m.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errStream != nil {
		t.Fatalf("ExecuteStream initial error = %v", errStream)
	}
	for range stream.Chunks {
	}

	afterStream, ok := m.GetByID(primary.ID)
	if !ok || afterStream == nil {
		t.Fatal("primary auth missing after in-stream 401")
	}
	if afterStream.RejectedAccessToken != staleJWTToken {
		t.Fatalf("RejectedAccessToken = %q, want %q after in-stream 401", afterStream.RejectedAccessToken, staleJWTToken)
	}
	if !m.shouldRefresh(afterStream, time.Now()) {
		t.Fatal("shouldRefresh = false after in-stream 401, want true")
	}

	// Background refresh loop must pick it up and recover the credential.
	loop := newAuthAutoRefreshLoop(m, 5*time.Second, 1)
	loop.handleDueAuth(context.Background(), time.Now(), primary.ID)
	select {
	case job := <-loop.jobs:
		if !m.beginRefreshJob(context.Background(), job) {
			t.Fatal("beginRefreshJob returned false")
		}
		if _, errRefresh := m.refreshAuthForRequestAtEpoch(context.Background(), job.id, "", job.registrationEpoch); errRefresh != nil {
			t.Fatalf("background refresh error = %v", errRefresh)
		}
		m.finishRefreshJob(job, time.Time{}, false)
	default:
		t.Fatal("expected handleDueAuth to enqueue refresh job after in-stream 401")
	}

	recovered, ok := m.GetByID(primary.ID)
	if !ok || recovered == nil {
		t.Fatal("primary auth missing after recovery")
	}
	if got := authAccessToken(recovered); got != freshJWTToken {
		t.Fatalf("recovered access_token = %q, want %q", got, freshJWTToken)
	}
	if recovered.Unavailable || recovered.Status != StatusActive {
		t.Fatalf("recovered Unavailable=%v Status=%s, want false/StatusActive", recovered.Unavailable, recovered.Status)
	}
	if state := recovered.ModelStates[model]; state != nil && state.Unavailable {
		t.Fatalf("model state still Unavailable after recovery: %+v", state)
	}
}

func TestIssue6742_InStreamUnauthorizedBlocksSiblingModelShardUntilRefreshed(t *testing.T) {
	now := time.Now()
	staleJWTToken := makeIssue6742JWT(now.Add(3 * 24 * time.Hour))
	freshJWTToken := makeIssue6742JWT(now.Add(10 * 24 * time.Hour))
	backupJWTToken := makeIssue6742JWT(now.Add(10 * 24 * time.Hour))
	modelA := "gpt-5.6-luna"
	modelB := "gpt-6-astra"

	primary := &Auth{
		ID:       "aa-codex-multimodel-primary",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{
			"type":          "codex",
			"access_token":  staleJWTToken,
			"refresh_token": "primary-refresh-token",
			"expired":       now.Add(3 * 24 * time.Hour).Format(time.RFC3339),
		},
	}
	backup := &Auth{
		ID:       "bb-codex-multimodel-backup",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{
			"type":          "codex",
			"access_token":  backupJWTToken,
			"refresh_token": "backup-refresh-token",
			"expired":       now.Add(10 * 24 * time.Hour).Format(time.RFC3339),
		},
	}

	executor := &inStreamUnauthorizedExecutor{
		unauthorizedRefreshExecutor: unauthorizedRefreshExecutor{
			id:           "codex",
			tokenInvalid: map[string]struct{}{},
			refreshTokens: map[string]string{
				primary.ID: freshJWTToken,
			},
		},
	}

	m := NewManager(nil, &FillFirstSelector{}, nil)
	m.RegisterExecutor(executor)

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(primary.ID, "codex", []*registry.ModelInfo{{ID: modelA}, {ID: modelB}})
	reg.RegisterClient(backup.ID, "codex", []*registry.ModelInfo{{ID: modelA}, {ID: modelB}})
	t.Cleanup(func() {
		reg.UnregisterClient(primary.ID)
		reg.UnregisterClient(backup.ID)
	})

	if _, errRegister := m.Register(context.Background(), primary); errRegister != nil {
		t.Fatalf("register primary: %v", errRegister)
	}
	if _, errRegister := m.Register(context.Background(), backup); errRegister != nil {
		t.Fatalf("register backup: %v", errRegister)
	}

	// Pre-warm the scheduler shard for sibling modelB while primary is healthy.
	respWarm, errWarm := m.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: modelB}, cliproxyexecutor.Options{})
	if errWarm != nil {
		t.Fatalf("warm-up Execute(modelB) error = %v", errWarm)
	}
	if got := string(respWarm.Payload); got != primary.ID+":"+staleJWTToken {
		t.Fatalf("warm-up Execute(modelB) payload = %q, want primary", got)
	}

	// Now upstream invalidates staleJWTToken, and modelA hits an in-stream 401.
	executor.mu.Lock()
	executor.tokenInvalid[staleJWTToken] = struct{}{}
	executor.mu.Unlock()

	stream, errStream := m.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: modelA}, cliproxyexecutor.Options{})
	if errStream != nil {
		t.Fatalf("ExecuteStream(modelA) initial error = %v", errStream)
	}
	for range stream.Chunks {
	}

	// Before background refresh runs, a request for sibling modelB must NOT select primary
	// from the pre-warmed modelB scheduler shard with the rejected token; it must route to backup.
	execCallsBefore := len(executor.ExecuteCalls())
	respB, errB := m.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: modelB}, cliproxyexecutor.Options{})
	if errB != nil {
		t.Fatalf("Execute(modelB) before refresh error = %v, want success via backup", errB)
	}
	if got := string(respB.Payload); got != backup.ID+":"+backupJWTToken {
		t.Fatalf("Execute(modelB) before refresh payload = %q, want backup %q", got, backup.ID+":"+backupJWTToken)
	}
	for _, id := range executor.ExecuteCalls()[execCallsBefore:] {
		if id == primary.ID {
			t.Fatalf("sibling modelB selected primary with rejected token before refresh completed")
		}
	}
}
