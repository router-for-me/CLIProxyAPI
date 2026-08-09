package loguploader

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReadSupabaseHistoryLedgerReconcilesStatusesAndSplitObjects(t *testing.T) {
	t.Parallel()

	location := mustLocation(t, "Asia/Shanghai")
	workDir := t.TempDir()
	hour := time.Date(2026, time.July, 18, 1, 0, 0, 0, location)
	first := historyAuditFixture(hour, "first-object", providerCodex, "alice", 2, 100)
	second := historyAuditFixture(hour, "second-object", providerCodex, "bob", 3, 200)
	legacy := historyAuditFixture(hour.Add(time.Hour), "legacy-object", "", "legacy", 1, 50)

	writeHistoryAuditFile(t, filepath.Join(workDir, "history", "2026-07.jsonl"),
		withHistoryStatus(first, "uploaded_cleanup_pending"),
		withHistoryStatus(second, "uploaded_cleanup_pending"),
	)
	writeHistoryAuditFile(t, filepath.Join(workDir, "audit.jsonl"),
		withHistoryStatus(first, "uploaded"),
		withHistoryStatus(second, "uploaded_archive_delete_pending"),
		withHistoryStatus(legacy, "uploaded"),
	)

	ledger, errRead := readSupabaseHistoryLedger(workDir, location)
	if errRead != nil {
		t.Fatalf("read history ledger: %v", errRead)
	}
	if len(ledger.Records) != 3 {
		t.Fatalf("records = %d, want three unique objects", len(ledger.Records))
	}
	if ledger.Summary.DuplicateRecords != 2 {
		t.Fatalf("duplicate records = %d, want 2", ledger.Summary.DuplicateRecords)
	}
	if ledger.Records[0].ObjectKey != "first-object" || ledger.Records[1].ObjectKey != "second-object" {
		t.Fatalf("same-hour split objects were not retained in deterministic order: %#v", ledger.Records)
	}
	if ledger.Records[2].Provider != "" {
		t.Fatalf("legacy provider = %q, want empty for model classification", ledger.Records[2].Provider)
	}
	if ledger.Summary.SourceCount != 6 || ledger.Summary.SourceBytes != 350 {
		t.Fatalf("ledger source totals = %#v", ledger.Summary)
	}
}

func TestReadSupabaseHistoryLedgerRejectsConflictingObjectWithoutLeakingNames(t *testing.T) {
	t.Parallel()

	location := mustLocation(t, "Asia/Shanghai")
	workDir := t.TempDir()
	hour := time.Date(2026, time.July, 18, 1, 0, 0, 0, location)
	first := historyAuditFixture(hour, "private/object/path", providerCodex, "sensitive-person", 1, 100)
	conflict := historyAuditFixture(hour, "private/object/path", providerCodex, "sensitive-person", 1, 101)
	writeHistoryAuditFile(t, filepath.Join(workDir, "audit.jsonl"), first, conflict)

	_, errRead := readSupabaseHistoryLedger(workDir, location)
	if errRead == nil || !strings.Contains(errRead.Error(), "conflicting successful records") {
		t.Fatalf("conflicting object error = %v", errRead)
	}
	for _, secret := range []string{"private/object/path", "sensitive-person"} {
		if strings.Contains(errRead.Error(), secret) {
			t.Fatalf("history error leaked %q: %v", secret, errRead)
		}
	}
}

func TestReadSupabaseHistoryLedgerRejectsConflictingManagedEventIDs(t *testing.T) {
	t.Parallel()

	location := mustLocation(t, "Asia/Shanghai")
	workDir := t.TempDir()
	hour := time.Date(2026, time.July, 23, 1, 0, 0, 0, location)
	first := historyAuditFixture(hour, "private-managed-object", providerCodex, "private-name", 1, 100)
	first.SupabaseEventID = "cliproxy-v1." + strings.Repeat("d", 64)
	second := withHistoryStatus(first, "uploaded_cleanup_pending")
	second.SupabaseEventID = "cliproxy-v1." + strings.Repeat("e", 64)
	writeHistoryAuditFile(t, filepath.Join(workDir, "audit.jsonl"), first, second)

	_, errRead := readSupabaseHistoryLedger(workDir, location)
	if errRead == nil || !strings.Contains(errRead.Error(), "conflicting successful records") {
		t.Fatalf("conflicting managed event error = %v", errRead)
	}
	for _, private := range []string{first.ObjectKey, "private-name", first.SupabaseEventID, second.SupabaseEventID} {
		if strings.Contains(errRead.Error(), private) {
			t.Fatalf("managed event conflict leaked %q: %v", private, errRead)
		}
	}
}

func TestReadSupabaseHistoryLedgerHandlesOnlyActiveTruncatedTail(t *testing.T) {
	t.Parallel()

	location := mustLocation(t, "Asia/Shanghai")
	hour := time.Date(2026, time.July, 18, 1, 0, 0, 0, location)
	record := historyAuditFixture(hour, "object", providerCodex, "alice", 1, 100)

	t.Run("active tail is ignored", func(t *testing.T) {
		workDir := t.TempDir()
		writeHistoryAuditFile(t, filepath.Join(workDir, "audit.jsonl"), record)
		file, errOpen := os.OpenFile(filepath.Join(workDir, "audit.jsonl"), os.O_WRONLY|os.O_APPEND, 0)
		if errOpen != nil {
			t.Fatalf("open active audit: %v", errOpen)
		}
		if _, errWrite := io.WriteString(file, `{"status":"uploaded"`); errWrite != nil {
			t.Fatalf("append truncated tail: %v", errWrite)
		}
		if errClose := file.Close(); errClose != nil {
			t.Fatalf("close active audit: %v", errClose)
		}
		ledger, errRead := readSupabaseHistoryLedger(workDir, location)
		if errRead != nil {
			t.Fatalf("read active audit: %v", errRead)
		}
		if len(ledger.Records) != 1 || ledger.Summary.TruncatedTails != 1 {
			t.Fatalf("active tail summary = %#v records=%d", ledger.Summary, len(ledger.Records))
		}
	})

	t.Run("history tail is rejected", func(t *testing.T) {
		workDir := t.TempDir()
		path := filepath.Join(workDir, "history", "old.jsonl")
		writeHistoryAuditFile(t, path, record)
		raw, errRead := os.ReadFile(path)
		if errRead != nil {
			t.Fatalf("read history fixture: %v", errRead)
		}
		if errWrite := os.WriteFile(path, bytes.TrimSuffix(raw, []byte("\n")), 0o640); errWrite != nil {
			t.Fatalf("truncate history newline: %v", errWrite)
		}
		_, errLedger := readSupabaseHistoryLedger(workDir, location)
		if errLedger == nil || !strings.Contains(errLedger.Error(), "incomplete final line") {
			t.Fatalf("history tail error = %v", errLedger)
		}
	})

	t.Run("complete active line without newline is conservatively ignored", func(t *testing.T) {
		workDir := t.TempDir()
		path := filepath.Join(workDir, "audit.jsonl")
		writeHistoryAuditFile(t, path, record)
		raw, errRead := os.ReadFile(path)
		if errRead != nil {
			t.Fatalf("read active fixture: %v", errRead)
		}
		if errWrite := os.WriteFile(path, bytes.TrimSuffix(raw, []byte("\n")), 0o640); errWrite != nil {
			t.Fatalf("remove active newline: %v", errWrite)
		}
		ledger, errLedger := readSupabaseHistoryLedger(workDir, location)
		if errLedger != nil {
			t.Fatalf("read active complete tail: %v", errLedger)
		}
		if len(ledger.Records) != 0 || ledger.Summary.TruncatedTails != 1 {
			t.Fatalf("active complete tail summary = %#v records=%d", ledger.Summary, len(ledger.Records))
		}
	})
}

func TestReadSupabaseHistoryLedgerRejectsMalformedAndInvalidTotalsWithoutLeakingData(t *testing.T) {
	t.Parallel()

	location := mustLocation(t, "Asia/Shanghai")
	hour := time.Date(2026, time.July, 18, 1, 0, 0, 0, location)
	valid := historyAuditFixture(hour, "valid-object", providerCodex, "alice", 1, 100)
	tests := []struct {
		name  string
		write func(t *testing.T, path string)
	}{
		{name: "malformed middle line", write: func(t *testing.T, path string) {
			writeHistoryAuditFile(t, path, valid)
			file, errOpen := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
			if errOpen != nil {
				t.Fatalf("open malformed fixture: %v", errOpen)
			}
			if _, errWrite := io.WriteString(file, `{"status":"uploaded","object_key":"private-object","key_names":{"private-name":`+"\n"); errWrite != nil {
				t.Fatalf("write malformed fixture: %v", errWrite)
			}
			if errClose := file.Close(); errClose != nil {
				t.Fatalf("close malformed fixture: %v", errClose)
			}
		}},
		{name: "negative total", write: func(t *testing.T, path string) {
			invalid := valid
			invalid.SourceBytes = -1
			writeHistoryAuditFile(t, path, invalid)
		}},
		{name: "unsafe integer", write: func(t *testing.T, path string) {
			invalid := valid
			invalid.JSONLBytes = maxSafeJSONInteger + 1
			writeHistoryAuditFile(t, path, invalid)
		}},
		{name: "usage mismatch", write: func(t *testing.T, path string) {
			invalid := valid
			invalid.SourceCount++
			writeHistoryAuditFile(t, path, invalid)
		}},
		{name: "unsupported provider", write: func(t *testing.T, path string) {
			invalid := valid
			invalid.Provider = "private-provider"
			writeHistoryAuditFile(t, path, invalid)
		}},
		{name: "legacy model source count mismatch", write: func(t *testing.T, path string) {
			invalid := historyAuditFixture(hour, "private-object", "", "private-name", 1, 100)
			invalid.KeyNames["private-name"] = auditKeyNameSummary{
				SourceCount: 1,
				SourceBytes: 100,
				Models: map[string]auditModelSummary{
					"private-model": {SourceCount: 0, SourceBytes: 100},
				},
			}
			writeHistoryAuditFile(t, path, invalid)
		}},
		{name: "legacy model source bytes mismatch", write: func(t *testing.T, path string) {
			invalid := historyAuditFixture(hour, "private-object", "", "private-name", 1, 100)
			invalid.KeyNames["private-name"] = auditKeyNameSummary{
				SourceCount: 1,
				SourceBytes: 100,
				Models: map[string]auditModelSummary{
					"private-model": {SourceCount: 1, SourceBytes: 99},
				},
			}
			writeHistoryAuditFile(t, path, invalid)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workDir := t.TempDir()
			test.write(t, filepath.Join(workDir, "audit.jsonl"))
			_, errRead := readSupabaseHistoryLedger(workDir, location)
			if errRead == nil {
				t.Fatal("invalid history ledger was accepted")
			}
			for _, private := range []string{"private-object", "private-name", "private-model", "private-provider", `{"status"`} {
				if strings.Contains(errRead.Error(), private) {
					t.Fatalf("history error leaked private data %q: %v", private, errRead)
				}
			}
		})
	}
}

func TestReadSupabaseHistoryLedgerAcceptsModernInformationalModelTotals(t *testing.T) {
	t.Parallel()

	location := mustLocation(t, "Asia/Shanghai")
	workDir := t.TempDir()
	hour := time.Date(2026, time.July, 23, 1, 0, 0, 0, location)
	record := historyAuditFixture(hour, "modern-object", providerCodex, "alice", 1, 100)
	record.KeyNames["alice"] = auditKeyNameSummary{
		SourceCount: 1,
		SourceBytes: 100,
		Models: map[string]auditModelSummary{
			"gpt-5.6-sol":   {SourceBytes: maxSafeJSONInteger},
			"gpt-5.6-terra": {SourceBytes: 1},
		},
	}
	writeHistoryAuditFile(t, filepath.Join(workDir, "audit.jsonl"), record)

	ledger, errRead := readSupabaseHistoryLedger(workDir, location)
	if errRead != nil {
		t.Fatalf("read modern history with informational model totals: %v", errRead)
	}
	if len(ledger.Records) != 1 || ledger.Records[0].Provider != providerCodex {
		t.Fatalf("modern history ledger = %#v", ledger.Records)
	}
}

func TestReadSupabaseHistoryLedgerRejectsUnsafeFilesAndBounds(t *testing.T) {
	t.Parallel()

	location := mustLocation(t, "Asia/Shanghai")

	t.Run("oversized audit", func(t *testing.T) {
		workDir := t.TempDir()
		path := filepath.Join(workDir, "audit.jsonl")
		if errWrite := os.WriteFile(path, nil, 0o640); errWrite != nil {
			t.Fatalf("create audit: %v", errWrite)
		}
		if errTruncate := os.Truncate(path, maxSupabaseHistoryAuditBytes+1); errTruncate != nil {
			t.Fatalf("create sparse oversized audit: %v", errTruncate)
		}
		_, errRead := readSupabaseHistoryLedger(workDir, location)
		if errRead == nil || !strings.Contains(errRead.Error(), "safe size limit") {
			t.Fatalf("oversized audit error = %v", errRead)
		}
	})

	t.Run("history symlink", func(t *testing.T) {
		workDir := t.TempDir()
		target := filepath.Join(workDir, "target.jsonl")
		writeHistoryAuditFile(t, target, historyAuditFixture(time.Now(), "object", providerCodex, "alice", 1, 1))
		link := filepath.Join(workDir, "history", "linked.jsonl")
		if errMkdir := os.MkdirAll(filepath.Dir(link), 0o750); errMkdir != nil {
			t.Fatalf("create history directory: %v", errMkdir)
		}
		if errLink := os.Symlink(target, link); errLink != nil {
			t.Skipf("symlink unavailable: %v", errLink)
		}
		_, errRead := readSupabaseHistoryLedger(workDir, location)
		if errRead == nil || !strings.Contains(errRead.Error(), "symbolic link") {
			t.Fatalf("history symlink error = %v", errRead)
		}
	})
}

func TestReadSupabaseHistoryAuditFileRejectsMutationAfterPreflight(t *testing.T) {
	t.Parallel()

	location := mustLocation(t, "Asia/Shanghai")
	path := filepath.Join(t.TempDir(), "history.jsonl")
	writeHistoryAuditFile(t, path, historyAuditFixture(time.Now().In(location).Truncate(time.Hour), "object", providerCodex, "alice", 1, 1))
	info, errStat := os.Stat(path)
	if errStat != nil {
		t.Fatalf("stat history fixture: %v", errStat)
	}
	file, errOpen := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if errOpen != nil {
		t.Fatalf("open history fixture: %v", errOpen)
	}
	if _, errWrite := io.WriteString(file, "\n"); errWrite != nil {
		t.Fatalf("mutate history fixture: %v", errWrite)
	}
	if errClose := file.Close(); errClose != nil {
		t.Fatalf("close history fixture: %v", errClose)
	}

	_, _, errRead := readSupabaseHistoryAuditFile(path, false, info.Size(), location)
	if errRead == nil || !strings.Contains(errRead.Error(), "changed after preflight") {
		t.Fatalf("mutated history error = %v", errRead)
	}
}

func TestSyncSupabaseHistoryDryRunPreflightsWithoutWritesOrNetwork(t *testing.T) {
	service, state, record := newSupabaseHistoryTestFixture(t, "https://one.supabase.co/functions/v1/ingest")
	oldDestinationID, errDestination := supabaseDestinationID("https://old.supabase.co/functions/v1/ingest")
	if errDestination != nil {
		t.Fatalf("calculate old history destination: %v", errDestination)
	}
	oldCheckpoint := supabaseHistoryCheckpoint{
		DestinationID: oldDestinationID,
		ObjectKey:     record.ObjectKey,
		ArchiveSHA256: state.Objects[record.ObjectKey].ArchiveSHA256,
		EventID:       "cliproxy-v1." + strings.Repeat("c", 64),
		CommittedAt:   service.now(),
	}
	state.SupabaseHistory[supabaseHistoryCheckpointKey(oldDestinationID, record.ObjectKey)] = oldCheckpoint
	writeHistoryAuditFile(t, filepath.Join(service.cfg.WorkDir, "audit.jsonl"), record)
	if errSave := service.saveState(state); errSave != nil {
		t.Fatalf("save initial state: %v", errSave)
	}
	before, errRead := os.ReadFile(service.statePath())
	if errRead != nil {
		t.Fatalf("read initial state: %v", errRead)
	}
	requests := 0
	service.supabaseHTTPDoer = httpDoerFunc(func(*http.Request) (*http.Response, error) {
		requests++
		return nil, errors.New("network must not be used")
	})

	summary, errSync := service.SyncSupabaseHistory(context.Background(), true)
	if errSync != nil {
		t.Fatalf("dry-run history sync: %v", errSync)
	}
	after, errRead := os.ReadFile(service.statePath())
	if errRead != nil {
		t.Fatalf("read final state: %v", errRead)
	}
	if requests != 0 || !bytes.Equal(before, after) {
		t.Fatalf("dry-run requests=%d state_changed=%t", requests, !bytes.Equal(before, after))
	}
	sharedLockPath, errSharedPath := service.sharedResourceLockPath()
	if errSharedPath != nil {
		t.Fatalf("resolve shared lock path: %v", errSharedPath)
	}
	for _, lockPath := range []string{filepath.Join(service.cfg.WorkDir, "service.lock"), sharedLockPath} {
		if _, errStat := os.Stat(lockPath); !errors.Is(errStat, os.ErrNotExist) {
			t.Fatalf("dry-run created lock file %s: %v", filepath.Base(lockPath), errStat)
		}
	}
	if summary.Pending != 1 || summary.SourceBytes != 100 || summary.BatchJSONLBytes != 150 {
		t.Fatalf("dry-run summary = %#v", summary)
	}
}

func TestSyncSupabaseHistoryDoesNotDependOnLogsRoot(t *testing.T) {
	service, state, record := newSupabaseHistoryTestFixture(t, "https://one.supabase.co/functions/v1/ingest")
	writeHistoryAuditFile(t, filepath.Join(service.cfg.WorkDir, "audit.jsonl"), record)
	if errSave := service.saveState(state); errSave != nil {
		t.Fatalf("save initial state: %v", errSave)
	}
	blockedParent := filepath.Join(t.TempDir(), "not-a-directory")
	if errWrite := os.WriteFile(blockedParent, []byte("block logs root parent"), 0o600); errWrite != nil {
		t.Fatalf("write logs root parent blocker: %v", errWrite)
	}
	service.cfg.LogsRoot = filepath.Join(blockedParent, "missing-logs-root")
	t.Setenv(service.cfg.Supabase.IngestTokenEnv, "history-token")
	requests := 0
	service.supabaseHTTPDoer = historyAcknowledgingDoer(t, &requests, "inserted")

	summary, errSync := service.SyncSupabaseHistory(context.Background(), false)
	if errSync != nil {
		t.Fatalf("sync history with unavailable logs root: %v", errSync)
	}
	if requests != 1 || summary.Inserted != 1 || summary.Checkpointed != 1 {
		t.Fatalf("unavailable logs root summary=%#v requests=%d", summary, requests)
	}
}

func TestSyncSupabaseHistoryRejectsUploaderUsingSameWorkDir(t *testing.T) {
	service, _, _ := newSupabaseHistoryTestFixture(t, "https://one.supabase.co/functions/v1/ingest")
	holder, errLock := service.acquireProcessLock()
	if errLock != nil {
		t.Fatalf("acquire resident uploader lock: %v", errLock)
	}
	defer func() {
		if errClose := holder.Close(); errClose != nil {
			t.Errorf("release resident uploader lock: %v", errClose)
		}
	}()

	_, errSync := service.SyncSupabaseHistory(context.Background(), false)
	if errSync == nil || !strings.Contains(errSync.Error(), "already using this work directory") {
		t.Fatalf("same work directory history sync error = %v", errSync)
	}
}

func TestSyncSupabaseHistoryBackfillsAuditEventIDWithoutDurableHourMarker(t *testing.T) {
	eventID := "cliproxy-v1." + strings.Repeat("d", 64)
	tests := []struct {
		name    string
		records func(auditRecord) []auditRecord
	}{
		{
			name: "nonempty audit event ID",
			records: func(record auditRecord) []auditRecord {
				record.SupabaseEventID = eventID
				return []auditRecord{record}
			},
		},
		{
			name: "mixed audit event IDs",
			records: func(record auditRecord) []auditRecord {
				managed := withHistoryStatus(record, "uploaded_cleanup_pending")
				managed.SupabaseEventID = eventID
				completed := withHistoryStatus(record, "uploaded")
				completed.Timestamp = managed.Timestamp.Add(time.Second)
				return []auditRecord{managed, completed}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, state, record := newSupabaseHistoryTestFixture(t, "https://one.supabase.co/functions/v1/ingest")
			writeHistoryAuditFile(t, filepath.Join(service.cfg.WorkDir, "audit.jsonl"), test.records(record)...)
			if errSave := service.saveState(state); errSave != nil {
				t.Fatalf("save initial state: %v", errSave)
			}
			t.Setenv(service.cfg.Supabase.IngestTokenEnv, "history-token")
			requests := 0
			service.supabaseHTTPDoer = historyAcknowledgingDoer(t, &requests, "inserted")

			summary, errSync := service.SyncSupabaseHistory(context.Background(), false)
			if errSync != nil {
				t.Fatalf("backfill audit-only event ID: %v", errSync)
			}
			if requests != 1 || summary.LiveManaged != 0 || summary.Pending != 1 || summary.Inserted != 1 || summary.Checkpointed != 1 {
				t.Fatalf("audit-only event ID summary=%#v requests=%d", summary, requests)
			}
		})
	}
}

func TestSyncSupabaseHistoryUsesDurableHourMarkerForLiveManaged(t *testing.T) {
	eventID := "cliproxy-v1." + strings.Repeat("d", 64)
	for _, auditEventID := range []string{"", eventID} {
		name := "empty audit event ID"
		if auditEventID != "" {
			name = "matching audit event ID"
		}
		t.Run(name, func(t *testing.T) {
			service, state, record := newSupabaseHistoryTestFixture(t, "https://one.supabase.co/functions/v1/ingest")
			hourKey := hourStateKey(record.Hour, record.Provider)
			state = withUploadedHourSupabaseEventID(t, state, hourKey, eventID)
			record.SupabaseEventID = auditEventID
			writeHistoryAuditFile(t, filepath.Join(service.cfg.WorkDir, "audit.jsonl"), record)
			if errSave := service.saveState(state); errSave != nil {
				t.Fatalf("save live-managed state: %v", errSave)
			}
			requests := 0
			service.supabaseHTTPDoer = httpDoerFunc(func(*http.Request) (*http.Response, error) {
				requests++
				return nil, errors.New("unexpected request")
			})

			summary, errSync := service.SyncSupabaseHistory(context.Background(), false)
			if errSync != nil {
				t.Fatalf("sync durable live-managed history: %v", errSync)
			}
			if requests != 0 || summary.LiveManaged != 1 || summary.Pending != 0 {
				t.Fatalf("durable live-managed summary=%#v requests=%d", summary, requests)
			}
		})
	}
}

func TestSyncSupabaseHistoryRejectsConflictingDurableHourMarkerBeforeAnyWriteOrNetwork(t *testing.T) {
	service, state, record := newSupabaseHistoryTestFixture(t, "https://one.supabase.co/functions/v1/ingest")
	hourKey := hourStateKey(record.Hour, record.Provider)
	state = withUploadedHourSupabaseEventID(t, state, hourKey, "cliproxy-v1."+strings.Repeat("d", 64))
	record.SupabaseEventID = "cliproxy-v1." + strings.Repeat("e", 64)
	writeHistoryAuditFile(t, filepath.Join(service.cfg.WorkDir, "audit.jsonl"), record)
	if errSave := service.saveState(state); errSave != nil {
		t.Fatalf("save conflicting marker state: %v", errSave)
	}
	before, errRead := os.ReadFile(service.statePath())
	if errRead != nil {
		t.Fatalf("read state before conflicting marker preflight: %v", errRead)
	}
	t.Setenv(service.cfg.Supabase.IngestTokenEnv, "history-token")
	requests := 0
	service.supabaseHTTPDoer = historyAcknowledgingDoer(t, &requests, "inserted")
	stateWrites := 0
	service.syncStateParentDirectory = func(string) error {
		stateWrites++
		return nil
	}

	_, errSync := service.SyncSupabaseHistory(context.Background(), false)
	if errSync == nil || !strings.Contains(errSync.Error(), "trusted upload state") {
		t.Fatalf("conflicting durable marker error = %v", errSync)
	}
	after, errRead := os.ReadFile(service.statePath())
	if errRead != nil {
		t.Fatalf("read state after conflicting marker preflight: %v", errRead)
	}
	if requests != 0 || stateWrites != 0 || !bytes.Equal(before, after) {
		t.Fatalf("conflicting marker requests=%d state_writes=%d state_changed=%t", requests, stateWrites, !bytes.Equal(before, after))
	}
}

func TestPreflightSupabaseHistoryClassifiesLegacyModelsByProvider(t *testing.T) {
	service, state, record := newSupabaseHistoryTestFixture(t, "https://one.supabase.co/functions/v1/ingest")
	record.Provider = ""
	record.SourceCount = 6
	record.SourceBytes = 600
	record.JSONLBytes = 650
	record.CompressedBytes = 300
	record.KeyNames = map[string]auditKeyNameSummary{
		"alice": {
			SourceCount: 6,
			SourceBytes: 600,
			Models: map[string]auditModelSummary{
				"gpt-5.6-sol":              {SourceCount: 1, SourceBytes: 100},
				"claude-sonnet-4-20250514": {SourceCount: 2, SourceBytes: 200},
				"xai/grok-4.5":             {SourceCount: 3, SourceBytes: 300},
			},
		},
	}
	object := state.Objects[record.ObjectKey]
	object.CompressedSize = record.CompressedBytes
	state.Objects[record.ObjectKey] = object
	writeHistoryAuditFile(t, filepath.Join(service.cfg.WorkDir, "audit.jsonl"), record)

	ledger, errLedger := readSupabaseHistoryLedger(service.cfg.WorkDir, service.location)
	if errLedger != nil {
		t.Fatalf("read legacy history ledger: %v", errLedger)
	}
	if len(ledger.Records) != 1 || ledger.Records[0].Provider != "" {
		t.Fatalf("normalized legacy record = %#v", ledger.Records)
	}
	destinationID, errDestination := supabaseDestinationID(service.cfg.Supabase.IngestURL)
	if errDestination != nil {
		t.Fatalf("calculate history destination: %v", errDestination)
	}
	uploads, _, errPreflight := service.preflightSupabaseHistory(state, ledger, destinationID)
	if errPreflight != nil {
		t.Fatalf("preflight legacy history: %v", errPreflight)
	}
	if len(uploads) != 1 {
		t.Fatalf("legacy history uploads = %d, want 1", len(uploads))
	}
	payload, errPayload := decodeSupabaseOutboxPayload(uploads[0].entry.Payload)
	if errPayload != nil {
		t.Fatalf("decode legacy history payload: %v", errPayload)
	}
	wantUsage := []supabaseEventUsage{
		{KeyName: "alice", Provider: providerCodex, SourceCount: 1, SourceBytes: 100},
		{KeyName: "alice", Provider: providerClaude, SourceCount: 2, SourceBytes: 200},
		{KeyName: "alice", Provider: providerGrok, SourceCount: 3, SourceBytes: 300},
	}
	if len(payload.Usage) != len(wantUsage) {
		t.Fatalf("legacy usage rows = %#v", payload.Usage)
	}
	for index, want := range wantUsage {
		got := payload.Usage[index]
		if got.KeyName != want.KeyName || got.Provider != want.Provider || got.SourceCount != want.SourceCount ||
			got.SourceBytes != want.SourceBytes || got.JSONLBytes != nil {
			t.Fatalf("legacy usage[%d] = %#v, want %#v with nil jsonl_bytes", index, got, want)
		}
	}
	state.SupabaseOutbox.Entries[uploads[0].entry.EventID] = uploads[0].entry
	if errValidate := service.validateUploadState(&state); errValidate != nil {
		t.Fatalf("validate mixed-provider batch-only history outbox: %v", errValidate)
	}
}

func TestSyncSupabaseHistoryRejectsModernProviderStateMismatchBeforeNetwork(t *testing.T) {
	service, state, record := newSupabaseHistoryTestFixture(t, "https://one.supabase.co/functions/v1/ingest")
	record.Provider = providerClaude
	record.KeyNames["alice"] = auditKeyNameSummary{
		SourceCount: 1,
		SourceBytes: 100,
		Models: map[string]auditModelSummary{
			"claude-sonnet-4-20250514": {SourceCount: 1, SourceBytes: 100},
		},
	}
	writeHistoryAuditFile(t, filepath.Join(service.cfg.WorkDir, "audit.jsonl"), record)
	if errSave := service.saveState(state); errSave != nil {
		t.Fatalf("save provider mismatch state: %v", errSave)
	}
	requests := 0
	service.supabaseHTTPDoer = httpDoerFunc(func(*http.Request) (*http.Response, error) {
		requests++
		return nil, errors.New("unexpected request")
	})

	_, errSync := service.SyncSupabaseHistory(context.Background(), false)
	if errSync == nil || !strings.Contains(errSync.Error(), "trusted upload state") {
		t.Fatalf("provider state mismatch error = %v", errSync)
	}
	if requests != 0 {
		t.Fatalf("provider mismatch sent %d requests", requests)
	}
}

func TestSyncSupabaseHistoryIsCheckpointedAndDestinationScoped(t *testing.T) {
	service, state, record := newSupabaseHistoryTestFixture(t, "https://one.supabase.co/functions/v1/ingest")
	writeHistoryAuditFile(t, filepath.Join(service.cfg.WorkDir, "audit.jsonl"), record)
	if errSave := service.saveState(state); errSave != nil {
		t.Fatalf("save initial state: %v", errSave)
	}
	t.Setenv(service.cfg.Supabase.IngestTokenEnv, "history-token")
	requests := 0
	service.supabaseHTTPDoer = historyAcknowledgingDoer(t, &requests, "inserted")

	first, errFirst := service.SyncSupabaseHistory(context.Background(), false)
	if errFirst != nil {
		t.Fatalf("first history sync: %v", errFirst)
	}
	if requests != 1 || first.Inserted != 1 || first.Checkpointed != 1 {
		t.Fatalf("first summary=%#v requests=%d", first, requests)
	}
	second, errSecond := service.SyncSupabaseHistory(context.Background(), false)
	if errSecond != nil {
		t.Fatalf("second history sync: %v", errSecond)
	}
	if requests != 1 || second.Pending != 0 || second.AlreadyCheckpointed != 1 {
		t.Fatalf("second summary=%#v requests=%d", second, requests)
	}

	service.cfg.Supabase.IngestURL = "https://two.supabase.co/functions/v1/ingest"
	third, errThird := service.SyncSupabaseHistory(context.Background(), false)
	if errThird != nil {
		t.Fatalf("destination history sync: %v", errThird)
	}
	if requests != 2 || third.Inserted != 1 {
		t.Fatalf("destination summary=%#v requests=%d", third, requests)
	}
	currentDestinationID, errDestination := supabaseDestinationID(service.cfg.Supabase.IngestURL)
	if errDestination != nil {
		t.Fatalf("calculate current history destination: %v", errDestination)
	}
	persisted, errLoad := service.loadState()
	if errLoad != nil {
		t.Fatalf("load destination-scoped history: %v", errLoad)
	}
	if len(persisted.SupabaseHistory) != 1 {
		t.Fatalf("destination-scoped checkpoints = %#v, want only current destination", persisted.SupabaseHistory)
	}
	for mapKey, checkpoint := range persisted.SupabaseHistory {
		if checkpoint.DestinationID != currentDestinationID || mapKey != supabaseHistoryCheckpointKey(currentDestinationID, checkpoint.ObjectKey) {
			t.Fatalf("retained checkpoint = key %s value %#v, want current destination %s", mapKey, checkpoint, currentDestinationID)
		}
	}
}

func TestSyncSupabaseHistoryRecoversAfterFailedDestinationSwitch(t *testing.T) {
	const (
		oldIngestURL = "https://one.supabase.co/functions/v1/ingest"
		badIngestURL = "https://wrong.supabase.co/functions/v1/ingest"
	)
	tests := []struct {
		name             string
		missingToken     bool
		statusCode       int
		wantBadRequests  int
		wantBadAttempted int
	}{
		{name: "missing token", missingToken: true},
		{name: "404", statusCode: http.StatusNotFound, wantBadRequests: 1, wantBadAttempted: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, state, record := newSupabaseHistoryTestFixture(t, oldIngestURL)
			writeHistoryAuditFile(t, filepath.Join(service.cfg.WorkDir, "audit.jsonl"), record)
			if errSave := service.saveState(state); errSave != nil {
				t.Fatalf("save initial state: %v", errSave)
			}
			t.Setenv(service.cfg.Supabase.IngestTokenEnv, "history-token")
			var exactPayload []byte
			var exactEventID string
			initialRequests := 0
			service.supabaseHTTPDoer = httpDoerFunc(func(request *http.Request) (*http.Response, error) {
				initialRequests++
				raw, errRead := io.ReadAll(request.Body)
				if errRead != nil {
					t.Fatalf("read initial history payload: %v", errRead)
				}
				payload, errPayload := decodeSupabaseOutboxPayload(raw)
				if errPayload != nil {
					t.Fatalf("decode initial history payload: %v", errPayload)
				}
				exactPayload = bytes.Clone(raw)
				exactEventID = payload.EventID
				body := fmt.Sprintf(`{"status":"inserted","event_id":%q}`, payload.EventID)
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			first, errFirst := service.SyncSupabaseHistory(context.Background(), false)
			if errFirst != nil || initialRequests != 1 || first.Inserted != 1 || first.Checkpointed != 1 {
				t.Fatalf("initial history sync = summary %#v requests %d error %v", first, initialRequests, errFirst)
			}

			service.cfg.Supabase.IngestURL = badIngestURL
			badRequests := 0
			if test.missingToken {
				t.Setenv(service.cfg.Supabase.IngestTokenEnv, " \t\n ")
				service.supabaseHTTPDoer = httpDoerFunc(func(*http.Request) (*http.Response, error) {
					badRequests++
					return nil, errors.New("request must not be sent without a token")
				})
			} else {
				service.supabaseHTTPDoer = httpDoerFunc(func(request *http.Request) (*http.Response, error) {
					badRequests++
					if request.URL.String() != badIngestURL {
						t.Fatalf("bad destination request URL = %q", request.URL.String())
					}
					raw, errRead := io.ReadAll(request.Body)
					if errRead != nil {
						t.Fatalf("read bad destination payload: %v", errRead)
					}
					if !bytes.Equal(raw, exactPayload) {
						t.Fatal("destination switch changed exact history payload")
					}
					return &http.Response{StatusCode: test.statusCode, Body: io.NopCloser(strings.NewReader("not found"))}, nil
				})
			}
			failed, errFailed := service.SyncSupabaseHistory(context.Background(), false)
			if !errors.Is(errFailed, errSupabaseDeliveryConfiguration) || badRequests != test.wantBadRequests ||
				failed.Attempted != test.wantBadAttempted || failed.Checkpointed != 0 {
				t.Fatalf("failed destination sync = summary %#v requests %d error %v", failed, badRequests, errFailed)
			}

			failedRaw, errRead := os.ReadFile(service.statePath())
			if errRead != nil {
				t.Fatalf("read failed destination state: %v", errRead)
			}
			var failedState uploadState
			if errUnmarshal := json.Unmarshal(failedRaw, &failedState); errUnmarshal != nil {
				t.Fatalf("decode failed destination state: %v", errUnmarshal)
			}
			badDestinationID, errDestination := supabaseDestinationID(badIngestURL)
			if errDestination != nil {
				t.Fatalf("calculate bad destination ID: %v", errDestination)
			}
			if failedState.SupabaseOutbox.DestinationID != badDestinationID || len(failedState.SupabaseOutbox.Entries) != 1 || len(failedState.SupabaseHistory) != 0 {
				t.Fatalf("failed destination durable state = outbox %#v history %#v", failedState.SupabaseOutbox, failedState.SupabaseHistory)
			}

			service.cfg.Supabase.IngestURL = oldIngestURL
			t.Setenv(service.cfg.Supabase.IngestTokenEnv, "history-token")
			retargeted, errLoad := service.loadState()
			if errLoad != nil {
				t.Fatalf("load corrected destination state: %v", errLoad)
			}
			oldDestinationID, errDestination := supabaseDestinationID(oldIngestURL)
			if errDestination != nil {
				t.Fatalf("calculate corrected destination ID: %v", errDestination)
			}
			entry, exists := retargeted.SupabaseOutbox.Entries[exactEventID]
			if !exists || retargeted.SupabaseOutbox.DestinationID != oldDestinationID || !retargeted.dirty ||
				entry.Status != supabaseOutboxStatusPending || entry.PayloadSHA256 != sha256Hex(exactPayload) || !bytes.Equal(entry.Payload, exactPayload) {
				t.Fatalf("corrected destination retarget = state %#v entry %#v dirty %t", retargeted.SupabaseOutbox, entry, retargeted.dirty)
			}
			afterLoadRaw, errRead := os.ReadFile(service.statePath())
			if errRead != nil {
				t.Fatalf("read state after corrected load: %v", errRead)
			}
			if !bytes.Equal(afterLoadRaw, failedRaw) {
				t.Fatal("corrected load persisted retarget before delivery")
			}

			recoveryRequests := 0
			service.supabaseHTTPDoer = httpDoerFunc(func(request *http.Request) (*http.Response, error) {
				recoveryRequests++
				if request.URL.String() != oldIngestURL {
					t.Fatalf("recovery request URL = %q", request.URL.String())
				}
				raw, errRead := io.ReadAll(request.Body)
				if errRead != nil {
					t.Fatalf("read recovery payload: %v", errRead)
				}
				if !bytes.Equal(raw, exactPayload) {
					t.Fatal("recovery changed exact history payload")
				}
				body := fmt.Sprintf(`{"status":"duplicate","event_id":%q}`, exactEventID)
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			recovered, errRecovered := service.SyncSupabaseHistory(context.Background(), false)
			if errRecovered != nil || recoveryRequests != 1 || recovered.Duplicate != 1 || recovered.Checkpointed != 1 {
				t.Fatalf("recovered history sync = summary %#v requests %d error %v", recovered, recoveryRequests, errRecovered)
			}
			finalState, errLoad := service.loadState()
			if errLoad != nil {
				t.Fatalf("load recovered destination state: %v", errLoad)
			}
			checkpointKey := supabaseHistoryCheckpointKey(oldDestinationID, record.ObjectKey)
			checkpoint, checkpointed := finalState.SupabaseHistory[checkpointKey]
			if len(finalState.SupabaseOutbox.Entries) != 0 || finalState.SupabaseOutbox.DestinationID != oldDestinationID ||
				len(finalState.SupabaseHistory) != 1 || !checkpointed || checkpoint.EventID != exactEventID || checkpoint.DestinationID != oldDestinationID {
				t.Fatalf("final recovered state = outbox %#v history %#v", finalState.SupabaseOutbox, finalState.SupabaseHistory)
			}
		})
	}
}

func TestSupabaseHistoryAcceptsMoreThanTenThousandCurrentDestinationCheckpoints(t *testing.T) {
	t.Parallel()

	service := newStateOutboxTestService(t, true)
	state := service.newUploadState()
	destinationID, errDestination := supabaseDestinationID(service.cfg.Supabase.IngestURL)
	if errDestination != nil {
		t.Fatalf("calculate history destination: %v", errDestination)
	}
	const checkpointCount = 10_001
	firstHour := time.Date(2025, time.January, 1, 0, 0, 0, 0, service.location)
	var lastKey string
	for index := 0; index < checkpointCount; index++ {
		objectKey := fmt.Sprintf("cliproxy-logs/history/%05d", index)
		archiveSHA256 := fmt.Sprintf("%064x", index+1)
		eventID := fmt.Sprintf("cliproxy-v1.%064x", index+1)
		hour := firstHour.Add(time.Duration(index) * time.Hour)
		checkpoint := supabaseHistoryCheckpoint{
			DestinationID: destinationID,
			ObjectKey:     objectKey,
			ArchiveSHA256: archiveSHA256,
			EventID:       eventID,
			CommittedAt:   service.now(),
		}
		lastKey = supabaseHistoryCheckpointKey(destinationID, objectKey)
		state.Objects[objectKey] = uploadedObject{
			ObjectKey:      objectKey,
			CompressedSize: 1,
			ArchiveSHA256:  archiveSHA256,
			Verification:   "put-success-or-remote-head-match",
			UploadedAt:     service.now(),
			VerifiedAt:     service.now(),
		}
		state.Hours[hourStateKey(hour, providerCodex)] = uploadedHour{
			Status:         "sealed",
			ObjectKey:      objectKey,
			ArchiveSHA256:  archiveSHA256,
			ManifestSHA256: strings.Repeat("b", 64),
			UploadedAt:     service.now(),
		}
		state.SupabaseHistory[lastKey] = checkpoint
	}

	if errValidate := service.validateUploadState(&state); errValidate != nil {
		t.Fatalf("validate %d current-destination checkpoints: %v", checkpointCount, errValidate)
	}
	rawState, errMarshal := json.MarshalIndent(state, "", "  ")
	if errMarshal != nil {
		t.Fatalf("marshal %d current-destination checkpoints: %v", checkpointCount, errMarshal)
	}
	if int64(len(rawState)) > maxUploadStateBytes {
		t.Fatalf("checkpoint fixture unexpectedly exceeds the 128 MiB state limit: %d bytes", len(rawState))
	}
	if _, _, errPreflight := service.preflightSupabaseHistory(state, supabaseHistoryLedger{}, destinationID); errPreflight != nil {
		t.Fatalf("preflight %d current-destination checkpoints: %v", checkpointCount, errPreflight)
	}

	invalid := state.SupabaseHistory[lastKey]
	invalid.EventID = "invalid-event-id"
	state.SupabaseHistory[lastKey] = invalid
	if errValidate := service.validateUploadState(&state); errValidate == nil || !strings.Contains(errValidate.Error(), "invalid event ID") {
		t.Fatalf("invalid checkpoint beyond 10,000 was not fully validated: %v", errValidate)
	}
}

func TestSyncSupabaseHistoryUsesDuplicateAckAfterCheckpointSaveFailure(t *testing.T) {
	service, state, record := newSupabaseHistoryTestFixture(t, "https://one.supabase.co/functions/v1/ingest")
	writeHistoryAuditFile(t, filepath.Join(service.cfg.WorkDir, "audit.jsonl"), record)
	if errSave := service.saveState(state); errSave != nil {
		t.Fatalf("save initial state: %v", errSave)
	}
	t.Setenv(service.cfg.Supabase.IngestTokenEnv, "history-token")
	requests := 0
	service.supabaseHTTPDoer = historyAcknowledgingDoer(t, &requests, "inserted")
	syncCalls := 0
	service.syncStateParentDirectory = func(string) error {
		syncCalls++
		if syncCalls == 3 {
			return errors.New("forced checkpoint directory sync failure")
		}
		return nil
	}

	_, errFirst := service.SyncSupabaseHistory(context.Background(), false)
	if !errors.Is(errFirst, errSupabaseDeliveryState) {
		t.Fatalf("checkpoint save failure = %v", errFirst)
	}
	service.syncStateParentDirectory = syncParentDirectory
	service.supabaseHTTPDoer = historyAcknowledgingDoer(t, &requests, "duplicate")
	second, errSecond := service.SyncSupabaseHistory(context.Background(), false)
	if errSecond != nil {
		t.Fatalf("rerun after checkpoint failure: %v", errSecond)
	}
	if requests != 2 || second.Duplicate != 1 || second.Checkpointed != 1 {
		t.Fatalf("rerun summary=%#v requests=%d", second, requests)
	}
}

func TestSyncSupabaseHistoryRetriesBlockedExactPayloadOnLaterRun(t *testing.T) {
	service, state, record := newSupabaseHistoryTestFixture(t, "https://one.supabase.co/functions/v1/ingest")
	writeHistoryAuditFile(t, filepath.Join(service.cfg.WorkDir, "audit.jsonl"), record)
	if errSave := service.saveState(state); errSave != nil {
		t.Fatalf("save initial state: %v", errSave)
	}
	t.Setenv(service.cfg.Supabase.IngestTokenEnv, "history-token")
	var firstPayload []byte
	service.supabaseHTTPDoer = httpDoerFunc(func(request *http.Request) (*http.Response, error) {
		var errRead error
		firstPayload, errRead = io.ReadAll(request.Body)
		if errRead != nil {
			t.Fatalf("read first blocked history payload: %v", errRead)
		}
		return &http.Response{StatusCode: http.StatusUnprocessableEntity, Body: io.NopCloser(strings.NewReader("blocked"))}, nil
	})

	first, errFirst := service.SyncSupabaseHistory(context.Background(), false)
	if !errors.Is(errFirst, errSupabaseDeliveryBlocked) || first.Attempted != 1 || first.Checkpointed != 0 {
		t.Fatalf("first blocked sync = summary %#v, error %v", first, errFirst)
	}
	blockedState, errLoad := service.loadState()
	if errLoad != nil {
		t.Fatalf("load blocked history state: %v", errLoad)
	}
	if len(blockedState.SupabaseOutbox.Entries) != 1 {
		t.Fatalf("blocked history outbox = %#v", blockedState.SupabaseOutbox.Entries)
	}
	for _, entry := range blockedState.SupabaseOutbox.Entries {
		if entry.Status != supabaseOutboxStatusBlocked || !bytes.Equal(entry.Payload, firstPayload) {
			t.Fatalf("blocked history entry = %#v", entry)
		}
	}

	service.supabaseHTTPDoer = httpDoerFunc(func(request *http.Request) (*http.Response, error) {
		raw, errRead := io.ReadAll(request.Body)
		if errRead != nil {
			t.Fatalf("read retried history payload: %v", errRead)
		}
		if !bytes.Equal(raw, firstPayload) {
			t.Fatal("history retry changed exact payload bytes")
		}
		payload, errPayload := decodeSupabaseOutboxPayload(raw)
		if errPayload != nil {
			t.Fatalf("decode retried history payload: %v", errPayload)
		}
		body := fmt.Sprintf(`{"status":"duplicate","event_id":%q}`, payload.EventID)
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	second, errSecond := service.SyncSupabaseHistory(context.Background(), false)
	if errSecond != nil || second.Attempted != 1 || second.Duplicate != 1 || second.Checkpointed != 1 {
		t.Fatalf("second recovered sync = summary %#v, error %v", second, errSecond)
	}
	recoveredState, errLoad := service.loadState()
	if errLoad != nil {
		t.Fatalf("load recovered history state: %v", errLoad)
	}
	if len(recoveredState.SupabaseOutbox.Entries) != 0 || len(recoveredState.SupabaseHistory) != 1 {
		t.Fatalf("recovered history state = outbox %#v checkpoints %#v", recoveredState.SupabaseOutbox.Entries, recoveredState.SupabaseHistory)
	}
}

func TestSyncSupabaseHistoryPreflightsEverythingBeforeNetwork(t *testing.T) {
	service, state, record := newSupabaseHistoryTestFixture(t, "https://one.supabase.co/functions/v1/ingest")
	bad := record
	bad.ObjectKey = "missing-state-object"
	bad.KeyNames = map[string]auditKeyNameSummary{"do-not-leak": {SourceCount: 1, SourceBytes: 100}}
	writeHistoryAuditFile(t, filepath.Join(service.cfg.WorkDir, "audit.jsonl"), record, bad)
	if errSave := service.saveState(state); errSave != nil {
		t.Fatalf("save initial state: %v", errSave)
	}
	t.Setenv(service.cfg.Supabase.IngestTokenEnv, "history-token")
	requests := 0
	service.supabaseHTTPDoer = httpDoerFunc(func(*http.Request) (*http.Response, error) {
		requests++
		return nil, errors.New("unexpected request")
	})

	_, errSync := service.SyncSupabaseHistory(context.Background(), false)
	if errSync == nil || !strings.Contains(errSync.Error(), "trusted upload state") {
		t.Fatalf("state mismatch error = %v", errSync)
	}
	if requests != 0 {
		t.Fatalf("preflight sent %d requests", requests)
	}
	if strings.Contains(errSync.Error(), "missing-state-object") || strings.Contains(errSync.Error(), "do-not-leak") {
		t.Fatalf("preflight error leaked private data: %v", errSync)
	}
}

func TestSyncSupabaseHistoryRejectsMismatchedLiveManagedRecordBeforeAnyWriteOrNetwork(t *testing.T) {
	service, state, managed := newSupabaseHistoryTestFixture(t, "https://one.supabase.co/functions/v1/ingest")
	managed.SupabaseEventID = "cliproxy-v1." + strings.Repeat("d", 64)
	managed.CompressedBytes++

	backfill := historyAuditFixture(managed.Hour.Add(time.Hour), "cliproxy-logs/2026/07/18/backfill", providerCodex, "bob", 2, 200)
	state.Objects[backfill.ObjectKey] = uploadedObject{
		ObjectKey:      backfill.ObjectKey,
		CompressedSize: backfill.CompressedBytes,
		ArchiveSHA256:  strings.Repeat("c", 64),
		Verification:   "put-success-or-remote-head-match",
		UploadedAt:     service.now(),
		VerifiedAt:     service.now(),
	}
	state.Hours[hourStateKey(backfill.Hour, backfill.Provider)] = uploadedHour{
		Status:         "sealed",
		ObjectKey:      backfill.ObjectKey,
		ArchiveSHA256:  strings.Repeat("c", 64),
		ManifestSHA256: strings.Repeat("e", 64),
		UploadedAt:     service.now(),
	}
	writeHistoryAuditFile(t, filepath.Join(service.cfg.WorkDir, "audit.jsonl"), managed, backfill)
	if errSave := service.saveState(state); errSave != nil {
		t.Fatalf("save complete-preflight state: %v", errSave)
	}
	before, errRead := os.ReadFile(service.statePath())
	if errRead != nil {
		t.Fatalf("read state before complete preflight: %v", errRead)
	}
	t.Setenv(service.cfg.Supabase.IngestTokenEnv, "history-token")
	requests := 0
	service.supabaseHTTPDoer = historyAcknowledgingDoer(t, &requests, "inserted")
	stateWrites := 0
	service.syncStateParentDirectory = func(string) error {
		stateWrites++
		return nil
	}

	_, errSync := service.SyncSupabaseHistory(context.Background(), false)
	if errSync == nil || !strings.Contains(errSync.Error(), "trusted upload state") {
		t.Fatalf("mismatched managed record error = %v", errSync)
	}
	after, errRead := os.ReadFile(service.statePath())
	if errRead != nil {
		t.Fatalf("read state after complete preflight: %v", errRead)
	}
	if requests != 0 || stateWrites != 0 || !bytes.Equal(before, after) {
		t.Fatalf("complete preflight requests=%d state_writes=%d state_changed=%t", requests, stateWrites, !bytes.Equal(before, after))
	}
}

func TestSyncSupabaseHistorySanitizesInvalidStateErrors(t *testing.T) {
	service, state, record := newSupabaseHistoryTestFixture(t, "https://one.supabase.co/functions/v1/ingest")
	writeHistoryAuditFile(t, filepath.Join(service.cfg.WorkDir, "audit.jsonl"), record)
	const privateObject = "private/state/object"
	object := state.Objects[record.ObjectKey]
	delete(state.Objects, record.ObjectKey)
	object.ObjectKey = privateObject
	object.ArchiveSHA256 = "private-checksum"
	state.Objects[privateObject] = object
	if errSave := service.saveState(state); errSave != nil {
		t.Fatalf("save invalid state fixture: %v", errSave)
	}

	_, errSync := service.SyncSupabaseHistory(context.Background(), true)
	if errSync == nil || !strings.Contains(errSync.Error(), "trusted upload state") {
		t.Fatalf("invalid state error = %v", errSync)
	}
	for _, private := range []string{privateObject, "private-checksum", record.ObjectKey} {
		if strings.Contains(errSync.Error(), private) {
			t.Fatalf("invalid state error leaked %q: %v", private, errSync)
		}
	}
}

func TestSyncSupabaseHistorySkipsLiveManagedObject(t *testing.T) {
	service, state, record := newSupabaseHistoryTestFixture(t, "https://one.supabase.co/functions/v1/ingest")
	eventID := "cliproxy-v1." + strings.Repeat("d", 64)
	record.SupabaseEventID = eventID
	state = withUploadedHourSupabaseEventID(t, state, hourStateKey(record.Hour, record.Provider), eventID)
	writeHistoryAuditFile(t, filepath.Join(service.cfg.WorkDir, "audit.jsonl"), record)
	if errSave := service.saveState(state); errSave != nil {
		t.Fatalf("save initial state: %v", errSave)
	}
	requests := 0
	service.supabaseHTTPDoer = httpDoerFunc(func(*http.Request) (*http.Response, error) {
		requests++
		return nil, errors.New("unexpected request")
	})

	summary, errSync := service.SyncSupabaseHistory(context.Background(), false)
	if errSync != nil {
		t.Fatalf("sync live-managed history: %v", errSync)
	}
	if requests != 0 || summary.LiveManaged != 1 || summary.Pending != 0 {
		t.Fatalf("live-managed summary=%#v requests=%d", summary, requests)
	}
}

func historyAuditFixture(hour time.Time, objectKey, provider, keyName string, sourceCount int, sourceBytes int64) auditRecord {
	model := "gpt-5.6-sol"
	switch provider {
	case providerClaude:
		model = "claude-sonnet-4-20250514"
	case providerGrok:
		model = "grok-4.5"
	}
	return auditRecord{
		Timestamp:   hour.Add(time.Hour),
		Status:      "uploaded",
		Provider:    provider,
		Hour:        hour,
		SourceCount: sourceCount,
		SourceBytes: sourceBytes,
		KeyNames: map[string]auditKeyNameSummary{keyName: {
			SourceCount: sourceCount,
			SourceBytes: sourceBytes,
			Models: map[string]auditModelSummary{
				model: {SourceCount: sourceCount, SourceBytes: sourceBytes},
			},
		}},
		JSONLBytes:      sourceBytes + 50,
		CompressedBytes: sourceBytes / 2,
		ObjectKey:       objectKey,
	}
}

func withHistoryStatus(record auditRecord, status string) auditRecord {
	record.Status = status
	return record
}

func withUploadedHourSupabaseEventID(t *testing.T, state uploadState, hourKey, eventID string) uploadState {
	t.Helper()
	hour, exists := state.Hours[hourKey]
	if !exists {
		t.Fatalf("uploaded hour %s missing", hourKey)
	}
	hour.SupabaseEventID = eventID
	state.Hours[hourKey] = hour
	return state
}

func uploadedHourSupabaseEventID(t *testing.T, hour uploadedHour) string {
	t.Helper()
	return hour.SupabaseEventID
}

func writeHistoryAuditFile(t *testing.T, path string, records ...auditRecord) {
	t.Helper()
	if errMkdir := os.MkdirAll(filepath.Dir(path), 0o750); errMkdir != nil {
		t.Fatalf("create history fixture directory: %v", errMkdir)
	}
	var buffer bytes.Buffer
	for _, record := range records {
		raw, errMarshal := json.Marshal(record)
		if errMarshal != nil {
			t.Fatalf("marshal history audit record: %v", errMarshal)
		}
		buffer.Write(raw)
		buffer.WriteByte('\n')
	}
	if errWrite := os.WriteFile(path, buffer.Bytes(), 0o640); errWrite != nil {
		t.Fatalf("write history audit fixture: %v", errWrite)
	}
}

func newSupabaseHistoryTestFixture(t *testing.T, ingestURL string) (*Service, uploadState, auditRecord) {
	t.Helper()
	location := mustLocation(t, "Asia/Shanghai")
	root := filepath.Join(t.TempDir(), "keys")
	workDir := filepath.Join(t.TempDir(), "uploader")
	cfg := testConfig(root, workDir)
	cfg.Supabase = SupabaseConfig{Enabled: true, IngestURL: ingestURL, IngestTokenEnv: "LOG_STATS_INGEST_TOKEN"}
	service := mustTestService(t, cfg, nil, time.Date(2026, time.July, 18, 4, 0, 0, 0, location))
	hour := time.Date(2026, time.July, 18, 1, 0, 0, 0, location)
	record := historyAuditFixture(hour, "cliproxy-logs/2026/07/18/archive", providerCodex, "alice", 1, 100)
	state := service.newUploadState()
	state.Objects[record.ObjectKey] = uploadedObject{
		ObjectKey:      record.ObjectKey,
		CompressedSize: record.CompressedBytes,
		ArchiveSHA256:  strings.Repeat("a", 64),
		Verification:   "put-success-or-remote-head-match",
		UploadedAt:     service.now(),
		VerifiedAt:     service.now(),
	}
	state.Hours[hourStateKey(hour, providerCodex)] = uploadedHour{
		Status:         "sealed",
		ObjectKey:      record.ObjectKey,
		ArchiveSHA256:  strings.Repeat("a", 64),
		ManifestSHA256: strings.Repeat("b", 64),
		UploadedAt:     service.now(),
	}
	return service, state, record
}

func historyAcknowledgingDoer(t *testing.T, requests *int, status string) httpDoer {
	t.Helper()
	return httpDoerFunc(func(request *http.Request) (*http.Response, error) {
		(*requests)++
		if request.Header.Get("Authorization") != "Bearer history-token" || request.Header.Get("apikey") != "" {
			t.Fatalf("history request headers = %#v", request.Header)
		}
		raw, errRead := io.ReadAll(request.Body)
		if errRead != nil {
			t.Fatalf("read history request: %v", errRead)
		}
		var payload supabaseEventPayload
		if errUnmarshal := json.Unmarshal(raw, &payload); errUnmarshal != nil {
			t.Fatalf("decode history payload: %v", errUnmarshal)
		}
		if payload.UsagePrecision != supabaseUsagePrecisionBatchOnly || payload.Usage[0].JSONLBytes != nil {
			t.Fatalf("history payload precision = %#v", payload)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(fmt.Sprintf(`{"status":%q,"event_id":%q}`, status, payload.EventID))),
			Header:     make(http.Header),
		}, nil
	})
}
