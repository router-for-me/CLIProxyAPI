package claudemaster

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func recoveryFixture(t *testing.T) BackendOptions {
	t.Helper()
	return writeSyntheticBackendCredential(t, t.TempDir(), "selected.json", map[string]any{
		"account_uuid": "synthetic-account", "email": "synthetic@example.invalid",
	})
}

func recoveryStage(t *testing.T, opts BackendOptions, raw []byte) string {
	t.Helper()
	f, err := os.CreateTemp(opts.AuthDir, ".refresh-*")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := f.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return f.Name()
}

func recoverySnapshot(t *testing.T, opts BackendOptions, changes map[string]any) []byte {
	t.Helper()
	_, auth, err := loadBackendCredential(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range changes {
		auth.Metadata[key] = value
	}
	raw, err := json.Marshal(auth.Metadata)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestRecoverStagedRefreshPromotesRotatedCredential(t *testing.T) {
	opts := recoveryFixture(t)
	raw := recoverySnapshot(t, opts, map[string]any{"refresh_token": "synthetic-rotated", "access_token": "synthetic-new"})
	recoveryStage(t, opts, raw)
	_, auth, err := loadBackendCredential(t.Context(), opts)
	if err != nil || auth.Metadata["refresh_token"] != "synthetic-rotated" {
		t.Fatalf("complete rotated token was not recovered: %v", err)
	}
	entries, err := os.ReadDir(opts.AuthDir)
	if err != nil || len(entries) != 1 || entries[0].Name() != opts.AuthID {
		t.Fatal("recovered stage remained in the auth directory")
	}
}

func TestLockedProfileRecoversInterruptedRefresh(t *testing.T) {
	lock := profileFixture(t)
	installProfileFixture(t, lock)
	profile, err := lock.Profile()
	if err != nil {
		t.Fatal(err)
	}
	opts := BackendOptions{AuthDir: profile.AuthDir, AuthID: profile.AuthID, Provider: "claude"}
	current := []byte(`{"type":"claude","access_token":"synthetic-access","refresh_token":"synthetic-old"}`)
	if err := os.WriteFile(filepath.Join(opts.AuthDir, opts.AuthID), current, 0600); err != nil {
		t.Fatal(err)
	}
	recoveryStage(t, opts, []byte(`{"type":"claude","access_token":"synthetic-new","refresh_token":"synthetic-rotated"}`))
	if _, err := lock.Profile(); err != nil {
		t.Fatalf("locked profile did not recover: %v", err)
	}
	_, auth, err := loadBackendCredential(t.Context(), opts)
	if err != nil || auth.Metadata["refresh_token"] != "synthetic-rotated" {
		t.Fatal("locked recovery lost the rotated token")
	}
}

func TestRecoverStagedRefreshDiscardsOnlyIncompleteJSONWithValidCurrent(t *testing.T) {
	for _, partial := range []string{"", `{"type":"codex","refresh_token":`} {
		t.Run(partial, func(t *testing.T) {
			opts := recoveryFixture(t)
			before, err := os.ReadFile(filepath.Join(opts.AuthDir, opts.AuthID))
			if err != nil {
				t.Fatal(err)
			}
			path := recoveryStage(t, opts, []byte(partial))
			if err := recoverStagedRefresh(opts.AuthDir, opts.AuthID, opts.Provider); err != nil {
				t.Fatal(err)
			}
			after, err := os.ReadFile(filepath.Join(opts.AuthDir, opts.AuthID))
			if err != nil || string(after) != string(before) {
				t.Fatal("incomplete stage changed selected credentials")
			}
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				t.Fatal("incomplete stage was not removed")
			}
		})
	}
}

func TestRecoverStagedRefreshPreservesAmbiguousOrUnsafeStages(t *testing.T) {
	for _, kind := range []string{"multiple", "account-mismatch", "provider-mismatch", "complete-invalid", "public", "symlink", "hardlink", "invalid-current"} {
		t.Run(kind, func(t *testing.T) {
			opts := recoveryFixture(t)
			before, err := os.ReadFile(filepath.Join(opts.AuthDir, opts.AuthID))
			if err != nil {
				t.Fatal(err)
			}
			changes := map[string]any{"refresh_token": "synthetic-sensitive-rotated"}
			if kind == "account-mismatch" {
				changes["account_uuid"] = "different-account"
			}
			if kind == "provider-mismatch" {
				changes["type"] = "claude"
			}
			if kind == "complete-invalid" {
				changes["refresh_token"] = ""
			}
			raw := recoverySnapshot(t, opts, changes)
			stage := recoveryStage(t, opts, raw)
			switch kind {
			case "multiple":
				recoveryStage(t, opts, raw)
			case "public":
				if err := os.Chmod(stage, 0644); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Remove(stage); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(opts.AuthDir, opts.AuthID), stage); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(stage, filepath.Join(t.TempDir(), "linked-stage")); err != nil {
					t.Fatal(err)
				}
			case "invalid-current":
				before = []byte(`{"type":`)
				if err := os.WriteFile(filepath.Join(opts.AuthDir, opts.AuthID), before, 0600); err != nil {
					t.Fatal(err)
				}
			}
			err = recoverStagedRefresh(opts.AuthDir, opts.AuthID, opts.Provider)
			if err == nil || strings.Contains(err.Error(), "synthetic-sensitive") {
				t.Fatal("unsafe recovery succeeded or exposed a credential")
			}
			after, err := os.ReadFile(filepath.Join(opts.AuthDir, opts.AuthID))
			if err != nil || string(after) != string(before) {
				t.Fatal("refused recovery changed selected credentials")
			}
			if _, err := os.Lstat(stage); err != nil {
				t.Fatal("refused recovery removed the staged token")
			}
		})
	}
}

func TestBackendStoreRetainsCompleteStageAfterFailedCommit(t *testing.T) {
	opts := recoveryFixture(t)
	store, auth, err := loadBackendCredential(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	store.renameFile = func(string, string) error { return errors.New("synthetic commit failure") }
	auth.Metadata["refresh_token"] = "synthetic-rotated"
	if _, err := store.Save(t.Context(), auth); err == nil {
		t.Fatal("injected commit failure succeeded")
	}
	entries, err := os.ReadDir(opts.AuthDir)
	if err != nil || len(entries) != 2 {
		t.Fatal("complete rotated stage was discarded on failed commit")
	}
	_, recovered, err := loadBackendCredential(t.Context(), opts)
	if err != nil || recovered.Metadata["refresh_token"] != "synthetic-rotated" {
		t.Fatal("failed-commit stage was not recoverable")
	}
}

func TestBackendStoreLaterSuccessfulSaveCannotBeRolledBackByRetainedStage(t *testing.T) {
	opts := recoveryFixture(t)
	store, auth, err := loadBackendCredential(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	store.renameFile = func(string, string) error { return errors.New("synthetic commit failure") }
	auth.Metadata["refresh_token"] = "synthetic-first-rotation"
	if _, err := store.Save(t.Context(), auth); err == nil {
		t.Fatal("injected commit failure succeeded")
	}
	store.renameFile = nil
	auth.Metadata["refresh_token"] = "synthetic-second-rotation"
	if _, err := store.Save(t.Context(), auth); err != nil {
		t.Fatal(err)
	}
	_, recovered, err := loadBackendCredential(t.Context(), opts)
	if err != nil || recovered.Metadata["refresh_token"] != "synthetic-second-rotation" {
		t.Fatal("retained older stage rolled back a newer successful save")
	}
	entries, err := os.ReadDir(opts.AuthDir)
	if err != nil || len(entries) != 1 {
		t.Fatal("later successful save left an ambiguous stage")
	}
}

func TestRecoverStagedRefreshChecksBothAccountUUIDSpellings(t *testing.T) {
	for _, tc := range []struct {
		name    string
		current map[string]any
		stage   map[string]any
		allowed bool
	}{
		{"camel-same", map[string]any{"accountUuid": "account-a"}, map[string]any{"accountUuid": "account-a"}, true},
		{"snake-to-camel", map[string]any{"account_uuid": "account-a"}, map[string]any{"accountUuid": "account-a"}, true},
		{"camel-to-snake", map[string]any{"accountUuid": "account-a"}, map[string]any{"account_uuid": "account-a"}, true},
		{"trimmed-alias", map[string]any{"accountUuid": " account-a "}, map[string]any{"account_uuid": "account-a"}, true},
		{"camel-mismatch", map[string]any{"accountUuid": "account-a"}, map[string]any{"accountUuid": "account-b"}, false},
		{"contradictory-stage", map[string]any{"accountUuid": "account-a"}, map[string]any{"account_uuid": "account-a", "accountUuid": "account-b"}, false},
		{"contradictory-current", map[string]any{"account_uuid": "account-a", "accountUuid": "account-b"}, map[string]any{"account_uuid": "account-a"}, false},
		{"wrong-type", map[string]any{"accountUuid": "account-a"}, map[string]any{"accountUuid": 1}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := recoveryFixture(t)
			current := map[string]any{"type": opts.Provider, "access_token": "synthetic-access", "refresh_token": "synthetic-original"}
			stage := map[string]any{"type": opts.Provider, "access_token": "synthetic-new", "refresh_token": "synthetic-rotated"}
			for key, value := range tc.current {
				current[key] = value
			}
			for key, value := range tc.stage {
				stage[key] = value
			}
			before, err := json.Marshal(current)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(opts.AuthDir, opts.AuthID)
			if err := os.WriteFile(path, before, 0600); err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(stage)
			if err != nil {
				t.Fatal(err)
			}
			stagedPath := recoveryStage(t, opts, raw)
			err = recoverStagedRefresh(opts.AuthDir, opts.AuthID, opts.Provider)
			if (err == nil) != tc.allowed {
				t.Fatalf("account alias recovery allowed=%t, err=%v", tc.allowed, err)
			}
			after, errRead := os.ReadFile(path)
			if errRead != nil {
				t.Fatal(errRead)
			}
			if tc.allowed {
				if string(after) != string(raw) {
					t.Fatal("same-account rotated token was not recovered")
				}
			} else {
				if string(after) != string(before) {
					t.Fatal("account mismatch changed the selected credential")
				}
				if _, err := os.Lstat(stagedPath); err != nil {
					t.Fatal("refused recovery discarded the rotated token")
				}
			}
		})
	}
}

func TestBackendStoreRepeatedCommitFailuresRecoverNewestRotatedToken(t *testing.T) {
	opts := recoveryFixture(t)
	store, auth, err := loadBackendCredential(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	store.renameFile = func(string, string) error { return errors.New("synthetic repeated commit failure") }
	for _, token := range []string{"synthetic-first-rotation", "synthetic-second-rotation"} {
		auth.Metadata["refresh_token"] = token
		if _, err := store.Save(t.Context(), auth); err == nil {
			t.Fatal("injected commit failure succeeded")
		}
		entries, err := os.ReadDir(opts.AuthDir)
		if err != nil || len(entries) != 2 {
			t.Fatal("repeated failure left zero or multiple complete stages")
		}
	}
	_, recovered, err := loadBackendCredential(t.Context(), opts)
	if err != nil || recovered.Metadata["refresh_token"] != "synthetic-second-rotation" {
		t.Fatal("repeated failure did not recover the newest rotated token")
	}
	entries, err := os.ReadDir(opts.AuthDir)
	if err != nil || len(entries) != 1 {
		t.Fatal("repeated failure recovery left ambiguous stages")
	}
}
