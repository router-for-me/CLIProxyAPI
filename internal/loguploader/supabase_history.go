package loguploader

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// SupabaseHistorySummary reports sanitized local history synchronization totals.
type SupabaseHistorySummary struct {
	Records              int   `json:"records"`
	Pending              int   `json:"pending"`
	LiveManaged          int   `json:"live_managed"`
	AlreadyCheckpointed  int   `json:"already_checkpointed"`
	Attempted            int   `json:"attempted"`
	Inserted             int   `json:"inserted"`
	Duplicate            int   `json:"duplicate"`
	Checkpointed         int   `json:"checkpointed"`
	SourceCount          int64 `json:"source_count"`
	SourceBytes          int64 `json:"source_bytes"`
	BatchJSONLBytes      int64 `json:"batch_jsonl_bytes"`
	BatchCompressedBytes int64 `json:"batch_compressed_bytes"`
	DuplicateRecords     int   `json:"duplicate_records"`
	TruncatedTails       int   `json:"truncated_tails"`
}

type supabaseHistoryUpload struct {
	checkpointKey string
	checkpoint    supabaseHistoryCheckpoint
	entry         supabaseOutboxEntry
}

// SyncSupabaseHistory sends successful local audit batches to the configured
// Supabase ingest endpoint. It never reads object storage or raw request logs.
func (s *Service) SyncSupabaseHistory(ctx context.Context, dryRun bool) (summary SupabaseHistorySummary, syncErr error) {
	if dryRun {
		return s.syncSupabaseHistory(ctx, true)
	}
	lock, errLock := s.acquireWorkDirLock()
	if errLock != nil {
		return summary, errLock
	}
	defer func() {
		syncErr = errors.Join(syncErr, lock.Close())
	}()
	return s.syncSupabaseHistory(ctx, dryRun)
}

func (s *Service) syncSupabaseHistory(ctx context.Context, dryRun bool) (SupabaseHistorySummary, error) {
	var summary SupabaseHistorySummary
	if !s.cfg.Supabase.Enabled {
		return summary, fmt.Errorf("Supabase history synchronization is disabled")
	}
	destinationID, errDestination := supabaseDestinationID(s.cfg.Supabase.IngestURL)
	if errDestination != nil {
		return summary, fmt.Errorf("Supabase history destination is invalid")
	}
	ledger, errLedger := readSupabaseHistoryLedger(s.cfg.WorkDir, s.location)
	if errLedger != nil {
		return summary, errLedger
	}
	state, errState := s.loadState()
	if errState != nil {
		return summary, fmt.Errorf("trusted upload state is invalid")
	}
	uploads, preflightSummary, errPreflight := s.preflightSupabaseHistory(state, ledger, destinationID)
	if errPreflight != nil {
		return summary, errPreflight
	}
	summary = preflightSummary
	if dryRun {
		return summary, nil
	}
	prunedHistory := false
	for checkpointKey, checkpoint := range state.SupabaseHistory {
		if checkpoint.DestinationID == destinationID {
			continue
		}
		delete(state.SupabaseHistory, checkpointKey)
		prunedHistory = true
	}
	if prunedHistory {
		if _, errSave := s.saveStateWithResult(state); errSave != nil {
			return summary, errSupabaseDeliveryState
		}
	}
	if len(uploads) == 0 {
		return summary, nil
	}

	for _, upload := range uploads {
		select {
		case <-ctx.Done():
			return summary, errSupabaseDeliveryRetryable
		default:
		}
		if _, checkpointed := state.SupabaseHistory[upload.checkpointKey]; checkpointed {
			continue
		}
		if existing, exists := state.SupabaseOutbox.Entries[upload.entry.EventID]; exists {
			if !sameSupabaseHistoryOutboxEntry(existing, upload.entry) {
				return summary, fmt.Errorf("pending Supabase history event does not match local audit state")
			}
		} else {
			state.SupabaseOutbox.Entries[upload.entry.EventID] = upload.entry
			published, errSave := s.saveStateWithResult(state)
			if errSave != nil {
				if !published {
					delete(state.SupabaseOutbox.Entries, upload.entry.EventID)
				}
				return summary, errSupabaseDeliveryState
			}
		}

		delivery, errDelivery := s.drainSupabaseOutboxWithPreferredEvent(ctx, &state, upload.entry.EventID)
		summary.Attempted += delivery.Attempted
		summary.Inserted += delivery.Inserted
		summary.Duplicate += delivery.Duplicate
		if errDelivery != nil {
			return summary, errDelivery
		}
		if delivery.Inserted+delivery.Duplicate != 1 {
			return summary, fmt.Errorf("Supabase history event was not acknowledged")
		}
		if _, stillPending := state.SupabaseOutbox.Entries[upload.entry.EventID]; stillPending {
			return summary, fmt.Errorf("acknowledged Supabase history event remains pending")
		}

		state.SupabaseHistory[upload.checkpointKey] = upload.checkpoint
		published, errCheckpoint := s.saveStateWithResult(state)
		if errCheckpoint != nil {
			delete(state.SupabaseHistory, upload.checkpointKey)
			if published {
				if errRollback := s.saveState(state); errRollback != nil {
					return summary, errors.Join(errSupabaseDeliveryState, errRollback)
				}
			}
			return summary, errSupabaseDeliveryState
		}
		summary.Checkpointed++
	}
	return summary, nil
}

func (s *Service) preflightSupabaseHistory(state uploadState, ledger supabaseHistoryLedger, destinationID string) ([]supabaseHistoryUpload, SupabaseHistorySummary, error) {
	summary := SupabaseHistorySummary{
		Records:              len(ledger.Records),
		SourceCount:          ledger.Summary.SourceCount,
		SourceBytes:          ledger.Summary.SourceBytes,
		BatchJSONLBytes:      ledger.Summary.JSONLBytes,
		BatchCompressedBytes: ledger.Summary.CompressedBytes,
		DuplicateRecords:     ledger.Summary.DuplicateRecords,
		TruncatedTails:       ledger.Summary.TruncatedTails,
	}
	hourByObject := make(map[string]string, len(state.Hours))
	for hourKey, hour := range state.Hours {
		hourByObject[hour.ObjectKey] = hourKey
	}

	activeEntries, existingPayloadBytes, errCapacity := supabaseOutboxActiveCapacity(state.SupabaseOutbox.Entries)
	if errCapacity != nil {
		return nil, summary, fmt.Errorf("Supabase history event cannot be queued: %w", errCapacity)
	}
	uploads := make([]supabaseHistoryUpload, 0, len(ledger.Records))
	checkpointKeys := make(map[string]struct{}, len(ledger.Records))
	for _, record := range ledger.Records {
		object, objectExists := state.Objects[record.ObjectKey]
		hourKey, hourExists := hourByObject[record.ObjectKey]
		if !objectExists || !hourExists {
			return nil, summary, fmt.Errorf("history ledger does not match trusted upload state")
		}
		hour := state.Hours[hourKey]
		stateProvider := record.Provider
		if stateProvider == "" {
			stateProvider = providerCodex
		}
		if hourStateKey(record.Hour.In(s.location), stateProvider) != hourKey ||
			hour.ObjectKey != record.ObjectKey || hour.ArchiveSHA256 != object.ArchiveSHA256 ||
			record.CompressedBytes != object.CompressedSize {
			return nil, summary, fmt.Errorf("history ledger does not match trusted upload state")
		}
		if hour.SupabaseEventID != "" {
			if record.SupabaseEventID != "" && record.SupabaseEventID != hour.SupabaseEventID {
				return nil, summary, fmt.Errorf("history ledger does not match trusted upload state")
			}
			summary.LiveManaged++
			continue
		}
		payload, errPayload := s.buildSupabaseHistoryPayload(record, hour, object)
		if errPayload != nil {
			return nil, summary, fmt.Errorf("history ledger cannot produce a valid Supabase event")
		}
		rawPayload, errMarshal := json.Marshal(payload)
		if errMarshal != nil {
			return nil, summary, fmt.Errorf("marshal Supabase history event")
		}
		payloadSHA256 := sha256.Sum256(rawPayload)
		entry := supabaseOutboxEntry{
			EventID:       payload.EventID,
			HourKey:       hourKey,
			ObjectKey:     record.ObjectKey,
			Status:        supabaseOutboxStatusPending,
			Payload:       bytes.Clone(rawPayload),
			PayloadSHA256: fmt.Sprintf("%x", payloadSHA256),
			EnqueuedAt:    s.now().In(s.location),
		}
		checkpointKey := supabaseHistoryCheckpointKey(destinationID, record.ObjectKey)
		checkpoint := supabaseHistoryCheckpoint{
			DestinationID: destinationID,
			ObjectKey:     record.ObjectKey,
			ArchiveSHA256: object.ArchiveSHA256,
			EventID:       payload.EventID,
			CommittedAt:   s.now().In(s.location),
		}
		if existing, checkpointed := state.SupabaseHistory[checkpointKey]; checkpointed {
			if existing != checkpoint {
				if existing.DestinationID != checkpoint.DestinationID || existing.ObjectKey != checkpoint.ObjectKey ||
					existing.ArchiveSHA256 != checkpoint.ArchiveSHA256 || existing.EventID != checkpoint.EventID || existing.CommittedAt.IsZero() {
					return nil, summary, fmt.Errorf("Supabase history checkpoint conflicts with trusted upload state")
				}
			}
			summary.AlreadyCheckpointed++
			continue
		}
		if _, duplicate := checkpointKeys[checkpointKey]; duplicate {
			return nil, summary, fmt.Errorf("history ledger contains a duplicate checkpoint identity")
		}
		checkpointKeys[checkpointKey] = struct{}{}
		if existing, exists := state.SupabaseOutbox.Entries[entry.EventID]; exists {
			if !sameSupabaseHistoryOutboxEntry(existing, entry) {
				return nil, summary, fmt.Errorf("pending Supabase history event conflicts with trusted upload state")
			}
		} else if errCapacity := validateSupabaseOutboxCapacity(activeEntries, existingPayloadBytes, int64(len(entry.Payload))); errCapacity != nil {
			return nil, summary, fmt.Errorf("Supabase history event cannot be queued: %w", errCapacity)
		}
		uploads = append(uploads, supabaseHistoryUpload{checkpointKey: checkpointKey, checkpoint: checkpoint, entry: entry})
		summary.Pending++
	}
	sort.Slice(uploads, func(i, j int) bool {
		if uploads[i].entry.HourKey != uploads[j].entry.HourKey {
			return uploads[i].entry.HourKey < uploads[j].entry.HourKey
		}
		return uploads[i].entry.EventID < uploads[j].entry.EventID
	})
	return uploads, summary, nil
}

func (s *Service) buildSupabaseHistoryPayload(record auditRecord, hour uploadedHour, object uploadedObject) (supabaseEventPayload, error) {
	usage, errUsage := buildSupabaseHistoryUsage(record)
	if errUsage != nil {
		return supabaseEventPayload{}, errUsage
	}
	hourStart := record.Hour.In(s.location).Format(time.RFC3339)
	payload := supabaseEventPayload{
		SchemaVersion:   supabaseEventSchemaVersion,
		TargetID:        "tos:" + s.target.ID,
		ObjectKey:       record.ObjectKey,
		ArchiveSHA256:   strings.ToLower(object.ArchiveSHA256),
		ManifestSHA256:  strings.ToLower(hour.ManifestSHA256),
		HourStart:       hourStart,
		Timezone:        s.policy.Timezone,
		UsageDate:       record.Hour.In(s.location).Format("2006-01-02"),
		SourceCount:     int64(record.SourceCount),
		SourceBytes:     record.SourceBytes,
		JSONLBytes:      record.JSONLBytes,
		CompressedBytes: record.CompressedBytes,
		TestMode:        false,
		UsagePrecision:  supabaseUsagePrecisionBatchOnly,
		Usage:           usage,
	}
	payload.EventID = supabaseHistoryEventID(payload)
	if errValidate := validateSupabaseEventPayload(payload); errValidate != nil {
		return supabaseEventPayload{}, errValidate
	}
	return payload, nil
}

func buildSupabaseHistoryUsage(record auditRecord) ([]supabaseEventUsage, error) {
	type usageKey struct {
		keyName  string
		provider string
	}

	usageByKey := make(map[usageKey]supabaseEventUsage)
	for keyName, key := range record.KeyNames {
		if record.Provider != "" {
			pair := usageKey{keyName: keyName, provider: record.Provider}
			usageByKey[pair] = supabaseEventUsage{
				KeyName:     keyName,
				Provider:    record.Provider,
				SourceCount: int64(key.SourceCount),
				SourceBytes: key.SourceBytes,
			}
			continue
		}
		for modelName, model := range key.Models {
			provider := classifyProvider(modelName)
			pair := usageKey{keyName: keyName, provider: provider}
			row := usageByKey[pair]
			row.KeyName = keyName
			row.Provider = provider
			var errAdd error
			row.SourceCount, errAdd = addSafeJSONInteger(row.SourceCount, int64(model.SourceCount))
			if errAdd != nil {
				return nil, fmt.Errorf("history usage source_count total overflow")
			}
			row.SourceBytes, errAdd = addSafeJSONInteger(row.SourceBytes, model.SourceBytes)
			if errAdd != nil {
				return nil, fmt.Errorf("history usage source_bytes total overflow")
			}
			usageByKey[pair] = row
		}
	}

	usage := make([]supabaseEventUsage, 0, len(usageByKey))
	for _, row := range usageByKey {
		usage = append(usage, row)
	}
	sort.Slice(usage, func(i, j int) bool {
		if usage[i].KeyName != usage[j].KeyName {
			return usage[i].KeyName < usage[j].KeyName
		}
		return usage[i].Provider < usage[j].Provider
	})
	return usage, nil
}

func supabaseHistoryCheckpointKey(destinationID, objectKey string) string {
	digest := sha256.Sum256([]byte("cliproxy-supabase-history-checkpoint-v1" + destinationID + objectKey))
	return fmt.Sprintf("%x", digest)
}

func sameSupabaseHistoryOutboxEntry(first, second supabaseOutboxEntry) bool {
	if first.EventID != second.EventID || first.HourKey != second.HourKey || first.ObjectKey != second.ObjectKey ||
		first.PayloadSHA256 != second.PayloadSHA256 || !bytes.Equal(first.Payload, second.Payload) {
		return false
	}
	switch first.Status {
	case supabaseOutboxStatusPending:
		return first.BlockCategory == "" && first.BlockStatus == 0
	case supabaseOutboxStatusBlocked:
		return validSupabaseBlockMetadata(first.BlockCategory, first.BlockStatus)
	default:
		return false
	}
}
