package cliproxy

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/store/mongostate"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

type fakeCircuitBreakerDeletionStore struct {
	upserted       []*mongostate.CircuitBreakerDeletionRecord
	recordsByID    map[string]mongostate.CircuitBreakerDeletionRecord
	applied        []mongostate.CircuitBreakerDeletionAction
	appliedIDs     []string
	upsertErr      error
	applyActionErr error
}

func (f *fakeCircuitBreakerDeletionStore) Insert(_ context.Context, record *mongostate.CircuitBreakerDeletionRecord) error {
	if record != nil {
		cloned := *record
		f.upserted = append(f.upserted, &cloned)
	}
	return nil
}

func (f *fakeCircuitBreakerDeletionStore) UpsertPending(_ context.Context, record *mongostate.CircuitBreakerDeletionRecord) (mongostate.CircuitBreakerDeletionRecord, error) {
	if record != nil {
		cloned := *record
		f.upserted = append(f.upserted, &cloned)
		return cloned, f.upsertErr
	}
	return mongostate.CircuitBreakerDeletionRecord{}, f.upsertErr
}

func (f *fakeCircuitBreakerDeletionStore) GetByID(_ context.Context, id string) (mongostate.CircuitBreakerDeletionRecord, error) {
	if record, ok := f.recordsByID[id]; ok {
		return record, nil
	}
	return mongostate.CircuitBreakerDeletionRecord{}, mongostate.ErrCircuitBreakerDeletionNotFound
}

func (f *fakeCircuitBreakerDeletionStore) ApplyAction(_ context.Context, id string, action mongostate.CircuitBreakerDeletionAction) (mongostate.CircuitBreakerDeletionRecord, error) {
	f.appliedIDs = append(f.appliedIDs, id)
	f.applied = append(f.applied, action)
	record := f.recordsByID[id]
	record.Status = action.Status
	record.ActionBy = action.ActionBy
	record.ActionError = action.ActionError
	now := time.Now().UTC()
	record.ActionAt = &now
	record.UpdatedAt = now
	record.Persisted = action.Persisted
	record.AlreadyRemoved = action.AlreadyRemoved
	record.RuntimeSuspended = action.RuntimeSuspended
	f.recordsByID[id] = record
	return record, f.applyActionErr
}

func (f *fakeCircuitBreakerDeletionStore) Close(context.Context) error { return nil }

func writeBaseConfigFile(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("failed to write base config file: %v", err)
	}
}

func TestPersistCircuitBreakerAutoRemoval_CodexAPIKey(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	writeBaseConfigFile(t, configPath)

	svc := &Service{
		cfg: &config.Config{
			CodexKey: []config.CodexKey{{
				APIKey:   "k1",
				BaseURL:  "https://codex.example.com",
				Models:   []config.CodexModel{{Name: "gpt-5-codex", Alias: "alias-gpt-5-codex"}},
				Priority: 1,
			}},
		},
		configPath: configPath,
	}
	auth := &coreauth.Auth{
		ID:       "auth-codex-1",
		Provider: "codex",
		Attributes: map[string]string{
			"api_key":   "k1",
			"base_url":  "https://codex.example.com",
			"auth_kind": "apikey",
		},
	}

	persisted, alreadyRemoved, err := svc.persistCircuitBreakerAutoRemoval(auth, "alias-gpt-5-codex")
	if err != nil {
		t.Fatalf("persistCircuitBreakerAutoRemoval() error = %v", err)
	}
	if !persisted {
		t.Fatal("persisted = false, want true")
	}
	if alreadyRemoved {
		t.Fatal("alreadyRemoved = true, want false")
	}
	if len(svc.cfg.CodexKey) != 1 {
		t.Fatalf("codex entries len = %d, want 1", len(svc.cfg.CodexKey))
	}
	if len(svc.cfg.CodexKey[0].Models) != 0 {
		t.Fatalf("models len = %d, want 0", len(svc.cfg.CodexKey[0].Models))
	}
	if len(svc.cfg.CodexKey[0].ExcludedModels) != 1 || svc.cfg.CodexKey[0].ExcludedModels[0] != "alias-gpt-5-codex" {
		t.Fatalf("excluded models = %v, want [alias-gpt-5-codex]", svc.cfg.CodexKey[0].ExcludedModels)
	}

	persistedAgain, alreadyRemovedAgain, err := svc.persistCircuitBreakerAutoRemoval(auth, "alias-gpt-5-codex")
	if err != nil {
		t.Fatalf("second persistCircuitBreakerAutoRemoval() error = %v", err)
	}
	if persistedAgain {
		t.Fatal("second persisted = true, want false")
	}
	if !alreadyRemovedAgain {
		t.Fatal("second alreadyRemoved = false, want true")
	}
}

func TestPersistCircuitBreakerAutoRemoval_OAuthProvider(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	writeBaseConfigFile(t, configPath)

	svc := &Service{
		cfg:        &config.Config{},
		configPath: configPath,
	}
	auth := &coreauth.Auth{
		ID:       "auth-qwen-oauth-1",
		Provider: "qwen",
		Attributes: map[string]string{
			"auth_kind": "oauth",
		},
	}

	persisted, alreadyRemoved, err := svc.persistCircuitBreakerAutoRemoval(auth, "qwen-plus")
	if err != nil {
		t.Fatalf("persistCircuitBreakerAutoRemoval() error = %v", err)
	}
	if !persisted {
		t.Fatal("persisted = false, want true")
	}
	if alreadyRemoved {
		t.Fatal("alreadyRemoved = true, want false")
	}
	if got := svc.cfg.OAuthExcludedModels["qwen"]; len(got) != 1 || got[0] != "qwen-plus" {
		t.Fatalf("oauth-excluded-models[qwen] = %v, want [qwen-plus]", got)
	}
}

func TestNormalizeModelForAutoRemoval_StripsSuffixAndPrefix(t *testing.T) {
	auth := &coreauth.Auth{Prefix: "teamA"}
	got := normalizeModelForAutoRemoval(auth, "teamA/gpt-5-codex:high")
	if got != "gpt-5-codex" {
		t.Fatalf("normalizeModelForAutoRemoval() = %q, want %q", got, "gpt-5-codex")
	}
}

func TestOnCircuitBreakerOpened_WritesPendingCandidateWithoutPersistingConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	writeBaseConfigFile(t, configPath)

	store := &fakeCircuitBreakerDeletionStore{recordsByID: map[string]mongostate.CircuitBreakerDeletionRecord{}}
	coreMgr := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{
		ID:       "auth-codex-1",
		Provider: "codex",
		Attributes: map[string]string{
			"api_key":   "k1",
			"base_url":  "https://codex.example.com",
			"auth_kind": "apikey",
		},
	}
	if _, err := coreMgr.Update(context.Background(), auth); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	svc := &Service{
		cfg: &config.Config{
			CircuitBreakerAutoRemoval: config.CircuitBreakerAutoRemovalConfig{AutoRemoveThreshold: 3},
			CodexKey: []config.CodexKey{{
				APIKey:   "k1",
				BaseURL:  "https://codex.example.com",
				Models:   []config.CodexModel{{Name: "gpt-5-codex", Alias: "alias-gpt-5-codex"}},
				Priority: 1,
			}},
		},
		configPath:                  configPath,
		coreManager:                 coreMgr,
		circuitBreakerDeletionStore: store,
	}

	svc.OnCircuitBreakerOpened(context.Background(), registry.CircuitBreakerOpenEvent{
		ClientID:            auth.ID,
		Provider:            "codex",
		ModelID:             "alias-gpt-5-codex",
		OpenCycles:          3,
		FailureCount:        6,
		ConsecutiveFailures: 3,
		OpenedAt:            time.Now().UTC(),
	})

	if len(store.upserted) != 1 {
		t.Fatalf("upserted len = %d, want 1", len(store.upserted))
	}
	record := store.upserted[0]
	if record.Status != mongostate.CircuitBreakerDeletionStatusPending {
		t.Fatalf("record.Status = %q, want %q", record.Status, mongostate.CircuitBreakerDeletionStatusPending)
	}
	if record.RuntimeSuspended {
		t.Fatal("record.RuntimeSuspended = true, want false")
	}
	if record.Persisted {
		t.Fatal("record.Persisted = true, want false")
	}
	if len(svc.cfg.CodexKey[0].Models) != 1 {
		t.Fatalf("models len = %d, want 1", len(svc.cfg.CodexKey[0].Models))
	}
	if len(svc.cfg.CodexKey[0].ExcludedModels) != 0 {
		t.Fatalf("excluded models = %v, want empty", svc.cfg.CodexKey[0].ExcludedModels)
	}
}

func TestDeleteCircuitBreakerDeletion_DeletesPendingRecordAndPersistsConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	writeBaseConfigFile(t, configPath)

	store := &fakeCircuitBreakerDeletionStore{
		recordsByID: map[string]mongostate.CircuitBreakerDeletionRecord{
			"abc": {
				ID:              primitiveObjectIDForTest(t, "0000000000000000000000ab"),
				AuthID:          "auth-codex-1",
				Provider:        "codex",
				Model:           "alias-gpt-5-codex",
				NormalizedModel: "alias-gpt-5-codex",
				Status:          mongostate.CircuitBreakerDeletionStatusPending,
			},
		},
	}
	coreMgr := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{
		ID:       "auth-codex-1",
		Provider: "codex",
		Attributes: map[string]string{
			"api_key":   "k1",
			"base_url":  "https://codex.example.com",
			"auth_kind": "apikey",
		},
	}
	if _, err := coreMgr.Update(context.Background(), auth); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	svc := &Service{
		cfg: &config.Config{
			CodexKey: []config.CodexKey{{
				APIKey:   "k1",
				BaseURL:  "https://codex.example.com",
				Models:   []config.CodexModel{{Name: "gpt-5-codex", Alias: "alias-gpt-5-codex"}},
				Priority: 1,
			}},
		},
		configPath:                  configPath,
		coreManager:                 coreMgr,
		circuitBreakerDeletionStore: store,
	}

	item, err := svc.DeleteCircuitBreakerDeletion(context.Background(), "abc", "management_api")
	if err != nil {
		t.Fatalf("DeleteCircuitBreakerDeletion() error = %v", err)
	}
	if item.Status != mongostate.CircuitBreakerDeletionStatusDeleted {
		t.Fatalf("item.Status = %q, want %q", item.Status, mongostate.CircuitBreakerDeletionStatusDeleted)
	}
	if len(store.applied) != 1 || store.applied[0].Status != mongostate.CircuitBreakerDeletionStatusDeleted {
		t.Fatalf("applied = %+v, want deleted action", store.applied)
	}
	if len(svc.cfg.CodexKey[0].Models) != 0 {
		t.Fatalf("models len = %d, want 0", len(svc.cfg.CodexKey[0].Models))
	}
}

func TestDismissCircuitBreakerDeletion_DismissesPendingWithoutPersistingConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	writeBaseConfigFile(t, configPath)

	store := &fakeCircuitBreakerDeletionStore{
		recordsByID: map[string]mongostate.CircuitBreakerDeletionRecord{
			"abc": {
				ID:              primitiveObjectIDForTest(t, "0000000000000000000000ac"),
				AuthID:          "auth-codex-1",
				Provider:        "codex",
				Model:           "alias-gpt-5-codex",
				NormalizedModel: "alias-gpt-5-codex",
				Status:          mongostate.CircuitBreakerDeletionStatusPending,
			},
		},
	}
	svc := &Service{
		cfg: &config.Config{
			CodexKey: []config.CodexKey{{
				APIKey:   "k1",
				BaseURL:  "https://codex.example.com",
				Models:   []config.CodexModel{{Name: "gpt-5-codex", Alias: "alias-gpt-5-codex"}},
				Priority: 1,
			}},
		},
		configPath:                  configPath,
		circuitBreakerDeletionStore: store,
	}

	item, err := svc.DismissCircuitBreakerDeletion(context.Background(), "abc", "management_api")
	if err != nil {
		t.Fatalf("DismissCircuitBreakerDeletion() error = %v", err)
	}
	if item.Status != mongostate.CircuitBreakerDeletionStatusDismissed {
		t.Fatalf("item.Status = %q, want %q", item.Status, mongostate.CircuitBreakerDeletionStatusDismissed)
	}
	if len(store.applied) != 1 || store.applied[0].Status != mongostate.CircuitBreakerDeletionStatusDismissed {
		t.Fatalf("applied = %+v, want dismissed action", store.applied)
	}
	if len(svc.cfg.CodexKey[0].Models) != 1 {
		t.Fatalf("models len = %d, want 1", len(svc.cfg.CodexKey[0].Models))
	}
}

func primitiveObjectIDForTest(t *testing.T, hex string) primitive.ObjectID {
	t.Helper()
	id, err := primitive.ObjectIDFromHex(hex)
	if err != nil {
		t.Fatalf("ObjectIDFromHex(%q) error = %v", hex, err)
	}
	return id
}
