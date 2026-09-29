package auth

import (
	"context"
	"errors"
	"strings"
	"testing"

	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

// persistFailureStore always fails Save to simulate an unwritable auth file.
type persistFailureStore struct{}

func (s *persistFailureStore) List(context.Context) ([]*Auth, error) { return nil, nil }

func (s *persistFailureStore) Save(context.Context, *Auth) (string, error) {
	return "", errors.New("persist store failure: permission denied")
}

func (s *persistFailureStore) Delete(context.Context, string) error { return nil }

// findWarnEntries returns warn-level entries whose message contains needle and
// that reference authID either in a structured field or in the message itself.
func findWarnEntries(hook *logtest.Hook, needle, authID string) []string {
	var matched []string
	for _, entry := range hook.AllEntries() {
		if entry.Level != log.WarnLevel || !strings.Contains(entry.Message, needle) {
			continue
		}
		if id, _ := entry.Data["auth_id"].(string); id != authID && !strings.Contains(entry.Message, authID) {
			continue
		}
		matched = append(matched, entry.Message)
	}
	return matched
}

// A persist failure after registering an auth must surface at warn level:
// the credential may exist only in memory while the disk copy is stale.
func TestRegisterPersistFailureLogsWarn(t *testing.T) {
	hook := setupTestLoggerHook(t)
	m := NewManager(&persistFailureStore{}, nil, nil)

	auth := &Auth{
		ID:       "auth-register-persist-1",
		Provider: "claude",
		Metadata: map[string]any{"access_token": "at", "refresh_token": "rt"},
	}
	registered, errRegister := m.Register(context.Background(), auth)
	if errRegister != nil {
		t.Fatalf("Register must stay non-fatal on persist failure, got error: %v", errRegister)
	}
	if registered == nil {
		t.Fatal("Register returned nil auth on persist failure")
	}

	matched := findWarnEntries(hook, "failed to persist", "auth-register-persist-1")
	if len(matched) == 0 {
		t.Fatalf("expected warn log for register persist failure, got logs: %#v", hook.AllEntries())
	}
}

// A persist failure after an update must surface at warn level.
func TestUpdatePersistFailureLogsWarn(t *testing.T) {
	hook := setupTestLoggerHook(t)
	m := NewManager(&persistFailureStore{}, nil, nil)

	auth := &Auth{
		ID:       "auth-update-persist-1",
		Provider: "claude",
		Metadata: map[string]any{"access_token": "at", "refresh_token": "rt"},
	}
	if _, errRegister := m.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("Register(skipPersist) returned error: %v", errRegister)
	}

	hook.Reset()
	if _, errUpdate := m.Update(context.Background(), auth); errUpdate != nil {
		t.Fatalf("Update must stay non-fatal on persist failure, got error: %v", errUpdate)
	}

	matched := findWarnEntries(hook, "failed to persist", "auth-update-persist-1")
	if len(matched) == 0 {
		t.Fatalf("expected warn log for update persist failure, got logs: %#v", hook.AllEntries())
	}
}

// The issue scenario: a token refresh succeeds in memory but the store rejects
// the write. The refresh itself must keep succeeding, and the lost persistence
// must be visible at warn level (previously silent for non-meta providers).
func TestRefreshPersistFailureLogsWarn(t *testing.T) {
	hook := setupTestLoggerHook(t)
	m := NewManager(&persistFailureStore{}, nil, nil)

	auth := &Auth{
		ID:       "auth-refresh-persist-1",
		Provider: "claude",
		Metadata: map[string]any{"access_token": "stale-access-token", "refresh_token": "rt"},
	}
	if _, errRegister := m.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("Register(skipPersist) returned error: %v", errRegister)
	}
	m.RegisterExecutor(&unauthorizedRefreshExecutor{id: "claude"})

	hook.Reset()
	refreshed, errRefresh := m.ForceRefreshAuth(context.Background(), auth.ID)
	if errRefresh != nil {
		t.Fatalf("ForceRefreshAuth must stay non-fatal on persist failure, got error: %v", errRefresh)
	}
	if refreshed == nil {
		t.Fatal("ForceRefreshAuth returned nil auth on persist failure")
	}

	matched := findWarnEntries(hook, "failed to persist", "auth-refresh-persist-1")
	if len(matched) == 0 {
		t.Fatalf("expected warn log for refresh persist failure, got logs: %#v", hook.AllEntries())
	}
}

// Meta mint persist failures propagate as errors and must be logged at warn
// level (previously debug only, invisible at the default log level).
func TestMetaRefreshPersistFailureLogsWarn(t *testing.T) {
	hook := setupTestLoggerHook(t)
	m := NewManager(&persistFailureStore{}, nil, nil)

	auth := &Auth{
		ID:       "auth-meta-persist-1",
		Provider: "meta",
		Metadata: map[string]any{"dca_token": "dca:valid"},
	}
	if _, errRegister := m.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("Register(skipPersist) returned error: %v", errRegister)
	}
	m.RegisterExecutor(&unauthorizedRefreshExecutor{id: "meta"})

	hook.Reset()
	if _, errRefresh := m.ForceRefreshAuth(context.Background(), auth.ID); errRefresh == nil {
		t.Fatal("expected meta mint persist failure to return an error")
	}

	matched := findWarnEntries(hook, "persist refreshed auth", "auth-meta-persist-1")
	if len(matched) == 0 {
		t.Fatalf("expected warn log for meta refresh persist failure, got logs: %#v", hook.AllEntries())
	}
}
