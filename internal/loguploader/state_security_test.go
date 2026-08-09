package loguploader

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadStateRejectsOversizedFileBeforeDecode(t *testing.T) {
	t.Parallel()

	service := newStateOutboxTestService(t, false)
	file, errOpen := os.OpenFile(service.statePath(), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if errOpen != nil {
		t.Fatalf("create oversized state fixture: %v", errOpen)
	}
	errTruncate := file.Truncate(maxUploadStateBytes + 1)
	errClose := file.Close()
	if errFixture := errors.Join(errTruncate, errClose); errFixture != nil {
		t.Fatalf("create sparse oversized state fixture: %v", errFixture)
	}

	_, errLoad := service.loadState()
	if errLoad == nil || !strings.Contains(errLoad.Error(), "128 MiB") {
		t.Fatalf("oversized state error = %v", errLoad)
	}
	if strings.Contains(errLoad.Error(), "parse upload state") {
		t.Fatalf("oversized state reached JSON decoding: %v", errLoad)
	}
}

func TestValidateUploadStateRejectsInvalidUploadedHourSupabaseEventID(t *testing.T) {
	service, state, record := newSupabaseHistoryTestFixture(t, "https://one.supabase.co/functions/v1/ingest")
	hourKey := hourStateKey(record.Hour, record.Provider)
	const invalidEventID = "private-invalid-event-id"
	state = withUploadedHourSupabaseEventID(t, state, hourKey, invalidEventID)

	errValidate := service.validateUploadState(&state)
	if errValidate == nil || !strings.Contains(errValidate.Error(), "Supabase event ID") {
		t.Fatalf("invalid uploaded hour event ID error = %v", errValidate)
	}
	if strings.Contains(errValidate.Error(), invalidEventID) {
		t.Fatalf("invalid uploaded hour event ID leaked private value: %v", errValidate)
	}
}

func TestReadUploadStateFileRejectsSymlink(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	target := filepath.Join(directory, "private.log")
	if errWrite := os.WriteFile(target, []byte("private-state-target"), 0o600); errWrite != nil {
		t.Fatalf("write state symlink target: %v", errWrite)
	}
	link := filepath.Join(directory, "state.json")
	if errLink := os.Symlink(target, link); errLink != nil {
		t.Skipf("symlink unavailable: %v", errLink)
	}

	raw, errRead := readUploadStateFile(link)
	if errRead == nil || !strings.Contains(errRead.Error(), "symbolic link") {
		t.Fatalf("state symlink error = %v", errRead)
	}
	if raw != nil || strings.Contains(errRead.Error(), "private-state-target") {
		t.Fatalf("state symlink read leaked target data: raw=%q err=%v", raw, errRead)
	}
}

func TestReadUploadStateFileRejectsPathReplacementAfterPreflight(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	path := filepath.Join(directory, "state.json")
	if errWrite := os.WriteFile(path, []byte("original"), 0o600); errWrite != nil {
		t.Fatalf("write original state fixture: %v", errWrite)
	}
	replacement := filepath.Join(directory, "replacement.json")
	if errWrite := os.WriteFile(replacement, []byte("replaced"), 0o600); errWrite != nil {
		t.Fatalf("write replacement state fixture: %v", errWrite)
	}

	raw, errRead := readUploadStateFileWithOpener(path, func(openPath string) (*os.File, error) {
		if errRename := os.Rename(openPath, openPath+".original"); errRename != nil {
			return nil, errRename
		}
		if errRename := os.Rename(replacement, openPath); errRename != nil {
			return nil, errRename
		}
		return os.Open(openPath)
	})
	if errRead == nil || !strings.Contains(errRead.Error(), "changed after preflight") {
		t.Fatalf("state replacement error = %v", errRead)
	}
	if raw != nil || strings.Contains(errRead.Error(), "replaced") {
		t.Fatalf("state replacement read leaked replacement data: raw=%q err=%v", raw, errRead)
	}
}

func TestSaveStateRejectsOversizedSerializationWithoutReplacingExistingState(t *testing.T) {
	service := newStateOutboxTestService(t, false)
	baseline := service.newUploadState()
	if errSave := service.saveState(baseline); errSave != nil {
		t.Fatalf("save baseline state: %v", errSave)
	}
	before, errRead := os.ReadFile(service.statePath())
	if errRead != nil {
		t.Fatalf("read baseline state: %v", errRead)
	}

	oversized := baseline
	// NUL is encoded as six JSON bytes, so this exceeds the serialized limit
	// with a substantially smaller in-memory source string.
	oversized.Target.ObjectPrefix = strings.Repeat("\x00", int(maxUploadStateBytes/6)+1024)
	errSave := service.saveState(oversized)
	if errSave == nil || !strings.Contains(errSave.Error(), "128 MiB") {
		t.Fatalf("oversized save error = %v", errSave)
	}

	after, errRead := os.ReadFile(service.statePath())
	if errRead != nil {
		t.Fatalf("read state after rejected save: %v", errRead)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("rejected oversized save replaced the existing state bytes")
	}
	if _, errStat := os.Stat(service.statePath() + ".tmp"); !errors.Is(errStat, os.ErrNotExist) {
		t.Fatalf("rejected oversized save left a temporary state file: %v", errStat)
	}
	if _, errLoad := service.loadState(); errLoad != nil {
		t.Fatalf("load preserved baseline state: %v", errLoad)
	}
}

func TestSaveStatePreservesPresetFixedTempFile(t *testing.T) {
	service := newStateOutboxTestService(t, false)
	fixedTempPath := service.statePath() + ".tmp"
	const sentinel = "unrelated fixed temporary file"
	if errWrite := os.WriteFile(fixedTempPath, []byte(sentinel), 0o600); errWrite != nil {
		t.Fatalf("write fixed temporary sentinel: %v", errWrite)
	}

	if errSave := service.saveState(service.newUploadState()); errSave != nil {
		t.Fatalf("save state with fixed temporary sentinel: %v", errSave)
	}
	got, errRead := os.ReadFile(fixedTempPath)
	if errRead != nil {
		t.Fatalf("read preserved fixed temporary sentinel: %v", errRead)
	}
	if string(got) != sentinel {
		t.Fatalf("fixed temporary sentinel = %q, want %q", got, sentinel)
	}
	if _, errLoad := service.loadState(); errLoad != nil {
		t.Fatalf("load normally saved state: %v", errLoad)
	}
}

func TestSaveStateDoesNotFollowPresetFixedTempSymlink(t *testing.T) {
	service := newStateOutboxTestService(t, false)
	target := filepath.Join(t.TempDir(), "outside-state-target")
	const sentinel = "outside file must remain unchanged"
	if errWrite := os.WriteFile(target, []byte(sentinel), 0o600); errWrite != nil {
		t.Fatalf("write outside state target: %v", errWrite)
	}
	fixedTempPath := service.statePath() + ".tmp"
	if errLink := os.Symlink(target, fixedTempPath); errLink != nil {
		t.Skipf("symlink unavailable: %v", errLink)
	}

	if errSave := service.saveState(service.newUploadState()); errSave != nil {
		t.Fatalf("save state with fixed temporary symlink: %v", errSave)
	}
	got, errRead := os.ReadFile(target)
	if errRead != nil {
		t.Fatalf("read outside state target: %v", errRead)
	}
	if string(got) != sentinel {
		t.Fatalf("outside state target = %q, want %q", got, sentinel)
	}
	info, errLstat := os.Lstat(fixedTempPath)
	if errLstat != nil {
		t.Fatalf("lstat preserved fixed temporary symlink: %v", errLstat)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("fixed temporary path mode = %v, want symlink", info.Mode())
	}
}

func TestSaveStateRenameFailureCleansTemporaryFileAndPreservesFixedTempFile(t *testing.T) {
	service := newStateOutboxTestService(t, false)
	fixedTempPath := service.statePath() + ".tmp"
	const sentinel = "unrelated fixed temporary file"
	if errWrite := os.WriteFile(fixedTempPath, []byte(sentinel), 0o600); errWrite != nil {
		t.Fatalf("write fixed temporary sentinel: %v", errWrite)
	}
	if errMkdir := os.Mkdir(service.statePath(), 0o750); errMkdir != nil {
		t.Fatalf("block state publication: %v", errMkdir)
	}

	errSave := service.saveState(service.newUploadState())
	if errSave == nil || !strings.Contains(errSave.Error(), "publish upload state") {
		t.Fatalf("blocked state publication error = %v", errSave)
	}
	got, errRead := os.ReadFile(fixedTempPath)
	if errRead != nil {
		t.Fatalf("read fixed temporary sentinel after failure: %v", errRead)
	}
	if string(got) != sentinel {
		t.Fatalf("fixed temporary sentinel after failure = %q, want %q", got, sentinel)
	}
	temporaryPaths, errGlob := filepath.Glob(filepath.Join(service.cfg.WorkDir, "state.json.*.tmp"))
	if errGlob != nil {
		t.Fatalf("glob state temporary files: %v", errGlob)
	}
	if len(temporaryPaths) != 0 {
		t.Fatalf("state publication failure left temporary files: %v", temporaryPaths)
	}
}

func blockStatePublication(t *testing.T, service *Service) func() {
	t.Helper()
	statePath := service.statePath()
	backupPath := statePath + ".save-failure-test"
	if errRename := os.Rename(statePath, backupPath); errRename != nil {
		t.Fatalf("back up durable state before blocking publication: %v", errRename)
	}
	if errMkdir := os.Mkdir(statePath, 0o750); errMkdir != nil {
		if errRestore := os.Rename(backupPath, statePath); errRestore != nil {
			t.Errorf("restore durable state after blocker setup failure: %v", errRestore)
		}
		t.Fatalf("block state publication: %v", errMkdir)
	}
	restored := false
	restore := func() {
		t.Helper()
		if restored {
			return
		}
		if errRemove := os.Remove(statePath); errRemove != nil {
			t.Errorf("remove state publication blocker: %v", errRemove)
			return
		}
		if errRename := os.Rename(backupPath, statePath); errRename != nil {
			t.Errorf("restore durable state after blocked publication: %v", errRename)
			return
		}
		restored = true
	}
	t.Cleanup(restore)
	return restore
}

func TestLoadStateRejectsInvalidSupabaseOutboxBase64(t *testing.T) {
	t.Parallel()

	service := newStateOutboxTestService(t, true)
	state, entry := validStateOutboxEntry(t, service, "alice")
	state.SupabaseOutbox.Entries[entry.EventID] = entry
	rawState, errMarshal := json.Marshal(state)
	if errMarshal != nil {
		t.Fatalf("marshal state with valid payload bytes: %v", errMarshal)
	}
	rawState = corruptBase64Payload(rawState, entry.Payload)
	if errWrite := os.WriteFile(service.statePath(), rawState, 0o600); errWrite != nil {
		t.Fatalf("write state with invalid base64 payload: %v", errWrite)
	}

	_, errLoad := service.loadState()
	if errLoad == nil || !strings.Contains(errLoad.Error(), "base64") {
		t.Fatalf("invalid base64 state error = %v", errLoad)
	}
}

func TestValidateSupabaseStateErrorsDoNotEchoUntrustedMapKeys(t *testing.T) {
	t.Parallel()

	const tokenLikeMapKey = "sk-live-map-key-must-never-be-logged"
	tests := []struct {
		name   string
		mutate func(state *uploadState)
	}{
		{name: "outbox", mutate: func(state *uploadState) {
			state.SupabaseOutbox.Entries[tokenLikeMapKey] = supabaseOutboxEntry{}
		}},
		{name: "history", mutate: func(state *uploadState) {
			state.SupabaseHistory[tokenLikeMapKey] = supabaseHistoryCheckpoint{ObjectKey: tokenLikeMapKey}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := newStateOutboxTestService(t, true)
			state := service.newUploadState()
			test.mutate(&state)

			errValidate := service.validateUploadState(&state)
			if errValidate == nil {
				t.Fatal("state with an untrusted map key was accepted")
			}
			if strings.Contains(errValidate.Error(), tokenLikeMapKey) {
				t.Fatalf("validation error echoed an untrusted map key: %v", errValidate)
			}
		})
	}
}

func TestValidateSupabaseOutboxErrorsDoNotEchoUntrustedValues(t *testing.T) {
	t.Parallel()

	const secretMarker = "sk-live-secret"
	untrustedValue := secretMarker + strings.Repeat("x", 8*1024)
	tests := []struct {
		name   string
		mutate func(t *testing.T, state *uploadState, entryID string)
	}{
		{name: "status", mutate: func(t *testing.T, state *uploadState, entryID string) {
			entry := state.SupabaseOutbox.Entries[entryID]
			entry.Status = untrustedValue
			state.SupabaseOutbox.Entries[entryID] = entry
		}},
		{name: "unknown payload field", mutate: func(t *testing.T, state *uploadState, entryID string) {
			entry := state.SupabaseOutbox.Entries[entryID]
			encodedFieldName, errMarshal := json.Marshal(untrustedValue)
			if errMarshal != nil {
				t.Fatalf("encode untrusted field name: %v", errMarshal)
			}
			insertion := append([]byte(",\n  "), encodedFieldName...)
			insertion = append(insertion, []byte(": true\n}\n")...)
			entry.Payload = bytes.Replace(entry.Payload, []byte("\n}\n"), insertion, 1)
			entry.PayloadSHA256 = sha256Hex(entry.Payload)
			state.SupabaseOutbox.Entries[entryID] = entry
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := newStateOutboxTestService(t, true)
			state, entry := validStateOutboxEntry(t, service, "alice")
			state.SupabaseOutbox.Entries[entry.EventID] = entry
			test.mutate(t, &state, entry.EventID)

			errValidate := service.validateUploadState(&state)
			if errValidate == nil {
				t.Fatal("state with an untrusted outbox value was accepted")
			}
			if strings.Contains(errValidate.Error(), secretMarker) {
				t.Fatalf("validation error echoed an untrusted value: %v", errValidate)
			}
			if len(errValidate.Error()) > 512 {
				t.Fatalf("validation error amplified an untrusted value to %d bytes", len(errValidate.Error()))
			}
		})
	}
}

func TestValidateUploadStateRejectsInconsistentReferences(t *testing.T) {
	t.Parallel()

	location := mustLocation(t, "Asia/Shanghai")
	now := time.Date(2026, time.July, 15, 5, 10, 0, 0, location)
	root := filepath.Join(t.TempDir(), "keys")
	workDir := filepath.Join(t.TempDir(), "uploader")
	service := mustTestService(t, testConfig(root, workDir), nil, now)

	if errValidate := service.validateUploadState(validSecurityUploadState(service)); errValidate != nil {
		t.Fatalf("valid state rejected: %v", errValidate)
	}

	tests := []struct {
		name    string
		wantErr string
		mutate  func(state *uploadState)
	}{
		{
			name:    "non-deterministic pending delete path",
			wantErr: "invalid pending delete path",
			mutate: func(state *uploadState) {
				fingerprint := securityUploadedFingerprint(service)
				source := state.Uploaded[fingerprint]
				source.PendingDeleteAt = filepath.Join(workDir, "delete-pending", "different.log")
				state.Uploaded[fingerprint] = source
			},
		},
		{
			name:    "object map key mismatch",
			wantErr: "does not match object key",
			mutate: func(state *uploadState) {
				objectKey := securityUploadedObjectKey()
				object := state.Objects[objectKey]
				object.ObjectKey = "cliproxy-logs/wrong-object.jsonl.zst"
				state.Objects[objectKey] = object
			},
		},
		{
			name:    "object checksum is invalid",
			wantErr: "invalid archive checksum",
			mutate: func(state *uploadState) {
				objectKey := securityUploadedObjectKey()
				object := state.Objects[objectKey]
				object.ArchiveSHA256 = "not-a-sha256"
				state.Objects[objectKey] = object
			},
		},
		{
			name:    "hour references missing object",
			wantErr: "references missing object",
			mutate: func(state *uploadState) {
				delete(state.Objects, securityUploadedObjectKey())
			},
		},
		{
			name:    "hour and object checksums differ",
			wantErr: "archive checksum does not match",
			mutate: func(state *uploadState) {
				hourKey := hourStateKey(time.Date(2026, time.July, 15, 1, 0, 0, 0, service.location), providerCodex)
				hour := state.Hours[hourKey]
				hour.ArchiveSHA256 = strings.Repeat("9", 64)
				state.Hours[hourKey] = hour
			},
		},
		{
			name:    "orphan object",
			wantErr: "is not referenced by a sealed hour",
			mutate: func(state *uploadState) {
				objectKey := "cliproxy-logs/2026/07/15/orphan.jsonl.zst"
				state.Objects[objectKey] = uploadedObject{ObjectKey: objectKey, ArchiveSHA256: strings.Repeat("8", 64)}
			},
		},
		{
			name:    "uploaded fingerprint key mismatch",
			wantErr: "does not match its source identity",
			mutate: func(state *uploadState) {
				fingerprint := securityUploadedFingerprint(service)
				source := state.Uploaded[fingerprint]
				delete(state.Uploaded, fingerprint)
				state.Uploaded["wrong-fingerprint"] = source
			},
		},
		{
			name:    "uploaded object differs from sealed hour",
			wantErr: "does not reference a sealed hour and object",
			mutate: func(state *uploadState) {
				fingerprint := securityUploadedFingerprint(service)
				source := state.Uploaded[fingerprint]
				source.ObjectKey = "cliproxy-logs/missing.jsonl.zst"
				state.Uploaded[fingerprint] = source
			},
		},
		{
			name:    "prepared manifest mismatch",
			wantErr: "manifest checksum mismatch",
			mutate: func(state *uploadState) {
				preparedKey := hourStateKey(time.Date(2026, time.July, 15, 2, 0, 0, 0, service.location), providerCodex)
				prepared := state.PreparedHours[preparedKey]
				prepared.ManifestSHA256 = strings.Repeat("0", 64)
				state.PreparedHours[preparedKey] = prepared
			},
		},
		{
			name:    "prepared object is already committed",
			wantErr: "reuses committed object",
			mutate: func(state *uploadState) {
				preparedKey := hourStateKey(time.Date(2026, time.July, 15, 2, 0, 0, 0, service.location), providerCodex)
				prepared := state.PreparedHours[preparedKey]
				prepared.ObjectKey = securityUploadedObjectKey()
				state.PreparedHours[preparedKey] = prepared
			},
		},
		{
			name:    "prepared and uploaded share fingerprint",
			wantErr: "is shared by uploaded source and prepared hour",
			mutate: func(state *uploadState) {
				fingerprint := securityUploadedFingerprint(service)
				uploaded := state.Uploaded[fingerprint]
				preparedKey := hourStateKey(time.Date(2026, time.July, 15, 2, 0, 0, 0, service.location), providerCodex)
				prepared := state.PreparedHours[preparedKey]
				prepared.Sources = []preparedSource{{
					Fingerprint:  fingerprint,
					RelativePath: uploaded.RelativePath,
					KeyName:      "panda",
					Model:        "model-a",
					Size:         uploaded.Size,
					ModTime:      uploaded.ModTime,
					SHA256:       uploaded.SHA256,
				}}
				prepared.ManifestSHA256 = manifestSHA256(prepared.Sources)
				state.PreparedHours[preparedKey] = prepared
			},
		},
		{
			name:    "prepared hours share fingerprint",
			wantErr: "is shared by prepared hour",
			mutate: func(state *uploadState) {
				preparedKey := hourStateKey(time.Date(2026, time.July, 15, 2, 0, 0, 0, service.location), providerCodex)
				original := state.PreparedHours[preparedKey]
				hour := time.Date(2026, time.July, 15, 3, 0, 0, 0, service.location)
				duplicate := original
				duplicate.Hour = hour
				duplicate.ObjectKey = "cliproxy-logs/2026/07/15/2026-07-15-03-codex56sol-1K.jsonl.zst"
				duplicate.ArchivePath = filepath.Join(service.cfg.WorkDir, "archives", "2026", "07", "15", filepath.Base(duplicate.ObjectKey))
				duplicate.Sources = append([]preparedSource(nil), original.Sources...)
				duplicate.ManifestSHA256 = manifestSHA256(duplicate.Sources)
				state.PreparedHours[hourStateKey(hour, providerCodex)] = duplicate
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := validSecurityUploadState(service)
			test.mutate(state)
			errValidate := service.validateUploadState(state)
			if errValidate == nil || !strings.Contains(errValidate.Error(), test.wantErr) {
				t.Fatalf("validateUploadState error = %v, want %q", errValidate, test.wantErr)
			}
		})
	}
}

func TestValidateUploadStateAcceptsDeterministicPendingDeletePath(t *testing.T) {
	t.Parallel()

	location := mustLocation(t, "Asia/Shanghai")
	now := time.Date(2026, time.July, 15, 5, 10, 0, 0, location)
	service := mustTestService(t, testConfig(filepath.Join(t.TempDir(), "keys"), filepath.Join(t.TempDir(), "uploader")), nil, now)
	state := validSecurityUploadState(service)
	fingerprint := securityUploadedFingerprint(service)
	source := state.Uploaded[fingerprint]
	source.PendingDeleteAt = service.pendingDeletePath(fingerprint)
	state.Uploaded[fingerprint] = source

	if errValidate := service.validateUploadState(state); errValidate != nil {
		t.Fatalf("deterministic pending delete path rejected: %v", errValidate)
	}
}

func TestDeleteUploadedSourcesRejectsNonDeterministicPendingPath(t *testing.T) {
	t.Parallel()

	location := mustLocation(t, "Asia/Shanghai")
	now := time.Date(2026, time.July, 15, 5, 10, 0, 0, location)
	root := filepath.Join(t.TempDir(), "keys")
	workDir := filepath.Join(t.TempDir(), "uploader")
	service := mustTestService(t, testConfig(root, workDir), nil, now)
	outsidePath := filepath.Join(t.TempDir(), "outside.log")
	if errWrite := os.WriteFile(outsidePath, []byte("must remain"), 0o600); errWrite != nil {
		t.Fatalf("write outside file: %v", errWrite)
	}
	fingerprint := "panda/source.log|11|1"
	state := service.newUploadState()
	state.Uploaded[fingerprint] = uploadedSource{
		TargetID:        service.target.ID,
		RelativePath:    "panda/source.log",
		SHA256:          strings.Repeat("a", 64),
		PendingDeleteAt: outsidePath,
	}
	deleted := 0

	changed, deleteErrors := service.deleteUploadedSources(state, []string{fingerprint}, &deleted)
	if changed || deleted != 0 || len(deleteErrors) != 1 || !strings.Contains(deleteErrors[0].Error(), "invalid pending delete path") {
		t.Fatalf("delete result changed=%t deleted=%d errors=%v", changed, deleted, deleteErrors)
	}
	if _, errStat := os.Stat(outsidePath); errStat != nil {
		t.Fatalf("non-deterministic pending path was touched: %v", errStat)
	}
	if _, exists := state.Uploaded[fingerprint]; !exists {
		t.Fatal("uploaded state was removed after rejecting pending path")
	}
}

func TestDeleteUploadedSourcesResumesDeterministicPendingPath(t *testing.T) {
	t.Parallel()

	location := mustLocation(t, "Asia/Shanghai")
	now := time.Date(2026, time.July, 15, 5, 10, 0, 0, location)
	root := filepath.Join(t.TempDir(), "keys")
	workDir := filepath.Join(t.TempDir(), "uploader")
	service := mustTestService(t, testConfig(root, workDir), nil, now)
	stagingPath := filepath.Join(workDir, "staging.log")
	if errMkdir := os.MkdirAll(workDir, 0o750); errMkdir != nil {
		t.Fatalf("create work directory: %v", errMkdir)
	}
	if errWrite := os.WriteFile(stagingPath, []byte("uploaded source"), 0o600); errWrite != nil {
		t.Fatalf("write staged source: %v", errWrite)
	}
	info, errStat := os.Stat(stagingPath)
	if errStat != nil {
		t.Fatalf("stat staged source: %v", errStat)
	}
	relativePath := "panda/source.log"
	fingerprint := sourceFingerprint(relativePath, info.Size(), info.ModTime())
	pendingPath := service.pendingDeletePath(fingerprint)
	if errMkdir := os.MkdirAll(filepath.Dir(pendingPath), 0o700); errMkdir != nil {
		t.Fatalf("create pending directory: %v", errMkdir)
	}
	if errRename := os.Rename(stagingPath, pendingPath); errRename != nil {
		t.Fatalf("stage deterministic pending source: %v", errRename)
	}
	checksum, _, errChecksum := fileSHA256(pendingPath)
	if errChecksum != nil {
		t.Fatalf("checksum pending source: %v", errChecksum)
	}
	state := service.newUploadState()
	state.Uploaded[fingerprint] = uploadedSource{
		TargetID:        service.target.ID,
		RelativePath:    relativePath,
		Size:            info.Size(),
		ModTime:         info.ModTime(),
		SHA256:          checksum,
		PendingDeleteAt: pendingPath,
	}
	deleted := 0

	changed, deleteErrors := service.deleteUploadedSources(state, []string{fingerprint}, &deleted)
	if !changed || deleted != 1 || len(deleteErrors) != 0 {
		t.Fatalf("delete result changed=%t deleted=%d errors=%v", changed, deleted, deleteErrors)
	}
	if _, errStat := os.Stat(pendingPath); !os.IsNotExist(errStat) {
		t.Fatalf("deterministic pending source still exists: %v", errStat)
	}
	if _, exists := state.Uploaded[fingerprint]; exists {
		t.Fatal("uploaded state remains after deterministic pending deletion")
	}
}

func validSecurityUploadState(service *Service) *uploadState {
	state := service.newUploadState()
	uploadedHour := time.Date(2026, time.July, 15, 1, 0, 0, 0, service.location)
	uploadedModTime := uploadedHour.Add(10 * time.Minute)
	uploadedRelativePath := "panda/source.log"
	uploadedFingerprint := sourceFingerprint(uploadedRelativePath, 100, uploadedModTime)
	uploadedObjectKey := securityUploadedObjectKey()
	uploadedArchiveSHA := strings.Repeat("a", 64)
	state.Objects[uploadedObjectKey] = uploadedObject{
		ObjectKey:      uploadedObjectKey,
		CompressedSize: 80,
		ArchiveSHA256:  uploadedArchiveSHA,
		Verification:   "put-success-or-remote-head-match",
		UploadedAt:     uploadedHour.Add(time.Hour),
		VerifiedAt:     uploadedHour.Add(time.Hour),
	}
	state.Hours[hourStateKey(uploadedHour, providerCodex)] = uploadedHourState(uploadedObjectKey, uploadedArchiveSHA, uploadedHour.Add(time.Hour))
	state.Uploaded[uploadedFingerprint] = uploadedSource{
		ObjectKey:    uploadedObjectKey,
		HourKey:      hourStateKey(uploadedHour, providerCodex),
		TargetID:     service.target.ID,
		UploadedAt:   uploadedHour.Add(time.Hour),
		RelativePath: uploadedRelativePath,
		Size:         100,
		ModTime:      uploadedModTime,
		SHA256:       strings.Repeat("b", 64),
	}

	preparedTime := time.Date(2026, time.July, 15, 2, 0, 0, 0, service.location)
	preparedModTime := preparedTime.Add(20 * time.Minute)
	preparedRelativePath := "alice/prepared.log"
	preparedSourceFingerprint := sourceFingerprint(preparedRelativePath, 120, preparedModTime)
	preparedObjectKey := "cliproxy-logs/2026/07/15/2026-07-15-02-codex56sol-1K.jsonl.zst"
	prepared := preparedHour{
		TargetID:        service.target.ID,
		Hour:            preparedTime,
		Provider:        providerCodex,
		ObjectKey:       preparedObjectKey,
		ArchivePath:     filepath.Join(service.cfg.WorkDir, "archives", "2026", "07", "15", filepath.Base(preparedObjectKey)),
		JSONLBytes:      1024,
		CompressedBytes: 512,
		ArchiveSHA256:   strings.Repeat("c", 64),
		PreparedAt:      preparedTime.Add(time.Hour),
		Sources: []preparedSource{{
			Fingerprint:  preparedSourceFingerprint,
			RelativePath: preparedRelativePath,
			KeyName:      "alice",
			Model:        "model-b",
			Size:         120,
			ModTime:      preparedModTime,
			SHA256:       strings.Repeat("d", 64),
		}},
	}
	prepared.ManifestSHA256 = manifestSHA256(prepared.Sources)
	state.PreparedHours[hourStateKey(preparedTime, providerCodex)] = prepared
	return &state
}

func uploadedHourState(objectKey, archiveSHA string, uploadedAt time.Time) uploadedHour {
	return uploadedHour{
		Status:         "sealed",
		ObjectKey:      objectKey,
		ArchiveSHA256:  archiveSHA,
		ManifestSHA256: strings.Repeat("e", 64),
		UploadedAt:     uploadedAt,
	}
}

func securityUploadedObjectKey() string {
	return "cliproxy-logs/2026/07/15/2026-07-15-01-codex56sol-1K.jsonl.zst"
}

func securityUploadedFingerprint(service *Service) string {
	hour := time.Date(2026, time.July, 15, 1, 0, 0, 0, service.location)
	return sourceFingerprint("panda/source.log", 100, hour.Add(10*time.Minute))
}
