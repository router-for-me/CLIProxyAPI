package loguploader

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

type uploadCall struct {
	Bucket    string
	ObjectKey string
	Path      string
}

type fakeObjectUploader struct {
	err      error
	errors   []error
	onUpload func()
	calls    []uploadCall
}

type matchingFakeObjectUploader struct {
	*fakeObjectUploader
	matches    bool
	matchErr   error
	matchCalls int
}

func (u *matchingFakeObjectUploader) MatchObject(_ context.Context, _, _, _ string) (bool, error) {
	u.matchCalls++
	return u.matches, u.matchErr
}

func (u *fakeObjectUploader) UploadFile(_ context.Context, bucket, objectKey, path string) error {
	if _, errStat := os.Stat(path); errStat != nil {
		return errStat
	}
	u.calls = append(u.calls, uploadCall{Bucket: bucket, ObjectKey: objectKey, Path: path})
	if u.onUpload != nil {
		u.onUpload()
	}
	if len(u.errors) > 0 {
		errUpload := u.errors[0]
		u.errors = u.errors[1:]
		return errUpload
	}
	return u.err
}

func TestRunOnceDryRunCreatesZstdJSONLAndAuditWithoutDeleting(t *testing.T) {
	t.Parallel()

	location := mustLocation(t, "Asia/Shanghai")
	now := time.Date(2026, time.July, 15, 3, 10, 0, 0, location)
	timestamp := time.Date(2026, time.July, 15, 1, 30, 0, 0, location)
	root := filepath.Join(t.TempDir(), "keys")
	workDir := filepath.Join(t.TempDir(), "uploader")
	rawLog := requestLog(timestamp, "gpt-5.6-sol", "你好，dry run\nsecond line")
	sourcePath := mustWriteLog(t, root, "panda", "v1-responses-2026-07-15T013000-dry.log", rawLog, now.Add(-2*time.Hour))
	secondTimestamp := timestamp.Add(15 * time.Minute)
	secondRawLog := requestLog(secondTimestamp, "claude-opus-4", "second key and model")
	secondSourcePath := mustWriteLog(t, root, "alice", "v1-messages-2026-07-15T014500-dry.log", secondRawLog, now.Add(-2*time.Hour))
	thirdTimestamp := timestamp.Add(20 * time.Minute)
	thirdRawLog := requestLog(thirdTimestamp, "gemini-2.5-pro", "second model for panda")
	thirdSourcePath := mustWriteLog(t, root, "panda", "v1-responses-2026-07-15T015000-dry.log", thirdRawLog, now.Add(-2*time.Hour))

	uploader := &fakeObjectUploader{}
	service := mustTestService(t, testConfig(root, workDir), uploader, now)
	service.cfg.Upload.Enabled = true
	service.cfg.Retention.DeleteSourceAfterUpload = true
	service.cfg.Retention.KeepLocalArchives = false

	if errRun := service.RunOnce(context.Background(), true); errRun != nil {
		t.Fatalf("run dry-run upload: %v", errRun)
	}
	if len(uploader.calls) != 0 {
		t.Fatalf("dry run made %d upload calls, want 0", len(uploader.calls))
	}
	if _, errStat := os.Stat(sourcePath); errStat != nil {
		t.Fatalf("dry run removed source log: %v", errStat)
	}
	if _, errStat := os.Stat(secondSourcePath); errStat != nil {
		t.Fatalf("dry run removed second source log: %v", errStat)
	}
	if _, errStat := os.Stat(thirdSourcePath); errStat != nil {
		t.Fatalf("dry run removed third source log: %v", errStat)
	}
	if _, errStat := os.Stat(service.statePath()); !errors.Is(errStat, os.ErrNotExist) {
		t.Fatalf("dry run unexpectedly created upload state: %v", errStat)
	}

	archives := findFilesWithSuffix(t, workDir, ".jsonl.zst")
	if len(archives) != 1 {
		t.Fatalf("archive count = %d, want 1: %v", len(archives), archives)
	}
	for _, jsonlPath := range findFilesWithSuffix(t, workDir, ".jsonl") {
		if filepath.Base(jsonlPath) != "audit.jsonl" {
			t.Errorf("uncompressed temporary JSONL file was left behind: %s", jsonlPath)
		}
	}
	if got := filepath.Base(archives[0]); !strings.HasPrefix(got, "2026-07-15-01-codex56sol-") {
		t.Errorf("archive filename = %q, missing date/hour/codex56sol prefix", got)
	}
	decompressed := readZstdFile(t, archives[0])
	if !validJSONL(decompressed) {
		t.Fatalf("decompressed archive is not valid JSONL")
	}
	lines := nonemptyLines(decompressed)
	if len(lines) != 3 {
		t.Fatalf("JSONL record count = %d, want 3", len(lines))
	}
	type archivedRecord struct {
		KeyName string `json:"key_name"`
		Model   string `json:"model"`
		RawLog  string `json:"raw_log"`
	}
	records := make(map[string]archivedRecord, len(lines))
	for _, line := range lines {
		var record archivedRecord
		if errUnmarshal := json.Unmarshal(line, &record); errUnmarshal != nil {
			t.Fatalf("decode archived record: %v", errUnmarshal)
		}
		records[record.KeyName+"/"+record.Model] = record
	}
	if record := records["panda/gpt-5.6-sol"]; record.RawLog != rawLog {
		t.Errorf("unexpected panda record: model=%q raw_matches=%t", record.Model, record.RawLog == rawLog)
	}
	if record := records["alice/claude-opus-4"]; record.RawLog != secondRawLog {
		t.Errorf("unexpected alice record: model=%q raw_matches=%t", record.Model, record.RawLog == secondRawLog)
	}
	if record := records["panda/gemini-2.5-pro"]; record.RawLog != thirdRawLog {
		t.Errorf("unexpected second panda model record: model=%q raw_matches=%t", record.Model, record.RawLog == thirdRawLog)
	}

	audit := readAudit(t, workDir)
	if len(audit) != 1 {
		t.Fatalf("audit count = %d, want 1", len(audit))
	}
	if audit[0].Status != "dry_run" {
		t.Errorf("unexpected audit record: %+v", audit[0])
	}
	if audit[0].SourceCount != 3 || audit[0].SourceBytes != int64(len(rawLog)+len(secondRawLog)+len(thirdRawLog)) {
		t.Errorf("unexpected source totals in audit: %+v", audit[0])
	}
	pandaSummary := audit[0].KeyNames["panda"]
	if pandaSummary.SourceCount != 2 || pandaSummary.SourceBytes != int64(len(rawLog)+len(thirdRawLog)) || pandaSummary.Models["gpt-5.6-sol"].SourceCount != 1 || pandaSummary.Models["gemini-2.5-pro"].SourceCount != 1 {
		t.Errorf("unexpected panda audit summary: %+v", pandaSummary)
	}
	aliceSummary := audit[0].KeyNames["alice"]
	if aliceSummary.SourceCount != 1 || aliceSummary.SourceBytes != int64(len(secondRawLog)) || aliceSummary.Models["claude-opus-4"].SourceCount != 1 {
		t.Errorf("unexpected alice audit summary: %+v", aliceSummary)
	}
	if audit[0].JSONLBytes != int64(len(decompressed)) {
		t.Errorf("audit JSONL bytes = %d, decompressed bytes = %d", audit[0].JSONLBytes, len(decompressed))
	}
	if audit[0].CompressedBytes <= 0 || audit[0].DeletedSources != 0 {
		t.Errorf("unexpected compressed/deleted counts: %+v", audit[0])
	}
}

func TestRepeatedDryRunReplacesHourlyArchiveInsteadOfCreatingParts(t *testing.T) {
	t.Parallel()

	location := mustLocation(t, "Asia/Shanghai")
	now := time.Date(2026, time.July, 15, 3, 10, 0, 0, location)
	hour := time.Date(2026, time.July, 15, 1, 0, 0, 0, location)
	root := filepath.Join(t.TempDir(), "keys")
	workDir := filepath.Join(t.TempDir(), "uploader")
	mustWriteLog(t, root, "first", "first.log", requestLog(hour.Add(10*time.Minute), "model-a", "first"), hour.Add(11*time.Minute))

	service := mustTestService(t, testConfig(root, workDir), nil, now)
	service.cfg.Upload.Enabled = true
	if errRun := service.RunOnce(context.Background(), true); errRun != nil {
		t.Fatalf("first dry run: %v", errRun)
	}
	mustWriteLog(t, root, "second", "second.log", requestLog(hour.Add(20*time.Minute), "model-b", "second and larger"), hour.Add(21*time.Minute))
	if errRun := service.RunOnce(context.Background(), true); errRun != nil {
		t.Fatalf("second dry run: %v", errRun)
	}
	archives := findFilesWithSuffix(t, workDir, ".jsonl.zst")
	if len(archives) != 1 {
		t.Fatalf("hourly archives = %d, want exactly one: %v", len(archives), archives)
	}
	if strings.Contains(filepath.Base(archives[0]), "-part") {
		t.Errorf("dry-run archive contains part suffix: %s", archives[0])
	}
	if records := nonemptyLines(readZstdFile(t, archives[0])); len(records) != 2 {
		t.Errorf("replacement archive records = %d, want 2", len(records))
	}
}

func TestObjectNameConflictFailsWithoutCreatingPartArchive(t *testing.T) {
	t.Parallel()

	location := mustLocation(t, "Asia/Shanghai")
	now := time.Date(2026, time.July, 15, 4, 10, 0, 0, location)
	timestamp := time.Date(2026, time.July, 15, 2, 15, 0, 0, location)
	root := filepath.Join(t.TempDir(), "keys")
	workDir := filepath.Join(t.TempDir(), "uploader")
	mustWriteLog(t, root, "panda", "conflict.log", requestLog(timestamp, "gpt-5.6-sol", "conflict"), now.Add(-2*time.Hour))

	uploader := &fakeObjectUploader{err: ErrObjectConflict}
	cfg := testConfig(root, workDir)
	cfg.Upload.Enabled = true
	service := mustTestService(t, cfg, uploader, now)
	if errRun := service.RunOnce(context.Background(), false); errRun == nil {
		t.Fatal("object conflict unexpectedly succeeded")
	}
	if len(uploader.calls) != 1 {
		t.Fatalf("upload calls = %d, want 1", len(uploader.calls))
	}
	if strings.Contains(uploader.calls[0].ObjectKey, "-part") {
		t.Errorf("conflicting object used a part suffix: %s", uploader.calls[0].ObjectKey)
	}
	state, errState := service.loadState()
	if errState != nil {
		t.Fatalf("load state: %v", errState)
	}
	if len(state.Objects) != 0 || len(state.Hours) != 0 {
		t.Errorf("conflicting object was persisted: objects=%d hours=%d", len(state.Objects), len(state.Hours))
	}
	if archives := findFilesWithSuffix(t, workDir, ".jsonl.zst"); len(archives) != 1 {
		t.Errorf("conflict archives = %d, want one retained archive", len(archives))
	}
}

func TestMatchingObjectConflictRecoversWithoutPartArchive(t *testing.T) {
	t.Parallel()

	location := mustLocation(t, "Asia/Shanghai")
	now := time.Date(2026, time.July, 15, 4, 10, 0, 0, location)
	timestamp := time.Date(2026, time.July, 15, 2, 15, 0, 0, location)
	root := filepath.Join(t.TempDir(), "keys")
	workDir := filepath.Join(t.TempDir(), "uploader")
	mustWriteLog(t, root, "panda", "conflict.log", requestLog(timestamp, "gpt-5.6-sol", "conflict"), now.Add(-2*time.Hour))

	uploader := &matchingFakeObjectUploader{
		fakeObjectUploader: &fakeObjectUploader{err: ErrObjectConflict},
		matches:            true,
	}
	cfg := testConfig(root, workDir)
	cfg.Upload.Enabled = true
	service := mustTestService(t, cfg, uploader, now)
	if errRun := service.RunOnce(context.Background(), false); errRun != nil {
		t.Fatalf("recover matching object conflict: %v", errRun)
	}
	if len(uploader.calls) != 1 || uploader.matchCalls != 1 {
		t.Fatalf("upload calls = %d, match calls = %d; want 1 each", len(uploader.calls), uploader.matchCalls)
	}
	if strings.Contains(uploader.calls[0].ObjectKey, "-part") {
		t.Errorf("matching conflict used a part suffix: %s", uploader.calls[0].ObjectKey)
	}
	state, errState := service.loadState()
	if errState != nil {
		t.Fatalf("load recovered state: %v", errState)
	}
	if len(state.Objects) != 1 || len(state.Hours) != 1 {
		t.Errorf("recovered state objects=%d hours=%d, want one each", len(state.Objects), len(state.Hours))
	}
}

func TestPreparedHourRecoveryReusesExactObjectKeyWhenSourcesChange(t *testing.T) {
	t.Parallel()

	location := mustLocation(t, "Asia/Shanghai")
	now := time.Date(2026, time.July, 15, 4, 10, 0, 0, location)
	hour := time.Date(2026, time.July, 15, 2, 0, 0, 0, location)
	root := filepath.Join(t.TempDir(), "keys")
	workDir := filepath.Join(t.TempDir(), "uploader")
	mustWriteLog(t, root, "panda", "first.log", requestLog(hour.Add(10*time.Minute), "model-a", "first"), hour.Add(20*time.Minute))

	uploader := &matchingFakeObjectUploader{
		fakeObjectUploader: &fakeObjectUploader{errors: []error{errors.New("temporary network failure"), ErrObjectConflict}},
		matches:            true,
	}
	cfg := testConfig(root, workDir)
	cfg.Upload.Enabled = true
	service := mustTestService(t, cfg, uploader, now)
	if errRun := service.RunOnce(context.Background(), false); errRun == nil {
		t.Fatal("first upload unexpectedly succeeded")
	}
	state, errState := service.loadState()
	if errState != nil {
		t.Fatalf("load prepared state: %v", errState)
	}
	if len(state.PreparedHours) != 1 || len(state.Hours) != 0 {
		t.Fatalf("first run state = prepared:%d sealed:%d", len(state.PreparedHours), len(state.Hours))
	}
	firstObjectKey := uploader.calls[0].ObjectKey
	mustWriteLog(t, root, "alice", "late.log", requestLog(hour.Add(30*time.Minute), "model-b", "new source changes the batch size"), hour.Add(40*time.Minute))

	if errRun := service.RunOnce(context.Background(), false); errRun == nil {
		t.Fatal("late source for a newly sealed hour should be retained and reported")
	}
	if len(uploader.calls) != 2 {
		t.Fatalf("upload calls = %d, want fixed-key retry only", len(uploader.calls))
	}
	if uploader.calls[1].ObjectKey != firstObjectKey || strings.Contains(uploader.calls[1].ObjectKey, "-part") {
		t.Fatalf("prepared retry key = %q, want %q", uploader.calls[1].ObjectKey, firstObjectKey)
	}
	state, errState = service.loadState()
	if errState != nil {
		t.Fatalf("load recovered state: %v", errState)
	}
	if len(state.PreparedHours) != 0 || len(state.Hours) != 1 || len(state.Objects) != 1 {
		t.Fatalf("recovered state = prepared:%d sealed:%d objects:%d", len(state.PreparedHours), len(state.Hours), len(state.Objects))
	}
	if archives := findFilesWithSuffix(t, filepath.Join(workDir, "archives"), ".jsonl.zst"); len(archives) != 1 {
		t.Fatalf("production archives = %d, want exactly one", len(archives))
	}
}

func TestEnablingDeletionLaterCleansPreviouslyUploadedSources(t *testing.T) {
	t.Parallel()

	location := mustLocation(t, "Asia/Shanghai")
	now := time.Date(2026, time.July, 15, 4, 10, 0, 0, location)
	timestamp := time.Date(2026, time.July, 15, 2, 15, 0, 0, location)
	root := filepath.Join(t.TempDir(), "keys")
	workDir := filepath.Join(t.TempDir(), "uploader")
	sourcePath := mustWriteLog(t, root, "panda", "retained.log", requestLog(timestamp, "gpt-5.6-sol", "retained"), now.Add(-2*time.Hour))

	uploader := &fakeObjectUploader{}
	cfg := testConfig(root, workDir)
	cfg.Upload.Enabled = true
	service := mustTestService(t, cfg, uploader, now)
	if errRun := service.RunOnce(context.Background(), false); errRun != nil {
		t.Fatalf("upload with retention: %v", errRun)
	}
	service.cfg.Retention.DeleteSourceAfterUpload = true
	service.cfg.Retention.KeepLocalArchives = false
	if errRun := service.RunOnce(context.Background(), false); errRun != nil {
		t.Fatalf("retry local cleanup: %v", errRun)
	}
	if _, errStat := os.Stat(sourcePath); !errors.Is(errStat, os.ErrNotExist) {
		t.Fatalf("previously uploaded source was not deleted: %v", errStat)
	}
	if len(uploader.calls) != 1 {
		t.Errorf("source was uploaded again while enabling deletion: calls=%d", len(uploader.calls))
	}
	if _, errStat := os.Stat(uploader.calls[0].Path); !errors.Is(errStat, os.ErrNotExist) {
		t.Fatalf("previously retained archive was not deleted: %v", errStat)
	}
	state, errState := service.loadState()
	if errState != nil {
		t.Fatalf("load state: %v", errState)
	}
	if len(state.Uploaded) != 0 {
		t.Errorf("deleted source entries remain in state: %d", len(state.Uploaded))
	}
	for objectKey, object := range state.Objects {
		if object.ArchivePath != "" {
			t.Errorf("object %s still has pending local archive %s", objectKey, object.ArchivePath)
		}
	}
}

func TestDryRunNeverAppliesPendingRetentionCleanup(t *testing.T) {
	t.Parallel()

	location := mustLocation(t, "Asia/Shanghai")
	now := time.Date(2026, time.July, 15, 4, 10, 0, 0, location)
	timestamp := time.Date(2026, time.July, 15, 2, 15, 0, 0, location)
	root := filepath.Join(t.TempDir(), "keys")
	workDir := filepath.Join(t.TempDir(), "uploader")
	sourcePath := mustWriteLog(t, root, "panda", "dry-retention.log", requestLog(timestamp, "gpt-5.6-sol", "retained"), now.Add(-2*time.Hour))

	uploader := &fakeObjectUploader{}
	cfg := testConfig(root, workDir)
	cfg.Upload.Enabled = true
	service := mustTestService(t, cfg, uploader, now)
	if errRun := service.RunOnce(context.Background(), false); errRun != nil {
		t.Fatalf("initial upload: %v", errRun)
	}
	archivePath := uploader.calls[0].Path
	service.cfg.Retention.DeleteSourceAfterUpload = true
	service.cfg.Retention.KeepLocalArchives = false
	if errRun := service.RunOnce(context.Background(), true); errRun != nil {
		t.Fatalf("dry-run with pending cleanup: %v", errRun)
	}
	if _, errStat := os.Stat(sourcePath); errStat != nil {
		t.Fatalf("dry-run deleted retained source: %v", errStat)
	}
	if _, errStat := os.Stat(archivePath); errStat != nil {
		t.Fatalf("dry-run deleted retained archive: %v", errStat)
	}
}

func TestAuditFailurePreventsDestructiveCleanup(t *testing.T) {
	t.Parallel()

	location := mustLocation(t, "Asia/Shanghai")
	now := time.Date(2026, time.July, 15, 4, 10, 0, 0, location)
	timestamp := time.Date(2026, time.July, 15, 2, 15, 0, 0, location)
	root := filepath.Join(t.TempDir(), "keys")
	workDir := filepath.Join(t.TempDir(), "uploader")
	sourcePath := mustWriteLog(t, root, "panda", "audit-failure.log", requestLog(timestamp, "gpt-5.6-sol", "audit"), now.Add(-2*time.Hour))
	if errMkdir := os.MkdirAll(filepath.Join(workDir, "audit.jsonl"), 0o750); errMkdir != nil {
		t.Fatalf("create blocking audit directory: %v", errMkdir)
	}

	uploader := &fakeObjectUploader{}
	cfg := testConfig(root, workDir)
	cfg.Upload.Enabled = true
	cfg.Retention.DeleteSourceAfterUpload = true
	cfg.Retention.KeepLocalArchives = false
	service := mustTestService(t, cfg, uploader, now)
	if errRun := service.RunOnce(context.Background(), false); errRun == nil {
		t.Fatalf("upload unexpectedly succeeded without a durable audit record")
	}
	if _, errStat := os.Stat(sourcePath); errStat != nil {
		t.Fatalf("source was deleted after audit failure: %v", errStat)
	}
	if len(uploader.calls) != 1 {
		t.Fatalf("upload calls = %d, want 1", len(uploader.calls))
	}
	if _, errStat := os.Stat(uploader.calls[0].Path); errStat != nil {
		t.Fatalf("archive was deleted after audit failure: %v", errStat)
	}
	state, errState := service.loadState()
	if errState != nil {
		t.Fatalf("load prepared state after audit failure: %v", errState)
	}
	if len(state.PreparedHours) != 1 || len(state.Hours) != 0 || len(state.Uploaded) != 0 {
		t.Fatalf("audit failure state = prepared:%d sealed:%d uploaded:%d", len(state.PreparedHours), len(state.Hours), len(state.Uploaded))
	}
}

func TestSourceChangedAfterUploadIsRetainedWithoutSecondHourlyObject(t *testing.T) {
	t.Parallel()

	location := mustLocation(t, "Asia/Shanghai")
	now := time.Date(2026, time.July, 15, 4, 10, 0, 0, location)
	timestamp := time.Date(2026, time.July, 15, 2, 15, 0, 0, location)
	root := filepath.Join(t.TempDir(), "keys")
	workDir := filepath.Join(t.TempDir(), "uploader")
	sourcePath := mustWriteLog(t, root, "panda", "changing.log", requestLog(timestamp, "gpt-5.6-sol", "first"), now.Add(-2*time.Hour))

	uploader := &fakeObjectUploader{}
	uploader.onUpload = func() {
		file, errOpen := os.OpenFile(sourcePath, os.O_APPEND|os.O_WRONLY, 0)
		if errOpen != nil {
			t.Errorf("open source for simulated append: %v", errOpen)
			return
		}
		if _, errWrite := file.WriteString("late data\n"); errWrite != nil {
			t.Errorf("append simulated late data: %v", errWrite)
		}
		if errClose := file.Close(); errClose != nil {
			t.Errorf("close changed source: %v", errClose)
		}
		if errTimes := os.Chtimes(sourcePath, now.Add(-2*time.Hour), now.Add(-2*time.Hour)); errTimes != nil {
			t.Errorf("settle changed source: %v", errTimes)
		}
		uploader.onUpload = nil
	}
	cfg := testConfig(root, workDir)
	cfg.Upload.Enabled = true
	cfg.Retention.DeleteSourceAfterUpload = true
	service := mustTestService(t, cfg, uploader, now)
	if errRun := service.RunOnce(context.Background(), false); errRun != nil {
		t.Fatalf("upload changing source: %v", errRun)
	}
	if _, errStat := os.Stat(sourcePath); errStat != nil {
		t.Fatalf("changed source was deleted: %v", errStat)
	}
	if errRun := service.RunOnce(context.Background(), false); errRun == nil {
		t.Fatal("changed source unexpectedly reopened a finalized hour")
	}
	if _, errStat := os.Stat(sourcePath); errStat != nil {
		t.Fatalf("late changed source was not retained: %v", errStat)
	}
	if len(uploader.calls) != 1 {
		t.Errorf("upload calls = %d, want only the finalized hourly object", len(uploader.calls))
	}
	audit := readAudit(t, workDir)
	if got := audit[len(audit)-1].Status; got != "late_logs_retained" {
		t.Errorf("final audit status = %q, want late_logs_retained", got)
	}
}

func TestDeleteRejectsSameSizeSameModTimeContentReplacement(t *testing.T) {
	t.Parallel()

	location := mustLocation(t, "Asia/Shanghai")
	now := time.Date(2026, time.July, 15, 4, 10, 0, 0, location)
	timestamp := time.Date(2026, time.July, 15, 2, 15, 0, 0, location)
	modTime := now.Add(-2 * time.Hour)
	root := filepath.Join(t.TempDir(), "keys")
	workDir := filepath.Join(t.TempDir(), "uploader")
	original := requestLog(timestamp, "gpt-5.6-sol", "first")
	replacement := strings.Replace(original, "first", "other", 1)
	if len(original) != len(replacement) {
		t.Fatal("replacement fixture must preserve source size")
	}
	sourcePath := mustWriteLog(t, root, "panda", "same-identity.log", original, modTime)

	uploader := &fakeObjectUploader{}
	cfg := testConfig(root, workDir)
	cfg.Upload.Enabled = true
	service := mustTestService(t, cfg, uploader, now)
	if errRun := service.RunOnce(context.Background(), false); errRun != nil {
		t.Fatalf("initial upload: %v", errRun)
	}
	if errWrite := os.WriteFile(sourcePath, []byte(replacement), 0o600); errWrite != nil {
		t.Fatalf("replace source with same-size content: %v", errWrite)
	}
	if errTimes := os.Chtimes(sourcePath, modTime, modTime); errTimes != nil {
		t.Fatalf("restore source modtime: %v", errTimes)
	}
	service.cfg.Retention.DeleteSourceAfterUpload = true
	if errRun := service.RunOnce(context.Background(), false); errRun == nil {
		t.Fatal("backdated replacement should be retained and reported")
	}
	contents, errRead := os.ReadFile(sourcePath)
	if errRead != nil {
		t.Fatalf("replacement source was deleted: %v", errRead)
	}
	if string(contents) != replacement {
		t.Fatal("replacement source content changed")
	}
	if len(uploader.calls) != 1 {
		t.Fatalf("replacement caused another upload: calls=%d", len(uploader.calls))
	}
}

func TestUploadStateTargetMismatchFailsBeforeCleanup(t *testing.T) {
	t.Parallel()

	location := mustLocation(t, "Asia/Shanghai")
	now := time.Date(2026, time.July, 15, 4, 10, 0, 0, location)
	timestamp := time.Date(2026, time.July, 15, 2, 15, 0, 0, location)
	root := filepath.Join(t.TempDir(), "keys")
	workDir := filepath.Join(t.TempDir(), "uploader")
	sourcePath := mustWriteLog(t, root, "panda", "target.log", requestLog(timestamp, "model-a", "target"), now.Add(-2*time.Hour))

	firstUploader := &fakeObjectUploader{}
	firstConfig := testConfig(root, workDir)
	firstConfig.Upload.Enabled = true
	firstService := mustTestService(t, firstConfig, firstUploader, now)
	if errRun := firstService.RunOnce(context.Background(), false); errRun != nil {
		t.Fatalf("initial target upload: %v", errRun)
	}

	secondUploader := &fakeObjectUploader{}
	secondConfig := firstConfig
	secondConfig.Upload.ObjectPrefix = "different-prefix"
	secondConfig.Retention.DeleteSourceAfterUpload = true
	secondService := mustTestService(t, secondConfig, secondUploader, now)
	if errRun := secondService.RunOnce(context.Background(), false); errRun == nil || !strings.Contains(errRun.Error(), "target mismatch") {
		t.Fatalf("target mismatch error = %v", errRun)
	}
	if _, errStat := os.Stat(sourcePath); errStat != nil {
		t.Fatalf("target mismatch deleted source: %v", errStat)
	}
	if len(secondUploader.calls) != 0 {
		t.Fatalf("target mismatch made %d upload calls", len(secondUploader.calls))
	}
}

func TestSuccessfulUploadPersistsStateAndDeduplicatesSource(t *testing.T) {
	t.Parallel()

	location := mustLocation(t, "Asia/Shanghai")
	now := time.Date(2026, time.July, 15, 4, 10, 0, 0, location)
	timestamp := time.Date(2026, time.July, 15, 2, 15, 0, 0, location)
	root := filepath.Join(t.TempDir(), "keys")
	workDir := filepath.Join(t.TempDir(), "uploader")
	sourcePath := mustWriteLog(t, root, "panda", "v1-responses-2026-07-15T021500-state.log", requestLog(timestamp, "gpt-5.6-sol", "state test"), now.Add(-2*time.Hour))

	uploader := &fakeObjectUploader{}
	cfg := testConfig(root, workDir)
	cfg.Upload.Enabled = true
	cfg.Upload.Bucket = "llm-d1"
	cfg.Upload.ObjectPrefix = "/cliproxy-logs/"
	cfg.Retention.KeepLocalArchives = true
	service := mustTestService(t, cfg, uploader, now)

	if errRun := service.RunOnce(context.Background(), false); errRun != nil {
		t.Fatalf("first upload run: %v", errRun)
	}
	if len(uploader.calls) != 1 {
		t.Fatalf("upload calls after first run = %d, want 1", len(uploader.calls))
	}
	call := uploader.calls[0]
	if call.Bucket != "llm-d1" {
		t.Errorf("bucket = %q, want llm-d1", call.Bucket)
	}
	if !strings.HasPrefix(call.ObjectKey, "cliproxy-logs/2026/07/15/2026-07-15-02-codex56sol-") {
		t.Errorf("unexpected object key: %q", call.ObjectKey)
	}
	if strings.Contains(call.ObjectKey, "/panda/") {
		t.Errorf("object key unexpectedly contains key_name: %q", call.ObjectKey)
	}
	if _, errStat := os.Stat(sourcePath); errStat != nil {
		t.Fatalf("source should be retained: %v", errStat)
	}
	if _, errStat := os.Stat(call.Path); errStat != nil {
		t.Fatalf("local archive should be retained: %v", errStat)
	}

	rawState, errRead := os.ReadFile(service.statePath())
	if errRead != nil {
		t.Fatalf("read upload state: %v", errRead)
	}
	var state uploadState
	if errUnmarshal := json.Unmarshal(rawState, &state); errUnmarshal != nil {
		t.Fatalf("decode upload state: %v", errUnmarshal)
	}
	if len(state.Uploaded) != 1 {
		t.Fatalf("state uploaded entries = %d, want 1", len(state.Uploaded))
	}
	if finalized, exists := state.Hours["2026-07-15-02"]; !exists || finalized.ObjectKey != call.ObjectKey {
		t.Fatalf("finalized hour state = %+v, exists=%t; want object %s", finalized, exists, call.ObjectKey)
	}
	for _, uploaded := range state.Uploaded {
		if uploaded.ObjectKey != call.ObjectKey {
			t.Errorf("state object key = %q, want %q", uploaded.ObjectKey, call.ObjectKey)
		}
		if uploaded.UploadedAt.IsZero() {
			t.Errorf("state uploaded_at is zero")
		}
	}

	if errRun := service.RunOnce(context.Background(), false); errRun != nil {
		t.Fatalf("second upload run: %v", errRun)
	}
	if len(uploader.calls) != 1 {
		t.Fatalf("upload calls after second run = %d, want source to be deduplicated", len(uploader.calls))
	}
	if got := len(readAudit(t, workDir)); got != 1 {
		t.Fatalf("audit records after deduplicated run = %d, want 1", got)
	}
}

func TestLoadStateRejectsLegacyStateWithoutTargetBinding(t *testing.T) {
	t.Parallel()

	location := mustLocation(t, "Asia/Shanghai")
	root := filepath.Join(t.TempDir(), "keys")
	workDir := filepath.Join(t.TempDir(), "uploader")
	if errMkdir := os.MkdirAll(workDir, 0o750); errMkdir != nil {
		t.Fatalf("create work directory: %v", errMkdir)
	}
	objectKey := "cliproxy-logs/2026/07/15/2026-07-15-02-all-models-1G.jsonl.zst"
	rawState := `{"uploaded":{},"objects":{"` + objectKey + `":{"uploaded_at":"2026-07-15T03:05:00+08:00"}}}`
	if errWrite := os.WriteFile(filepath.Join(workDir, "state.json"), []byte(rawState), 0o600); errWrite != nil {
		t.Fatalf("write legacy state: %v", errWrite)
	}
	service := mustTestService(t, testConfig(root, workDir), nil, time.Date(2026, time.July, 15, 4, 0, 0, 0, location))
	if _, errLoad := service.loadState(); errLoad == nil || !strings.Contains(errLoad.Error(), "legacy upload state") {
		t.Fatalf("legacy state error = %v", errLoad)
	}
}

func TestArchiveHourUsesLogCompletionTimeAndPreservesRequestTimestamp(t *testing.T) {
	t.Parallel()

	location := mustLocation(t, "Asia/Shanghai")
	now := time.Date(2026, time.July, 15, 3, 6, 0, 0, location)
	requestTimestamp := time.Date(2026, time.July, 15, 1, 59, 0, 0, location)
	completionTime := time.Date(2026, time.July, 15, 2, 10, 0, 0, location)
	root := filepath.Join(t.TempDir(), "keys")
	workDir := filepath.Join(t.TempDir(), "uploader")
	mustWriteLog(t, root, "long-request", "v1-responses-2026-07-15T015900-long.log",
		requestLog(requestTimestamp, "gpt-5.6-sol", "completed after the hour"), completionTime)

	service := mustTestService(t, testConfig(root, workDir), nil, now)
	service.cfg.Upload.Enabled = true
	if errRun := service.RunOnce(context.Background(), true); errRun != nil {
		t.Fatalf("archive completed long request: %v", errRun)
	}
	archives := findFilesWithSuffix(t, workDir, ".jsonl.zst")
	if len(archives) != 1 || !strings.HasPrefix(filepath.Base(archives[0]), "2026-07-15-02-codex56sol-") {
		t.Fatalf("completion-hour archives = %v", archives)
	}
	var record struct {
		Timestamp time.Time `json:"timestamp"`
	}
	lines := nonemptyLines(readZstdFile(t, archives[0]))
	if len(lines) != 1 || json.Unmarshal(lines[0], &record) != nil {
		t.Fatalf("decode archived record: lines=%d", len(lines))
	}
	if !record.Timestamp.Equal(requestTimestamp) {
		t.Errorf("request timestamp = %s, want %s", record.Timestamp, requestTimestamp)
	}
}

func TestDeleteSourceAfterUploadSwitch(t *testing.T) {
	t.Parallel()

	location := mustLocation(t, "Asia/Shanghai")
	now := time.Date(2026, time.July, 15, 6, 10, 0, 0, location)
	timestamp := time.Date(2026, time.July, 15, 4, 30, 0, 0, location)

	tests := []struct {
		name       string
		delete     bool
		uploadErr  error
		wantExists bool
		wantStatus string
		wantDelete int
		wantRunErr bool
		wantAudits int
	}{
		{name: "switch disabled retains source", delete: false, wantExists: true, wantStatus: "uploaded", wantAudits: 1},
		{name: "switch enabled deletes source after success", delete: true, wantExists: false, wantStatus: "uploaded", wantDelete: 1, wantAudits: 2},
		{name: "failed upload never deletes source", delete: true, uploadErr: errors.New("TOS unavailable"), wantExists: true, wantStatus: "failed", wantRunErr: true, wantAudits: 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "keys")
			workDir := filepath.Join(t.TempDir(), "uploader")
			sourcePath := mustWriteLog(t, root, "key-owner", "v1-responses-2026-07-15T043000-delete.log", requestLog(timestamp, "claude-opus-4", test.name), now.Add(-2*time.Hour))
			uploader := &fakeObjectUploader{err: test.uploadErr}
			cfg := testConfig(root, workDir)
			cfg.Upload.Enabled = true
			cfg.Retention.DeleteSourceAfterUpload = test.delete
			cfg.Retention.KeepLocalArchives = true
			service := mustTestService(t, cfg, uploader, now)

			errRun := service.RunOnce(context.Background(), false)
			if (errRun != nil) != test.wantRunErr {
				t.Fatalf("RunOnce error = %v, want error %t", errRun, test.wantRunErr)
			}
			_, errStat := os.Stat(sourcePath)
			exists := errStat == nil
			if errStat != nil && !errors.Is(errStat, os.ErrNotExist) {
				t.Fatalf("stat source: %v", errStat)
			}
			if exists != test.wantExists {
				t.Errorf("source exists = %t, want %t", exists, test.wantExists)
			}
			audit := readAudit(t, workDir)
			if len(audit) != test.wantAudits {
				t.Fatalf("audit count = %d, want %d", len(audit), test.wantAudits)
			}
			finalAudit := audit[len(audit)-1]
			if finalAudit.Status != test.wantStatus || finalAudit.DeletedSources != test.wantDelete {
				t.Errorf("unexpected audit status/deletion: %+v", finalAudit)
			}
			if test.delete && test.uploadErr == nil && audit[0].Status != "uploaded_cleanup_pending" {
				t.Errorf("cleanup was not preceded by a durable audit record: %+v", audit[0])
			}
			if test.uploadErr != nil {
				if finalAudit.Error == "" {
					t.Errorf("failed audit record is missing error")
				}
				state, errState := service.loadState()
				if errState != nil {
					t.Fatalf("load failed upload state: %v", errState)
				}
				if len(state.PreparedHours) != 1 || len(state.Hours) != 0 || len(state.Uploaded) != 0 {
					t.Errorf("failed upload state = prepared:%d sealed:%d uploaded:%d", len(state.PreparedHours), len(state.Hours), len(state.Uploaded))
				}
			}
		})
	}
}

func TestScanSourcesRequiresCompleteSettledHour(t *testing.T) {
	t.Parallel()

	location := mustLocation(t, "Asia/Shanghai")
	now := time.Date(2026, time.July, 15, 3, 6, 0, 0, location)
	root := filepath.Join(t.TempDir(), "keys")
	workDir := filepath.Join(t.TempDir(), "uploader")

	mustWriteLog(t, root, "ready", "v1-responses-2026-07-15T025900-ready.log",
		requestLog(time.Date(2026, time.July, 15, 2, 59, 0, 0, location), "model-a", "ready"), now.Add(-8*time.Minute))
	mustWriteLog(t, root, "current-hour", "v1-responses-2026-07-15T030000-current.log",
		requestLog(time.Date(2026, time.July, 15, 3, 0, 0, 0, location), "model-b", "not complete"), now.Add(-time.Minute))
	mustWriteLog(t, root, "recent-write", "v1-responses-2026-07-15T010000-recent.log",
		requestLog(time.Date(2026, time.July, 15, 1, 0, 0, 0, location), "model-c", "still being written"), now.Add(-3*time.Minute))
	mustWriteLog(t, root, "exact-cutoff", "v1-responses-2026-07-15T010100-cutoff.log",
		requestLog(time.Date(2026, time.July, 15, 1, 1, 0, 0, location), "model-d", "settled"), now.Add(-7*time.Minute))

	cfg := testConfig(root, workDir)
	cfg.Schedule.SettleDelay = 5 * time.Minute
	service := mustTestService(t, cfg, nil, now)
	sources, errScan := service.scanSources(uploadState{Uploaded: make(map[string]uploadedSource)})
	if errScan != nil {
		t.Fatalf("scan sources: %v", errScan)
	}
	got := make([]string, 0, len(sources))
	for _, source := range sources {
		got = append(got, source.KeyName)
	}
	sort.Strings(got)
	want := []string{"exact-cutoff", "ready"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("ready key_names = %v, want %v", got, want)
	}
}

func TestRunOnStartImmediatelyProcessesHistoryButSkipsCurrentHour(t *testing.T) {
	t.Parallel()

	location := mustLocation(t, "Asia/Shanghai")
	now := time.Date(2026, time.July, 15, 4, 10, 0, 0, location)
	root := filepath.Join(t.TempDir(), "keys")
	workDir := filepath.Join(t.TempDir(), "uploader")
	firstHistoricalTimestamp := time.Date(2026, time.July, 15, 1, 15, 0, 0, location)
	secondHistoricalTimestamp := time.Date(2026, time.July, 15, 2, 15, 0, 0, location)
	currentTimestamp := time.Date(2026, time.July, 15, 4, 1, 0, 0, location)
	mustWriteLog(t, root, "history-one", "historical-one.log", requestLog(firstHistoricalTimestamp, "model-old-a", "ready one"), now.Add(-3*time.Hour))
	mustWriteLog(t, root, "history-two", "historical-two.log", requestLog(secondHistoricalTimestamp, "model-old-b", "ready two"), now.Add(-2*time.Hour))
	currentPath := mustWriteLog(t, root, "current", "current.log", requestLog(currentTimestamp, "model-current", "not ready"), now.Add(-time.Minute))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	uploader := &fakeObjectUploader{}
	uploader.onUpload = func() {
		if len(uploader.calls) == 2 {
			cancel()
		}
	}
	cfg := testConfig(root, workDir)
	cfg.Upload.Enabled = true
	cfg.Schedule.RunOnStart = true
	service := mustTestService(t, cfg, uploader, now)

	if errRun := service.Run(ctx, false); errRun != nil {
		t.Fatalf("run service with run-on-start: %v", errRun)
	}
	if len(uploader.calls) != 2 {
		t.Fatalf("startup upload calls = %d, want both completed historical hours", len(uploader.calls))
	}
	if !strings.Contains(uploader.calls[0].ObjectKey, "/2026/07/15/2026-07-15-01-codex56sol-") {
		t.Errorf("first startup object key = %q, want historical hour 01", uploader.calls[0].ObjectKey)
	}
	if !strings.Contains(uploader.calls[1].ObjectKey, "/2026/07/15/2026-07-15-02-codex56sol-") {
		t.Errorf("second startup object key = %q, want historical hour 02", uploader.calls[1].ObjectKey)
	}
	if _, errStat := os.Stat(currentPath); errStat != nil {
		t.Fatalf("current-hour source should remain untouched: %v", errStat)
	}
	state, errState := service.loadState()
	if errState != nil {
		t.Fatalf("load startup state: %v", errState)
	}
	if len(state.Uploaded) != 2 {
		t.Fatalf("uploaded source state entries = %d, want both historical sources", len(state.Uploaded))
	}
	for _, uploaded := range state.Uploaded {
		if !strings.HasPrefix(uploaded.RelativePath, "history-") {
			t.Errorf("current-hour source was uploaded at startup: %q", uploaded.RelativePath)
		}
	}
}

func TestNextScheduledRun(t *testing.T) {
	t.Parallel()

	location := mustLocation(t, "Asia/Shanghai")
	tests := []struct {
		name string
		now  time.Time
		want time.Time
	}{
		{
			name: "before settle point in current hour",
			now:  time.Date(2026, time.July, 15, 10, 2, 0, 0, location),
			want: time.Date(2026, time.July, 15, 10, 5, 0, 0, location),
		},
		{
			name: "exact settle point schedules next hour",
			now:  time.Date(2026, time.July, 15, 10, 5, 0, 0, location),
			want: time.Date(2026, time.July, 15, 11, 5, 0, 0, location),
		},
		{
			name: "after settle point schedules next hour",
			now:  time.Date(2026, time.July, 15, 10, 45, 0, 0, location),
			want: time.Date(2026, time.July, 15, 11, 5, 0, 0, location),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := nextScheduledRun(test.now, time.Hour, 5*time.Minute); !got.Equal(test.want) {
				t.Errorf("next run = %s, want %s", got, test.want)
			}
		})
	}
}

func TestNextDelayUsesCatchUpWhenSettledSourceBacklogExists(t *testing.T) {
	t.Parallel()

	location := mustLocation(t, "Asia/Shanghai")
	// Mid-hour: normal schedule would wait until next :05; backlog must not wait that long.
	now := time.Date(2026, time.July, 15, 10, 45, 0, 0, location)
	root := filepath.Join(t.TempDir(), "keys")
	workDir := filepath.Join(t.TempDir(), "uploader")
	// Settled historical log (hour 08 ready well before now).
	mustWriteLog(t, root, "user-a", "old.log",
		requestLog(time.Date(2026, time.July, 15, 8, 10, 0, 0, location), "gpt", "backlog"),
		now.Add(-2*time.Hour))

	cfg := testConfig(root, workDir)
	cfg.Schedule.SettleDelay = 5 * time.Minute
	cfg.Schedule.CatchUpDelay = 2 * time.Minute
	service := mustTestService(t, cfg, &fakeObjectUploader{}, now)

	hasWork, errHas := service.hasCatchUpWork()
	if errHas != nil {
		t.Fatalf("hasCatchUpWork: %v", errHas)
	}
	if !hasWork {
		t.Fatal("hasCatchUpWork() = false, want true for settled source backlog")
	}
	delay := service.nextDelay()
	if delay != 2*time.Minute {
		t.Fatalf("nextDelay() = %s, want catch-up delay 2m (not next hour boundary)", delay)
	}
}

func TestNextDelayUsesHourlyScheduleWhenNoBacklog(t *testing.T) {
	t.Parallel()

	location := mustLocation(t, "Asia/Shanghai")
	now := time.Date(2026, time.July, 15, 10, 45, 0, 0, location)
	root := filepath.Join(t.TempDir(), "keys")
	workDir := filepath.Join(t.TempDir(), "uploader")
	cfg := testConfig(root, workDir)
	cfg.Schedule.SettleDelay = 5 * time.Minute
	cfg.Schedule.CatchUpDelay = 2 * time.Minute
	service := mustTestService(t, cfg, &fakeObjectUploader{}, now)

	hasWork, errHas := service.hasCatchUpWork()
	if errHas != nil {
		t.Fatalf("hasCatchUpWork: %v", errHas)
	}
	if hasWork {
		t.Fatal("hasCatchUpWork() = true, want false with empty logs root")
	}
	delay := service.nextDelay()
	// From 10:45, next scheduled run is 11:05 → 20 minutes.
	want := 20 * time.Minute
	if delay < want-time.Second || delay > want+time.Second {
		t.Fatalf("nextDelay() = %s, want about %s", delay, want)
	}
}

func TestRunCatchUpProcessesMultipleHoursWithoutScheduleGap(t *testing.T) {
	t.Parallel()

	location := mustLocation(t, "Asia/Shanghai")
	now := time.Date(2026, time.July, 15, 5, 10, 0, 0, location)
	root := filepath.Join(t.TempDir(), "keys")
	workDir := filepath.Join(t.TempDir(), "uploader")
	mustWriteLog(t, root, "u1", "h1.log",
		requestLog(time.Date(2026, time.July, 15, 1, 10, 0, 0, location), "m", "h1"), now.Add(-4*time.Hour))
	mustWriteLog(t, root, "u2", "h2.log",
		requestLog(time.Date(2026, time.July, 15, 2, 10, 0, 0, location), "m", "h2"), now.Add(-3*time.Hour))
	mustWriteLog(t, root, "u3", "h3.log",
		requestLog(time.Date(2026, time.July, 15, 3, 10, 0, 0, location), "m", "h3"), now.Add(-2*time.Hour))

	cfg := testConfig(root, workDir)
	cfg.Upload.Enabled = true
	cfg.Schedule.SettleDelay = 0
	uploader := &fakeObjectUploader{}
	service := mustTestService(t, cfg, uploader, now)

	// One catch-up burst must archive all three settled hours.
	service.runCatchUp(context.Background(), false, "test")
	if len(uploader.calls) != 3 {
		t.Fatalf("upload calls = %d, want 3 consecutive hours in one catch-up", len(uploader.calls))
	}
	hasWork, errHas := service.hasCatchUpWork()
	if errHas != nil {
		t.Fatalf("hasCatchUpWork after catch-up: %v", errHas)
	}
	if hasWork {
		t.Fatal("hasCatchUpWork() still true after processing all settled hours")
	}
}

func testConfig(root, workDir string) Config {
	return Config{
		LogsRoot: root,
		WorkDir:  workDir,
		Timezone: "Asia/Shanghai",
		Schedule: ScheduleConfig{
			Interval:     time.Hour,
			SettleDelay:  0,
			CatchUpDelay: 5 * time.Minute,
		},
		Upload: UploadConfig{
			Endpoint:     "https://tos-cn-beijing.volces.com",
			Region:       "cn-beijing",
			Bucket:       "llm-d1",
			ObjectPrefix: "cliproxy-logs",
		},
		Retention: RetentionConfig{KeepLocalArchives: true},
	}
}

func mustTestService(t *testing.T, cfg Config, uploader ObjectUploader, now time.Time) *Service {
	t.Helper()
	service, errNew := NewService(cfg, uploader)
	if errNew != nil {
		t.Fatalf("create service: %v", errNew)
	}
	service.now = func() time.Time { return now }
	return service
}

func requestLog(timestamp time.Time, model, payload string) string {
	return "Timestamp: " + timestamp.Format(time.RFC3339Nano) + "\n" +
		"=== REQUEST BODY ===\n" +
		`{"model":` + string(mustJSON(model)) + `,"input":` + string(mustJSON(payload)) + "}\n" +
		"=== RESPONSE ===\n" +
		`{"ok":true}` + "\n"
}

func mustJSON(value string) []byte {
	encoded, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		panic(errMarshal)
	}
	return encoded
}

func findFilesWithSuffix(t *testing.T, root, suffix string) []string {
	t.Helper()
	var matches []string
	errWalk := filepath.Walk(root, func(path string, info os.FileInfo, errWalk error) error {
		if errWalk != nil {
			return errWalk
		}
		if !info.IsDir() && strings.HasSuffix(info.Name(), suffix) {
			matches = append(matches, path)
		}
		return nil
	})
	if errWalk != nil {
		t.Fatalf("walk %s: %v", root, errWalk)
	}
	return matches
}

func readZstdFile(t *testing.T, path string) []byte {
	t.Helper()
	file, errOpen := os.Open(path)
	if errOpen != nil {
		t.Fatalf("open Zstandard archive: %v", errOpen)
	}
	defer func() {
		if errClose := file.Close(); errClose != nil {
			t.Errorf("close Zstandard archive: %v", errClose)
		}
	}()
	decoder, errDecoder := zstd.NewReader(file)
	if errDecoder != nil {
		t.Fatalf("create Zstandard decoder: %v", errDecoder)
	}
	defer decoder.Close()
	data, errRead := io.ReadAll(decoder)
	if errRead != nil {
		t.Fatalf("decompress archive: %v", errRead)
	}
	return data
}

func nonemptyLines(data []byte) [][]byte {
	var lines [][]byte
	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(line)) > 0 {
			lines = append(lines, line)
		}
	}
	return lines
}

func readAudit(t *testing.T, workDir string) []auditRecord {
	t.Helper()
	raw, errRead := os.ReadFile(filepath.Join(workDir, "audit.jsonl"))
	if errRead != nil {
		t.Fatalf("read audit log: %v", errRead)
	}
	lines := nonemptyLines(raw)
	records := make([]auditRecord, 0, len(lines))
	for index, line := range lines {
		var record auditRecord
		if errUnmarshal := json.Unmarshal(line, &record); errUnmarshal != nil {
			t.Fatalf("decode audit line %d: %v", index+1, errUnmarshal)
		}
		records = append(records, record)
	}
	return records
}
