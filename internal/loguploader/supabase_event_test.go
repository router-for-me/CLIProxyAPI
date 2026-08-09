package loguploader

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPrepareSupabaseEventProducesStableSchemaV1Payload(t *testing.T) {
	t.Parallel()

	service := mustSupabaseEventService(t)
	prepared := validPreparedHourForSupabaseEvent(service)
	first, errPrepare := service.prepareSupabaseEvent(prepared)
	if errPrepare != nil {
		t.Fatalf("prepare Supabase event: %v", errPrepare)
	}

	permuted := prepared
	permuted.Usage = append([]preparedUsage(nil), prepared.Usage...)
	permuted.Usage[0], permuted.Usage[1] = permuted.Usage[1], permuted.Usage[0]
	second, errPrepare := service.prepareSupabaseEvent(permuted)
	if errPrepare != nil {
		t.Fatalf("prepare permuted Supabase event: %v", errPrepare)
	}
	if first.EventID() != second.EventID() {
		t.Errorf("stable event IDs differ: %q != %q", first.EventID(), second.EventID())
	}
	if !bytes.Equal(first.RawJSON(), second.RawJSON()) {
		t.Errorf("stable raw JSON differs:\nfirst:  %s\nsecond: %s", first.RawJSON(), second.RawJSON())
	}
	if !strings.HasPrefix(first.EventID(), "cliproxy-v1.") || len(first.EventID()) != len("cliproxy-v1.")+64 {
		t.Errorf("event ID = %q, want Windows-safe cliproxy-v1.<sha256>", first.EventID())
	}

	expectedRaw := fmt.Sprintf(
		`{"schema_version":1,"event_id":%q,"target_id":%q,"object_key":"cliproxy-logs/2026/07/15/archive.jsonl.zst","archive_sha256":"%s","manifest_sha256":"%s","hour_start":"2026-07-15T01:00:00+08:00","timezone":"Asia/Shanghai","usage_date":"2026-07-15","source_count":3,"source_bytes":350,"jsonl_bytes":280,"compressed_bytes":180,"test_mode":false,"usage":[{"key_name":"alice","provider":"codex","source_count":1,"source_bytes":200,"jsonl_bytes":160},{"key_name":"panda","provider":"codex","source_count":2,"source_bytes":150,"jsonl_bytes":120}]}`,
		first.EventID(), "tos:"+service.target.ID, strings.Repeat("a", 64), strings.Repeat("b", 64),
	)
	if got := string(first.RawJSON()); got != expectedRaw {
		t.Errorf("raw JSON =\n%s\nwant =\n%s", got, expectedRaw)
	}

	mutated := first.RawJSON()
	mutated[0] = 'x'
	if bytes.Equal(mutated, first.RawJSON()) {
		t.Fatal("RawJSON returned mutable storage instead of a copy")
	}
}

func TestPrepareSupabaseEventIDChangesWithCanonicalBatchIdentity(t *testing.T) {
	t.Parallel()

	service := mustSupabaseEventService(t)
	prepared := validPreparedHourForSupabaseEvent(service)
	baseline, errPrepare := service.prepareSupabaseEvent(prepared)
	if errPrepare != nil {
		t.Fatalf("prepare baseline Supabase event: %v", errPrepare)
	}

	tests := []struct {
		name   string
		mutate func(service *Service, prepared *preparedHour)
	}{
		{
			name: "target",
			mutate: func(service *Service, prepared *preparedHour) {
				service.target.ID = strings.Repeat("f", 64)
				prepared.TargetID = service.target.ID
			},
		},
		{name: "object key", mutate: func(_ *Service, prepared *preparedHour) {
			prepared.ObjectKey = "cliproxy-logs/2026/07/15/other.jsonl.zst"
		}},
		{name: "hour", mutate: func(_ *Service, prepared *preparedHour) {
			prepared.Hour = prepared.Hour.Add(time.Hour)
		}},
		{name: "provider", mutate: func(_ *Service, prepared *preparedHour) {
			prepared.Provider = providerGrok
			for index := range prepared.Usage {
				prepared.Usage[index].Provider = providerGrok
			}
		}},
		{name: "archive SHA", mutate: func(_ *Service, prepared *preparedHour) {
			prepared.ArchiveSHA256 = strings.Repeat("c", 64)
		}},
		{name: "manifest SHA", mutate: func(_ *Service, prepared *preparedHour) {
			prepared.ManifestSHA256 = strings.Repeat("d", 64)
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidateService := *service
			candidatePrepared := prepared
			candidatePrepared.Usage = append([]preparedUsage(nil), prepared.Usage...)
			test.mutate(&candidateService, &candidatePrepared)
			mustBindPreparedUsageIntegrity(&candidateService, &candidatePrepared)
			candidate, errCandidate := candidateService.prepareSupabaseEvent(candidatePrepared)
			if errCandidate != nil {
				t.Fatalf("prepare changed event: %v", errCandidate)
			}
			if candidate.EventID() == baseline.EventID() {
				t.Errorf("event ID did not change from %q", baseline.EventID())
			}
		})
	}
}

func TestSupabaseEventIDChangesWithTestMode(t *testing.T) {
	t.Parallel()

	payload := mustSupabaseEventPayload(t)
	productionID := supabaseEventID(payload, providerCodex)
	payload.TestMode = true
	testID := supabaseEventID(payload, providerCodex)
	if testID == productionID {
		t.Fatalf("test_mode did not change event ID %q", productionID)
	}
}

func TestPrepareSupabaseEventRejectsUsageNotBoundToPreparedSources(t *testing.T) {
	t.Parallel()

	service := mustSupabaseEventService(t)
	valid := validPreparedHourForSupabaseEvent(service)
	tests := []struct {
		name   string
		mutate func(prepared *preparedHour)
	}{
		{name: "provider mismatch", mutate: func(prepared *preparedHour) {
			prepared.Usage[0].Provider = providerGrok
		}},
		{name: "redistributed source counts", mutate: func(prepared *preparedHour) {
			prepared.Usage[0].SourceCount = 1
			prepared.Usage[1].SourceCount = 2
		}},
		{name: "negative per-source JSONL bytes hidden by aggregate", mutate: func(prepared *preparedHour) {
			prepared.Sources[0].JSONLBytes = exactInt64(-1)
			prepared.Sources[2].JSONLBytes = exactInt64(121)
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			prepared := valid
			prepared.Usage = append([]preparedUsage(nil), valid.Usage...)
			prepared.Sources = append([]preparedSource(nil), valid.Sources...)
			for index := range prepared.Sources {
				if prepared.Sources[index].JSONLBytes != nil {
					prepared.Sources[index].JSONLBytes = exactInt64(*prepared.Sources[index].JSONLBytes)
				}
			}
			test.mutate(&prepared)
			_, errPrepare := service.prepareSupabaseEvent(prepared)
			if errPrepare == nil || (!strings.Contains(errPrepare.Error(), "prepared sources") &&
				!strings.Contains(errPrepare.Error(), "jsonl_bytes") &&
				!strings.Contains(errPrepare.Error(), "usage checksum mismatch")) {
				t.Fatalf("prepareSupabaseEvent error = %v, want exact source usage failure", errPrepare)
			}
		})
	}
}

func TestManifestSHA256IgnoresExactUsageMetadata(t *testing.T) {
	t.Parallel()

	modTime := time.Date(2026, time.July, 15, 1, 10, 0, 0, time.UTC)
	base := preparedSource{
		Fingerprint:  sourceFingerprint("panda/source.log", 100, modTime),
		RelativePath: "panda/source.log",
		KeyName:      "panda",
		Model:        "gpt-5.6-sol",
		Size:         100,
		ModTime:      modTime,
		SHA256:       strings.Repeat("a", 64),
		JSONLBytes:   exactInt64(80),
	}
	changed := base
	changed.JSONLBytes = exactInt64(0)
	if got, want := manifestSHA256([]preparedSource{changed}), manifestSHA256([]preparedSource{base}); got != want {
		t.Errorf("manifest changed with exact usage metadata: got %s, want %s", got, want)
	}
}

func TestValidateSupabaseEventPayloadRejectsInvalidContractData(t *testing.T) {
	t.Parallel()

	valid := mustSupabaseEventPayload(t)
	tests := []struct {
		name    string
		wantErr string
		mutate  func(payload *supabaseEventPayload)
	}{
		{name: "bare target hash", wantErr: "target_id", mutate: func(payload *supabaseEventPayload) {
			payload.TargetID = strings.TrimPrefix(payload.TargetID, "tos:")
		}},
		{name: "negative integer", wantErr: "source_bytes", mutate: func(payload *supabaseEventPayload) {
			payload.SourceBytes = -1
		}},
		{name: "unsafe JSON integer", wantErr: "jsonl_bytes", mutate: func(payload *supabaseEventPayload) {
			payload.JSONLBytes = maxSafeJSONInteger + 1
		}},
		{name: "archive SHA", wantErr: "archive_sha256", mutate: func(payload *supabaseEventPayload) {
			payload.ArchiveSHA256 = "not-a-sha256"
		}},
		{name: "usage date", wantErr: "usage_date", mutate: func(payload *supabaseEventPayload) {
			payload.UsageDate = "2026-07-16"
		}},
		{name: "duplicate usage pair", wantErr: "duplicate", mutate: func(payload *supabaseEventPayload) {
			payload.Usage = append(payload.Usage, payload.Usage[0])
		}},
		{name: "unsupported provider", wantErr: "provider", mutate: func(payload *supabaseEventPayload) {
			payload.Usage[0].Provider = "unknown"
		}},
		{name: "usage totals", wantErr: "totals", mutate: func(payload *supabaseEventPayload) {
			(*payload.Usage[0].JSONLBytes)++
		}},
		{name: "invalid timezone", wantErr: "timezone", mutate: func(payload *supabaseEventPayload) {
			payload.Timezone = "Mars/Olympus"
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			candidate.Usage = make([]supabaseEventUsage, len(valid.Usage))
			for index, usage := range valid.Usage {
				candidate.Usage[index] = usage
				if usage.JSONLBytes != nil {
					jsonlBytes := *usage.JSONLBytes
					candidate.Usage[index].JSONLBytes = &jsonlBytes
				}
			}
			test.mutate(&candidate)
			errValidate := validateSupabaseEventPayload(candidate)
			if errValidate == nil || !strings.Contains(errValidate.Error(), test.wantErr) {
				t.Fatalf("validateSupabaseEventPayload error = %v, want %q", errValidate, test.wantErr)
			}
		})
	}
}

func TestBatchOnlySupabaseEventOmitsPerKeyJSONLBytes(t *testing.T) {
	t.Parallel()

	payload := mustSupabaseEventPayload(t)
	payload.UsagePrecision = supabaseUsagePrecisionBatchOnly
	for index := range payload.Usage {
		payload.Usage[index].JSONLBytes = nil
	}
	payload.EventID = supabaseHistoryEventID(payload)
	if errValidate := validateSupabaseEventPayload(payload); errValidate != nil {
		t.Fatalf("validate batch-only event: %v", errValidate)
	}
	raw, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		t.Fatalf("marshal batch-only event: %v", errMarshal)
	}
	if !bytes.Contains(raw, []byte(`"usage_precision":"batch_only"`)) {
		t.Fatalf("batch-only payload omitted usage precision: %s", raw)
	}
	if bytes.Contains(raw, []byte(`"usage":[{"key_name":"alice","provider":"codex","source_count":1,"source_bytes":200,"jsonl_bytes"`)) {
		t.Fatalf("batch-only payload exposed a fabricated per-key JSONL value: %s", raw)
	}
	if !strings.HasPrefix(payload.EventID, "cliproxy-v1.") || len(payload.EventID) != len("cliproxy-v1.")+64 {
		t.Fatalf("history event ID = %q", payload.EventID)
	}
}

func TestValidateSupabaseEventPayloadRejectsPrecisionMismatch(t *testing.T) {
	t.Parallel()

	exact := mustSupabaseEventPayload(t)
	exact.Usage[0].JSONLBytes = nil
	if errValidate := validateSupabaseEventPayload(exact); errValidate == nil || !strings.Contains(errValidate.Error(), "exact") {
		t.Fatalf("exact event with unknown usage JSONL error = %v", errValidate)
	}

	batchOnly := mustSupabaseEventPayload(t)
	batchOnly.UsagePrecision = supabaseUsagePrecisionBatchOnly
	if errValidate := validateSupabaseEventPayload(batchOnly); errValidate == nil || !strings.Contains(errValidate.Error(), "batch-only") {
		t.Fatalf("batch-only event with exact usage JSONL error = %v", errValidate)
	}
}

func TestValidateSupabaseKeyNameContract(t *testing.T) {
	t.Parallel()

	accepted := []string{
		"panda",
		"User-1",
		"张三-Mobile",
		"team-a",
		"key-0123456789ab",
		"unauthenticated",
		strings.Repeat("a", 47),
		"ordinary display name " + strings.Repeat("x", 26),
		strings.Repeat("😀", 48),
		strings.Repeat("界", 48),
	}
	for _, keyName := range accepted {
		if errValidate := validateSupabaseKeyName(keyName); errValidate != nil {
			t.Errorf("accepted key_name %q error = %v", keyName, errValidate)
		}
	}

	rejected := []string{
		"cpa_team-a_0123456789abcdef",
		"cpa_nologsecretvalue1234567890",
		"cpa_normalloggedkey12345678",
		"CpA_team-a_0123456789abcdef",
		"CPA_NOLOGSECRETVALUE1234567890",
		"cpa_",
		"sk-proj-0123456789abcdef",
		"Bearer secret-token",
		"abcdefgh.ijklmnop.qrstuvwx",
		strings.Repeat("Ab1_", 8),
		strings.Repeat("a", 48),
		strings.Repeat("a", 49),
		strings.Repeat("界", 49),
		"  CpA_team-a  ",
		"",
	}
	for _, keyName := range rejected {
		errValidate := validateSupabaseKeyName(keyName)
		if errValidate == nil {
			t.Errorf("rejected key_name %q was accepted", keyName)
			continue
		}
		trimmed := strings.TrimSpace(keyName)
		if keyName != "" && strings.Contains(errValidate.Error(), keyName) {
			t.Errorf("validation error echoed rejected key_name")
		} else if trimmed != "" && trimmed != keyName && strings.Contains(errValidate.Error(), trimmed) {
			t.Errorf("validation error echoed trimmed rejected key_name")
		}
	}
}

func TestLoadStateRequiresExactUsageOnlyWhenSupabaseEnabled(t *testing.T) {
	t.Parallel()

	location := mustLocation(t, "Asia/Shanghai")
	now := time.Date(2026, time.July, 15, 5, 10, 0, 0, location)
	root := filepath.Join(t.TempDir(), "keys")
	workDir := filepath.Join(t.TempDir(), "uploader")
	if errMkdir := os.MkdirAll(workDir, 0o750); errMkdir != nil {
		t.Fatalf("create work directory: %v", errMkdir)
	}
	disabledConfig := testConfig(root, workDir)
	disabledService := mustTestService(t, disabledConfig, nil, now)
	state := validSecurityUploadState(disabledService)
	for hourKey, prepared := range state.PreparedHours {
		prepared.Usage = nil
		prepared.Sources[0].KeyName = "sk-legacy-secret-must-not-leak"
		state.PreparedHours[hourKey] = prepared
	}
	if errSave := disabledService.saveState(*state); errSave != nil {
		t.Fatalf("save schema-v2 state without exact usage: %v", errSave)
	}

	if _, errLoad := disabledService.loadState(); errLoad != nil {
		t.Fatalf("Supabase-disabled schema-v2 state was rejected: %v", errLoad)
	}
	enabledConfig := disabledConfig
	enabledConfig.Supabase.Enabled = true
	enabledService := mustTestService(t, enabledConfig, nil, now)
	_, errLoad := enabledService.loadState()
	if errLoad == nil || !strings.Contains(errLoad.Error(), "exact Supabase usage metadata") {
		t.Fatalf("Supabase-enabled legacy prepared state error = %v", errLoad)
	}
	if strings.Contains(errLoad.Error(), "legacy-secret") {
		t.Fatalf("legacy prepared state error leaked key material: %v", errLoad)
	}
}

func TestLoadStatePreservesPolicyV1FractionalOffsetHourCompatibility(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		bindUsageIntegrity bool
	}{
		{name: "legacy prepared hour", bindUsageIntegrity: false},
		{name: "versioned prepared hour", bindUsageIntegrity: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			location := mustLocation(t, "Asia/Kathmandu")
			now := time.Date(2026, time.July, 15, 5, 10, 0, 0, location)
			root := filepath.Join(t.TempDir(), "keys")
			workDir := filepath.Join(t.TempDir(), "uploader")
			if errMkdir := os.MkdirAll(workDir, 0o750); errMkdir != nil {
				t.Fatalf("create work directory: %v", errMkdir)
			}
			cfg := testConfig(root, workDir)
			cfg.Timezone = "Asia/Kathmandu"
			service := mustTestService(t, cfg, nil, now)
			state := validSecurityUploadState(service)
			for hourKey, prepared := range state.PreparedHours {
				delete(state.PreparedHours, hourKey)
				prepared.Hour = prepared.Hour.Add(time.Hour).Truncate(time.Hour)
				if prepared.Hour.Minute() == 0 {
					t.Fatalf("policy-v1 absolute truncation unexpectedly produced local HH:00 %s", prepared.Hour)
				}
				if test.bindUsageIntegrity {
					prepared.Sources[0].JSONLBytes = exactInt64(prepared.JSONLBytes)
					prepared.Usage = []preparedUsage{{
						KeyName:     prepared.Sources[0].KeyName,
						Provider:    prepared.Provider,
						SourceCount: 1,
						SourceBytes: prepared.Sources[0].Size,
						JSONLBytes:  prepared.JSONLBytes,
					}}
					prepared.UsageSchemaVersion = preparedUsageSchemaVersion
					usageSHA256, errUsageSHA := service.preparedUsageSHA256(prepared)
					if errUsageSHA != nil {
						t.Fatalf("bind policy-v1 exact usage integrity: %v", errUsageSHA)
					}
					prepared.UsageSHA256 = usageSHA256
				}
				state.PreparedHours[hourStateKey(prepared.Hour, prepared.Provider)] = prepared
			}
			if errSave := service.saveState(*state); errSave != nil {
				t.Fatalf("save fractional-offset prepared state: %v", errSave)
			}

			if _, errLoad := service.loadState(); errLoad != nil {
				t.Fatalf("load policy-v1 fractional-offset prepared state: %v", errLoad)
			}
		})
	}
}

func TestLoadStateRejectsInvalidExactUsageWhenSupabaseEnabled(t *testing.T) {
	t.Parallel()

	location := mustLocation(t, "Asia/Shanghai")
	now := time.Date(2026, time.July, 15, 5, 10, 0, 0, location)
	root := filepath.Join(t.TempDir(), "keys")
	workDir := filepath.Join(t.TempDir(), "uploader")
	if errMkdir := os.MkdirAll(workDir, 0o750); errMkdir != nil {
		t.Fatalf("create work directory: %v", errMkdir)
	}
	cfg := testConfig(root, workDir)
	cfg.Supabase.Enabled = true
	service := mustTestService(t, cfg, nil, now)
	state := validSecurityUploadState(service)
	for hourKey, prepared := range state.PreparedHours {
		prepared.Sources[0].JSONLBytes = exactInt64(prepared.JSONLBytes)
		prepared.Usage = []preparedUsage{{
			KeyName:     prepared.Sources[0].KeyName,
			Provider:    prepared.Provider,
			SourceCount: 1,
			SourceBytes: prepared.Sources[0].Size,
			JSONLBytes:  prepared.JSONLBytes + 1,
		}}
		mustBindPreparedUsageIntegrity(service, &prepared)
		state.PreparedHours[hourKey] = prepared
	}
	if errSave := service.saveState(*state); errSave != nil {
		t.Fatalf("save state with invalid exact usage: %v", errSave)
	}

	_, errLoad := service.loadState()
	if errLoad == nil || !strings.Contains(errLoad.Error(), "prepared sources") {
		t.Fatalf("invalid exact usage state error = %v, want prepared sources", errLoad)
	}
}

func TestLoadStateRejectsCoherentExactUsageTampering(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(prepared *preparedHour)
	}{
		{name: "source key name", mutate: func(prepared *preparedHour) {
			prepared.Sources[0].KeyName = "tampered-key"
			prepared.Usage[0].KeyName = "tampered-key"
		}},
		{name: "per-source JSONL bytes", mutate: func(prepared *preparedHour) {
			prepared.Sources[0].JSONLBytes = exactInt64(*prepared.Sources[0].JSONLBytes + 1)
			prepared.Usage[0].JSONLBytes++
			prepared.JSONLBytes++
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			location := mustLocation(t, "Asia/Shanghai")
			now := time.Date(2026, time.July, 15, 5, 10, 0, 0, location)
			root := filepath.Join(t.TempDir(), "keys")
			workDir := filepath.Join(t.TempDir(), "uploader")
			if errMkdir := os.MkdirAll(workDir, 0o750); errMkdir != nil {
				t.Fatalf("create work directory: %v", errMkdir)
			}
			cfg := testConfig(root, workDir)
			cfg.Supabase.Enabled = true
			service := mustTestService(t, cfg, nil, now)
			state := validSecurityUploadState(service)
			for hourKey, prepared := range state.PreparedHours {
				prepared.Sources[0].JSONLBytes = exactInt64(prepared.JSONLBytes)
				prepared.Usage = []preparedUsage{{
					KeyName:     prepared.Sources[0].KeyName,
					Provider:    prepared.Provider,
					SourceCount: 1,
					SourceBytes: prepared.Sources[0].Size,
					JSONLBytes:  prepared.JSONLBytes,
				}}
				mustBindPreparedUsageIntegrity(service, &prepared)
				manifestBefore := prepared.ManifestSHA256
				test.mutate(&prepared)
				if gotManifest := manifestSHA256(prepared.Sources); gotManifest != manifestBefore {
					t.Fatalf("legacy archive manifest changed after usage-only tamper: got %s, want %s", gotManifest, manifestBefore)
				}
				state.PreparedHours[hourKey] = prepared
			}
			if errSave := service.saveState(*state); errSave != nil {
				t.Fatalf("save tampered prepared state: %v", errSave)
			}

			_, errLoad := service.loadState()
			if errLoad == nil || !strings.Contains(errLoad.Error(), "usage checksum mismatch") {
				t.Fatalf("tampered exact usage state error = %v, want usage checksum mismatch", errLoad)
			}
		})
	}
}

func TestLoadStateRejectsCoherentPreparedHourAndMapKeyTampering(t *testing.T) {
	t.Parallel()

	location := mustLocation(t, "Asia/Kathmandu")
	now := time.Date(2026, time.July, 15, 5, 10, 0, 0, location)
	root := filepath.Join(t.TempDir(), "keys")
	workDir := filepath.Join(t.TempDir(), "uploader")
	if errMkdir := os.MkdirAll(workDir, 0o750); errMkdir != nil {
		t.Fatalf("create work directory: %v", errMkdir)
	}
	cfg := testConfig(root, workDir)
	cfg.Timezone = "Asia/Kathmandu"
	cfg.Supabase.Enabled = true
	service := mustTestService(t, cfg, nil, now)
	state := validSecurityUploadState(service)
	for hourKey, prepared := range state.PreparedHours {
		delete(state.PreparedHours, hourKey)
		prepared.Hour = prepared.Hour.Truncate(time.Hour)
		prepared.Sources[0].JSONLBytes = exactInt64(prepared.JSONLBytes)
		prepared.Usage = []preparedUsage{{
			KeyName:     prepared.Sources[0].KeyName,
			Provider:    prepared.Provider,
			SourceCount: 1,
			SourceBytes: prepared.Sources[0].Size,
			JSONLBytes:  prepared.JSONLBytes,
		}}
		mustBindPreparedUsageIntegrity(service, &prepared)
		prepared.Hour = prepared.Hour.Add(time.Hour)
		state.PreparedHours[hourStateKey(prepared.Hour, prepared.Provider)] = prepared
	}
	if errSave := service.saveState(*state); errSave != nil {
		t.Fatalf("save coherently tampered prepared hour: %v", errSave)
	}

	_, errLoad := service.loadState()
	if errLoad == nil || !strings.Contains(errLoad.Error(), "usage checksum mismatch") {
		t.Fatalf("coherently tampered prepared hour error = %v, want usage checksum mismatch", errLoad)
	}
}

func TestPreparedUsageSHA256BindsDSTFoldOffset(t *testing.T) {
	t.Parallel()

	location := mustLocation(t, "America/New_York")
	cfg := testConfig(t.TempDir(), t.TempDir())
	cfg.Timezone = "America/New_York"
	service := mustTestService(t, cfg, nil, time.Date(2026, time.November, 1, 8, 0, 0, 0, time.UTC))
	first := validPreparedHourForSupabaseEvent(service)
	first.Hour = time.Date(2026, time.November, 1, 5, 0, 0, 0, time.UTC).In(location)
	second := first
	second.Hour = time.Date(2026, time.November, 1, 6, 0, 0, 0, time.UTC).In(location)
	if hourStateKey(first.Hour, first.Provider) != hourStateKey(second.Hour, second.Provider) {
		t.Fatalf("fold hours unexpectedly have different wall keys: %s != %s", hourStateKey(first.Hour, first.Provider), hourStateKey(second.Hour, second.Provider))
	}

	firstSHA, errFirst := service.preparedUsageSHA256(first)
	if errFirst != nil {
		t.Fatalf("checksum first fold hour: %v", errFirst)
	}
	secondSHA, errSecond := service.preparedUsageSHA256(second)
	if errSecond != nil {
		t.Fatalf("checksum second fold hour: %v", errSecond)
	}
	if firstSHA == secondSHA {
		t.Fatalf("DST fold offsets produced the same usage checksum %s", firstSHA)
	}
}

func TestPreparedHourBoundaryUsesPolicyV1AbsoluteHour(t *testing.T) {
	t.Parallel()

	location := mustLocation(t, "Asia/Kathmandu")
	cfg := testConfig(t.TempDir(), t.TempDir())
	cfg.Timezone = "Asia/Kathmandu"
	service := mustTestService(t, cfg, nil, time.Date(2026, time.July, 15, 5, 10, 0, 0, location))
	absoluteHour := time.Date(2026, time.July, 15, 1, 37, 0, 0, location).Truncate(time.Hour)
	if absoluteHour.Minute() == 0 {
		t.Fatalf("policy-v1 absolute hour unexpectedly used local HH:00: %s", absoluteHour)
	}
	if errBoundary := service.validatePreparedHourBoundary(absoluteHour); errBoundary != nil {
		t.Fatalf("policy-v1 absolute hour rejected: %v", errBoundary)
	}

	localWallHour := time.Date(2026, time.July, 15, 1, 0, 0, 0, location)
	if errBoundary := service.validatePreparedHourBoundary(localWallHour); errBoundary == nil ||
		!strings.Contains(errBoundary.Error(), "canonical hour boundary") {
		t.Fatalf("noncanonical local-wall hour error = %v", errBoundary)
	}
}

func TestPreparedHourRejectsNonCanonicalHourBoundary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		offset time.Duration
	}{
		{name: "minute", offset: time.Minute},
		{name: "second", offset: time.Second},
		{name: "nanosecond", offset: time.Nanosecond},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := mustSupabaseEventService(t)
			prepared := validPreparedHourForSupabaseEvent(service)
			prepared.Hour = prepared.Hour.Add(test.offset)
			if _, errPrepare := service.prepareSupabaseEvent(prepared); errPrepare == nil ||
				!strings.Contains(errPrepare.Error(), "canonical hour boundary") {
				t.Fatalf("prepare non-canonical hour error = %v", errPrepare)
			}

			state := validSecurityUploadState(service)
			for hourKey, statePrepared := range state.PreparedHours {
				statePrepared.Sources[0].JSONLBytes = exactInt64(statePrepared.JSONLBytes)
				statePrepared.Usage = []preparedUsage{{
					KeyName:     statePrepared.Sources[0].KeyName,
					Provider:    statePrepared.Provider,
					SourceCount: 1,
					SourceBytes: statePrepared.Sources[0].Size,
					JSONLBytes:  statePrepared.JSONLBytes,
				}}
				mustBindPreparedUsageIntegrity(service, &statePrepared)
				statePrepared.Hour = statePrepared.Hour.Add(test.offset)
				state.PreparedHours[hourKey] = statePrepared
			}
			if errValidate := service.validateUploadState(state); errValidate == nil ||
				!strings.Contains(errValidate.Error(), "canonical hour boundary") {
				t.Fatalf("validate non-canonical hour state error = %v", errValidate)
			}
		})
	}
}

func mustSupabaseEventService(t *testing.T) *Service {
	t.Helper()
	location := mustLocation(t, "Asia/Shanghai")
	cfg := testConfig(t.TempDir(), t.TempDir())
	cfg.Supabase.Enabled = true
	return mustTestService(t, cfg, nil, time.Date(2026, time.July, 15, 3, 0, 0, 0, location))
}

func validPreparedHourForSupabaseEvent(service *Service) preparedHour {
	prepared := preparedHour{
		TargetID:        service.target.ID,
		Hour:            time.Date(2026, time.July, 15, 1, 0, 0, 0, service.location),
		Provider:        providerCodex,
		ObjectKey:       "cliproxy-logs/2026/07/15/archive.jsonl.zst",
		JSONLBytes:      280,
		CompressedBytes: 180,
		ArchiveSHA256:   strings.Repeat("a", 64),
		ManifestSHA256:  strings.Repeat("b", 64),
		Sources: []preparedSource{
			{KeyName: "panda", Size: 100, JSONLBytes: exactInt64(80)},
			{KeyName: "alice", Size: 200, JSONLBytes: exactInt64(160)},
			{KeyName: "panda", Size: 50, JSONLBytes: exactInt64(40)},
		},
		Usage: []preparedUsage{
			{KeyName: "panda", Provider: providerCodex, SourceCount: 2, SourceBytes: 150, JSONLBytes: 120},
			{KeyName: "alice", Provider: providerCodex, SourceCount: 1, SourceBytes: 200, JSONLBytes: 160},
		},
	}
	mustBindPreparedUsageIntegrity(service, &prepared)
	return prepared
}

func exactInt64(value int64) *int64 {
	return &value
}

func mustBindPreparedUsageIntegrity(service *Service, prepared *preparedHour) {
	prepared.UsageSchemaVersion = preparedUsageSchemaVersion
	usageSHA256, errUsageSHA := service.preparedUsageSHA256(*prepared)
	if errUsageSHA != nil {
		panic(errUsageSHA)
	}
	prepared.UsageSHA256 = usageSHA256
}

func mustSupabaseEventPayload(t *testing.T) supabaseEventPayload {
	t.Helper()
	service := mustSupabaseEventService(t)
	event, errPrepare := service.prepareSupabaseEvent(validPreparedHourForSupabaseEvent(service))
	if errPrepare != nil {
		t.Fatalf("prepare valid Supabase event: %v", errPrepare)
	}
	var payload supabaseEventPayload
	if errUnmarshal := json.Unmarshal(event.RawJSON(), &payload); errUnmarshal != nil {
		t.Fatalf("decode valid Supabase event: %v", errUnmarshal)
	}
	return payload
}
