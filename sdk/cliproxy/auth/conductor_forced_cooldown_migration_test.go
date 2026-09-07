package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestManager_ForcedCooldownLegacyMigration(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	SetQuotaCooldownDisabled(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })
	for _, fixture := range []string{"cooldown_v1_stale_forced_error.json", "cooldown_v1_ambiguous_quota.json"} {
		t.Run(fixture, func(t *testing.T) {
			testForcedCooldownLegacyMigration(t, fixture)
		})
	}
}

func testForcedCooldownLegacyMigration(t *testing.T, fixture string) {
	t.Helper()
	ctx := context.Background()
	// Captured from upstream 934fb7928c42a8dd0aeaf39a321bef6601b55eb6:
	// A ordinary 404 or 429, B forced 502, B success, then registry removal of B.
	// The old writer keeps B's error on A's aggregate deadline. Only timestamps
	// are rebased below; do not replace this with a hand-built aggregate record.
	// The quota fixture is also byte-identical to a genuine credential-level
	// forced failure following A's quota: the old format cannot identify scope.
	data, errRead := os.ReadFile(filepath.Join("testdata", fixture))
	if errRead != nil {
		t.Fatal(errRead)
	}
	var document map[string]any
	if errDecode := json.Unmarshal(data, &document); errDecode != nil {
		t.Fatal(errDecode)
	}
	anchor, errTime := time.Parse(time.RFC3339Nano, document["updated_at"].(string))
	if errTime != nil {
		t.Fatal(errTime)
	}
	shiftCooldownFixtureTimes(t, document, time.Now().UTC().Sub(anchor))
	data, errEncode := json.Marshal(document)
	if errEncode != nil {
		t.Fatal(errEncode)
	}
	dir := t.TempDir()
	if errWrite := os.WriteFile(filepath.Join(dir, "legacy.cds"), data, 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	store := NewFileCooldownStateStore(dir)
	records, errLoad := store.Load(ctx)
	if errLoad != nil || len(records) != 2 {
		t.Fatalf("load captured legacy records: count=%d error=%v", len(records), errLoad)
	}
	deadline := records[0].NextRetryAfter
	quota := records[0].Quota
	// The first restore reads the old file, the second reads the new writer's
	// migrated file. Neither may promote the stale error into a forced action.
	for restart := 0; restart < 2; restart++ {
		m, auth := newCooldownMonotonicManager(t, "ordinary-a", "unrelated-c")
		m.SetCooldownStateStore(store)
		if errRestore := m.RestoreCooldownStates(ctx); errRestore != nil {
			t.Fatal(errRestore)
		}
		snapshot, _ := m.GetByID(auth.ID)
		if !snapshot.ForcedCooldownUntil.IsZero() || !snapshot.ModelStates["ordinary-a"].ForcedCooldownUntil.IsZero() {
			t.Error("legacy error history was promoted into a forced action")
		}
		if !snapshot.NextRetryAfter.Equal(deadline) || !snapshot.ModelStates["ordinary-a"].NextRetryAfter.Equal(deadline) {
			t.Error("migration discarded the ordinary cooldown deadline")
		}
		if !cooldownQuotaEqual(snapshot.Quota, quota) || !cooldownQuotaEqual(snapshot.ModelStates["ordinary-a"].Quota, quota) {
			t.Error("migration changed the legacy quota state")
		}
		if blocked, _, _ := isAuthBlockedForModel(snapshot, "ordinary-a", time.Now()); !blocked {
			t.Error("migration cleared the ordinary model cooldown before recovery")
		}
		if picked, errPick := m.scheduler.pickSingle(ctx, auth.Provider, "unrelated-c", cliproxyexecutor.Options{}, nil); errPick != nil || picked == nil {
			t.Errorf("migration blocked an unrelated model: %v", errPick)
		}
		records, errLoad = store.Load(ctx)
		if errLoad != nil || len(records) != 2 {
			t.Fatalf("migration dropped persisted ordinary cooldowns: count=%d error=%v", len(records), errLoad)
		}
		if restart == 1 {
			m.MarkResult(ctx, Result{AuthID: auth.ID, Provider: auth.Provider, Model: "ordinary-a", Success: true})
			snapshot, _ = m.GetByID(auth.ID)
			for _, model := range []string{"ordinary-a", "unrelated-c", ""} {
				if blocked, _, _ := isAuthBlockedForModel(snapshot, model, time.Now()); blocked {
					t.Errorf("legacy ordinary recovery still blocks model=%q", model)
				}
			}
			records, errLoad = store.Load(ctx)
			if errLoad != nil || len(records) != 0 {
				t.Errorf("ordinary recovery did not remove persisted cooldowns: count=%d error=%v", len(records), errLoad)
			}
		}
	}
}

func shiftCooldownFixtureTimes(t *testing.T, value any, delta time.Duration) {
	t.Helper()
	switch current := value.(type) {
	case []any:
		for _, item := range current {
			shiftCooldownFixtureTimes(t, item, delta)
		}
	case map[string]any:
		for key, item := range current {
			switch key {
			case "next_retry_after", "next_recover_at", "updated_at", "observed_at":
				stamp, errTime := time.Parse(time.RFC3339Nano, item.(string))
				if errTime != nil {
					t.Fatal(errTime)
				}
				if !stamp.IsZero() {
					current[key] = stamp.Add(delta).Format(time.RFC3339Nano)
				}
			default:
				shiftCooldownFixtureTimes(t, item, delta)
			}
		}
	}
}

// Keep legacy fixtures genuinely field-absent even after the new writer starts
// serializing an explicit zero. Current records must use the real store writer.
func writeLegacyCooldownRecords(t *testing.T, dir string, records []CooldownStateRecord) {
	t.Helper()
	for _, record := range records {
		if !record.ForcedCooldownUntil.IsZero() || record.LastFailureScope != "" {
			t.Fatal("cannot encode explicit cooldown provenance as a legacy fixture")
		}
	}
	data, errEncode := json.Marshal(records)
	if errEncode != nil {
		t.Fatal(errEncode)
	}
	var raw []map[string]json.RawMessage
	if errDecode := json.Unmarshal(data, &raw); errDecode != nil {
		t.Fatal(errDecode)
	}
	for _, record := range raw {
		delete(record, "forced_cooldown_until")
	}
	data, errEncode = json.Marshal(struct {
		Version int                          `json:"version"`
		Records []map[string]json.RawMessage `json:"records"`
	}{Version: 1, Records: raw})
	if errEncode != nil {
		t.Fatal(errEncode)
	}
	if errWrite := os.WriteFile(filepath.Join(dir, "legacy.cds"), data, 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
}

func TestManager_ForcedCooldownExplicitWireDeadline(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	SetQuotaCooldownDisabled(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })
	for _, scope := range []string{"model", "credential"} {
		for _, deadlineKind := range []string{"zero", "expired", "active"} {
			for _, errorKind := range []string{"forced", "ordinary", "absent"} {
				t.Run(scope+"/"+deadlineKind+"/"+errorKind, func(t *testing.T) {
					ctx := context.Background()
					m, auth := newCooldownMonotonicManager(t, "wire-model", "other-model")
					m.MarkResult(ctx, Result{AuthID: auth.ID, Provider: auth.Provider, Model: "wire-model", Success: true})
					store := NewFileCooldownStateStore(t.TempDir())
					m.SetCooldownStateStore(store)
					now := time.Now().UTC()
					forced := time.Time{}
					if deadlineKind == "active" {
						forced = now.Add(10 * time.Minute)
					} else if deadlineKind == "expired" {
						forced = now.Add(-time.Minute)
					}
					record := CooldownStateRecord{AuthID: auth.ID, Provider: auth.Provider, NextRetryAfter: now.Add(2 * time.Hour), ForcedCooldownUntil: forced, UpdatedAt: now}
					if scope == "model" {
						record.Model = "wire-model"
					}
					if errorKind != "absent" {
						record.LastError = &Error{HTTPStatus: http.StatusNotFound, Message: "ordinary error"}
						if errorKind == "forced" {
							record.LastError = &Error{Code: ErrorCodeForceCooldown, HTTPStatus: http.StatusBadGateway}
						}
					}
					if errSave := store.Save(ctx, []CooldownStateRecord{record}); errSave != nil {
						t.Fatal(errSave)
					}
					data, errRead := os.ReadFile(filepath.Join(store.dir, auth.ID+".cds"))
					if errRead != nil {
						t.Fatal(errRead)
					}
					var wire struct {
						Records []map[string]json.RawMessage `json:"records"`
					}
					if errDecode := json.Unmarshal(data, &wire); errDecode != nil {
						t.Fatal(errDecode)
					}
					if len(wire.Records) != 1 || len(wire.Records[0]["forced_cooldown_until"]) == 0 {
						t.Fatal("writer omitted the explicit forced deadline, including its zero case")
					}
					if errRestore := m.RestoreCooldownStates(ctx); errRestore != nil {
						t.Fatal(errRestore)
					}
					state := forcedCooldownTestState(t, m, auth.ID, record.Model)
					if !state.ForcedCooldownUntil.Equal(forced) || !state.NextRetryAfter.Equal(record.NextRetryAfter) {
						t.Error("restore inferred or altered a deadline from diagnostic error history")
					}
					m.MarkResult(ctx, Result{AuthID: auth.ID, Provider: auth.Provider, Model: "wire-model", Success: true})
					state = forcedCooldownTestState(t, m, auth.ID, record.Model)
					if deadlineKind == "active" {
						if !state.ForcedCooldownUntil.Equal(forced) || !state.NextRetryAfter.Equal(forced) || !state.Unavailable {
							t.Error("success did not retain exactly the explicit active deadline")
						}
					} else if state.Unavailable || !state.NextRetryAfter.IsZero() || state.ForcedCooldownUntil.After(now) {
						t.Error("success retained a zero/expired forced action or ordinary deadline")
					}
					restarted, _ := newCooldownMonotonicManager(t, "wire-model", "other-model")
					restarted.SetCooldownStateStore(store)
					if errRestore := restarted.RestoreCooldownStates(ctx); errRestore != nil {
						t.Fatal(errRestore)
					}
					snapshot, _ := restarted.GetByID(auth.ID)
					for _, model := range []string{"wire-model", "other-model", ""} {
						wantBlocked := deadlineKind == "active" && (scope == "credential" || model != "other-model")
						if blocked, _, _ := isAuthBlockedForModel(snapshot, model, now); blocked != wantBlocked {
							t.Errorf("post-restart model=%q blocked=%v, want=%v", model, blocked, wantBlocked)
						}
						if blocked, _, _ := isAuthBlockedForModel(snapshot, model, now.Add(11*time.Minute)); blocked {
							t.Errorf("post-restart model=%q blocked beyond explicit deadline", model)
						}
					}
				})
			}
		}
	}
}
