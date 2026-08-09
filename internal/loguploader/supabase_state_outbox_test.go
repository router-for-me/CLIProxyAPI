package loguploader

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestLoadStateMigratesV2ToV3WithEmptySupabaseOutbox(t *testing.T) {
	t.Parallel()

	service := newStateOutboxTestService(t, true)
	state := service.newUploadState()
	state.SchemaVersion = 2
	state.SupabaseOutbox = supabaseOutboxState{}
	state.SupabaseHistory = nil
	if errSave := service.saveState(state); errSave != nil {
		t.Fatalf("save schema-v2 state: %v", errSave)
	}

	loaded, errLoad := service.loadState()
	if errLoad != nil {
		t.Fatalf("load schema-v2 state: %v", errLoad)
	}
	wantDestinationID, errDestination := supabaseDestinationID(service.cfg.Supabase.IngestURL)
	if errDestination != nil {
		t.Fatalf("calculate Supabase destination ID: %v", errDestination)
	}
	if loaded.SchemaVersion != 3 || loaded.SupabaseOutbox.SchemaVersion != 1 ||
		loaded.SupabaseOutbox.DestinationID != wantDestinationID || len(loaded.SupabaseOutbox.Entries) != 0 ||
		loaded.SupabaseHistory == nil || len(loaded.SupabaseHistory) != 0 {
		t.Fatalf("unexpected migrated state: %#v", loaded)
	}
	if !loaded.dirty {
		t.Fatal("schema-v2 migration did not mark state dirty for in-lock persistence")
	}
}

func TestSupabaseOutboxPayloadRoundTripsExactBytes(t *testing.T) {
	t.Parallel()

	service := newStateOutboxTestService(t, true)
	state, entry := validStateOutboxEntry(t, service, "张三<&>")
	state.SupabaseOutbox.Entries[entry.EventID] = entry
	if errSave := service.saveState(state); errSave != nil {
		t.Fatalf("save state with Supabase outbox: %v", errSave)
	}

	rawState, errRead := os.ReadFile(service.statePath())
	if errRead != nil {
		t.Fatalf("read saved state: %v", errRead)
	}
	var encoded struct {
		SupabaseOutbox struct {
			Entries map[string]struct {
				Payload string `json:"payload"`
			} `json:"entries"`
		} `json:"supabase_outbox"`
	}
	if errUnmarshal := json.Unmarshal(rawState, &encoded); errUnmarshal != nil {
		t.Fatalf("decode saved state wrapper: %v", errUnmarshal)
	}
	if gotEncoded, wantEncoded := encoded.SupabaseOutbox.Entries[entry.EventID].Payload, base64.StdEncoding.EncodeToString(entry.Payload); gotEncoded != wantEncoded {
		t.Fatalf("stored payload = %q, want exact base64 %q", gotEncoded, wantEncoded)
	}

	loaded, errLoad := service.loadState()
	if errLoad != nil {
		t.Fatalf("load state with Supabase outbox: %v", errLoad)
	}
	got := loaded.SupabaseOutbox.Entries[entry.EventID].Payload
	if !bytes.Equal(got, entry.Payload) {
		t.Fatalf("payload bytes changed across save/load\ngot:  %q\nwant: %q", got, entry.Payload)
	}
}

func TestSupabaseDestinationIDNormalizesEquivalentIngestURLs(t *testing.T) {
	t.Parallel()

	first, errFirst := supabaseDestinationID(" https://Project-Ref.Supabase.co:443/functions/v1/log-stats-ingest ")
	if errFirst != nil {
		t.Fatalf("calculate first destination ID: %v", errFirst)
	}
	second, errSecond := supabaseDestinationID("https://project-ref.supabase.co/functions/v1/log-stats-ingest")
	if errSecond != nil {
		t.Fatalf("calculate second destination ID: %v", errSecond)
	}
	if first != second {
		t.Fatalf("equivalent URLs produced different destination IDs: %s != %s", first, second)
	}
	if len(first) != 64 || strings.ToLower(first) != first {
		t.Fatalf("destination ID = %q, want 64 lowercase hex characters", first)
	}
}

func TestLoadStateRetargetsValidatedNonEmptySupabaseOutbox(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		status        string
		blockCategory string
		blockStatus   int
	}{
		{name: "pending", status: supabaseOutboxStatusPending},
		{name: "blocked", status: supabaseOutboxStatusBlocked, blockCategory: "unprocessable", blockStatus: http.StatusUnprocessableEntity},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := newStateOutboxTestService(t, true)
			state, entry := validStateOutboxEntry(t, service, "alice-"+test.name)
			entry.Status = test.status
			entry.BlockCategory = test.blockCategory
			entry.BlockStatus = test.blockStatus
			state.SupabaseOutbox.Entries[entry.EventID] = entry
			if errSave := service.saveState(state); errSave != nil {
				t.Fatalf("save bound Supabase outbox: %v", errSave)
			}
			before, errRead := os.ReadFile(service.statePath())
			if errRead != nil {
				t.Fatalf("read state before retarget: %v", errRead)
			}

			retargetConfig := service.cfg
			retargetConfig.Supabase.IngestURL = "https://different-project.supabase.co/functions/v1/log-stats-ingest"
			retargetService := mustTestService(t, retargetConfig, nil, service.now())
			loaded, errLoad := retargetService.loadState()
			if errLoad != nil {
				t.Fatalf("load validated outbox for retarget: %v", errLoad)
			}
			wantDestinationID, errDestination := supabaseDestinationID(retargetConfig.Supabase.IngestURL)
			if errDestination != nil {
				t.Fatalf("calculate retarget destination: %v", errDestination)
			}
			got, exists := loaded.SupabaseOutbox.Entries[entry.EventID]
			if !exists || loaded.SupabaseOutbox.DestinationID != wantDestinationID || !loaded.dirty {
				t.Fatalf("retargeted outbox = %#v dirty=%t", loaded.SupabaseOutbox, loaded.dirty)
			}
			if got.EventID != entry.EventID || got.HourKey != entry.HourKey || got.ObjectKey != entry.ObjectKey ||
				got.Status != entry.Status || got.PayloadSHA256 != entry.PayloadSHA256 ||
				got.BlockCategory != entry.BlockCategory || got.BlockStatus != entry.BlockStatus ||
				!got.EnqueuedAt.Equal(entry.EnqueuedAt) || !bytes.Equal(got.Payload, entry.Payload) {
				t.Fatalf("retarget changed outbox entry: got %#v want %#v", got, entry)
			}
			after, errRead := os.ReadFile(service.statePath())
			if errRead != nil {
				t.Fatalf("read state after retarget load: %v", errRead)
			}
			if !bytes.Equal(after, before) {
				t.Fatal("loadState persisted retarget before its caller chose a save point")
			}
		})
	}
}

func TestLoadStateRejectsInvalidOutboxBeforeRetargetWithoutPersisting(t *testing.T) {
	t.Parallel()

	service := newStateOutboxTestService(t, true)
	state, entry := validStateOutboxEntry(t, service, "invalid-retarget")
	entry.Payload = append(bytes.Clone(entry.Payload), ' ')
	state.SupabaseOutbox.Entries[entry.EventID] = entry
	if errSave := service.saveState(state); errSave != nil {
		t.Fatalf("save invalid retarget fixture: %v", errSave)
	}
	before, errRead := os.ReadFile(service.statePath())
	if errRead != nil {
		t.Fatalf("read invalid state before load: %v", errRead)
	}

	retargetConfig := service.cfg
	retargetConfig.Supabase.IngestURL = "https://different-project.supabase.co/functions/v1/log-stats-ingest"
	retargetService := mustTestService(t, retargetConfig, nil, service.now())
	_, errLoad := retargetService.loadState()
	if errLoad == nil || !strings.Contains(errLoad.Error(), "payload checksum does not match") {
		t.Fatalf("invalid entry retarget error = %v", errLoad)
	}
	after, errRead := os.ReadFile(service.statePath())
	if errRead != nil {
		t.Fatalf("read invalid state after load: %v", errRead)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("invalid outbox retarget was persisted")
	}
}

func TestLoadStateWithSupabaseDisabledPreservesExistingOutbox(t *testing.T) {
	t.Parallel()

	enabledService := newStateOutboxTestService(t, true)
	state, entry := validStateOutboxEntry(t, enabledService, "alice")
	state.SupabaseOutbox.Entries[entry.EventID] = entry
	wantDestinationID := state.SupabaseOutbox.DestinationID
	if errSave := enabledService.saveState(state); errSave != nil {
		t.Fatalf("save enabled Supabase outbox: %v", errSave)
	}

	disabledConfig := enabledService.cfg
	disabledConfig.Supabase.Enabled = false
	disabledService := mustTestService(t, disabledConfig, nil, enabledService.now())
	loaded, errLoad := disabledService.loadState()
	if errLoad != nil {
		t.Fatalf("load disabled Supabase outbox: %v", errLoad)
	}
	got, exists := loaded.SupabaseOutbox.Entries[entry.EventID]
	if !exists || !bytes.Equal(got.Payload, entry.Payload) || loaded.SupabaseOutbox.DestinationID != wantDestinationID {
		t.Fatalf("disabled load changed existing outbox: %#v", loaded.SupabaseOutbox)
	}
	if loaded.dirty {
		t.Fatal("disabled load unexpectedly rewrote existing Supabase outbox state")
	}
}

func TestLoadStateWithSupabaseDisabledRejectsInvalidOutboxDestination(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		destinationID string
		withEntry     bool
	}{
		{name: "empty with active entry", destinationID: "", withEntry: true},
		{name: "malformed with active entry", destinationID: "not-a-destination-sha", withEntry: true},
		{name: "uppercase with active entry", destinationID: strings.Repeat("A", 64), withEntry: true},
		{name: "malformed empty outbox", destinationID: "not-a-destination-sha"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			enabledService := newStateOutboxTestService(t, true)
			state, entry := validStateOutboxEntry(t, enabledService, "alice")
			state.SupabaseOutbox.DestinationID = test.destinationID
			if test.withEntry {
				state.SupabaseOutbox.Entries[entry.EventID] = entry
			}
			if errSave := enabledService.saveState(state); errSave != nil {
				t.Fatalf("save outbox with corrupt destination: %v", errSave)
			}

			disabledConfig := enabledService.cfg
			disabledConfig.Supabase.Enabled = false
			disabledService := mustTestService(t, disabledConfig, nil, enabledService.now())
			_, errLoad := disabledService.loadState()
			if errLoad == nil || !strings.Contains(errLoad.Error(), "destination identity") {
				t.Fatalf("invalid disabled destination error = %v", errLoad)
			}
		})
	}
}

func TestLoadStateRebindsEmptySupabaseOutboxDestination(t *testing.T) {
	t.Parallel()

	service := newStateOutboxTestService(t, true)
	state := service.newUploadState()
	state.SupabaseOutbox.DestinationID = strings.Repeat("f", 64)
	if errSave := service.saveState(state); errSave != nil {
		t.Fatalf("save empty outbox with old destination: %v", errSave)
	}

	loaded, errLoad := service.loadState()
	if errLoad != nil {
		t.Fatalf("load empty outbox for destination rebind: %v", errLoad)
	}
	wantDestinationID, errDestination := supabaseDestinationID(service.cfg.Supabase.IngestURL)
	if errDestination != nil {
		t.Fatalf("calculate configured destination ID: %v", errDestination)
	}
	if loaded.SupabaseOutbox.DestinationID != wantDestinationID || !loaded.dirty {
		t.Fatalf("empty outbox destination was not rebound: %#v", loaded.SupabaseOutbox)
	}
}

func TestValidateSupabaseOutboxRejectsCorruptEntries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		wantErr string
		mutate  func(t *testing.T, state *uploadState, entryID string)
	}{
		{name: "map key mismatch", wantErr: "map key", mutate: func(t *testing.T, state *uploadState, entryID string) {
			entry := state.SupabaseOutbox.Entries[entryID]
			delete(state.SupabaseOutbox.Entries, entryID)
			state.SupabaseOutbox.Entries["cliproxy-v1."+strings.Repeat("f", 64)] = entry
		}},
		{name: "payload checksum", wantErr: "payload checksum", mutate: func(t *testing.T, state *uploadState, entryID string) {
			entry := state.SupabaseOutbox.Entries[entryID]
			entry.PayloadSHA256 = strings.Repeat("0", 64)
			state.SupabaseOutbox.Entries[entryID] = entry
		}},
		{name: "payload event ID mismatch", wantErr: "payload event_id", mutate: func(t *testing.T, state *uploadState, entryID string) {
			mutateOutboxPayload(t, state, entryID, false, func(payload *supabaseEventPayload) {
				payload.EventID = "cliproxy-v1." + strings.Repeat("f", 64)
			})
		}},
		{name: "non-deterministic event ID", wantErr: "deterministic event ID", mutate: func(t *testing.T, state *uploadState, entryID string) {
			mutateOutboxPayload(t, state, entryID, true, func(payload *supabaseEventPayload) {
				payload.EventID = "cliproxy-v1." + strings.Repeat("f", 64)
			})
		}},
		{name: "exact usage provider mismatch", wantErr: "payload usage provider", mutate: func(t *testing.T, state *uploadState, entryID string) {
			mutateOutboxPayload(t, state, entryID, true, func(payload *supabaseEventPayload) {
				payload.Usage[0].Provider = providerClaude
				payload.EventID = supabaseEventID(*payload, providerCodex)
			})
		}},
		{name: "unknown payload field", wantErr: "strictly decode payload", mutate: func(t *testing.T, state *uploadState, entryID string) {
			entry := state.SupabaseOutbox.Entries[entryID]
			entry.Payload = bytes.Replace(entry.Payload, []byte("\n}\n"), []byte(",\n  \"unexpected\": true\n}\n"), 1)
			entry.PayloadSHA256 = sha256Hex(entry.Payload)
			state.SupabaseOutbox.Entries[entryID] = entry
		}},
		{name: "case insensitive CPA key name", wantErr: "secret-like", mutate: func(t *testing.T, state *uploadState, entryID string) {
			mutateOutboxPayload(t, state, entryID, true, func(payload *supabaseEventPayload) {
				payload.Usage[0].KeyName = "CpA_reserved"
				payload.EventID = supabaseEventID(*payload, providerCodex)
			})
		}},
		{name: "missing sealed hour", wantErr: "sealed hour", mutate: func(t *testing.T, state *uploadState, entryID string) {
			entry := state.SupabaseOutbox.Entries[entryID]
			entry.HourKey = "2026-07-15-02:codex"
			state.SupabaseOutbox.Entries[entryID] = entry
		}},
		{name: "entry object mismatch", wantErr: "object key", mutate: func(t *testing.T, state *uploadState, entryID string) {
			entry := state.SupabaseOutbox.Entries[entryID]
			entry.ObjectKey = "cliproxy-logs/wrong.jsonl.zst"
			state.SupabaseOutbox.Entries[entryID] = entry
		}},
		{name: "payload archive mismatch", wantErr: "archive checksum", mutate: func(t *testing.T, state *uploadState, entryID string) {
			mutateOutboxPayload(t, state, entryID, true, func(payload *supabaseEventPayload) {
				payload.ArchiveSHA256 = strings.Repeat("f", 64)
				payload.EventID = supabaseEventID(*payload, providerCodex)
			})
		}},
		{name: "payload manifest mismatch", wantErr: "manifest checksum", mutate: func(t *testing.T, state *uploadState, entryID string) {
			mutateOutboxPayload(t, state, entryID, true, func(payload *supabaseEventPayload) {
				payload.ManifestSHA256 = strings.Repeat("f", 64)
				payload.EventID = supabaseEventID(*payload, providerCodex)
			})
		}},
		{name: "payload target mismatch", wantErr: "target identity", mutate: func(t *testing.T, state *uploadState, entryID string) {
			mutateOutboxPayload(t, state, entryID, true, func(payload *supabaseEventPayload) {
				payload.TargetID = "tos:" + strings.Repeat("f", 64)
				payload.EventID = supabaseEventID(*payload, providerCodex)
			})
		}},
		{name: "payload compressed bytes mismatch", wantErr: "compressed bytes", mutate: func(t *testing.T, state *uploadState, entryID string) {
			mutateOutboxPayload(t, state, entryID, true, func(payload *supabaseEventPayload) {
				payload.CompressedBytes++
				payload.EventID = supabaseEventID(*payload, providerCodex)
			})
		}},
		{name: "payload hour mismatch", wantErr: "payload hour", mutate: func(t *testing.T, state *uploadState, entryID string) {
			mutateOutboxPayload(t, state, entryID, true, func(payload *supabaseEventPayload) {
				hourStart, errParse := time.Parse(time.RFC3339, payload.HourStart)
				if errParse != nil {
					t.Fatalf("parse valid payload hour: %v", errParse)
				}
				payload.HourStart = hourStart.Add(time.Hour).Format(time.RFC3339)
				payload.EventID = supabaseEventID(*payload, providerCodex)
			})
		}},
		{name: "invalid status", wantErr: "unsupported status", mutate: func(t *testing.T, state *uploadState, entryID string) {
			entry := state.SupabaseOutbox.Entries[entryID]
			entry.Status = "sent"
			state.SupabaseOutbox.Entries[entryID] = entry
		}},
		{name: "pending block metadata", wantErr: "pending entry", mutate: func(t *testing.T, state *uploadState, entryID string) {
			entry := state.SupabaseOutbox.Entries[entryID]
			entry.BlockCategory = "invalid-request"
			entry.BlockStatus = 400
			state.SupabaseOutbox.Entries[entryID] = entry
		}},
		{name: "blocked status metadata", wantErr: "blocked entry", mutate: func(t *testing.T, state *uploadState, entryID string) {
			entry := state.SupabaseOutbox.Entries[entryID]
			entry.Status = supabaseOutboxStatusBlocked
			entry.BlockCategory = "invalid-request"
			entry.BlockStatus = 401
			state.SupabaseOutbox.Entries[entryID] = entry
		}},
		{name: "empty enqueue time", wantErr: "enqueue time", mutate: func(t *testing.T, state *uploadState, entryID string) {
			entry := state.SupabaseOutbox.Entries[entryID]
			entry.EnqueuedAt = time.Time{}
			state.SupabaseOutbox.Entries[entryID] = entry
		}},
		{name: "outbox schema", wantErr: "outbox schema", mutate: func(t *testing.T, state *uploadState, entryID string) {
			state.SupabaseOutbox.SchemaVersion = 2
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := newStateOutboxTestService(t, true)
			state, entry := validStateOutboxEntry(t, service, "alice")
			state.SupabaseOutbox.Entries[entry.EventID] = entry
			test.mutate(t, &state, entry.EventID)
			errValidate := service.validateUploadState(&state)
			if errValidate == nil || !strings.Contains(errValidate.Error(), test.wantErr) {
				t.Fatalf("validate corrupt outbox error = %v, want %q", errValidate, test.wantErr)
			}
		})
	}
}

func TestValidateSupabaseOutboxRejectsNonCanonicalPayloadHourStart(t *testing.T) {
	t.Parallel()

	service := newStateOutboxTestService(t, true)
	state, entry := validStateOutboxEntry(t, service, "alice")
	sealedHour := state.Hours[entry.HourKey]
	delete(state.Hours, entry.HourKey)
	entry.HourKey = "2026-07-15-08:codex"
	state.Hours[entry.HourKey] = sealedHour
	state.SupabaseOutbox.Entries[entry.EventID] = entry
	mutateOutboxPayload(t, &state, entry.EventID, true, func(payload *supabaseEventPayload) {
		payload.HourStart = "2026-07-15T08:59:59+08:00"
		payload.EventID = supabaseEventID(*payload, providerCodex)
	})

	errValidate := service.validateUploadState(&state)
	if errValidate == nil || !strings.Contains(errValidate.Error(), "canonical hour boundary") {
		t.Fatalf("non-canonical payload hour error = %v", errValidate)
	}
}

func TestValidateSupabaseOutboxAcceptsBlockedMetadata(t *testing.T) {
	t.Parallel()

	tests := []struct {
		category string
		status   int
	}{
		{category: "invalid-request", status: 400},
		{category: "conflict", status: 409},
		{category: "payload-too-large", status: 413},
		{category: "unprocessable", status: 422},
	}
	for _, test := range tests {
		t.Run(test.category, func(t *testing.T) {
			service := newStateOutboxTestService(t, true)
			state, entry := validStateOutboxEntry(t, service, "alice")
			entry.Status = supabaseOutboxStatusBlocked
			entry.BlockCategory = test.category
			entry.BlockStatus = test.status
			state.SupabaseOutbox.Entries[entry.EventID] = entry
			if errValidate := service.validateUploadState(&state); errValidate != nil {
				t.Fatalf("valid blocked metadata rejected: %v", errValidate)
			}
		})
	}
}

func TestValidateSupabaseOutboxCapacityLimits(t *testing.T) {
	t.Parallel()
	if maxSupabaseOutboxTotalPayloadBytes != 64*1024*1024 {
		t.Fatalf("total outbox payload limit = %d, want 64 MiB", maxSupabaseOutboxTotalPayloadBytes)
	}

	if errCapacity := validateSupabaseOutboxCapacity(0, 0, maxSupabaseOutboxPayloadBytes+1); errCapacity == nil ||
		!strings.Contains(errCapacity.Error(), "4 MiB") {
		t.Fatalf("oversized payload capacity error = %v", errCapacity)
	}
	if errCapacity := validateSupabaseOutboxCapacity(maxSupabaseOutboxEntries, 0, 1); errCapacity == nil ||
		!strings.Contains(errCapacity.Error(), "10,000") {
		t.Fatalf("entry count capacity error = %v", errCapacity)
	}
	if errCapacity := validateSupabaseOutboxCapacity(0, maxSupabaseOutboxTotalPayloadBytes, 1); errCapacity == nil ||
		!strings.Contains(errCapacity.Error(), "64 MiB") {
		t.Fatalf("total payload capacity error = %v", errCapacity)
	}
	if errCapacity := validateSupabaseOutboxCapacity(
		maxSupabaseOutboxEntries-1,
		maxSupabaseOutboxTotalPayloadBytes-maxSupabaseOutboxPayloadBytes,
		maxSupabaseOutboxPayloadBytes,
	); errCapacity != nil {
		t.Fatalf("exact outbox capacity boundary rejected: %v", errCapacity)
	}
}

func TestSupabaseOutboxActiveCapacityIgnoresBlockedEntries(t *testing.T) {
	t.Parallel()

	entries := map[string]supabaseOutboxEntry{
		"pending": {
			Status:  supabaseOutboxStatusPending,
			Payload: []byte("pending"),
		},
		"blocked-a": {
			Status:  supabaseOutboxStatusBlocked,
			Payload: bytes.Repeat([]byte{'a'}, maxSupabaseOutboxPayloadBytes),
		},
		"blocked-b": {
			Status:  supabaseOutboxStatusBlocked,
			Payload: bytes.Repeat([]byte{'b'}, maxSupabaseOutboxPayloadBytes),
		},
	}

	activeEntries, activePayloadBytes, errCapacity := supabaseOutboxActiveCapacity(entries)
	if errCapacity != nil {
		t.Fatalf("active capacity: %v", errCapacity)
	}
	if activeEntries != 1 || activePayloadBytes != int64(len("pending")) {
		t.Fatalf("active capacity = entries %d bytes %d, want 1 and %d", activeEntries, activePayloadBytes, len("pending"))
	}
}

func TestValidateSupabaseOutboxCountsActualEntryMapBeforeEntryValidation(t *testing.T) {
	t.Parallel()

	service := newStateOutboxTestService(t, true)
	state := service.newUploadState()
	for index := 0; index <= maxSupabaseOutboxEntries; index++ {
		eventID := fmt.Sprintf("cliproxy-v1.%064x", index)
		state.SupabaseOutbox.Entries[eventID] = supabaseOutboxEntry{EventID: eventID, Status: supabaseOutboxStatusPending}
	}

	errValidate := service.validateUploadState(&state)
	if errValidate == nil || !strings.Contains(errValidate.Error(), "10,000") {
		t.Fatalf("actual entry map limit error = %v", errValidate)
	}
}

func TestValidateSupabaseOutboxAggregatesActualPayloadMapBeforeEntryValidation(t *testing.T) {
	t.Parallel()

	service := newStateOutboxTestService(t, true)
	state := service.newUploadState()
	sharedPayload := make([]byte, maxSupabaseOutboxPayloadBytes)
	entryCount := maxSupabaseOutboxTotalPayloadBytes/maxSupabaseOutboxPayloadBytes + 1
	for index := 0; index < entryCount; index++ {
		eventID := fmt.Sprintf("cliproxy-v1.%064x", index)
		state.SupabaseOutbox.Entries[eventID] = supabaseOutboxEntry{
			EventID: eventID,
			Status:  supabaseOutboxStatusPending,
			Payload: sharedPayload,
		}
	}

	errValidate := service.validateUploadState(&state)
	if errValidate == nil || !strings.Contains(errValidate.Error(), "64 MiB") {
		t.Fatalf("actual payload map limit error = %v", errValidate)
	}
}

func TestValidateSupabaseOutboxRejectsPayloadOverFourMiBBeforeDecode(t *testing.T) {
	t.Parallel()

	service := newStateOutboxTestService(t, true)
	state, entry := validStateOutboxEntry(t, service, "alice")
	entry.Payload = bytes.Repeat([]byte{'x'}, maxSupabaseOutboxPayloadBytes+1)
	entry.PayloadSHA256 = sha256Hex(entry.Payload)
	state.SupabaseOutbox.Entries[entry.EventID] = entry
	errValidate := service.validateUploadState(&state)
	if errValidate == nil || !strings.Contains(errValidate.Error(), "4 MiB") {
		t.Fatalf("oversized payload validation error = %v", errValidate)
	}
}

func TestLoadStateRejectsSchemaV2WithSupabaseData(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(state *uploadState, entry supabaseOutboxEntry)
	}{
		{name: "outbox", mutate: func(state *uploadState, entry supabaseOutboxEntry) {
			state.SupabaseOutbox.Entries[entry.EventID] = entry
		}},
		{name: "history", mutate: func(state *uploadState, entry supabaseOutboxEntry) {
			state.SupabaseHistory[entry.ObjectKey] = supabaseHistoryCheckpoint{
				ObjectKey: entry.ObjectKey, ArchiveSHA256: strings.Repeat("a", 64), EventID: entry.EventID, CommittedAt: time.Now(),
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := newStateOutboxTestService(t, true)
			state, entry := validStateOutboxEntry(t, service, "alice")
			state.SchemaVersion = 2
			test.mutate(&state, entry)
			if errSave := service.saveState(state); errSave != nil {
				t.Fatalf("save invalid schema-v2 state: %v", errSave)
			}
			_, errLoad := service.loadState()
			if errLoad == nil || !strings.Contains(errLoad.Error(), "must not contain Supabase") {
				t.Fatalf("schema-v2 Supabase data error = %v", errLoad)
			}
		})
	}
}

func TestSchemaV2ValidatorRejectsSchemaV3State(t *testing.T) {
	t.Parallel()

	service := newStateOutboxTestService(t, true)
	raw, errMarshal := json.Marshal(service.newUploadState())
	if errMarshal != nil {
		t.Fatalf("marshal schema-v3 state: %v", errMarshal)
	}
	if errValidate := validateSchemaV2Fixture(raw); errValidate == nil || !strings.Contains(errValidate.Error(), "unsupported") {
		t.Fatalf("schema-v2 validator error = %v, want unsupported schema", errValidate)
	}
}

func TestDrainSupabaseOutboxAcknowledgesMatchingHTTP200(t *testing.T) {
	tests := []struct {
		name          string
		status        string
		wantInserted  int
		wantDuplicate int
	}{
		{name: "inserted", status: "inserted", wantInserted: 1},
		{name: "duplicate", status: "duplicate", wantDuplicate: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := newStateOutboxTestService(t, true)
			state, entry := validStateOutboxEntry(t, service, "张三<&>")
			state.SupabaseOutbox.Entries[entry.EventID] = entry
			t.Setenv(service.cfg.Supabase.IngestTokenEnv, "  exact-token  ")

			service.supabaseHTTPDoer = httpDoerFunc(func(request *http.Request) (*http.Response, error) {
				if request.Method != http.MethodPost || request.URL.String() != service.cfg.Supabase.IngestURL {
					t.Fatalf("unexpected request target: %s %s", request.Method, request.URL)
				}
				body, errRead := io.ReadAll(request.Body)
				if errRead != nil {
					t.Fatalf("read request body: %v", errRead)
				}
				if !bytes.Equal(body, entry.Payload) {
					t.Fatalf("request body changed\ngot:  %q\nwant: %q", body, entry.Payload)
				}
				if request.Header.Get("Authorization") != "Bearer exact-token" ||
					request.Header.Get("Content-Type") != "application/json" ||
					request.Header.Get("Accept") != "application/json" ||
					request.Header.Get("apikey") != "" {
					t.Fatalf("unexpected request headers: %#v", request.Header)
				}
				if _, hasDeadline := request.Context().Deadline(); hasDeadline {
					t.Fatal("delivery added a request context deadline")
				}
				body = []byte(fmt.Sprintf(`{"status":%q,"event_id":%q}`, test.status, entry.EventID))
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body))}, nil
			})

			summary, errDrain := service.drainSupabaseOutbox(context.Background(), &state)
			if errDrain != nil {
				t.Fatalf("drain matching acknowledgement: %v", errDrain)
			}
			if summary.Attempted != 1 || summary.Inserted != test.wantInserted || summary.Duplicate != test.wantDuplicate ||
				summary.Blocked != 0 || summary.Retryable != 0 {
				t.Fatalf("unexpected delivery summary: %#v", summary)
			}
			if _, exists := state.SupabaseOutbox.Entries[entry.EventID]; exists {
				t.Fatal("acknowledged entry remained pending")
			}
			loaded, errLoad := service.loadState()
			if errLoad != nil {
				t.Fatalf("load acknowledged state: %v", errLoad)
			}
			if _, exists := loaded.SupabaseOutbox.Entries[entry.EventID]; exists {
				t.Fatal("acknowledged entry remained in durable state")
			}
		})
	}
}

func TestDrainSupabaseOutboxRejectsInvalidHTTP200(t *testing.T) {
	tests := []struct {
		name string
		body func(entry supabaseOutboxEntry) string
	}{
		{name: "event mismatch", body: func(supabaseOutboxEntry) string {
			return `{"status":"inserted","event_id":"cliproxy-v1.` + strings.Repeat("f", 64) + `"}`
		}},
		{name: "malformed", body: func(supabaseOutboxEntry) string { return `{"status":` }},
		{name: "unknown field", body: func(entry supabaseOutboxEntry) string {
			return fmt.Sprintf(`{"status":"inserted","event_id":%q,"secret":"untrusted-body"}`, entry.EventID)
		}},
		{name: "duplicate field", body: func(entry supabaseOutboxEntry) string {
			return fmt.Sprintf(`{"status":"inserted","status":"duplicate","event_id":%q}`, entry.EventID)
		}},
		{name: "missing field", body: func(supabaseOutboxEntry) string { return `{"status":"inserted"}` }},
		{name: "trailing object", body: func(entry supabaseOutboxEntry) string {
			return fmt.Sprintf(`{"status":"inserted","event_id":%q}{}`, entry.EventID)
		}},
		{name: "invalid status", body: func(entry supabaseOutboxEntry) string {
			return fmt.Sprintf(`{"status":"accepted","event_id":%q}`, entry.EventID)
		}},
		{name: "oversized", body: func(supabaseOutboxEntry) string {
			return strings.Repeat("x", maxSupabaseDeliveryResponseBytes+1)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := newStateOutboxTestService(t, true)
			state, entry := validStateOutboxEntry(t, service, "alice")
			state.SupabaseOutbox.Entries[entry.EventID] = entry
			t.Setenv(service.cfg.Supabase.IngestTokenEnv, "test-token")
			responseBody := &trackingReadCloser{reader: strings.NewReader(test.body(entry))}
			service.supabaseHTTPDoer = httpDoerFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Body: responseBody}, nil
			})

			summary, errDrain := service.drainSupabaseOutbox(context.Background(), &state)
			if !errors.Is(errDrain, errSupabaseDeliveryRetryable) || errDrain.Error() != errSupabaseDeliveryRetryable.Error() {
				t.Fatalf("invalid acknowledgement error = %v, want fixed generic retryable", errDrain)
			}
			if summary.Attempted != 1 || summary.Retryable != 1 {
				t.Fatalf("unexpected retry summary: %#v", summary)
			}
			if _, exists := state.SupabaseOutbox.Entries[entry.EventID]; !exists {
				t.Fatal("invalid acknowledgement removed pending entry")
			}
			if !responseBody.closed {
				t.Fatal("response body was not closed")
			}
			if responseBody.bytesRead > maxSupabaseDeliveryResponseBytes+1 {
				t.Fatalf("response read %d bytes, want at most %d", responseBody.bytesRead, maxSupabaseDeliveryResponseBytes+1)
			}
		})
	}
}

func TestDrainSupabaseOutboxClassifiesPermanentAndConfigurationStatuses(t *testing.T) {
	tests := []struct {
		name          string
		status        int
		wantCategory  string
		wantBlocked   int
		wantConfigErr bool
	}{
		{name: "bad request", status: http.StatusBadRequest, wantCategory: "invalid-request", wantBlocked: 1},
		{name: "conflict", status: http.StatusConflict, wantCategory: "conflict", wantBlocked: 1},
		{name: "payload too large", status: http.StatusRequestEntityTooLarge, wantCategory: "payload-too-large", wantBlocked: 1},
		{name: "unprocessable", status: http.StatusUnprocessableEntity, wantCategory: "unprocessable", wantBlocked: 1},
		{name: "unauthorized", status: http.StatusUnauthorized, wantConfigErr: true},
		{name: "forbidden", status: http.StatusForbidden, wantConfigErr: true},
		{name: "not found", status: http.StatusNotFound, wantConfigErr: true},
		{name: "method not allowed", status: http.StatusMethodNotAllowed, wantConfigErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := newStateOutboxTestService(t, true)
			state, entry := validStateOutboxEntry(t, service, "alice")
			state.SupabaseOutbox.Entries[entry.EventID] = entry
			t.Setenv(service.cfg.Supabase.IngestTokenEnv, "test-token")
			service.supabaseHTTPDoer = httpDoerFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: test.status, Body: io.NopCloser(strings.NewReader("untrusted-body"))}, nil
			})

			summary, errDrain := service.drainSupabaseOutbox(context.Background(), &state)
			if test.wantConfigErr {
				if !errors.Is(errDrain, errSupabaseDeliveryConfiguration) || errDrain.Error() != errSupabaseDeliveryConfiguration.Error() {
					t.Fatalf("configuration status error = %v", errDrain)
				}
				if state.SupabaseOutbox.Entries[entry.EventID].Status != supabaseOutboxStatusPending {
					t.Fatal("configuration error changed pending entry")
				}
				return
			}
			if !errors.Is(errDrain, errSupabaseDeliveryBlocked) || errDrain.Error() != errSupabaseDeliveryBlocked.Error() {
				t.Fatalf("blocked response error = %v", errDrain)
			}
			got := state.SupabaseOutbox.Entries[entry.EventID]
			if summary.Attempted != 1 || summary.Blocked != test.wantBlocked || got.Status != supabaseOutboxStatusBlocked ||
				got.BlockCategory != test.wantCategory || got.BlockStatus != test.status {
				t.Fatalf("unexpected blocked transition: summary=%#v entry=%#v", summary, got)
			}
			loaded, errLoad := service.loadState()
			if errLoad != nil {
				t.Fatalf("load blocked state: %v", errLoad)
			}
			durable := loaded.SupabaseOutbox.Entries[entry.EventID]
			if durable.Status != supabaseOutboxStatusBlocked || durable.BlockCategory != test.wantCategory || durable.BlockStatus != test.status {
				t.Fatalf("blocked transition was not durable: %#v", durable)
			}
		})
	}
}

func TestDrainSupabaseOutboxRetriesTransientAndUnknownStatuses(t *testing.T) {
	statuses := []int{
		http.StatusCreated,
		http.StatusAccepted,
		http.StatusNoContent,
		http.StatusTemporaryRedirect,
		http.StatusRequestTimeout,
		http.StatusTooEarly,
		http.StatusTooManyRequests,
		http.StatusTeapot,
		http.StatusInternalServerError,
		http.StatusServiceUnavailable,
	}
	for _, status := range statuses {
		t.Run(fmt.Sprintf("status %d", status), func(t *testing.T) {
			service := newStateOutboxTestService(t, true)
			state, entry := validStateOutboxEntry(t, service, "alice")
			state.SupabaseOutbox.Entries[entry.EventID] = entry
			t.Setenv(service.cfg.Supabase.IngestTokenEnv, "test-token")
			service.supabaseHTTPDoer = httpDoerFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("untrusted-body"))}, nil
			})

			summary, errDrain := service.drainSupabaseOutbox(context.Background(), &state)
			if !errors.Is(errDrain, errSupabaseDeliveryRetryable) || errDrain.Error() != errSupabaseDeliveryRetryable.Error() {
				t.Fatalf("status %d error = %v, want fixed generic retryable", status, errDrain)
			}
			if summary.Attempted != 1 || summary.Retryable != 1 || len(state.SupabaseOutbox.Entries) != 1 {
				t.Fatalf("status %d changed retryable entry: summary=%#v state=%#v", status, summary, state.SupabaseOutbox)
			}
		})
	}
}

func TestDrainSupabaseOutboxClassifiesNon200WithoutReadingBody(t *testing.T) {
	statuses := []struct {
		name          string
		status        int
		blockCategory string
		configuration bool
	}{
		{name: "bad request", status: http.StatusBadRequest, blockCategory: "invalid-request"},
		{name: "conflict", status: http.StatusConflict, blockCategory: "conflict"},
		{name: "payload too large", status: http.StatusRequestEntityTooLarge, blockCategory: "payload-too-large"},
		{name: "unprocessable", status: http.StatusUnprocessableEntity, blockCategory: "unprocessable"},
		{name: "unauthorized", status: http.StatusUnauthorized, configuration: true},
		{name: "forbidden", status: http.StatusForbidden, configuration: true},
		{name: "not found", status: http.StatusNotFound, configuration: true},
		{name: "method not allowed", status: http.StatusMethodNotAllowed, configuration: true},
	}
	bodies := []struct {
		name    string
		newBody func() *non200ResponseBody
	}{
		{name: "nil body"},
		{name: "truncated read error", newBody: func() *non200ResponseBody {
			return &non200ResponseBody{closeErr: nil}
		}},
		{name: "close error", newBody: func() *non200ResponseBody {
			return &non200ResponseBody{closeErr: errors.New("untrusted-close-error")}
		}},
	}
	for _, status := range statuses {
		for _, bodyCase := range bodies {
			t.Run(status.name+"/"+bodyCase.name, func(t *testing.T) {
				service := newStateOutboxTestService(t, true)
				state, entry := validStateOutboxEntry(t, service, "alice")
				state.SupabaseOutbox.Entries[entry.EventID] = entry
				t.Setenv(service.cfg.Supabase.IngestTokenEnv, "test-token")
				var responseBody *non200ResponseBody
				var body io.ReadCloser
				if bodyCase.newBody != nil {
					responseBody = bodyCase.newBody()
					body = responseBody
				}
				service.supabaseHTTPDoer = httpDoerFunc(func(*http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: status.status, Body: body}, nil
				})

				summary, errDrain := service.drainSupabaseOutbox(context.Background(), &state)
				if status.configuration {
					if !errors.Is(errDrain, errSupabaseDeliveryConfiguration) || errDrain.Error() != errSupabaseDeliveryConfiguration.Error() {
						t.Fatalf("configuration response error = %v", errDrain)
					}
					if summary.Attempted != 1 || state.SupabaseOutbox.Entries[entry.EventID].Status != supabaseOutboxStatusPending {
						t.Fatalf("configuration response changed pending state: summary=%#v entry=%#v", summary, state.SupabaseOutbox.Entries[entry.EventID])
					}
				} else {
					if !errors.Is(errDrain, errSupabaseDeliveryBlocked) || errDrain.Error() != errSupabaseDeliveryBlocked.Error() ||
						summary.Attempted != 1 || summary.Blocked != 1 {
						t.Fatalf("permanent response = summary %#v, error %v", summary, errDrain)
					}
					blocked := state.SupabaseOutbox.Entries[entry.EventID]
					if blocked.Status != supabaseOutboxStatusBlocked || blocked.BlockCategory != status.blockCategory || blocked.BlockStatus != status.status {
						t.Fatalf("permanent response was not blocked: %#v", blocked)
					}
				}
				if responseBody != nil && (responseBody.readCalls != 0 || responseBody.closeCalls != 1) {
					t.Fatalf("non-200 body lifecycle = reads %d, closes %d; want reads 0, closes 1", responseBody.readCalls, responseBody.closeCalls)
				}
			})
		}
	}
}

func TestDrainSupabaseOutboxMissingTokenStopsBeforeRequest(t *testing.T) {
	service := newStateOutboxTestService(t, true)
	state, entry := validStateOutboxEntry(t, service, "alice")
	state.SupabaseOutbox.Entries[entry.EventID] = entry
	t.Setenv(service.cfg.Supabase.IngestTokenEnv, " \t\n ")
	requestCount := 0
	service.supabaseHTTPDoer = httpDoerFunc(func(*http.Request) (*http.Response, error) {
		requestCount++
		return nil, errors.New("must not be called")
	})

	summary, errDrain := service.drainSupabaseOutbox(context.Background(), &state)
	if !errors.Is(errDrain, errSupabaseDeliveryConfiguration) || errDrain.Error() != errSupabaseDeliveryConfiguration.Error() {
		t.Fatalf("missing token error = %v, want fixed generic configuration error", errDrain)
	}
	if requestCount != 0 || summary != (supabaseDeliverySummary{}) {
		t.Fatalf("missing token sent a request: count=%d summary=%#v", requestCount, summary)
	}
	for _, forbidden := range []string{service.cfg.Supabase.IngestTokenEnv, service.cfg.Supabase.IngestURL, entry.EventID} {
		if strings.Contains(errDrain.Error(), forbidden) {
			t.Fatalf("configuration error leaked %q: %v", forbidden, errDrain)
		}
	}
}

func TestDrainSupabaseOutboxDefaultHTTPClientSettings(t *testing.T) {
	client := newSupabaseHTTPClient()
	if client.Timeout != 0 {
		t.Fatalf("client timeout = %s, want zero", client.Timeout)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", client.Transport)
	}
	if !transport.DisableCompression {
		t.Fatal("automatic response compression is enabled")
	}
	if transport.TLSHandshakeTimeout != 0 || transport.ResponseHeaderTimeout != 0 ||
		transport.IdleConnTimeout != 0 || transport.ExpectContinueTimeout != 0 {
		t.Fatalf("post-connect transport timeouts are enabled: %#v", transport)
	}
	if client.CheckRedirect == nil {
		t.Fatal("redirect policy is unset")
	}
	if errRedirect := client.CheckRedirect(&http.Request{}, nil); !errors.Is(errRedirect, http.ErrUseLastResponse) {
		t.Fatalf("redirect policy error = %v, want ErrUseLastResponse", errRedirect)
	}
}

func TestDrainSupabaseOutboxServiceOwnsReusableDefaultClient(t *testing.T) {
	service := newStateOutboxTestService(t, true)
	client, ok := service.supabaseHTTPDoer.(*http.Client)
	if !ok || client == nil {
		t.Fatalf("service default HTTP doer = %T, want reusable *http.Client", service.supabaseHTTPDoer)
	}
	if client.Timeout != 0 {
		t.Fatalf("service client timeout = %s, want zero", client.Timeout)
	}
}

func TestDrainSupabaseOutboxDefaultClientDoesNotRedirectOrCompress(t *testing.T) {
	targetRequests := make(chan struct{}, 1)
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		targetRequests <- struct{}{}
	}))
	defer target.Close()

	service := newStateOutboxTestService(t, true)
	state, entry := validStateOutboxEntry(t, service, "alice")
	state.SupabaseOutbox.Entries[entry.EventID] = entry
	t.Setenv(service.cfg.Supabase.IngestTokenEnv, "test-token")
	type capturedRequest struct {
		headers http.Header
		body    []byte
		err     error
	}
	capturedRequests := make(chan capturedRequest, 1)
	redirect := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gotBody, errRead := io.ReadAll(request.Body)
		if errRead != nil {
			capturedRequests <- capturedRequest{err: errRead}
			http.Error(writer, "request read failed", http.StatusInternalServerError)
			return
		}
		capturedRequests <- capturedRequest{headers: request.Header.Clone(), body: gotBody}
		http.Redirect(writer, request, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	service.cfg.Supabase.IngestURL = redirect.URL + "/functions/v1/log-stats-ingest"

	summary, errDrain := service.drainSupabaseOutbox(context.Background(), &state)
	if !errors.Is(errDrain, errSupabaseDeliveryRetryable) || summary.Attempted != 1 || summary.Retryable != 1 {
		t.Fatalf("redirect response = summary %#v, error %v", summary, errDrain)
	}
	if len(targetRequests) != 0 {
		t.Fatalf("client followed redirect %d time(s)", len(targetRequests))
	}
	captured := <-capturedRequests
	if captured.err != nil {
		t.Fatalf("read wire request body: %v", captured.err)
	}
	gotHeaders := captured.headers
	if gotHeaders.Get("Accept-Encoding") != "" || gotHeaders.Get("User-Agent") != "" {
		t.Fatalf("client added transport headers: %#v", gotHeaders)
	}
	if gotHeaders.Get("Authorization") != "Bearer test-token" || gotHeaders.Get("Content-Type") != "application/json" ||
		gotHeaders.Get("Accept") != "application/json" || gotHeaders.Get("apikey") != "" {
		t.Fatalf("unexpected wire headers: %#v", gotHeaders)
	}
	if !bytes.Equal(captured.body, entry.Payload) {
		t.Fatalf("wire request body changed\ngot:  %q\nwant: %q", captured.body, entry.Payload)
	}
	nonemptyHeaders := 0
	for name, values := range gotHeaders {
		if name == "Content-Length" {
			continue
		}
		if len(values) > 0 && strings.TrimSpace(values[0]) != "" {
			nonemptyHeaders++
		}
	}
	if nonemptyHeaders != 3 {
		t.Fatalf("wire request has %d nonempty headers, want exactly 3: %#v", nonemptyHeaders, gotHeaders)
	}
}

func TestDrainSupabaseOutboxReloadsTokenBeforeEveryRequest(t *testing.T) {
	service := newStateOutboxTestService(t, true)
	state, entries := deliveryStateWithEntries(t, service, "alice", "panda")
	t.Setenv(service.cfg.Supabase.IngestTokenEnv, " first-token ")
	var authorizations []string
	service.supabaseHTTPDoer = httpDoerFunc(func(request *http.Request) (*http.Response, error) {
		authorizations = append(authorizations, request.Header.Get("Authorization"))
		if len(authorizations) == 1 {
			if errSet := os.Setenv(service.cfg.Supabase.IngestTokenEnv, " second-token "); errSet != nil {
				t.Fatalf("replace delivery token: %v", errSet)
			}
		}
		eventID := eventIDFromDeliveryRequest(t, request)
		body := fmt.Sprintf(`{"status":"inserted","event_id":%q}`, eventID)
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})

	summary, errDrain := service.drainSupabaseOutbox(context.Background(), &state)
	if errDrain != nil || summary.Attempted != len(entries) || summary.Inserted != len(entries) {
		t.Fatalf("reload-token drain = summary %#v, error %v", summary, errDrain)
	}
	if got, want := strings.Join(authorizations, ","), "Bearer first-token,Bearer second-token"; got != want {
		t.Fatalf("authorization sequence = %q, want %q", got, want)
	}
}

func TestDrainSupabaseOutboxRetriesBlockedAndSortsSnapshotOnce(t *testing.T) {
	service := newStateOutboxTestService(t, true)
	state, entries := deliveryStateWithEntries(t, service, "blocked", "late", "early-b", "early-a")
	t.Setenv(service.cfg.Supabase.IngestTokenEnv, "test-token")

	blocked := entries[0]
	blocked.Status = supabaseOutboxStatusBlocked
	blocked.BlockCategory = "invalid-request"
	blocked.BlockStatus = http.StatusBadRequest
	state.SupabaseOutbox.Entries[blocked.EventID] = blocked
	entries[1].HourKey = "2026-07-15-02:codex"
	entries[2].HourKey = "2026-07-15-01:codex"
	entries[3].HourKey = "2026-07-15-01:codex"
	for _, entry := range entries[1:] {
		state.SupabaseOutbox.Entries[entry.EventID] = entry
	}
	wantOrder := append([]supabaseOutboxEntry(nil), entries...)
	sort.Slice(wantOrder, func(i, j int) bool {
		if wantOrder[i].HourKey != wantOrder[j].HourKey {
			return wantOrder[i].HourKey < wantOrder[j].HourKey
		}
		return wantOrder[i].EventID < wantOrder[j].EventID
	})
	var gotOrder []string
	service.supabaseHTTPDoer = httpDoerFunc(func(request *http.Request) (*http.Response, error) {
		eventID := eventIDFromDeliveryRequest(t, request)
		gotOrder = append(gotOrder, eventID)
		body := fmt.Sprintf(`{"status":"inserted","event_id":%q}`, eventID)
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})

	summary, errDrain := service.drainSupabaseOutbox(context.Background(), &state)
	if errDrain != nil || summary.Attempted != 4 || summary.Inserted != 4 {
		t.Fatalf("sorted drain = summary %#v, error %v", summary, errDrain)
	}
	wantIDs := make([]string, 0, len(wantOrder))
	for _, entry := range wantOrder {
		wantIDs = append(wantIDs, entry.EventID)
	}
	if got, want := strings.Join(gotOrder, ","), strings.Join(wantIDs, ","); got != want {
		t.Fatalf("delivery order = %q, want %q", got, want)
	}
	if len(state.SupabaseOutbox.Entries) != 0 {
		t.Fatalf("acknowledged snapshot entries remain: %#v", state.SupabaseOutbox.Entries)
	}
}

func TestDrainSupabaseOutboxContinuesPastRetryableAndBlocked(t *testing.T) {
	service := newStateOutboxTestService(t, true)
	state, entries := deliveryStateWithEntries(t, service, "retry", "blocked", "inserted")
	t.Setenv(service.cfg.Supabase.IngestTokenEnv, "test-token")
	for index := range entries {
		entries[index].HourKey = fmt.Sprintf("%02d", index)
		state.SupabaseOutbox.Entries[entries[index].EventID] = entries[index]
	}
	requestCount := 0
	service.supabaseHTTPDoer = httpDoerFunc(func(request *http.Request) (*http.Response, error) {
		requestCount++
		switch requestCount {
		case 1:
			return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(strings.NewReader("retry"))}, nil
		case 2:
			return &http.Response{StatusCode: http.StatusBadRequest, Body: io.NopCloser(strings.NewReader("blocked"))}, nil
		default:
			eventID := eventIDFromDeliveryRequest(t, request)
			body := fmt.Sprintf(`{"status":"inserted","event_id":%q}`, eventID)
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
		}
	})

	summary, errDrain := service.drainSupabaseOutbox(context.Background(), &state)
	if !errors.Is(errDrain, errSupabaseDeliveryRetryable) || !errors.Is(errDrain, errSupabaseDeliveryBlocked) ||
		summary.Attempted != 3 || summary.Retryable != 1 ||
		summary.Blocked != 1 || summary.Inserted != 1 {
		t.Fatalf("continued drain = summary %#v, error %v", summary, errDrain)
	}
	if state.SupabaseOutbox.Entries[entries[0].EventID].Status != supabaseOutboxStatusPending ||
		state.SupabaseOutbox.Entries[entries[1].EventID].Status != supabaseOutboxStatusBlocked {
		t.Fatalf("unexpected retained entries: %#v", state.SupabaseOutbox.Entries)
	}
	if _, exists := state.SupabaseOutbox.Entries[entries[2].EventID]; exists {
		t.Fatal("later inserted entry was not acknowledged")
	}
}

func TestDrainSupabaseOutboxStopsOnConfigurationStatus(t *testing.T) {
	service := newStateOutboxTestService(t, true)
	state, entries := deliveryStateWithEntries(t, service, "first", "second")
	t.Setenv(service.cfg.Supabase.IngestTokenEnv, "test-token")
	requestCount := 0
	service.supabaseHTTPDoer = httpDoerFunc(func(*http.Request) (*http.Response, error) {
		requestCount++
		return &http.Response{StatusCode: http.StatusUnauthorized, Body: io.NopCloser(strings.NewReader("secret-response"))}, nil
	})

	summary, errDrain := service.drainSupabaseOutbox(context.Background(), &state)
	if !errors.Is(errDrain, errSupabaseDeliveryConfiguration) || requestCount != 1 || summary.Attempted != 1 {
		t.Fatalf("configuration stop = count %d, summary %#v, error %v", requestCount, summary, errDrain)
	}
	for _, entry := range entries {
		if state.SupabaseOutbox.Entries[entry.EventID].Status != supabaseOutboxStatusPending {
			t.Fatalf("configuration stop changed entry %s", entry.EventID)
		}
	}
}

func TestDrainSupabaseOutboxStopsBeforeAttemptWhenContextAlreadyCanceled(t *testing.T) {
	service := newStateOutboxTestService(t, true)
	state, entries := deliveryStateWithEntries(t, service, "alice", "panda")
	t.Setenv(service.cfg.Supabase.IngestTokenEnv, "test-token")
	requestCount := 0
	service.supabaseHTTPDoer = httpDoerFunc(func(request *http.Request) (*http.Response, error) {
		requestCount++
		return nil, request.Context().Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	summary, errDrain := service.drainSupabaseOutbox(ctx, &state)
	if !errors.Is(errDrain, errSupabaseDeliveryRetryable) || errDrain.Error() != errSupabaseDeliveryRetryable.Error() {
		t.Fatalf("pre-cancelled drain error = %v, want fixed generic retryable", errDrain)
	}
	if requestCount != 0 || summary != (supabaseDeliverySummary{}) {
		t.Fatalf("pre-cancelled drain attempted delivery: requests=%d summary=%#v", requestCount, summary)
	}
	if len(state.SupabaseOutbox.Entries) != len(entries) {
		t.Fatalf("pre-cancelled drain changed outbox: %#v", state.SupabaseOutbox.Entries)
	}
}

func TestDrainSupabaseOutboxStopsAfterDoCancelsContext(t *testing.T) {
	service := newStateOutboxTestService(t, true)
	state, entries := deliveryStateWithEntries(t, service, "alice", "panda")
	t.Setenv(service.cfg.Supabase.IngestTokenEnv, "test-token")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	requestCount := 0
	service.supabaseHTTPDoer = httpDoerFunc(func(request *http.Request) (*http.Response, error) {
		requestCount++
		cancel()
		return nil, request.Context().Err()
	})

	summary, errDrain := service.drainSupabaseOutbox(ctx, &state)
	if !errors.Is(errDrain, errSupabaseDeliveryRetryable) || errDrain.Error() != errSupabaseDeliveryRetryable.Error() {
		t.Fatalf("mid-drain cancellation error = %v, want fixed generic retryable", errDrain)
	}
	if requestCount != 1 || summary.Attempted != 1 || summary.Retryable != 1 {
		t.Fatalf("mid-drain cancellation did not stop promptly: requests=%d summary=%#v", requestCount, summary)
	}
	if len(state.SupabaseOutbox.Entries) != len(entries) {
		t.Fatalf("mid-drain cancellation changed outbox: %#v", state.SupabaseOutbox.Entries)
	}
}

func TestDrainSupabaseOutboxTreatsNetworkAndResponseFailuresAsRetryable(t *testing.T) {
	tests := []struct {
		name        string
		context     func() context.Context
		do          func(*http.Request) (*http.Response, error)
		assertClose func(t *testing.T)
	}{
		{name: "network", do: func(*http.Request) (*http.Response, error) { return nil, errors.New("network-secret") }},
		{name: "nil response", do: func(*http.Request) (*http.Response, error) { return nil, nil }},
		{name: "nil body", do: func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK}, nil
		}},
		{name: "read error", do: func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: &failingReadCloser{readErr: errors.New("read-secret")}}, nil
		}},
		{name: "close error", do: func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: &failingReadCloser{
				reader:   strings.NewReader(`{"status":"inserted","event_id":"unused"}`),
				closeErr: errors.New("close-secret"),
			}}, nil
		}},
	}
	responseWithErrorBody := &trackingReadCloser{reader: strings.NewReader("untrusted-response")}
	tests = append(tests, struct {
		name        string
		context     func() context.Context
		do          func(*http.Request) (*http.Response, error)
		assertClose func(t *testing.T)
	}{
		name: "response with network error",
		do: func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusBadGateway, Body: responseWithErrorBody}, errors.New("network-secret")
		},
		assertClose: func(t *testing.T) {
			if !responseWithErrorBody.closed || responseWithErrorBody.bytesRead != 0 {
				t.Fatalf("errored response was not safely closed: %#v", responseWithErrorBody)
			}
		},
	})

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := newStateOutboxTestService(t, true)
			state, entry := validStateOutboxEntry(t, service, "alice")
			state.SupabaseOutbox.Entries[entry.EventID] = entry
			t.Setenv(service.cfg.Supabase.IngestTokenEnv, "test-token")
			service.supabaseHTTPDoer = httpDoerFunc(test.do)
			ctx := context.Background()
			if test.context != nil {
				ctx = test.context()
			}

			summary, errDrain := service.drainSupabaseOutbox(ctx, &state)
			if !errors.Is(errDrain, errSupabaseDeliveryRetryable) || errDrain.Error() != errSupabaseDeliveryRetryable.Error() ||
				summary.Attempted != 1 || summary.Retryable != 1 {
				t.Fatalf("response failure = summary %#v, error %v", summary, errDrain)
			}
			if _, exists := state.SupabaseOutbox.Entries[entry.EventID]; !exists {
				t.Fatal("response failure removed pending entry")
			}
			if test.assertClose != nil {
				test.assertClose(t)
			}
		})
	}
}

func TestDrainSupabaseOutboxRollsBackTransitionWhenSaveFails(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
	}{
		{name: "acknowledgement", statusCode: http.StatusOK},
		{name: "blocked", statusCode: http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := newStateOutboxTestService(t, true)
			state, entries := deliveryStateWithEntries(t, service, "alice", "panda")
			if errSave := service.saveState(state); errSave != nil {
				t.Fatalf("save original pending state: %v", errSave)
			}
			restoreState := blockStatePublication(t, service)
			t.Setenv(service.cfg.Supabase.IngestTokenEnv, "test-token")
			requestCount := 0
			service.supabaseHTTPDoer = httpDoerFunc(func(request *http.Request) (*http.Response, error) {
				requestCount++
				responseBody := "untrusted-body"
				if test.statusCode == http.StatusOK {
					responseBody = fmt.Sprintf(`{"status":"inserted","event_id":%q}`, eventIDFromDeliveryRequest(t, request))
				}
				return &http.Response{
					StatusCode: test.statusCode,
					Body:       io.NopCloser(strings.NewReader(responseBody)),
				}, nil
			})

			summary, errDrain := service.drainSupabaseOutbox(context.Background(), &state)
			restoreState()
			if !errors.Is(errDrain, errSupabaseDeliveryState) || errDrain.Error() != errSupabaseDeliveryState.Error() {
				t.Fatalf("save failure error = %v, want fixed generic state error", errDrain)
			}
			if requestCount != 1 || summary.Attempted != 1 || summary.Inserted != 0 || summary.Duplicate != 0 || summary.Blocked != 0 {
				t.Fatalf("save failure counted an uncommitted transition: %#v", summary)
			}
			for _, entry := range entries {
				got, exists := state.SupabaseOutbox.Entries[entry.EventID]
				if !exists || got.Status != entry.Status || got.BlockCategory != "" || got.BlockStatus != 0 || !bytes.Equal(got.Payload, entry.Payload) {
					t.Fatalf("in-memory entry was not rolled back: %#v", got)
				}
			}
			loaded, errLoad := service.loadState()
			if errLoad != nil {
				t.Fatalf("reload old durable state: %v", errLoad)
			}
			for _, entry := range entries {
				durable, exists := loaded.SupabaseOutbox.Entries[entry.EventID]
				if !exists || durable.Status != supabaseOutboxStatusPending || !bytes.Equal(durable.Payload, entry.Payload) {
					t.Fatalf("old durable entry was not recoverable: %#v", durable)
				}
			}
		})
	}
}

func TestDrainSupabaseOutboxKeepsPublishedTransitionWhenParentSyncFails(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
	}{
		{name: "acknowledgement", statusCode: http.StatusOK},
		{name: "blocked", statusCode: http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := newStateOutboxTestService(t, true)
			state, entry := validStateOutboxEntry(t, service, "alice")
			state.SupabaseOutbox.Entries[entry.EventID] = entry
			if errSave := service.saveState(state); errSave != nil {
				t.Fatalf("save original pending state: %v", errSave)
			}
			syncCalls := 0
			service.syncStateParentDirectory = func(string) error {
				syncCalls++
				return errors.New("untrusted-parent-sync-path")
			}
			t.Setenv(service.cfg.Supabase.IngestTokenEnv, "test-token")
			requestCount := 0
			service.supabaseHTTPDoer = httpDoerFunc(func(request *http.Request) (*http.Response, error) {
				requestCount++
				responseBody := "untrusted-body"
				if test.statusCode == http.StatusOK {
					responseBody = fmt.Sprintf(`{"status":"inserted","event_id":%q}`, eventIDFromDeliveryRequest(t, request))
				}
				return &http.Response{StatusCode: test.statusCode, Body: io.NopCloser(strings.NewReader(responseBody))}, nil
			})

			summary, errDrain := service.drainSupabaseOutbox(context.Background(), &state)
			if !errors.Is(errDrain, errSupabaseDeliveryState) || errDrain.Error() != errSupabaseDeliveryState.Error() {
				t.Fatalf("post-rename sync error = %v, want fixed generic state error", errDrain)
			}
			if requestCount != 1 || syncCalls != 1 || summary.Attempted != 1 || summary.Inserted != 0 ||
				summary.Duplicate != 0 || summary.Blocked != 0 {
				t.Fatalf("published save failure summary = %#v, requests=%d syncs=%d", summary, requestCount, syncCalls)
			}

			loaded, errLoad := service.loadState()
			if errLoad != nil {
				t.Fatalf("reload published transition: %v", errLoad)
			}
			if test.statusCode == http.StatusOK {
				if _, inMemory := state.SupabaseOutbox.Entries[entry.EventID]; inMemory {
					t.Fatal("published acknowledgement was rolled back in memory")
				}
				if _, durable := loaded.SupabaseOutbox.Entries[entry.EventID]; durable {
					t.Fatal("published acknowledgement remained on disk")
				}
			} else {
				inMemory := state.SupabaseOutbox.Entries[entry.EventID]
				durable := loaded.SupabaseOutbox.Entries[entry.EventID]
				if inMemory.Status != supabaseOutboxStatusBlocked || inMemory.BlockCategory != "invalid-request" || inMemory.BlockStatus != http.StatusBadRequest {
					t.Fatalf("published blocked transition was rolled back in memory: %#v", inMemory)
				}
				if durable.Status != inMemory.Status || durable.BlockCategory != inMemory.BlockCategory || durable.BlockStatus != inMemory.BlockStatus {
					t.Fatalf("caller and durable blocked state differ: caller=%#v durable=%#v", inMemory, durable)
				}
			}

			requestCount = 0
			secondSummary, errSecond := service.drainSupabaseOutbox(context.Background(), &state)
			if test.statusCode == http.StatusOK {
				if errSecond != nil || requestCount != 0 || secondSummary != (supabaseDeliverySummary{}) {
					t.Fatalf("published acknowledgement was resent: requests=%d summary=%#v error=%v", requestCount, secondSummary, errSecond)
				}
			} else if !errors.Is(errSecond, errSupabaseDeliveryState) || requestCount != 1 ||
				secondSummary.Attempted != 1 || secondSummary.Blocked != 0 {
				t.Fatalf("published blocked transition retry = requests=%d summary=%#v error=%v", requestCount, secondSummary, errSecond)
			}
		})
	}
}

func TestDrainSupabaseOutboxRetriesByteIdenticalPayloadAfterLostResponse(t *testing.T) {
	service := newStateOutboxTestService(t, true)
	state, entry := validStateOutboxEntry(t, service, "张三<&>")
	state.SupabaseOutbox.Entries[entry.EventID] = entry
	if errSave := service.saveState(state); errSave != nil {
		t.Fatalf("save pending state: %v", errSave)
	}
	t.Setenv(service.cfg.Supabase.IngestTokenEnv, "test-token")
	var firstBody []byte
	service.supabaseHTTPDoer = httpDoerFunc(func(request *http.Request) (*http.Response, error) {
		var errRead error
		firstBody, errRead = io.ReadAll(request.Body)
		if errRead != nil {
			t.Fatalf("read accepted request body: %v", errRead)
		}
		return nil, errors.New("response-lost-after-accept")
	})

	firstSummary, errFirst := service.drainSupabaseOutbox(context.Background(), &state)
	if !errors.Is(errFirst, errSupabaseDeliveryRetryable) || firstSummary.Retryable != 1 {
		t.Fatalf("lost response = summary %#v, error %v", firstSummary, errFirst)
	}
	reloaded, errLoad := service.loadState()
	if errLoad != nil {
		t.Fatalf("reload pending event after lost response: %v", errLoad)
	}
	var secondBody []byte
	service.supabaseHTTPDoer = httpDoerFunc(func(request *http.Request) (*http.Response, error) {
		var errRead error
		secondBody, errRead = io.ReadAll(request.Body)
		if errRead != nil {
			t.Fatalf("read retried request body: %v", errRead)
		}
		body := fmt.Sprintf(`{"status":"duplicate","event_id":%q}`, entry.EventID)
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})

	secondSummary, errSecond := service.drainSupabaseOutbox(context.Background(), &reloaded)
	if errSecond != nil || secondSummary.Duplicate != 1 {
		t.Fatalf("duplicate retry = summary %#v, error %v", secondSummary, errSecond)
	}
	if !bytes.Equal(firstBody, entry.Payload) || !bytes.Equal(secondBody, entry.Payload) || !bytes.Equal(firstBody, secondBody) {
		t.Fatalf("retry changed payload bytes\nfirst:  %q\nsecond: %q\nwant:   %q", firstBody, secondBody, entry.Payload)
	}
}

type httpDoerFunc func(*http.Request) (*http.Response, error)

func (do httpDoerFunc) Do(request *http.Request) (*http.Response, error) {
	return do(request)
}

type trackingReadCloser struct {
	reader    io.Reader
	bytesRead int
	closed    bool
}

func (body *trackingReadCloser) Read(buffer []byte) (int, error) {
	read, errRead := body.reader.Read(buffer)
	body.bytesRead += read
	return read, errRead
}

func (body *trackingReadCloser) Close() error {
	body.closed = true
	return nil
}

type failingReadCloser struct {
	reader   io.Reader
	readErr  error
	closeErr error
}

type non200ResponseBody struct {
	readCalls  int
	closeCalls int
	closeErr   error
}

func (body *non200ResponseBody) Read(buffer []byte) (int, error) {
	body.readCalls++
	if body.readCalls == 1 {
		return copy(buffer, "truncated"), nil
	}
	return 0, errors.New("untrusted-read-error")
}

func (body *non200ResponseBody) Close() error {
	body.closeCalls++
	return body.closeErr
}

func (body *failingReadCloser) Read(buffer []byte) (int, error) {
	if body.reader != nil {
		return body.reader.Read(buffer)
	}
	return 0, body.readErr
}

func (body *failingReadCloser) Close() error {
	return body.closeErr
}

func deliveryStateWithEntries(t *testing.T, service *Service, keyNames ...string) (uploadState, []supabaseOutboxEntry) {
	t.Helper()
	var state uploadState
	entries := make([]supabaseOutboxEntry, 0, len(keyNames))
	for index, keyName := range keyNames {
		entryState, entry := validStateOutboxEntry(t, service, keyName)
		if index == 0 {
			state = entryState
		}
		state.SupabaseOutbox.Entries[entry.EventID] = entry
		entries = append(entries, entry)
	}
	return state, entries
}

func eventIDFromDeliveryRequest(t *testing.T, request *http.Request) string {
	t.Helper()
	body, errRead := io.ReadAll(request.Body)
	if errRead != nil {
		t.Fatalf("read delivery request: %v", errRead)
	}
	var payload struct {
		EventID string `json:"event_id"`
	}
	if errUnmarshal := json.Unmarshal(body, &payload); errUnmarshal != nil || payload.EventID == "" {
		t.Fatalf("decode delivery event ID: %v", errUnmarshal)
	}
	return payload.EventID
}

func newStateOutboxTestService(t *testing.T, enabled bool) *Service {
	t.Helper()
	location := mustLocation(t, "Asia/Shanghai")
	cfg := testConfig(filepath.Join(t.TempDir(), "keys"), filepath.Join(t.TempDir(), "uploader"))
	if errMkdir := os.MkdirAll(cfg.WorkDir, 0o750); errMkdir != nil {
		t.Fatalf("create uploader work directory: %v", errMkdir)
	}
	cfg.Supabase.Enabled = enabled
	cfg.Supabase.IngestURL = "https://Project-Ref.Supabase.co:443/functions/v1/log-stats-ingest"
	cfg.Supabase.IngestTokenEnv = "LOG_STATS_INGEST_TOKEN"
	if errValidate := cfg.Supabase.validate(); errValidate != nil {
		t.Fatalf("validate Supabase config: %v", errValidate)
	}
	return mustTestService(t, cfg, nil, time.Date(2026, time.July, 15, 3, 0, 0, 0, location))
}

func validStateOutboxEntry(t *testing.T, service *Service, keyName string) (uploadState, supabaseOutboxEntry) {
	t.Helper()
	prepared := validPreparedHourForSupabaseEvent(service)
	prepared.Sources = []preparedSource{{KeyName: keyName, Size: 350, JSONLBytes: exactInt64(280)}}
	prepared.Usage = []preparedUsage{{
		KeyName:     keyName,
		Provider:    providerCodex,
		SourceCount: 1,
		SourceBytes: 350,
		JSONLBytes:  280,
	}}
	mustBindPreparedUsageIntegrity(service, &prepared)
	payload, errPayload := service.buildSupabaseEventPayload(prepared)
	if errPayload != nil {
		t.Fatalf("build valid Supabase event payload: %v", errPayload)
	}
	var payloadBuffer bytes.Buffer
	encoder := json.NewEncoder(&payloadBuffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if errEncode := encoder.Encode(payload); errEncode != nil {
		t.Fatalf("encode exact Supabase event payload: %v", errEncode)
	}
	rawPayload := payloadBuffer.Bytes()
	payloadSHA256 := sha256.Sum256(rawPayload)

	state := service.newUploadState()
	hourKey := hourStateKey(prepared.Hour, prepared.Provider)
	state.Objects[prepared.ObjectKey] = uploadedObject{
		ObjectKey:      prepared.ObjectKey,
		CompressedSize: prepared.CompressedBytes,
		ArchiveSHA256:  prepared.ArchiveSHA256,
		Verification:   "put-success-or-remote-head-match",
		UploadedAt:     service.now(),
		VerifiedAt:     service.now(),
	}
	state.Hours[hourKey] = uploadedHour{
		Status:         "sealed",
		ObjectKey:      prepared.ObjectKey,
		ArchiveSHA256:  prepared.ArchiveSHA256,
		ManifestSHA256: prepared.ManifestSHA256,
		UploadedAt:     service.now(),
	}
	return state, supabaseOutboxEntry{
		EventID:       payload.EventID,
		HourKey:       hourKey,
		ObjectKey:     prepared.ObjectKey,
		Status:        supabaseOutboxStatusPending,
		Payload:       bytes.Clone(rawPayload),
		PayloadSHA256: hex.EncodeToString(payloadSHA256[:]),
		EnqueuedAt:    service.now(),
	}
}

func mutateOutboxPayload(t *testing.T, state *uploadState, entryID string, moveEntry bool, mutate func(payload *supabaseEventPayload)) {
	t.Helper()
	entry := state.SupabaseOutbox.Entries[entryID]
	var payload supabaseEventPayload
	if errUnmarshal := json.Unmarshal(entry.Payload, &payload); errUnmarshal != nil {
		t.Fatalf("decode outbox payload for mutation: %v", errUnmarshal)
	}
	mutate(&payload)
	rawPayload, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		t.Fatalf("encode mutated outbox payload: %v", errMarshal)
	}
	entry.Payload = rawPayload
	entry.PayloadSHA256 = sha256Hex(rawPayload)
	if moveEntry {
		delete(state.SupabaseOutbox.Entries, entryID)
		entry.EventID = payload.EventID
		state.SupabaseOutbox.Entries[entry.EventID] = entry
		return
	}
	state.SupabaseOutbox.Entries[entryID] = entry
}

func sha256Hex(payload []byte) string {
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func corruptBase64Payload(rawState []byte, payload []byte) []byte {
	encoded := base64.StdEncoding.EncodeToString(payload)
	return bytes.Replace(rawState, []byte(encoded), []byte("%%%not-base64%%%"), 1)
}

func validateSchemaV2Fixture(raw []byte) error {
	var header struct {
		SchemaVersion int `json:"schema_version"`
	}
	if errUnmarshal := json.Unmarshal(raw, &header); errUnmarshal != nil {
		return errUnmarshal
	}
	if header.SchemaVersion != 2 {
		return fmt.Errorf("unsupported upload state schema version %d", header.SchemaVersion)
	}
	return nil
}
