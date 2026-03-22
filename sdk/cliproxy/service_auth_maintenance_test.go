package cliproxy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v6/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

type trackingTokenStore struct {
	mu      sync.Mutex
	deleted []string
}

type failingTokenStore struct {
	err error
}

func (s *trackingTokenStore) List(context.Context) ([]*coreauth.Auth, error) {
	return nil, nil
}

func (s *trackingTokenStore) Save(_ context.Context, auth *coreauth.Auth) (string, error) {
	if auth == nil {
		return "", nil
	}
	return auth.ID, nil
}

func (s *trackingTokenStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleted = append(s.deleted, id)
	return nil
}

func (s *trackingTokenStore) SetBaseDir(string) {}

func (s *failingTokenStore) List(context.Context) ([]*coreauth.Auth, error) {
	return nil, nil
}

func (s *failingTokenStore) Save(_ context.Context, auth *coreauth.Auth) (string, error) {
	if auth == nil {
		return "", nil
	}
	return auth.ID, nil
}

func (s *failingTokenStore) Delete(_ context.Context, id string) error {
	if s == nil || s.err == nil {
		return nil
	}
	return s.err
}

func (s *failingTokenStore) SetBaseDir(string) {}

type timedTrackingTokenStore struct {
	mu      sync.Mutex
	deleted []timedDeleteRecord
}

type timedDeleteRecord struct {
	path string
	at   time.Time
}

func (s *timedTrackingTokenStore) List(context.Context) ([]*coreauth.Auth, error) {
	return nil, nil
}

func (s *timedTrackingTokenStore) Save(_ context.Context, auth *coreauth.Auth) (string, error) {
	if auth == nil {
		return "", nil
	}
	return auth.ID, nil
}

func (s *timedTrackingTokenStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleted = append(s.deleted, timedDeleteRecord{path: id, at: time.Now()})
	return nil
}

func (s *timedTrackingTokenStore) SetBaseDir(string) {}

func (s *timedTrackingTokenStore) snapshot() []timedDeleteRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]timedDeleteRecord, len(s.deleted))
	copy(out, s.deleted)
	return out
}

type authMaintenanceStressExecutor struct {
	delay      time.Duration
	failStatus map[string]int

	mu          sync.Mutex
	callsByAuth map[string]int
	successes   atomic.Int64
	failures    atomic.Int64
}

func (e *authMaintenanceStressExecutor) Identifier() string { return "stress" }

func (e *authMaintenanceStressExecutor) Execute(_ context.Context, auth *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	if e.delay > 0 {
		time.Sleep(e.delay)
	}
	authID := ""
	if auth != nil {
		authID = auth.ID
	}

	e.mu.Lock()
	if e.callsByAuth == nil {
		e.callsByAuth = make(map[string]int)
	}
	e.callsByAuth[authID]++
	statusCode := e.failStatus[authID]
	e.mu.Unlock()

	if statusCode > 0 {
		e.failures.Add(1)
		return coreexecutor.Response{}, &coreauth.Error{HTTPStatus: statusCode, Message: http.StatusText(statusCode)}
	}
	e.successes.Add(1)
	return coreexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
}

func (e *authMaintenanceStressExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, fmt.Errorf("not implemented")
}

func (e *authMaintenanceStressExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *authMaintenanceStressExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, fmt.Errorf("not implemented")
}

func (e *authMaintenanceStressExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("not implemented")
}

func (e *authMaintenanceStressExecutor) snapshotCalls() map[string]int {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make(map[string]int, len(e.callsByAuth))
	for id, count := range e.callsByAuth {
		out[id] = count
	}
	return out
}

func TestScanAuthMaintenanceCandidates_GroupsByPath(t *testing.T) {
	authDir := t.TempDir()
	service := &Service{
		cfg: &config.Config{
			AuthDir: authDir,
			AuthMaintenance: config.AuthMaintenanceConfig{
				Enable:               true,
				DeleteStatusCodes:    []int{401},
				DeleteQuotaExceeded:  true,
				QuotaStrikeThreshold: 3,
			},
		},
		coreManager: coreauth.NewManager(nil, nil, nil),
	}

	sharedPath := filepath.Join(authDir, "shared.json")
	quotaPath := filepath.Join(authDir, "quota.json")
	lowQuotaPath := filepath.Join(authDir, "quota-low.json")

	for _, auth := range []*coreauth.Auth{
		{
			ID:          "shared-primary",
			FileName:    filepath.Base(sharedPath),
			Provider:    "codex",
			Status:      coreauth.StatusError,
			LastError:   &coreauth.Error{HTTPStatus: 401, Message: "unauthorized"},
			Attributes:  map[string]string{"path": sharedPath},
			UpdatedAt:   timeNowForTest(),
			Unavailable: true,
		},
		{
			ID:         "shared-project-1",
			FileName:   filepath.Base(sharedPath),
			Provider:   "codex",
			Status:     coreauth.StatusActive,
			Attributes: map[string]string{"path": sharedPath},
			UpdatedAt:  timeNowForTest(),
		},
		{
			ID:         "quota-high",
			FileName:   filepath.Base(quotaPath),
			Provider:   "codex",
			Status:     coreauth.StatusError,
			Attributes: map[string]string{"path": quotaPath},
			Quota: coreauth.QuotaState{
				Exceeded:    true,
				Reason:      "quota",
				StrikeCount: 4,
			},
			UpdatedAt:   timeNowForTest(),
			Unavailable: true,
		},
		{
			ID:         "quota-low",
			FileName:   filepath.Base(lowQuotaPath),
			Provider:   "codex",
			Status:     coreauth.StatusError,
			Attributes: map[string]string{"path": lowQuotaPath},
			Quota: coreauth.QuotaState{
				Exceeded:    true,
				Reason:      "quota",
				StrikeCount: 2,
			},
			UpdatedAt:   timeNowForTest(),
			Unavailable: true,
		},
	} {
		if _, err := service.coreManager.Register(context.Background(), auth); err != nil {
			t.Fatalf("failed to register auth %s: %v", auth.ID, err)
		}
	}

	candidates := service.scanAuthMaintenanceCandidates(timeNowForTest(), service.cfg.AuthMaintenance, authDir)
	if len(candidates) != 2 {
		t.Fatalf("expected 2 maintenance candidates, got %d", len(candidates))
	}

	foundShared := false
	foundQuota := false
	for _, candidate := range candidates {
		switch candidate.Path {
		case sharedPath:
			foundShared = true
			if len(candidate.IDs) != 2 {
				t.Fatalf("expected shared candidate to contain 2 ids, got %d", len(candidate.IDs))
			}
		case quotaPath:
			foundQuota = true
			if candidate.Reason != "quota_strikes_4" {
				t.Fatalf("expected quota reason to reflect strike count, got %q", candidate.Reason)
			}
		case lowQuotaPath:
			t.Fatalf("did not expect low quota path to be queued")
		}
	}
	if !foundShared || !foundQuota {
		t.Fatalf("expected shared and quota candidates, got %#v", candidates)
	}
}

func TestAuthMaintenanceDeleteSpacing_AcceleratesLargeBacklog(t *testing.T) {
	cfg := config.AuthMaintenanceConfig{DeleteIntervalSeconds: 1}

	if got := authMaintenanceDeleteSpacing(cfg, 1); got != time.Second {
		t.Fatalf("spacing for small backlog = %s, want %s", got, time.Second)
	}
	if got := authMaintenanceDeleteSpacing(cfg, 16); got != 500*time.Millisecond {
		t.Fatalf("spacing for medium backlog = %s, want %s", got, 500*time.Millisecond)
	}
	if got := authMaintenanceDeleteSpacing(cfg, 64); got != 250*time.Millisecond {
		t.Fatalf("spacing for large backlog = %s, want %s", got, 250*time.Millisecond)
	}
}

func TestEnqueueAuthMaintenanceCandidate_RejectsInFlightDuplicate(t *testing.T) {
	service := &Service{}
	candidate := authMaintenanceCandidate{
		Key:    "/tmp/test.json",
		Path:   "/tmp/test.json",
		IDs:    []string{"auth-1"},
		Reason: "http_401",
	}

	if !service.enqueueAuthMaintenanceCandidate(candidate) {
		t.Fatal("expected initial enqueue to succeed")
	}
	popped, _, ok := service.popAuthMaintenanceCandidate()
	if !ok {
		t.Fatal("expected candidate to move into in-flight state")
	}
	if service.enqueueAuthMaintenanceCandidate(candidate) {
		t.Fatal("expected duplicate enqueue to be rejected while in flight")
	}
	service.finishAuthMaintenanceCandidate(popped)
	if !service.enqueueAuthMaintenanceCandidate(candidate) {
		t.Fatal("expected enqueue to succeed again after in-flight candidate finished")
	}
}

func TestScanAuthMaintenanceCandidates_429StatusCodeQueuesImmediateDelete(t *testing.T) {
	authDir := t.TempDir()
	service := &Service{
		cfg: &config.Config{
			AuthDir: authDir,
			AuthMaintenance: config.AuthMaintenanceConfig{
				Enable:               true,
				DeleteStatusCodes:    []int{429},
				DeleteQuotaExceeded:  true,
				QuotaStrikeThreshold: 9,
			},
		},
		coreManager: coreauth.NewManager(nil, nil, nil),
	}

	quotaPath := filepath.Join(authDir, "quota-immediate.json")
	auth := &coreauth.Auth{
		ID:         "quota-immediate",
		FileName:   filepath.Base(quotaPath),
		Provider:   "codex",
		Status:     coreauth.StatusError,
		LastError:  &coreauth.Error{HTTPStatus: 429, Message: "quota"},
		Attributes: map[string]string{"path": quotaPath},
		Quota: coreauth.QuotaState{
			Exceeded:    true,
			Reason:      "quota",
			StrikeCount: 1,
		},
		UpdatedAt:   timeNowForTest(),
		Unavailable: true,
	}
	if _, err := service.coreManager.Register(context.Background(), auth); err != nil {
		t.Fatalf("failed to register auth: %v", err)
	}

	candidates := service.scanAuthMaintenanceCandidates(timeNowForTest(), service.cfg.AuthMaintenance, authDir)
	if len(candidates) != 1 {
		t.Fatalf("expected 1 maintenance candidate, got %d", len(candidates))
	}
	if got := candidates[0].Reason; got != "http_429" {
		t.Fatalf("expected immediate 429 deletion reason, got %q", got)
	}
}

func TestScanAuthMaintenanceCandidates_StatusMessageJSON401QueuesDelete(t *testing.T) {
	authDir := t.TempDir()
	service := &Service{
		cfg: &config.Config{
			AuthDir: authDir,
			AuthMaintenance: config.AuthMaintenanceConfig{
				Enable:            true,
				DeleteStatusCodes: []int{401},
			},
		},
		coreManager: coreauth.NewManager(nil, nil, nil),
	}

	filePath := filepath.Join(authDir, "status-json-401.json")
	auth := &coreauth.Auth{
		ID:            "status-json-401",
		FileName:      filepath.Base(filePath),
		Provider:      "codex",
		Status:        coreauth.StatusError,
		StatusMessage: "{\n  \"error\": {\n    \"message\": \"Your authentication token has been invalidated. Please try signing in again.\",\n    \"type\": \"invalid_request_error\",\n    \"code\": \"token_invalidated\",\n    \"param\": null\n  },\n  \"status\": 401\n}",
		Attributes:    map[string]string{"path": filePath},
		UpdatedAt:     timeNowForTest(),
		Unavailable:   true,
	}
	if _, err := service.coreManager.Register(context.Background(), auth); err != nil {
		t.Fatalf("failed to register auth: %v", err)
	}

	candidates := service.scanAuthMaintenanceCandidates(timeNowForTest(), service.cfg.AuthMaintenance, authDir)
	if len(candidates) != 1 {
		t.Fatalf("expected 1 maintenance candidate, got %d", len(candidates))
	}
	if got := candidates[0].Reason; got != "http_401" {
		t.Fatalf("expected JSON 401 status_message to queue delete, got %q", got)
	}
}

func TestScanAuthMaintenanceCandidates_StatusMessageUsageLimitJSONQueuesDelete(t *testing.T) {
	authDir := t.TempDir()
	service := &Service{
		cfg: &config.Config{
			AuthDir: authDir,
			AuthMaintenance: config.AuthMaintenanceConfig{
				Enable:            true,
				DeleteStatusCodes: []int{429},
			},
		},
		coreManager: coreauth.NewManager(nil, nil, nil),
	}

	filePath := filepath.Join(authDir, "status-json-429.json")
	auth := &coreauth.Auth{
		ID:            "status-json-429",
		FileName:      filepath.Base(filePath),
		Provider:      "codex",
		Status:        coreauth.StatusError,
		StatusMessage: "{\"error\":{\"type\":\"usage_limit_reached\",\"message\":\"The usage limit has been reached\",\"plan_type\":\"free\",\"resets_at\":1774767151,\"resets_in_seconds\":596320}}",
		Attributes:    map[string]string{"path": filePath},
		UpdatedAt:     timeNowForTest(),
		Unavailable:   true,
	}
	if _, err := service.coreManager.Register(context.Background(), auth); err != nil {
		t.Fatalf("failed to register auth: %v", err)
	}

	candidates := service.scanAuthMaintenanceCandidates(timeNowForTest(), service.cfg.AuthMaintenance, authDir)
	if len(candidates) != 1 {
		t.Fatalf("expected 1 maintenance candidate, got %d", len(candidates))
	}
	if got := candidates[0].Reason; got != "http_429" {
		t.Fatalf("expected usage_limit_reached status_message to queue delete, got %q", got)
	}
}

func TestDeleteAuthMaintenanceCandidate_RemovesFileAndDisablesAllAuths(t *testing.T) {
	authDir := t.TempDir()
	filePath := filepath.Join(authDir, "shared.json")
	if err := os.WriteFile(filePath, []byte(`{"type":"codex"}`), 0o600); err != nil {
		t.Fatalf("failed to write auth file: %v", err)
	}

	store := &trackingTokenStore{}
	previousStore := sdkAuth.GetTokenStore()
	sdkAuth.RegisterTokenStore(store)
	t.Cleanup(func() { sdkAuth.RegisterTokenStore(previousStore) })

	service := &Service{
		cfg:         &config.Config{AuthDir: authDir},
		coreManager: coreauth.NewManager(nil, nil, nil),
	}
	for _, auth := range []*coreauth.Auth{
		{
			ID:         "shared-primary",
			FileName:   "shared.json",
			Provider:   "codex",
			Status:     coreauth.StatusError,
			Attributes: map[string]string{"path": filePath},
		},
		{
			ID:         "shared-project-1",
			FileName:   "shared.json",
			Provider:   "codex",
			Status:     coreauth.StatusActive,
			Attributes: map[string]string{"path": filePath},
		},
	} {
		if _, err := service.coreManager.Register(context.Background(), auth); err != nil {
			t.Fatalf("failed to register auth %s: %v", auth.ID, err)
		}
	}

	err := service.deleteAuthMaintenanceCandidate(context.Background(), authMaintenanceCandidate{
		Key:    filePath,
		Path:   filePath,
		IDs:    []string{"shared-primary", "shared-project-1"},
		Reason: "http_401",
	})
	if err != nil {
		t.Fatalf("deleteAuthMaintenanceCandidate() error = %v", err)
	}

	if _, errStat := os.Stat(filePath); !os.IsNotExist(errStat) {
		t.Fatalf("expected auth file to be removed, stat err: %v", errStat)
	}
	store.mu.Lock()
	deleted := append([]string(nil), store.deleted...)
	store.mu.Unlock()
	if len(deleted) != 1 || deleted[0] != filePath {
		t.Fatalf("expected token store delete for %s, got %#v", filePath, store.deleted)
	}
	for _, id := range []string{"shared-primary", "shared-project-1"} {
		auth, ok := service.coreManager.GetByID(id)
		if !ok {
			t.Fatalf("expected auth %s to remain in manager", id)
		}
		if !auth.Disabled || auth.Status != coreauth.StatusDisabled {
			t.Fatalf("expected auth %s to be disabled after maintenance delete, got disabled=%v status=%q", id, auth.Disabled, auth.Status)
		}
	}
}

func TestDeleteAuthMaintenanceCandidate_TokenStoreFailureStillDisablesAuths(t *testing.T) {
	authDir := t.TempDir()
	filePath := filepath.Join(authDir, "shared.json")
	if err := os.WriteFile(filePath, []byte(`{"type":"codex"}`), 0o600); err != nil {
		t.Fatalf("failed to write auth file: %v", err)
	}

	storeErr := errors.New("token store down")
	previousStore := sdkAuth.GetTokenStore()
	sdkAuth.RegisterTokenStore(&failingTokenStore{err: storeErr})
	t.Cleanup(func() { sdkAuth.RegisterTokenStore(previousStore) })

	service := &Service{
		cfg:         &config.Config{AuthDir: authDir},
		coreManager: coreauth.NewManager(nil, nil, nil),
	}
	service.ensureAuthUpdateQueue(context.Background())
	t.Cleanup(func() {
		if service.authQueueStop != nil {
			service.authQueueStop()
		}
	})
	for _, auth := range []*coreauth.Auth{
		{
			ID:         "shared-primary",
			FileName:   "shared.json",
			Provider:   "codex",
			Status:     coreauth.StatusError,
			Attributes: map[string]string{"path": filePath},
		},
		{
			ID:         "shared-project-1",
			FileName:   "shared.json",
			Provider:   "codex",
			Status:     coreauth.StatusActive,
			Attributes: map[string]string{"path": filePath},
		},
	} {
		if _, err := service.coreManager.Register(context.Background(), auth); err != nil {
			t.Fatalf("failed to register auth %s: %v", auth.ID, err)
		}
	}

	err := service.deleteAuthMaintenanceCandidate(context.Background(), authMaintenanceCandidate{
		Key:    filePath,
		Path:   filePath,
		IDs:    []string{"shared-primary", "shared-project-1"},
		Reason: "http_401",
	})
	if !errors.Is(err, storeErr) {
		t.Fatalf("deleteAuthMaintenanceCandidate() error = %v, want token store error", err)
	}

	deadline := time.Now().Add(1 * time.Second)
	for {
		allDisabled := true
		for _, id := range []string{"shared-primary", "shared-project-1"} {
			auth, ok := service.coreManager.GetByID(id)
			if !ok || auth == nil || !auth.Disabled || auth.Status != coreauth.StatusDisabled {
				allDisabled = false
				break
			}
		}
		if allDisabled {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for auths to be disabled after token store failure")
		}
		time.Sleep(10 * time.Millisecond)
	}

	if _, errStat := os.Stat(filePath); !os.IsNotExist(errStat) {
		t.Fatalf("expected auth file to be removed, stat err: %v", errStat)
	}
}

func TestScanAuthMaintenanceCandidates_DisabledPendingDeleteStillQueues(t *testing.T) {
	authDir := t.TempDir()
	filePath := filepath.Join(authDir, "pending-delete.json")
	if err := os.WriteFile(filePath, []byte(`{"type":"codex","disabled":true}`), 0o600); err != nil {
		t.Fatalf("failed to write auth file: %v", err)
	}

	service := &Service{
		cfg: &config.Config{
			AuthDir: authDir,
			AuthMaintenance: config.AuthMaintenanceConfig{
				Enable:            true,
				DeleteStatusCodes: []int{401, 429},
			},
		},
		coreManager: coreauth.NewManager(nil, nil, nil),
	}

	auth := &coreauth.Auth{
		ID:       "pending-delete",
		FileName: filepath.Base(filePath),
		Provider: "codex",
		Status:   coreauth.StatusDisabled,
		Disabled: true,
		Attributes: map[string]string{
			"path": filePath,
		},
		Metadata: map[string]any{
			"disabled":                               true,
			authMaintenancePendingDeleteMetadataKey:  true,
			authMaintenanceDeleteReasonMetadataKey:   "http_429",
			authMaintenanceDeleteQueuedAtMetadataKey: timeNowForTest().Format(time.RFC3339Nano),
		},
		UpdatedAt: timeNowForTest(),
	}
	if _, err := service.coreManager.Register(context.Background(), auth); err != nil {
		t.Fatalf("failed to register auth: %v", err)
	}

	candidates := service.scanAuthMaintenanceCandidates(timeNowForTest(), service.cfg.AuthMaintenance, authDir)
	if len(candidates) != 1 {
		t.Fatalf("expected 1 maintenance candidate, got %d", len(candidates))
	}
	if got := candidates[0].Reason; got != "http_429" {
		t.Fatalf("expected pending delete reason to survive scan fallback, got %q", got)
	}
}

func TestDeleteAuthMaintenanceCandidate_ClearsPendingDeleteMarkerOnSuccess(t *testing.T) {
	authDir := t.TempDir()
	filePath := filepath.Join(authDir, "pending-delete.json")
	if err := os.WriteFile(filePath, []byte(`{"type":"codex"}`), 0o600); err != nil {
		t.Fatalf("failed to write auth file: %v", err)
	}

	store := &trackingTokenStore{}
	previousStore := sdkAuth.GetTokenStore()
	sdkAuth.RegisterTokenStore(store)
	t.Cleanup(func() { sdkAuth.RegisterTokenStore(previousStore) })

	service := &Service{
		cfg:         &config.Config{AuthDir: authDir},
		coreManager: coreauth.NewManager(nil, nil, nil),
	}
	auth := &coreauth.Auth{
		ID:       "pending-delete",
		FileName: filepath.Base(filePath),
		Provider: "codex",
		Status:   coreauth.StatusDisabled,
		Disabled: true,
		Attributes: map[string]string{
			"path": filePath,
		},
		Metadata: map[string]any{
			"disabled":                               true,
			authMaintenancePendingDeleteMetadataKey:  true,
			authMaintenanceDeleteReasonMetadataKey:   "http_401",
			authMaintenanceDeleteQueuedAtMetadataKey: timeNowForTest().Format(time.RFC3339Nano),
		},
		UpdatedAt: timeNowForTest(),
	}
	if _, err := service.coreManager.Register(context.Background(), auth); err != nil {
		t.Fatalf("failed to register auth: %v", err)
	}

	err := service.deleteAuthMaintenanceCandidate(context.Background(), authMaintenanceCandidate{
		Key:    filePath,
		Path:   filePath,
		IDs:    []string{"pending-delete"},
		Reason: "http_401",
	})
	if err != nil {
		t.Fatalf("deleteAuthMaintenanceCandidate() error = %v", err)
	}

	updated, ok := service.coreManager.GetByID("pending-delete")
	if !ok || updated == nil {
		t.Fatal("expected auth to remain in manager after delete")
	}
	if authMaintenancePendingDelete(updated) {
		t.Fatal("expected pending delete marker to be cleared after successful delete")
	}
	candidates := service.scanAuthMaintenanceCandidates(timeNowForTest(), config.AuthMaintenanceConfig{
		Enable:            true,
		DeleteStatusCodes: []int{401, 429},
	}, authDir)
	if len(candidates) != 0 {
		t.Fatalf("expected no follow-up candidates after successful delete, got %#v", candidates)
	}
}

func TestAuthMaintenanceResult_DisablesImmediatelyWhileDeleteBacklogIsThrottled(t *testing.T) {
	authDir := t.TempDir()

	store := &timedTrackingTokenStore{}
	previousStore := sdkAuth.GetTokenStore()
	sdkAuth.RegisterTokenStore(store)
	t.Cleanup(func() { sdkAuth.RegisterTokenStore(previousStore) })

	service := &Service{
		cfg: &config.Config{
			AuthDir: authDir,
			AuthMaintenance: config.AuthMaintenanceConfig{
				Enable:                true,
				ScanIntervalSeconds:   30,
				DeleteIntervalSeconds: 2,
				DeleteStatusCodes:     []int{401, 429},
				DeleteQuotaExceeded:   true,
				QuotaStrikeThreshold:  2,
			},
		},
		coreManager: coreauth.NewManager(nil, nil, nil),
	}
	service.ensureAuthUpdateQueue(context.Background())
	t.Cleanup(func() {
		if service.authQueueStop != nil {
			service.authQueueStop()
		}
		if service.maintenanceCancel != nil {
			service.maintenanceCancel()
		}
	})

	executor := &authMaintenanceStressExecutor{
		delay: 2 * time.Millisecond,
		failStatus: map[string]int{
			"new-bad": 429,
		},
	}
	service.coreManager.RegisterExecutor(executor)

	const model = "stress-model"
	reg := registry.GetGlobalRegistry()
	allIDs := []string{"backlog-bad", "new-bad", "good"}
	for _, id := range allIDs {
		filePath := filepath.Join(authDir, id+".json")
		if err := os.WriteFile(filePath, []byte(`{"type":"stress"}`), 0o600); err != nil {
			t.Fatalf("write auth file %s: %v", id, err)
		}
		auth := &coreauth.Auth{
			ID:         id,
			FileName:   filepath.Base(filePath),
			Provider:   "stress",
			Status:     coreauth.StatusActive,
			Attributes: map[string]string{"path": filePath},
			UpdatedAt:  time.Now(),
			Metadata:   map[string]any{"label": id},
		}
		if _, err := service.coreManager.Register(context.Background(), auth); err != nil {
			t.Fatalf("register auth %s: %v", id, err)
		}
		reg.RegisterClient(id, "stress", []*registry.ModelInfo{{ID: model}})
		service.coreManager.RefreshSchedulerEntry(id)
	}
	t.Cleanup(func() {
		for _, id := range allIDs {
			reg.UnregisterClient(id)
		}
	})

	maintenanceCtx, maintenanceCancel := context.WithCancel(context.Background())
	defer maintenanceCancel()
	service.startAuthMaintenance(maintenanceCtx)

	backlogPath := filepath.Join(authDir, "backlog-bad.json")
	backlogCandidate := authMaintenanceCandidate{
		Key:    backlogPath,
		Path:   backlogPath,
		IDs:    []string{"backlog-bad"},
		Reason: "http_401",
	}
	service.disableAuthMaintenanceCandidate(context.Background(), backlogCandidate, "backlog-bad")
	if !service.enqueueAuthMaintenanceCandidate(backlogCandidate) {
		t.Fatal("expected backlog candidate to be queued")
	}

	deadline := time.Now().Add(1 * time.Second)
	for {
		if _, err := os.Stat(backlogPath); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for backlog delete")
		}
		time.Sleep(10 * time.Millisecond)
	}

	newBadPath := filepath.Join(authDir, "new-bad.json")
	req := coreexecutor.Request{Model: model, Payload: []byte(fmt.Sprintf(`{"model":"%s","input":"ping"}`, model))}
	_, err := service.coreManager.Execute(context.Background(), []string{"stress"}, req, coreexecutor.Options{
		Metadata: map[string]any{coreexecutor.PinnedAuthMetadataKey: "new-bad"},
	})
	if err == nil {
		t.Fatal("expected pinned bad auth to fail")
	}

	deadline = time.Now().Add(300 * time.Millisecond)
	for {
		auth, ok := service.coreManager.GetByID("new-bad")
		if ok && auth != nil && auth.Disabled && auth.Status == coreauth.StatusDisabled {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for new-bad to be disabled")
		}
		time.Sleep(10 * time.Millisecond)
	}

	if _, errStat := os.Stat(newBadPath); errStat != nil {
		if os.IsNotExist(errStat) {
			t.Fatal("expected new-bad file to remain until delete interval elapsed")
		}
		t.Fatalf("stat new-bad file: %v", errStat)
	}

	callsBefore := executor.snapshotCalls()
	for i := 0; i < 24; i++ {
		if _, errExec := service.coreManager.Execute(context.Background(), []string{"stress"}, req, coreexecutor.Options{}); errExec != nil {
			t.Fatalf("execute #%d error: %v", i, errExec)
		}
	}
	callsAfter := executor.snapshotCalls()
	if callsAfter["new-bad"] != callsBefore["new-bad"] {
		t.Fatalf("expected disabled auth to stop receiving traffic before physical delete: before=%d after=%d", callsBefore["new-bad"], callsAfter["new-bad"])
	}

	deadline = time.Now().Add(3 * time.Second)
	for {
		if _, errStat := os.Stat(newBadPath); os.IsNotExist(errStat) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for throttled delete")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestHandleAuthMaintenanceResult_SharedPathDisablesAllAuthsAndQueuesSingleCandidate(t *testing.T) {
	authDir := t.TempDir()
	filePath := filepath.Join(authDir, "shared.json")
	if err := os.WriteFile(filePath, []byte(`{"type":"codex"}`), 0o600); err != nil {
		t.Fatalf("failed to write auth file: %v", err)
	}

	service := &Service{
		cfg: &config.Config{
			AuthDir: authDir,
			AuthMaintenance: config.AuthMaintenanceConfig{
				Enable:            true,
				DeleteStatusCodes: []int{429},
			},
		},
		coreManager: coreauth.NewManager(nil, nil, nil),
	}

	for _, auth := range []*coreauth.Auth{
		{
			ID:         "shared-primary",
			FileName:   filepath.Base(filePath),
			Provider:   "codex",
			Status:     coreauth.StatusActive,
			Attributes: map[string]string{"path": filePath},
			Metadata:   map[string]any{"email": "primary@example.com"},
		},
		{
			ID:         "shared-project",
			FileName:   filepath.Base(filePath),
			Provider:   "codex",
			Status:     coreauth.StatusActive,
			Attributes: map[string]string{"path": filePath},
			Metadata:   map[string]any{"email": "project@example.com"},
		},
	} {
		if _, err := service.coreManager.Register(context.Background(), auth); err != nil {
			t.Fatalf("failed to register auth %s: %v", auth.ID, err)
		}
	}

	service.handleAuthMaintenanceResult(context.Background(), coreauth.Result{
		AuthID:  "shared-primary",
		Success: false,
		Error: &coreauth.Error{
			Message: `{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached"}}`,
		},
	})

	for _, id := range []string{"shared-primary", "shared-project"} {
		auth, ok := service.coreManager.GetByID(id)
		if !ok || auth == nil {
			t.Fatalf("expected auth %s to remain in manager", id)
		}
		if !auth.Disabled || auth.Status != coreauth.StatusDisabled {
			t.Fatalf("expected auth %s to be disabled immediately, got disabled=%v status=%q", id, auth.Disabled, auth.Status)
		}
		if !authMaintenancePendingDelete(auth) {
			t.Fatalf("expected auth %s to be marked pending delete", id)
		}
	}

	if got := service.authMaintenanceQueueLen(); got != 1 {
		t.Fatalf("expected a single queued candidate for the shared path, got %d", got)
	}
	candidate, _, ok := service.popAuthMaintenanceCandidate()
	if !ok {
		t.Fatal("expected queued candidate to be available")
	}
	if candidate.Path != filePath {
		t.Fatalf("expected shared candidate path %s, got %s", filePath, candidate.Path)
	}
	if len(candidate.IDs) != 2 {
		t.Fatalf("expected shared candidate to contain both ids, got %#v", candidate.IDs)
	}
	if candidate.Reason != "http_429" {
		t.Fatalf("expected shared candidate reason http_429, got %q", candidate.Reason)
	}
}

func TestScanAuthMaintenanceCandidates_LargeAuthPool9053(t *testing.T) {
	authDir := t.TempDir()
	service := &Service{
		cfg: &config.Config{
			AuthDir: authDir,
			AuthMaintenance: config.AuthMaintenanceConfig{
				Enable:               true,
				DeleteStatusCodes:    []int{401, 429},
				DeleteQuotaExceeded:  true,
				QuotaStrikeThreshold: 6,
			},
		},
		coreManager: coreauth.NewManager(nil, nil, nil),
	}

	const totalAuths = 9053
	expectedCandidates := 0
	for i := 0; i < totalAuths; i++ {
		fileName := fmt.Sprintf("pool-auth-%05d.json", i)
		filePath := filepath.Join(authDir, fileName)
		auth := &coreauth.Auth{
			ID:         fmt.Sprintf("pool/%05d", i),
			FileName:   fileName,
			Provider:   "codex",
			Status:     coreauth.StatusActive,
			Attributes: map[string]string{"path": filePath},
			UpdatedAt:  timeNowForTest(),
		}
		switch {
		case i%700 == 0:
			auth.Status = coreauth.StatusError
			auth.Unavailable = true
			auth.LastError = &coreauth.Error{HTTPStatus: 401, Message: "unauthorized"}
			expectedCandidates++
		case i%333 == 0:
			auth.Status = coreauth.StatusError
			auth.Unavailable = true
			auth.LastError = &coreauth.Error{HTTPStatus: 429, Message: "quota"}
			auth.Quota = coreauth.QuotaState{
				Exceeded:    true,
				Reason:      "quota",
				StrikeCount: 1,
			}
			expectedCandidates++
		}
		if _, err := service.coreManager.Register(context.Background(), auth); err != nil {
			t.Fatalf("failed to register auth %s: %v", auth.ID, err)
		}
	}

	start := time.Now()
	candidates := service.scanAuthMaintenanceCandidates(timeNowForTest(), service.cfg.AuthMaintenance, authDir)
	elapsed := time.Since(start)
	t.Logf("scanned %d auths and produced %d candidates in %s", totalAuths, len(candidates), elapsed)

	if len(candidates) != expectedCandidates {
		t.Fatalf("expected %d maintenance candidates, got %d", expectedCandidates, len(candidates))
	}
	if elapsed > 5*time.Second {
		t.Fatalf("expected large auth pool scan to complete within 5s, got %s", elapsed)
	}
}

func TestAuthMaintenanceBackgroundQueue_MixedLoadGraduallyRemoves401And429(t *testing.T) {
	authDir := t.TempDir()

	store := &timedTrackingTokenStore{}
	previousStore := sdkAuth.GetTokenStore()
	sdkAuth.RegisterTokenStore(store)
	t.Cleanup(func() { sdkAuth.RegisterTokenStore(previousStore) })

	service := &Service{
		cfg: &config.Config{
			AuthDir: authDir,
			AuthMaintenance: config.AuthMaintenanceConfig{
				Enable:                true,
				ScanIntervalSeconds:   1,
				DeleteIntervalSeconds: 1,
				DeleteStatusCodes:     []int{401, 429},
				DeleteQuotaExceeded:   true,
				QuotaStrikeThreshold:  2,
			},
		},
		coreManager: coreauth.NewManager(nil, nil, nil),
	}
	service.ensureAuthUpdateQueue(context.Background())
	t.Cleanup(func() {
		if service.authQueueStop != nil {
			service.authQueueStop()
		}
		if service.maintenanceCancel != nil {
			service.maintenanceCancel()
		}
	})

	executor := &authMaintenanceStressExecutor{
		delay: 2 * time.Millisecond,
		failStatus: map[string]int{
			"bad-401-a": 401,
			"bad-401-b": 401,
			"bad-429-a": 429,
			"bad-429-b": 429,
		},
	}
	service.coreManager.RegisterExecutor(executor)

	const model = "stress-model"
	badIDs := []string{"bad-401-a", "bad-401-b", "bad-429-a", "bad-429-b"}
	goodIDs := []string{"good-a", "good-b", "good-c", "good-d"}
	allIDs := append(append([]string(nil), badIDs...), goodIDs...)

	reg := registry.GetGlobalRegistry()
	for _, id := range allIDs {
		filePath := filepath.Join(authDir, id+".json")
		if err := os.WriteFile(filePath, []byte(`{"type":"stress"}`), 0o600); err != nil {
			t.Fatalf("write auth file %s: %v", id, err)
		}
		auth := &coreauth.Auth{
			ID:         id,
			FileName:   filepath.Base(filePath),
			Provider:   "stress",
			Status:     coreauth.StatusActive,
			Attributes: map[string]string{"path": filePath},
			UpdatedAt:  time.Now(),
		}
		if _, err := service.coreManager.Register(context.Background(), auth); err != nil {
			t.Fatalf("register auth %s: %v", id, err)
		}
		reg.RegisterClient(id, "stress", []*registry.ModelInfo{{ID: model}})
		service.coreManager.RefreshSchedulerEntry(id)
	}
	t.Cleanup(func() {
		for _, id := range allIDs {
			reg.UnregisterClient(id)
		}
	})

	maintenanceCtx, maintenanceCancel := context.WithCancel(context.Background())
	defer maintenanceCancel()
	service.startAuthMaintenance(maintenanceCtx)

	payload := []byte(fmt.Sprintf(`{"model":"%s","input":"%s"}`, model, strings.Repeat("token ", 8000)))
	req := coreexecutor.Request{Model: model, Payload: payload}

	for _, id := range badIDs {
		_, err := service.coreManager.Execute(context.Background(), []string{"stress"}, req, coreexecutor.Options{
			Metadata: map[string]any{coreexecutor.PinnedAuthMetadataKey: id},
		})
		if err == nil {
			t.Fatalf("expected pinned bad auth %s to fail", id)
		}
	}

	const (
		concurrency    = 48
		phase2Requests = 96
	)
	var (
		wg              sync.WaitGroup
		requestFailures atomic.Int64
	)
	workCtx, stopWork := context.WithCancel(context.Background())
	defer stopWork()

	start := time.Now()
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-workCtx.Done():
					return
				default:
				}
				_, err := service.coreManager.Execute(context.Background(), []string{"stress"}, req, coreexecutor.Options{})
				if err != nil {
					requestFailures.Add(1)
				}
			}
		}()
	}

	deadline := time.Now().Add(9 * time.Second)
	for {
		allRemoved := true
		allDisabled := true
		for _, id := range badIDs {
			path := filepath.Join(authDir, id+".json")
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				allRemoved = false
			}
			auth, ok := service.coreManager.GetByID(id)
			if !ok || auth == nil || !auth.Disabled || auth.Status != coreauth.StatusDisabled {
				allDisabled = false
			}
		}
		if allRemoved && allDisabled {
			break
		}
		if time.Now().After(deadline) {
			stopWork()
			wg.Wait()
			t.Fatalf("timed out waiting for maintenance deletes: removed=%v disabled=%v", allRemoved, allDisabled)
		}
		time.Sleep(100 * time.Millisecond)
	}

	stopWork()
	wg.Wait()
	elapsed := time.Since(start)

	if got := requestFailures.Load(); got != 0 {
		t.Fatalf("mixed load had %d execution failures", got)
	}
	if executor.successes.Load() == 0 {
		t.Fatal("expected successful executions during mixed load")
	}

	deleteRecords := store.snapshot()
	if len(deleteRecords) != len(badIDs) {
		t.Fatalf("expected %d maintenance deletions, got %d", len(badIDs), len(deleteRecords))
	}
	for i := 1; i < len(deleteRecords); i++ {
		if delta := deleteRecords[i].at.Sub(deleteRecords[i-1].at); delta < 800*time.Millisecond {
			t.Fatalf("expected staggered maintenance deletes, delta[%d]=%s", i, delta)
		}
	}

	badCallsBeforePhase2 := executor.snapshotCalls()
	for i := 0; i < phase2Requests; i++ {
		if _, err := service.coreManager.Execute(context.Background(), []string{"stress"}, req, coreexecutor.Options{}); err != nil {
			t.Fatalf("post-delete execute #%d error: %v", i, err)
		}
	}
	badCallsAfterPhase2 := executor.snapshotCalls()
	for _, id := range badIDs {
		if badCallsAfterPhase2[id] != badCallsBeforePhase2[id] {
			t.Fatalf("expected deleted auth %s to stop receiving traffic: before=%d after=%d", id, badCallsBeforePhase2[id], badCallsAfterPhase2[id])
		}
		if models := reg.GetModelsForClient(id); len(models) != 0 {
			t.Fatalf("expected registry models for %s to be removed, got %d", id, len(models))
		}
	}
	for _, id := range goodIDs {
		auth, ok := service.coreManager.GetByID(id)
		if !ok || auth == nil || auth.Disabled {
			t.Fatalf("expected good auth %s to remain active", id)
		}
	}

	rps := float64(executor.successes.Load()) / elapsed.Seconds()
	rpm := rps * 60
	tokensPerSecond := float64(executor.successes.Load()*8000) / elapsed.Seconds()
	tpm := tokensPerSecond * 60
	t.Logf(
		"mixed maintenance load: successes=%d executor_failures=%d concurrency=%d elapsed=%s rpm=%.0f tpm=%.0f deletions=%d",
		executor.successes.Load(),
		executor.failures.Load(),
		concurrency,
		elapsed,
		rpm,
		tpm,
		len(deleteRecords),
	)
}

func timeNowForTest() time.Time {
	return time.Unix(1_763_600_000, 0).UTC()
}
