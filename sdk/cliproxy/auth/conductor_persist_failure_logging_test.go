package auth

import (
	"context"
	"errors"
	"strings"
	"testing"

	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
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

// assertWarnEntry verifies that a warn entry exists matching needle, authID, provider, and errText,
// and verifies that formatting with internal/logging.LogFormatter preserves auth ID, provider, and error.
func assertWarnEntry(t *testing.T, hook *logtest.Hook, needle, authID, provider, errText string) {
	t.Helper()
	var matched *log.Entry
	for _, entry := range hook.AllEntries() {
		if entry.Level != log.WarnLevel || !strings.Contains(entry.Message, needle) {
			continue
		}
		if !strings.Contains(entry.Message, authID) && entry.Data["auth_id"] != authID && entry.Data["credential"] != authID {
			continue
		}
		matched = entry
		break
	}
	if matched == nil {
		t.Fatalf("expected warn log matching needle=%q authID=%q, got logs: %#v", needle, authID, hook.AllEntries())
	}
	if !strings.Contains(matched.Message, authID) {
		t.Fatalf("expected log message to contain auth ID %q, got: %s", authID, matched.Message)
	}
	if !strings.Contains(matched.Message, provider) {
		t.Fatalf("expected log message to contain provider %q, got: %s", provider, matched.Message)
	}
	if !strings.Contains(matched.Message, errText) {
		t.Fatalf("expected log message to contain error text %q, got: %s", errText, matched.Message)
	}
	if matched.Data["auth_id"] != authID {
		t.Fatalf("expected entry.Data[auth_id]=%q, got: %v", authID, matched.Data["auth_id"])
	}
	if matched.Data["credential"] != authID {
		t.Fatalf("expected entry.Data[credential]=%q, got: %v", authID, matched.Data["credential"])
	}
	if matched.Data["provider"] != provider {
		t.Fatalf("expected entry.Data[provider]=%q, got: %v", provider, matched.Data["provider"])
	}

	formatter := &internallogging.LogFormatter{}
	formattedBytes, errFormat := formatter.Format(matched)
	if errFormat != nil {
		t.Fatalf("LogFormatter.Format returned error: %v", errFormat)
	}
	formatted := string(formattedBytes)
	if !strings.Contains(formatted, `auth_id="`+authID+`"`) {
		t.Fatalf("formatted log output missing auth_id field for %q: %s", authID, formatted)
	}
	if !strings.Contains(formatted, `credential="`+authID+`"`) {
		t.Fatalf("formatted log output missing credential field for %q: %s", authID, formatted)
	}
	if !strings.Contains(formatted, "provider="+provider) {
		t.Fatalf("formatted log output missing provider field for %q: %s", provider, formatted)
	}
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

	assertWarnEntry(t, hook, "failed to persist registered auth", "auth-register-persist-1", "claude", "permission denied")
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

	assertWarnEntry(t, hook, "failed to persist updated auth", "auth-update-persist-1", "claude", "permission denied")
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

	assertWarnEntry(t, hook, "failed to persist updated auth", "auth-refresh-persist-1", "claude", "permission denied")
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

	assertWarnEntry(t, hook, "persist refreshed auth", "auth-meta-persist-1", "meta", "permission denied")
}
