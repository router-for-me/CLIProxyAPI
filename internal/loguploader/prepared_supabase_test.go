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
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/volcengine/ve-tos-golang-sdk/v2/tos"
	"github.com/volcengine/ve-tos-golang-sdk/v2/tos/codes"
)

type forcedMultipartObjectUploader struct {
	uploader *TOSUploader
}

func (u *forcedMultipartObjectUploader) UploadFile(ctx context.Context, bucket, objectKey, path string, expected objectIdentity) error {
	return u.uploader.uploadMultipart(ctx, bucket, objectKey, path, expected)
}

func (u *forcedMultipartObjectUploader) MatchObject(ctx context.Context, bucket, objectKey string, expected objectIdentity) (bool, error) {
	return u.uploader.MatchObject(ctx, bucket, objectKey, expected)
}

func TestPreparedUploadSuccessEnqueuesExactSupabaseEvent(t *testing.T) {
	service, uploader, state, hourKey, prepared := preparedSupabaseFixture(t)
	t.Setenv(service.cfg.Supabase.IngestTokenEnv, "test-supabase-token")
	service.supabaseHTTPDoer = httpDoerFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("temporary delivery failure")
	})

	errComplete := service.completePreparedHour(context.Background(), hourKey, prepared, &state)
	if !errors.Is(errComplete, errSupabaseDeliveryRetryable) {
		t.Errorf("complete prepared hour error = %v, want generic retryable Supabase delivery error", errComplete)
	}
	if len(uploader.calls) != 1 {
		t.Fatalf("TOS upload calls = %d, want 1", len(uploader.calls))
	}

	wantEvent, errPrepare := service.prepareSupabaseEvent(prepared)
	if errPrepare != nil {
		t.Fatalf("prepare expected Supabase event: %v", errPrepare)
	}
	loaded, errLoad := service.loadState()
	if errLoad != nil {
		t.Fatalf("reload committed upload state: %v", errLoad)
	}
	if len(loaded.PreparedHours) != 0 || len(loaded.Hours) != 1 || len(loaded.Objects) != 1 || len(loaded.Uploaded) != len(prepared.Sources) {
		t.Fatalf("committed state = prepared:%d hours:%d objects:%d uploaded:%d", len(loaded.PreparedHours), len(loaded.Hours), len(loaded.Objects), len(loaded.Uploaded))
	}
	entry, exists := loaded.SupabaseOutbox.Entries[wantEvent.EventID()]
	if !exists {
		t.Fatalf("pending Supabase event %s missing: %#v", wantEvent.EventID(), loaded.SupabaseOutbox.Entries)
	}
	if entry.Status != supabaseOutboxStatusPending || entry.HourKey != hourKey || entry.ObjectKey != prepared.ObjectKey || !bytes.Equal(entry.Payload, wantEvent.RawJSON()) {
		t.Errorf("pending Supabase entry is not the exact prepared event: %#v", entry)
	}
	if got := uploadedHourSupabaseEventID(t, loaded.Hours[hourKey]); got != wantEvent.EventID() {
		t.Errorf("committed hour supabase_event_id = %q, want %q", got, wantEvent.EventID())
	}
	if got := lastAuditSupabaseEventID(t, service.cfg.WorkDir); got != wantEvent.EventID() {
		t.Errorf("successful audit supabase_event_id = %q, want %q", got, wantEvent.EventID())
	}
}

func TestPreparedUploadFailureAuditOmitsSupabaseEventID(t *testing.T) {
	service, uploader, _, _, _ := preparedSupabaseFixture(t)
	if len(uploader.calls) != 0 {
		t.Fatalf("fixture upload calls after reset = %d, want 0", len(uploader.calls))
	}
	audits := readAudit(t, service.cfg.WorkDir)
	if got := audits[len(audits)-1].SupabaseEventID; got != "" {
		t.Errorf("failed TOS audit supabase_event_id = %q, want empty", got)
	}
}

func TestMatchingObjectConflictEnqueuesExactSupabaseEvent(t *testing.T) {
	service, baseUploader, state, hourKey, prepared := preparedSupabaseFixture(t)
	matcher := &matchingFakeObjectUploader{
		fakeObjectUploader: baseUploader,
		matches:            true,
	}
	baseUploader.err = ErrObjectConflict
	service.uploader = matcher
	t.Setenv(service.cfg.Supabase.IngestTokenEnv, "test-supabase-token")
	service.supabaseHTTPDoer = httpDoerFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("temporary delivery failure")
	})

	errComplete := service.completePreparedHour(context.Background(), hourKey, prepared, &state)
	if !errors.Is(errComplete, errSupabaseDeliveryRetryable) {
		t.Errorf("matching conflict completion error = %v, want generic retryable Supabase delivery error", errComplete)
	}
	if len(baseUploader.calls) != 1 || matcher.matchCalls != 1 {
		t.Fatalf("conflict recovery calls = upload:%d match:%d, want 1 each", len(baseUploader.calls), matcher.matchCalls)
	}
	wantEvent, errPrepare := service.prepareSupabaseEvent(prepared)
	if errPrepare != nil {
		t.Fatalf("prepare expected Supabase event: %v", errPrepare)
	}
	loaded, errLoad := service.loadState()
	if errLoad != nil {
		t.Fatalf("reload conflict recovery state: %v", errLoad)
	}
	entry, exists := loaded.SupabaseOutbox.Entries[wantEvent.EventID()]
	if !exists || !bytes.Equal(entry.Payload, wantEvent.RawJSON()) {
		t.Fatalf("matching conflict did not commit exact deterministic event: %#v", entry)
	}
}

func TestMultipartObjectConflictMatchesUploadedMetadataAndCommitsSupabaseEvent(t *testing.T) {
	service, _, state, hourKey, prepared := preparedSupabaseFixture(t)
	archiveInfo, errStat := os.Stat(prepared.ArchivePath)
	if errStat != nil {
		t.Fatalf("stat prepared archive: %v", errStat)
	}
	client := &fakeTOSObjectClient{
		createErr: &tos.TosServerError{
			RequestInfo: tos.RequestInfo{StatusCode: http.StatusConflict},
			Code:        codes.DuplicateObject,
		},
		headMetadataFromCreate: true,
		headContentLength:      archiveInfo.Size(),
	}
	service.uploader = &forcedMultipartObjectUploader{uploader: &TOSUploader{client: client}}
	t.Setenv(service.cfg.Supabase.IngestTokenEnv, "test-supabase-token")
	service.supabaseHTTPDoer = httpDoerFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("temporary delivery failure")
	})

	errComplete := service.completePreparedHour(context.Background(), hourKey, prepared, &state)
	if !errors.Is(errComplete, errSupabaseDeliveryRetryable) {
		t.Fatalf("multipart conflict completion error = %v, want retryable Supabase delivery error", errComplete)
	}
	if client.createCalls != 1 || client.headCalls != 1 || client.createInput == nil {
		t.Fatalf("multipart conflict calls = create:%d head:%d, want 1 each", client.createCalls, client.headCalls)
	}
	if got := client.createInput.Meta[archiveChecksumMetadataKey]; got != prepared.ArchiveSHA256 {
		t.Fatalf("multipart metadata = %q, want prepared archive SHA-256 %q", got, prepared.ArchiveSHA256)
	}

	wantEvent, errPrepare := service.prepareSupabaseEvent(prepared)
	if errPrepare != nil {
		t.Fatalf("prepare expected Supabase event: %v", errPrepare)
	}
	loaded, errLoad := service.loadState()
	if errLoad != nil {
		t.Fatalf("reload multipart conflict state: %v", errLoad)
	}
	if _, preparedExists := loaded.PreparedHours[hourKey]; preparedExists || loaded.Hours[hourKey].Status != "sealed" {
		t.Fatalf("multipart conflict was not committed: prepared=%t hour=%#v", preparedExists, loaded.Hours[hourKey])
	}
	entry, entryExists := loaded.SupabaseOutbox.Entries[wantEvent.EventID()]
	if !entryExists || entry.Status != supabaseOutboxStatusPending || !bytes.Equal(entry.Payload, wantEvent.RawJSON()) {
		t.Fatalf("multipart conflict did not retain exact pending event: %#v", entry)
	}
	audits := readAudit(t, service.cfg.WorkDir)
	finalAudit := audits[len(audits)-1]
	if finalAudit.Status != "uploaded" || finalAudit.SupabaseEventID != wantEvent.EventID() {
		t.Fatalf("multipart conflict audit = %#v, want successful upload with event ID", finalAudit)
	}
}

func TestMultipartObjectConflictChecksumMismatchRetainsPreparedHour(t *testing.T) {
	service, _, state, hourKey, prepared := preparedSupabaseFixture(t)
	archiveInfo, errStat := os.Stat(prepared.ArchivePath)
	if errStat != nil {
		t.Fatalf("stat prepared archive: %v", errStat)
	}
	client := &fakeTOSObjectClient{
		createErr: &tos.TosServerError{
			RequestInfo: tos.RequestInfo{StatusCode: http.StatusConflict},
			Code:        codes.DuplicateObject,
		},
		headResult: &tos.HeadObjectV2Output{
			ObjectMetaV2: tos.ObjectMetaV2{
				ContentLength: archiveInfo.Size(),
				Meta: fakeTOSMetadata{
					archiveChecksumMetadataKey: strings.Repeat("0", 64),
				},
			},
		},
	}
	service.uploader = &forcedMultipartObjectUploader{uploader: &TOSUploader{client: client}}
	requestCount := 0
	service.supabaseHTTPDoer = httpDoerFunc(func(*http.Request) (*http.Response, error) {
		requestCount++
		return nil, errors.New("must not deliver an uncommitted event")
	})

	errComplete := service.completePreparedHour(context.Background(), hourKey, prepared, &state)
	if errComplete == nil || !strings.Contains(errComplete.Error(), "checksum or size differs") {
		t.Fatalf("multipart checksum mismatch error = %v", errComplete)
	}
	if requestCount != 0 {
		t.Fatalf("Supabase requests = %d, want 0 before conflict recovery succeeds", requestCount)
	}
	loaded, errLoad := service.loadState()
	if errLoad != nil {
		t.Fatalf("reload checksum mismatch state: %v", errLoad)
	}
	for label, got := range map[string]uploadState{"caller": state, "reload": loaded} {
		if _, exists := got.PreparedHours[hourKey]; !exists || len(got.Hours) != 0 || len(got.Objects) != 0 || len(got.SupabaseOutbox.Entries) != 0 {
			t.Fatalf("%s checksum mismatch state mutated: prepared=%d hours=%d objects=%d outbox=%d", label, len(got.PreparedHours), len(got.Hours), len(got.Objects), len(got.SupabaseOutbox.Entries))
		}
	}
	if got := lastAuditSupabaseEventID(t, service.cfg.WorkDir); got != "" {
		t.Fatalf("checksum mismatch audit event ID = %q, want empty", got)
	}
}

func TestPreparedUploadRejectsSameSizeArchiveReplacementBeforeTOS(t *testing.T) {
	tests := []struct {
		name     string
		uploader func(*fakeTOSObjectClient) ObjectUploader
	}{
		{
			name: "single PUT",
			uploader: func(client *fakeTOSObjectClient) ObjectUploader {
				return &TOSUploader{client: client}
			},
		},
		{
			name: "multipart create",
			uploader: func(client *fakeTOSObjectClient) ObjectUploader {
				return &forcedMultipartObjectUploader{uploader: &TOSUploader{client: client}}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, _, state, hourKey, prepared := preparedSupabaseFixture(t)
			original, errRead := os.ReadFile(prepared.ArchivePath)
			if errRead != nil {
				t.Fatalf("read prepared archive: %v", errRead)
			}
			mutated := bytes.Clone(original)
			mutated[len(mutated)/2] ^= 0xff
			if errWrite := os.WriteFile(prepared.ArchivePath, mutated, 0o640); errWrite != nil {
				t.Fatalf("replace prepared archive with same-size content: %v", errWrite)
			}
			mutatedSHA256, mutatedSize, errChecksum := fileSHA256(prepared.ArchivePath)
			if errChecksum != nil {
				t.Fatalf("checksum mutated archive: %v", errChecksum)
			}
			if mutatedSize != prepared.CompressedBytes || mutatedSHA256 == prepared.ArchiveSHA256 {
				t.Fatalf("invalid same-size mutation fixture: size=%d want=%d sha_changed=%t", mutatedSize, prepared.CompressedBytes, mutatedSHA256 != prepared.ArchiveSHA256)
			}

			client := &fakeTOSObjectClient{putV2Result: &tos.PutObjectV2Output{}}
			service.uploader = test.uploader(client)
			t.Setenv(service.cfg.Supabase.IngestTokenEnv, "sensitive-supabase-token")
			supabaseRequests := 0
			service.supabaseHTTPDoer = httpDoerFunc(func(*http.Request) (*http.Response, error) {
				supabaseRequests++
				return nil, errors.New("must not enqueue a replaced archive")
			})

			errComplete := service.completePreparedHour(context.Background(), hourKey, prepared, &state)
			if errComplete == nil || !strings.Contains(errComplete.Error(), "prepared archive identity mismatch") {
				t.Fatalf("same-size replacement error = %v, want prepared archive identity mismatch", errComplete)
			}
			if client.putV2Calls != 0 || client.calls != 0 || client.createCalls != 0 || client.headCalls != 0 || client.abortCalls != 0 {
				t.Fatalf("same-size replacement reached TOS: put_v2=%d put_from_file=%d create=%d head=%d abort=%d", client.putV2Calls, client.calls, client.createCalls, client.headCalls, client.abortCalls)
			}
			if supabaseRequests != 0 {
				t.Fatalf("same-size replacement reached Supabase: requests=%d", supabaseRequests)
			}
			loaded, errLoad := service.loadState()
			if errLoad != nil {
				t.Fatalf("reload same-size replacement state: %v", errLoad)
			}
			for label, got := range map[string]uploadState{"caller": state, "reload": loaded} {
				if _, exists := got.PreparedHours[hourKey]; !exists || len(got.Hours) != 0 || len(got.Objects) != 0 || len(got.SupabaseOutbox.Entries) != 0 {
					t.Fatalf("%s same-size replacement state mutated: prepared=%d hours=%d objects=%d outbox=%d", label, len(got.PreparedHours), len(got.Hours), len(got.Objects), len(got.SupabaseOutbox.Entries))
				}
			}
			for _, secret := range []string{prepared.ArchivePath, prepared.ArchiveSHA256, mutatedSHA256, "sensitive-supabase-token"} {
				if strings.Contains(errComplete.Error(), secret) {
					t.Fatalf("same-size replacement error leaked sensitive identity data: %v", errComplete)
				}
			}
		})
	}
}

func TestPreparedUploadAttemptsOnlyNewPreferredEvent(t *testing.T) {
	service, _, state, hourKey, prepared := preparedSupabaseFixture(t)
	existingState, olderEntry := validStateOutboxEntry(t, service, "older-configuration-failure")
	for objectKey, object := range existingState.Objects {
		state.Objects[objectKey] = object
	}
	for existingHourKey, hour := range existingState.Hours {
		state.Hours[existingHourKey] = hour
	}
	state.SupabaseOutbox.Entries[olderEntry.EventID] = olderEntry
	wantNewEvent, errPrepare := service.prepareSupabaseEvent(prepared)
	if errPrepare != nil {
		t.Fatalf("prepare new event identity: %v", errPrepare)
	}
	t.Setenv(service.cfg.Supabase.IngestTokenEnv, "preferred-event-token")
	var requestOrder []string
	service.supabaseHTTPDoer = httpDoerFunc(func(request *http.Request) (*http.Response, error) {
		eventID := eventIDFromDeliveryRequest(t, request)
		requestOrder = append(requestOrder, eventID)
		if eventID == wantNewEvent.EventID() {
			body := `{"status":"inserted","event_id":"` + eventID + `"}`
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
		}
		return &http.Response{StatusCode: http.StatusUnauthorized, Body: io.NopCloser(strings.NewReader("untrusted"))}, nil
	})

	errComplete := service.completePreparedHour(context.Background(), hourKey, prepared, &state)
	if errComplete != nil {
		t.Fatalf("preferred delivery error = %v", errComplete)
	}
	wantOrder := []string{wantNewEvent.EventID()}
	if strings.Join(requestOrder, "|") != strings.Join(wantOrder, "|") {
		t.Fatalf("post-upload delivery order = %v, want only the new preferred event %v", requestOrder, wantOrder)
	}
	loaded, errLoad := service.loadState()
	if errLoad != nil {
		t.Fatalf("reload preferred delivery state: %v", errLoad)
	}
	if _, exists := loaded.SupabaseOutbox.Entries[wantNewEvent.EventID()]; exists {
		t.Fatalf("acknowledged new event %s remained pending", wantNewEvent.EventID())
	}
	if got := loaded.SupabaseOutbox.Entries[olderEntry.EventID]; got.Status != supabaseOutboxStatusPending {
		t.Fatalf("older configuration event changed: %#v", got)
	}
}

func TestPreparedUploadsAttemptEachNewPreferredEventOnceWithRetryableBacklog(t *testing.T) {
	location := mustLocation(t, "Asia/Shanghai")
	now := time.Date(2026, time.July, 15, 6, 10, 0, 0, location)
	root := filepath.Join(t.TempDir(), "keys")
	workDir := filepath.Join(t.TempDir(), "uploader")
	for index := 0; index < 3; index++ {
		hour := time.Date(2026, time.July, 15, 1+index, 0, 0, 0, location)
		mustWriteLog(t, root, "panda", fmt.Sprintf("prepared-%d.log", index), requestLog(hour.Add(15*time.Minute), "gpt-5.6-sol", "prepared Supabase"), hour.Add(30*time.Minute))
	}
	cfg := testConfig(root, workDir)
	cfg.Upload.Enabled = true
	cfg.Supabase = SupabaseConfig{
		Enabled:        true,
		IngestURL:      "https://project-ref.supabase.co/functions/v1/log-stats-ingest",
		IngestTokenEnv: "LOG_STATS_INGEST_TOKEN",
	}
	uploader := &fakeObjectUploader{err: errors.New("retain prepared batches")}
	service := mustTestService(t, cfg, uploader, now)
	if errRun := service.RunOnce(context.Background(), false); errRun == nil {
		t.Fatal("initial run unexpectedly completed instead of retaining prepared state")
	}
	state, errLoad := service.loadState()
	if errLoad != nil {
		t.Fatalf("load prepared Supabase state: %v", errLoad)
	}
	if len(state.PreparedHours) != 3 {
		t.Fatalf("prepared hours = %d, want 3", len(state.PreparedHours))
	}
	uploader.err = nil
	t.Setenv(service.cfg.Supabase.IngestTokenEnv, "preferred-event-token")
	requests := 0
	service.supabaseHTTPDoer = httpDoerFunc(func(*http.Request) (*http.Response, error) {
		requests++
		return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(strings.NewReader("retry"))}, nil
	})
	hourKeys := make([]string, 0, len(state.PreparedHours))
	for hourKey := range state.PreparedHours {
		hourKeys = append(hourKeys, hourKey)
	}
	sort.Strings(hourKeys)
	for _, hourKey := range hourKeys {
		prepared := state.PreparedHours[hourKey]
		if errComplete := service.completePreparedHour(context.Background(), hourKey, prepared, &state); !errors.Is(errComplete, errSupabaseDeliveryRetryable) {
			t.Fatalf("complete %s error = %v, want retryable", hourKey, errComplete)
		}
	}
	if requests != len(hourKeys) {
		t.Fatalf("preferred requests = %d, want %d (one per new event)", requests, len(hourKeys))
	}
}

func TestPreparedUploadMissingTokenMakesNoRequestAndRetainsPreferredNewEvent(t *testing.T) {
	service, _, state, hourKey, prepared := preparedSupabaseFixture(t)
	existingState, olderEntry := validStateOutboxEntry(t, service, "older-missing-token")
	for objectKey, object := range existingState.Objects {
		state.Objects[objectKey] = object
	}
	for existingHourKey, hour := range existingState.Hours {
		state.Hours[existingHourKey] = hour
	}
	state.SupabaseOutbox.Entries[olderEntry.EventID] = olderEntry
	wantNewEvent, errPrepare := service.prepareSupabaseEvent(prepared)
	if errPrepare != nil {
		t.Fatalf("prepare new event identity: %v", errPrepare)
	}
	t.Setenv(service.cfg.Supabase.IngestTokenEnv, "")
	requestCount := 0
	service.supabaseHTTPDoer = httpDoerFunc(func(*http.Request) (*http.Response, error) {
		requestCount++
		return nil, errors.New("must not send without a token")
	})

	errComplete := service.completePreparedHour(context.Background(), hourKey, prepared, &state)
	if !errors.Is(errComplete, errSupabaseDeliveryConfiguration) {
		t.Fatalf("missing-token completion error = %v, want configuration error", errComplete)
	}
	if requestCount != 0 {
		t.Fatalf("missing-token requests = %d, want 0", requestCount)
	}
	loaded, errLoad := service.loadState()
	if errLoad != nil {
		t.Fatalf("reload missing-token state: %v", errLoad)
	}
	for _, eventID := range []string{wantNewEvent.EventID(), olderEntry.EventID} {
		if got := loaded.SupabaseOutbox.Entries[eventID]; got.Status != supabaseOutboxStatusPending {
			t.Fatalf("missing-token event %s changed: %#v", eventID, got)
		}
	}
}

func TestPreparedUploadCommitTimestampsUseUploadCompletionTime(t *testing.T) {
	service, uploader, state, hourKey, prepared := preparedSupabaseFixture(t)
	startedAt := service.now()
	completedAt := startedAt.Add(37 * time.Minute)
	currentTime := startedAt
	service.now = func() time.Time { return currentTime }
	uploader.onUpload = func() { currentTime = completedAt }
	t.Setenv(service.cfg.Supabase.IngestTokenEnv, "timestamp-token")
	service.supabaseHTTPDoer = httpDoerFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("retain timestamp event")
	})

	errComplete := service.completePreparedHour(context.Background(), hourKey, prepared, &state)
	if !errors.Is(errComplete, errSupabaseDeliveryRetryable) {
		t.Fatalf("timestamp completion error = %v, want retryable delivery error", errComplete)
	}
	wantEvent, errPrepare := service.prepareSupabaseEvent(prepared)
	if errPrepare != nil {
		t.Fatalf("prepare timestamp event: %v", errPrepare)
	}
	loaded, errLoad := service.loadState()
	if errLoad != nil {
		t.Fatalf("reload timestamp state: %v", errLoad)
	}
	assertCompletedAt := func(label string, got time.Time) {
		t.Helper()
		if !got.Equal(completedAt) {
			t.Errorf("%s = %s, want upload completion time %s", label, got, completedAt)
		}
	}
	assertCompletedAt("hour uploaded_at", loaded.Hours[hourKey].UploadedAt)
	assertCompletedAt("object uploaded_at", loaded.Objects[prepared.ObjectKey].UploadedAt)
	assertCompletedAt("object verified_at", loaded.Objects[prepared.ObjectKey].VerifiedAt)
	for fingerprint, source := range loaded.Uploaded {
		assertCompletedAt("source "+fingerprint+" uploaded_at", source.UploadedAt)
	}
	assertCompletedAt("outbox enqueued_at", loaded.SupabaseOutbox.Entries[wantEvent.EventID()].EnqueuedAt)
}

func TestPreparedCommitPreflightTimestampBoundsCompletionCandidateJSON(t *testing.T) {
	service, _, state, hourKey, prepared := preparedSupabaseFixture(t)
	preflightAt := preparedCommitPreflightTimestamp()
	encodedPreflightAt, errEncodeTime := preflightAt.MarshalJSON()
	if errEncodeTime != nil {
		t.Fatalf("encode preflight timestamp: %v", errEncodeTime)
	}
	const wantWidestRFC3339Nano = `"9999-12-31T23:59:59.999999999+23:59"`
	if string(encodedPreflightAt) != wantWidestRFC3339Nano {
		t.Errorf("preflight timestamp JSON = %s, want explicit widest RFC3339Nano %s", encodedPreflightAt, wantWidestRFC3339Nano)
	}

	preflightCandidate, _, errPreflight := service.preflightPreparedCommit(state, hourKey, prepared, prepared.ArchivePath, preflightAt)
	if errPreflight != nil {
		t.Fatalf("build maximum-width preflight candidate: %v", errPreflight)
	}
	preflightRaw, errMarshalPreflight := json.MarshalIndent(preflightCandidate, "", "  ")
	if errMarshalPreflight != nil {
		t.Fatalf("marshal maximum-width preflight candidate: %v", errMarshalPreflight)
	}

	completionCandidates := []struct {
		name      string
		timestamp time.Time
	}{
		{
			name:      "representative local completion",
			timestamp: time.Date(2026, time.August, 9, 12, 34, 56, 987654321, time.FixedZone("east-eight", 8*60*60)),
		},
		{
			name:      "maximum positive offset completion",
			timestamp: time.Date(9999, time.December, 31, 23, 59, 59, 999999999, time.FixedZone("east-max", 23*60*60+59*60)),
		},
		{
			name:      "maximum negative offset completion",
			timestamp: time.Date(9999, time.December, 31, 23, 59, 59, 999999999, time.FixedZone("west-max", -(23*60*60+59*60))),
		},
		{
			name:      "maximum UTC completion",
			timestamp: time.Date(9999, time.December, 31, 23, 59, 59, 999999999, time.UTC),
		},
	}
	for _, completion := range completionCandidates {
		t.Run(completion.name, func(t *testing.T) {
			candidate, _, errCandidate := service.preflightPreparedCommit(state, hourKey, prepared, prepared.ArchivePath, completion.timestamp)
			if errCandidate != nil {
				t.Fatalf("build completion candidate: %v", errCandidate)
			}
			rawCandidate, errMarshal := json.MarshalIndent(candidate, "", "  ")
			if errMarshal != nil {
				t.Fatalf("marshal completion candidate: %v", errMarshal)
			}
			if len(preflightRaw) < len(rawCandidate) {
				t.Errorf("preflight candidate bytes = %d, completion candidate bytes = %d; PUT preflight under-budgets state size", len(preflightRaw), len(rawCandidate))
			}
		})
	}
}

func TestPreparedUploadSupabaseCapacityPreflightStopsBeforeTOSPut(t *testing.T) {
	service, uploader, state, hourKey, prepared := preparedSupabaseFixture(t)
	for index := 0; index < maxSupabaseOutboxEntries; index++ {
		eventID := "cliproxy-v1." + strings.Repeat("0", 60) + formatFourDigits(index)
		state.SupabaseOutbox.Entries[eventID] = supabaseOutboxEntry{EventID: eventID, Status: supabaseOutboxStatusPending}
	}

	errComplete := service.completePreparedHour(context.Background(), hourKey, prepared, &state)
	if errComplete == nil || !strings.Contains(errComplete.Error(), "10,000") {
		t.Errorf("capacity preflight error = %v, want active-entry limit error", errComplete)
	}
	if len(uploader.calls) != 0 {
		t.Fatalf("TOS upload calls = %d, want 0 when Supabase preflight fails", len(uploader.calls))
	}
	if _, exists := state.PreparedHours[hourKey]; !exists || len(state.Hours) != 0 || len(state.Objects) != 0 || len(state.Uploaded) != 0 {
		t.Fatalf("preflight failure mutated prepared state: prepared=%d hours=%d objects=%d uploaded=%d", len(state.PreparedHours), len(state.Hours), len(state.Objects), len(state.Uploaded))
	}
}

func TestPreparedUploadSupabasePreflightRejectsConflictsBeforeTOSPut(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, service *Service, state *uploadState, hourKey string, prepared *preparedHour)
	}{
		{
			name: "duplicate event",
			mutate: func(t *testing.T, service *Service, state *uploadState, _ string, prepared *preparedHour) {
				event, errPrepare := service.prepareSupabaseEvent(*prepared)
				if errPrepare != nil {
					t.Fatalf("prepare duplicate event identity: %v", errPrepare)
				}
				state.SupabaseOutbox.Entries[event.EventID()] = supabaseOutboxEntry{EventID: event.EventID()}
			},
		},
		{
			name: "duplicate object",
			mutate: func(_ *testing.T, _ *Service, state *uploadState, _ string, prepared *preparedHour) {
				state.Objects[prepared.ObjectKey] = uploadedObject{ObjectKey: prepared.ObjectKey}
			},
		},
		{
			name: "duplicate source",
			mutate: func(_ *testing.T, _ *Service, state *uploadState, _ string, prepared *preparedHour) {
				state.Uploaded[prepared.Sources[0].Fingerprint] = uploadedSource{}
			},
		},
		{
			name: "prepared usage integrity",
			mutate: func(_ *testing.T, _ *Service, state *uploadState, hourKey string, prepared *preparedHour) {
				prepared.UsageSHA256 = strings.Repeat("0", 64)
				state.PreparedHours[hourKey] = *prepared
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, uploader, state, hourKey, prepared := preparedSupabaseFixture(t)
			test.mutate(t, service, &state, hourKey, &prepared)
			if errComplete := service.completePreparedHour(context.Background(), hourKey, prepared, &state); errComplete == nil {
				t.Fatal("preflight conflict error = nil")
			}
			if len(uploader.calls) != 0 {
				t.Fatalf("TOS upload calls = %d, want 0", len(uploader.calls))
			}
			if _, exists := state.PreparedHours[hourKey]; !exists {
				t.Fatal("preflight conflict removed prepared state")
			}
			if _, sealed := state.Hours[hourKey]; sealed {
				t.Fatal("preflight conflict sealed prepared hour")
			}
		})
	}
}

func TestPreparedUploadPostPublishStateFailureKeepsSupabaseCommit(t *testing.T) {
	service, uploader, state, hourKey, prepared := preparedSupabaseFixture(t)
	service.syncStateParentDirectory = func(string) error {
		return errors.New("injected parent sync failure")
	}

	errComplete := service.completePreparedHour(context.Background(), hourKey, prepared, &state)
	if errComplete == nil || !strings.Contains(errComplete.Error(), "commit uploaded prepared hour") {
		t.Errorf("post-publish completion error = %v, want state commit error", errComplete)
	}
	if len(uploader.calls) != 1 {
		t.Fatalf("TOS upload calls = %d, want 1", len(uploader.calls))
	}
	loaded, errLoad := service.loadState()
	if errLoad != nil {
		t.Fatalf("reload post-publish state: %v", errLoad)
	}
	wantEvent, errPrepare := service.prepareSupabaseEvent(prepared)
	if errPrepare != nil {
		t.Fatalf("prepare expected Supabase event: %v", errPrepare)
	}
	for label, got := range map[string]uploadState{"caller": state, "reload": loaded} {
		if len(got.PreparedHours) != 0 || len(got.Hours) != 1 || len(got.Objects) != 1 || len(got.Uploaded) != len(prepared.Sources) {
			t.Errorf("%s post-publish state = prepared:%d hours:%d objects:%d uploaded:%d", label, len(got.PreparedHours), len(got.Hours), len(got.Objects), len(got.Uploaded))
		}
		if _, exists := got.SupabaseOutbox.Entries[wantEvent.EventID()]; !exists {
			t.Errorf("%s post-publish state lost Supabase event %s", label, wantEvent.EventID())
		}
	}
	if errRetry := service.runOnce(context.Background(), false); errRetry == nil {
		t.Error("post-publish retry error = nil, want pending Supabase delivery error")
	}
	if len(uploader.calls) != 1 {
		t.Fatalf("post-publish state repeated TOS upload: calls=%d", len(uploader.calls))
	}
}

func TestPreparedUploadPrePublishStateFailureRestoresPreparedAndSupabaseOutbox(t *testing.T) {
	service, uploader, state, hourKey, prepared := preparedSupabaseFixture(t)
	existingState, existingEntry := validStateOutboxEntry(t, service, "existing-pending")
	for objectKey, object := range existingState.Objects {
		state.Objects[objectKey] = object
	}
	for existingHourKey, hour := range existingState.Hours {
		state.Hours[existingHourKey] = hour
	}
	state.SupabaseOutbox.Entries[existingEntry.EventID] = existingEntry
	if errSave := service.saveState(state); errSave != nil {
		t.Fatalf("save prepared state with existing outbox: %v", errSave)
	}
	restoreState := blockStatePublication(t, service)

	errComplete := service.completePreparedHour(context.Background(), hourKey, prepared, &state)
	restoreState()
	if errComplete == nil || !strings.Contains(errComplete.Error(), "commit uploaded prepared hour") {
		t.Errorf("pre-publish completion error = %v, want state commit error", errComplete)
	}
	if len(uploader.calls) != 1 {
		t.Fatalf("TOS upload calls = %d, want 1", len(uploader.calls))
	}
	wantEvent, errPrepare := service.prepareSupabaseEvent(prepared)
	if errPrepare != nil {
		t.Fatalf("prepare expected Supabase event: %v", errPrepare)
	}
	loaded, errLoad := service.loadState()
	if errLoad != nil {
		t.Fatalf("reload pre-publish rollback state: %v", errLoad)
	}
	for label, got := range map[string]uploadState{"caller": state, "reload": loaded} {
		if _, exists := got.PreparedHours[hourKey]; !exists || len(got.Uploaded) != 0 {
			t.Errorf("%s did not retain prepared state: prepared=%d uploaded=%d", label, len(got.PreparedHours), len(got.Uploaded))
		}
		if _, exists := got.SupabaseOutbox.Entries[existingEntry.EventID]; !exists {
			t.Errorf("%s lost existing pending event %s", label, existingEntry.EventID)
		}
		if _, exists := got.SupabaseOutbox.Entries[wantEvent.EventID()]; exists {
			t.Errorf("%s retained unpublished new event %s", label, wantEvent.EventID())
		}
	}
}

func TestPreparedUploadAuditFailureRetainsPreparedWithoutSupabaseEvent(t *testing.T) {
	service, uploader, state, hourKey, prepared := preparedSupabaseFixture(t)
	service.cfg.Retention.DeleteSourceAfterUpload = true
	service.cfg.Retention.KeepLocalArchives = false
	auditPath := filepath.Join(service.cfg.WorkDir, "audit.jsonl")
	if errRemove := os.Remove(auditPath); errRemove != nil {
		t.Fatalf("remove fixture audit file: %v", errRemove)
	}
	if errMkdir := os.Mkdir(auditPath, 0o750); errMkdir != nil {
		t.Fatalf("block audit append: %v", errMkdir)
	}

	errComplete := service.completePreparedHour(context.Background(), hourKey, prepared, &state)
	if errComplete == nil || !strings.Contains(errComplete.Error(), "record successful upload") {
		t.Errorf("audit failure completion error = %v, want durable audit error", errComplete)
	}
	if len(uploader.calls) != 1 {
		t.Fatalf("TOS upload calls = %d, want 1", len(uploader.calls))
	}
	loaded, errLoad := service.loadState()
	if errLoad != nil {
		t.Fatalf("reload audit failure state: %v", errLoad)
	}
	for label, got := range map[string]uploadState{"caller": state, "reload": loaded} {
		if _, exists := got.PreparedHours[hourKey]; !exists || len(got.Hours) != 0 || len(got.Objects) != 0 || len(got.Uploaded) != 0 || len(got.SupabaseOutbox.Entries) != 0 {
			t.Errorf("%s audit failure state mutated: prepared=%d hours=%d objects=%d uploaded=%d outbox=%d", label, len(got.PreparedHours), len(got.Hours), len(got.Objects), len(got.Uploaded), len(got.SupabaseOutbox.Entries))
		}
	}
	sourcePath, errSource := safeSourcePath(service.cfg.LogsRoot, prepared.Sources[0].RelativePath)
	if errSource != nil {
		t.Fatalf("resolve prepared source: %v", errSource)
	}
	if _, errStat := os.Stat(sourcePath); errStat != nil {
		t.Fatalf("audit failure removed source: %v", errStat)
	}
	if _, errStat := os.Stat(prepared.ArchivePath); errStat != nil {
		t.Fatalf("audit failure removed archive: %v", errStat)
	}
}

func TestPreparedUploadDisabledSupabasePreservesExistingOutboxWithoutNewEvent(t *testing.T) {
	enabledService, uploader, state, hourKey, prepared := preparedSupabaseFixture(t)
	existingState, existingEntry := validStateOutboxEntry(t, enabledService, "disabled-preserved")
	for objectKey, object := range existingState.Objects {
		state.Objects[objectKey] = object
	}
	for existingHourKey, hour := range existingState.Hours {
		state.Hours[existingHourKey] = hour
	}
	state.SupabaseOutbox.Entries[existingEntry.EventID] = existingEntry
	if errSave := enabledService.saveState(state); errSave != nil {
		t.Fatalf("save enabled state before disabling Supabase: %v", errSave)
	}

	disabledConfig := enabledService.cfg
	disabledConfig.Supabase.Enabled = false
	disabledService := mustTestService(t, disabledConfig, uploader, enabledService.now())
	state, errLoad := disabledService.loadState()
	if errLoad != nil {
		t.Fatalf("load preserved outbox while disabled: %v", errLoad)
	}
	uploader.calls = nil
	if errComplete := disabledService.completePreparedHour(context.Background(), hourKey, prepared, &state); errComplete != nil {
		t.Fatalf("complete TOS upload with Supabase disabled: %v", errComplete)
	}
	if len(uploader.calls) != 1 {
		t.Fatalf("TOS upload calls = %d, want 1", len(uploader.calls))
	}
	wouldBeEvent, errPrepare := enabledService.prepareSupabaseEvent(prepared)
	if errPrepare != nil {
		t.Fatalf("prepare event identity for absence check: %v", errPrepare)
	}
	loaded, errReload := disabledService.loadState()
	if errReload != nil {
		t.Fatalf("reload disabled Supabase commit: %v", errReload)
	}
	if _, exists := loaded.SupabaseOutbox.Entries[existingEntry.EventID]; !exists || len(loaded.SupabaseOutbox.Entries) != 1 {
		t.Fatalf("disabled Supabase did not preserve existing outbox: %#v", loaded.SupabaseOutbox.Entries)
	}
	if _, exists := loaded.SupabaseOutbox.Entries[wouldBeEvent.EventID()]; exists {
		t.Fatalf("disabled Supabase created new event %s", wouldBeEvent.EventID())
	}
	if got := uploadedHourSupabaseEventID(t, loaded.Hours[hourKey]); got != "" {
		t.Errorf("disabled Supabase committed hour event ID = %q, want empty", got)
	}
	if got := lastAuditSupabaseEventID(t, disabledService.cfg.WorkDir); got != "" {
		t.Errorf("disabled Supabase audit event ID = %q, want empty", got)
	}
}

func TestPreparedUploadSupabaseDeliveryErrorStillCleansSourcesAndArchive(t *testing.T) {
	tests := []struct {
		name         string
		token        string
		wantError    error
		wantRequests int
		statusCode   int
	}{
		{name: "retryable", token: "delivery-token", wantError: errSupabaseDeliveryRetryable, wantRequests: 1},
		{name: "configuration", wantError: errSupabaseDeliveryConfiguration},
		{name: "blocked", token: "delivery-token", wantError: errSupabaseDeliveryBlocked, wantRequests: 1, statusCode: http.StatusUnprocessableEntity},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			location := mustLocation(t, "Asia/Shanghai")
			now := time.Date(2026, time.July, 15, 4, 10, 0, 0, location)
			hour := time.Date(2026, time.July, 15, 2, 0, 0, 0, location)
			root := filepath.Join(t.TempDir(), "keys")
			workDir := filepath.Join(t.TempDir(), "uploader")
			sourcePath := mustWriteLog(t, root, "panda", "cleanup-supabase.log", requestLog(hour.Add(15*time.Minute), "gpt-5.6-sol", "cleanup Supabase"), now.Add(-2*time.Hour))

			cfg := testConfig(root, workDir)
			cfg.Upload.Enabled = true
			cfg.Retention.DeleteSourceAfterUpload = true
			cfg.Retention.KeepLocalArchives = false
			cfg.Supabase.Enabled = true
			cfg.Supabase.IngestURL = "https://project-ref.supabase.co/functions/v1/log-stats-ingest"
			cfg.Supabase.IngestTokenEnv = "LOG_STATS_INGEST_TOKEN"
			if errValidate := cfg.Supabase.validate(); errValidate != nil {
				t.Fatalf("validate Supabase config: %v", errValidate)
			}
			t.Setenv(cfg.Supabase.IngestTokenEnv, test.token)
			uploader := &fakeObjectUploader{}
			service := mustTestService(t, cfg, uploader, now)
			requestCount := 0
			service.supabaseHTTPDoer = httpDoerFunc(func(*http.Request) (*http.Response, error) {
				requestCount++
				if test.statusCode != 0 {
					return &http.Response{StatusCode: test.statusCode, Body: io.NopCloser(strings.NewReader("blocked"))}, nil
				}
				return nil, errors.New("temporary delivery failure")
			})

			errRun := service.RunOnce(context.Background(), false)
			if !errors.Is(errRun, test.wantError) {
				t.Errorf("RunOnce error = %v, want %v", errRun, test.wantError)
			}
			if requestCount != test.wantRequests || len(uploader.calls) != 1 {
				t.Fatalf("calls = Supabase:%d TOS:%d, want %d and 1", requestCount, len(uploader.calls), test.wantRequests)
			}
			if _, errStat := os.Stat(sourcePath); !errors.Is(errStat, os.ErrNotExist) {
				t.Fatalf("source cleanup was blocked by Supabase error: %v", errStat)
			}
			if _, errStat := os.Stat(uploader.calls[0].Path); !errors.Is(errStat, os.ErrNotExist) {
				t.Fatalf("archive cleanup was blocked by Supabase error: %v", errStat)
			}
			loaded, errLoad := service.loadState()
			if errLoad != nil {
				t.Fatalf("reload cleanup state: %v", errLoad)
			}
			if len(loaded.PreparedHours) != 0 || len(loaded.Hours) != 1 || len(loaded.Objects) != 1 || len(loaded.SupabaseOutbox.Entries) != 1 {
				t.Fatalf("cleanup changed sealed upload state: prepared=%d hours=%d objects=%d outbox=%d", len(loaded.PreparedHours), len(loaded.Hours), len(loaded.Objects), len(loaded.SupabaseOutbox.Entries))
			}
			audits := readAudit(t, workDir)
			finalAudit := audits[len(audits)-1]
			if finalAudit.Status != "uploaded" || finalAudit.Error != test.wantError.Error() {
				t.Errorf("final cleanup audit = %#v, want uploaded with generic Supabase error", finalAudit)
			}
			errText := ""
			if errRun != nil {
				errText = errRun.Error()
			}
			leakedToken := test.token != "" && strings.Contains(errText, test.token)
			if strings.Contains(errText, cfg.Supabase.IngestURL) || strings.Contains(errText, cfg.Supabase.IngestTokenEnv) || leakedToken {
				t.Errorf("delivery error leaked sensitive configuration: %v", errRun)
			}
		})
	}
}

func TestPreparedUploadAuditCombinesSupabaseAndSourceCleanupErrors(t *testing.T) {
	service, _, state, hourKey, prepared := preparedSupabaseFixture(t)
	service.cfg.Retention.DeleteSourceAfterUpload = true
	service.cfg.Retention.KeepLocalArchives = true
	pendingPath := service.pendingDeletePath(prepared.Sources[0].Fingerprint)
	if errMkdir := os.MkdirAll(pendingPath, 0o750); errMkdir != nil {
		t.Fatalf("create non-regular pending deletion path: %v", errMkdir)
	}
	t.Setenv(service.cfg.Supabase.IngestTokenEnv, "cleanup-combination-token")
	service.supabaseHTTPDoer = httpDoerFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("temporary delivery failure")
	})

	errComplete := service.completePreparedHour(context.Background(), hourKey, prepared, &state)
	if !errors.Is(errComplete, errSupabaseDeliveryRetryable) {
		t.Fatalf("combined cleanup completion error = %v, want retryable Supabase delivery error", errComplete)
	}
	audits := readAudit(t, service.cfg.WorkDir)
	finalAudit := audits[len(audits)-1]
	if finalAudit.Status != "uploaded_delete_pending" {
		t.Fatalf("combined cleanup audit status = %q, want uploaded_delete_pending", finalAudit.Status)
	}
	if !strings.Contains(finalAudit.Error, errSupabaseDeliveryRetryable.Error()) ||
		!strings.Contains(finalAudit.Error, "pending deletion path is not a regular file") {
		t.Fatalf("combined cleanup audit error = %q, want generic Supabase and cleanup errors", finalAudit.Error)
	}
	if strings.Contains(finalAudit.Error, service.cfg.Supabase.IngestURL) ||
		strings.Contains(finalAudit.Error, service.cfg.Supabase.IngestTokenEnv) ||
		strings.Contains(finalAudit.Error, "cleanup-combination-token") {
		t.Fatalf("combined cleanup audit leaked Supabase configuration: %q", finalAudit.Error)
	}
}

func TestRunOnceDrainsSupabaseOutboxWithNoSourceFiles(t *testing.T) {
	service := newStateOutboxTestService(t, true)
	service.cfg.Upload.Enabled = true
	state, entry := validStateOutboxEntry(t, service, "startup-pending")
	state.SupabaseOutbox.Entries[entry.EventID] = entry
	if errSave := service.saveState(state); errSave != nil {
		t.Fatalf("save startup pending state: %v", errSave)
	}
	t.Setenv(service.cfg.Supabase.IngestTokenEnv, "startup-token")
	requestCount := 0
	service.supabaseHTTPDoer = httpDoerFunc(func(*http.Request) (*http.Response, error) {
		requestCount++
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"status":"inserted","event_id":"` + entry.EventID + `"}`)),
		}, nil
	})

	if errRun := service.runOnce(context.Background(), false); errRun != nil {
		t.Fatalf("startup drain with no sources: %v", errRun)
	}
	if requestCount != 1 {
		t.Fatalf("startup Supabase requests = %d, want 1", requestCount)
	}
	loaded, errLoad := service.loadState()
	if errLoad != nil {
		t.Fatalf("reload startup drain state: %v", errLoad)
	}
	if _, exists := loaded.SupabaseOutbox.Entries[entry.EventID]; exists {
		t.Fatalf("startup drain retained acknowledged event %s", entry.EventID)
	}
}

func TestRunOnceDrainsSupabaseOutboxBeforeResumingPreparedUpload(t *testing.T) {
	service, uploader, state, _, prepared := preparedSupabaseFixture(t)
	existingState, existingEntry := validStateOutboxEntry(t, service, "startup-before-resume")
	for objectKey, object := range existingState.Objects {
		state.Objects[objectKey] = object
	}
	for existingHourKey, hour := range existingState.Hours {
		state.Hours[existingHourKey] = hour
	}
	state.SupabaseOutbox.Entries[existingEntry.EventID] = existingEntry
	if errSave := service.saveState(state); errSave != nil {
		t.Fatalf("save startup and prepared state: %v", errSave)
	}
	t.Setenv(service.cfg.Supabase.IngestTokenEnv, "startup-order-token")
	var order []string
	uploader.onUpload = func() {
		order = append(order, "tos")
	}
	service.supabaseHTTPDoer = httpDoerFunc(func(request *http.Request) (*http.Response, error) {
		eventID := eventIDFromDeliveryRequest(t, request)
		order = append(order, "supabase:"+eventID)
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"status":"inserted","event_id":"` + eventID + `"}`)),
		}, nil
	})

	if errRun := service.runOnce(context.Background(), false); errRun != nil {
		t.Fatalf("run startup drain before prepared resume: %v", errRun)
	}
	newEvent, errPrepare := service.prepareSupabaseEvent(prepared)
	if errPrepare != nil {
		t.Fatalf("prepare resumed event identity: %v", errPrepare)
	}
	want := []string{"supabase:" + existingEntry.EventID, "tos", "supabase:" + newEvent.EventID()}
	if strings.Join(order, "|") != strings.Join(want, "|") {
		t.Errorf("startup/resume order = %v, want %v", order, want)
	}
}

func TestHasCatchUpWorkUsesPendingSupabaseBacklog(t *testing.T) {
	tests := []struct {
		name    string
		enabled bool
		blocked bool
		want    bool
	}{
		{name: "enabled pending", enabled: true, want: true},
		{name: "enabled blocked", enabled: true, blocked: true, want: false},
		{name: "disabled pending preserved", enabled: false, want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			writer := newStateOutboxTestService(t, true)
			state, entry := validStateOutboxEntry(t, writer, "catch-up-"+strings.ReplaceAll(test.name, " ", "-"))
			if test.blocked {
				entry.Status = supabaseOutboxStatusBlocked
				entry.BlockCategory = "conflict"
				entry.BlockStatus = http.StatusConflict
			}
			state.SupabaseOutbox.Entries[entry.EventID] = entry
			if errSave := writer.saveState(state); errSave != nil {
				t.Fatalf("save catch-up state: %v", errSave)
			}

			service := writer
			if !test.enabled {
				cfg := writer.cfg
				cfg.Supabase.Enabled = false
				service = mustTestService(t, cfg, nil, writer.now())
			}
			hasWork, errHas := service.hasCatchUpWork()
			if errHas != nil {
				t.Fatalf("hasCatchUpWork: %v", errHas)
			}
			if hasWork != test.want {
				t.Errorf("hasCatchUpWork() = %t, want %t", hasWork, test.want)
			}
			if test.want && service.nextDelay() != service.cfg.Schedule.CatchUpDelay {
				t.Errorf("nextDelay() = %s, want catch-up delay %s", service.nextDelay(), service.cfg.Schedule.CatchUpDelay)
			}
		})
	}
}

func preparedSupabaseFixture(t *testing.T) (*Service, *fakeObjectUploader, uploadState, string, preparedHour) {
	t.Helper()
	location := mustLocation(t, "Asia/Shanghai")
	now := time.Date(2026, time.July, 15, 4, 10, 0, 0, location)
	hour := time.Date(2026, time.July, 15, 2, 0, 0, 0, location)
	root := filepath.Join(t.TempDir(), "keys")
	workDir := filepath.Join(t.TempDir(), "uploader")
	mustWriteLog(t, root, "panda", "prepared-supabase.log", requestLog(hour.Add(15*time.Minute), "gpt-5.6-sol", "prepared Supabase"), now.Add(-2*time.Hour))

	cfg := testConfig(root, workDir)
	cfg.Upload.Enabled = true
	cfg.Supabase.Enabled = true
	cfg.Supabase.IngestURL = "https://project-ref.supabase.co/functions/v1/log-stats-ingest"
	cfg.Supabase.IngestTokenEnv = "LOG_STATS_INGEST_TOKEN"
	if errValidate := cfg.Supabase.validate(); errValidate != nil {
		t.Fatalf("validate Supabase config: %v", errValidate)
	}
	uploader := &fakeObjectUploader{err: errors.New("retain prepared batch")}
	service := mustTestService(t, cfg, uploader, now)
	if errRun := service.RunOnce(context.Background(), false); errRun == nil {
		t.Fatal("initial run unexpectedly completed instead of retaining prepared state")
	}
	state, errLoad := service.loadState()
	if errLoad != nil {
		t.Fatalf("load prepared Supabase state: %v", errLoad)
	}
	hourKey := hourStateKey(hour, providerCodex)
	prepared, exists := state.PreparedHours[hourKey]
	if !exists {
		t.Fatalf("prepared hour %s missing", hourKey)
	}
	uploader.err = nil
	uploader.calls = nil
	return service, uploader, state, hourKey, prepared
}

func lastAuditSupabaseEventID(t *testing.T, workDir string) string {
	t.Helper()
	raw, errRead := os.ReadFile(filepath.Join(workDir, "audit.jsonl"))
	if errRead != nil {
		t.Fatalf("read audit log: %v", errRead)
	}
	lines := nonemptyLines(raw)
	if len(lines) == 0 {
		t.Fatal("audit log is empty")
	}
	var record struct {
		SupabaseEventID string `json:"supabase_event_id"`
	}
	if errDecode := json.Unmarshal(lines[len(lines)-1], &record); errDecode != nil {
		t.Fatalf("decode final audit record: %v", errDecode)
	}
	return record.SupabaseEventID
}

func formatFourDigits(value int) string {
	return string([]byte{
		byte('0' + value/1000%10),
		byte('0' + value/100%10),
		byte('0' + value/10%10),
		byte('0' + value%10),
	})
}
