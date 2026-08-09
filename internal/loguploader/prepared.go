package loguploader

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	log "github.com/sirupsen/logrus"
)

// preparedCommitPreflightTimestamp uses the widest valid time.Time JSON form so
// the pre-upload state-size check cannot under-budget the real completion time.
func preparedCommitPreflightTimestamp() time.Time {
	const maximumRFC3339OffsetSeconds = 23*60*60 + 59*60
	return time.Date(
		9999, time.December, 31, 23, 59, 59, 999999999,
		time.FixedZone("preflight-maximum-rfc3339-offset", maximumRFC3339OffsetSeconds),
	)
}

func (s *Service) resumePreparedHours(ctx context.Context, state *uploadState) error {
	hourKeys := make([]string, 0, len(state.PreparedHours))
	for hourKey := range state.PreparedHours {
		hourKeys = append(hourKeys, hourKey)
	}
	sort.Strings(hourKeys)
	if len(hourKeys) > 0 {
		log.WithField("count", len(hourKeys)).Info("resuming prepared hours")
	}
	var resumeErrors []error
	for _, hourKey := range hourKeys {
		prepared, exists := state.PreparedHours[hourKey]
		if !exists {
			continue
		}
		log.WithFields(log.Fields{
			"hour":         prepared.Hour.Format(time.RFC3339),
			"object_key":   prepared.ObjectKey,
			"archive_size": prepared.CompressedBytes,
		}).Info("resuming prepared hour")
		if errComplete := s.completePreparedHour(ctx, hourKey, prepared, state); errComplete != nil {
			resumeErrors = append(resumeErrors, errComplete)
		}
	}
	return errors.Join(resumeErrors...)
}

func (s *Service) completePreparedHour(ctx context.Context, hourKey string, prepared preparedHour, state *uploadState) error {
	record := s.auditRecordForPrepared(prepared)
	if prepared.TargetID != s.target.ID {
		return s.recordBatchFailure(record, fmt.Errorf("prepared hour target does not match configured upload target"))
	}
	if gotManifest := manifestSHA256(prepared.Sources); gotManifest != prepared.ManifestSHA256 {
		return s.recordBatchFailure(record, fmt.Errorf("prepared hour manifest checksum mismatch: got %s, want %s", gotManifest, prepared.ManifestSHA256))
	}
	archiveRoot := filepath.Join(s.cfg.WorkDir, "archives")
	archivePath, errPath := safeExistingPath(archiveRoot, prepared.ArchivePath)
	if errPath != nil {
		return s.recordBatchFailure(record, errPath)
	}
	// Fast verification: check file size only. SHA256 was already computed
	// and stored in state.json during processBatch. Re-hashing a 10+ GB
	// archive on every resume adds tens of minutes of delay.
	info, errStat := os.Stat(archivePath)
	if errStat != nil {
		return s.recordBatchFailure(record, fmt.Errorf("stat prepared archive: %w", errStat))
	}
	if info.Size() != prepared.CompressedBytes {
		return s.recordBatchFailure(record, fmt.Errorf("prepared archive size mismatch: got %d, want %d", info.Size(), prepared.CompressedBytes))
	}
	log.WithFields(log.Fields{
		"hour":         prepared.Hour.Format(time.RFC3339),
		"archive_size": info.Size(),
	}).Info("prepared archive verified (size check)")
	if s.uploader == nil {
		return s.recordBatchFailure(record, fmt.Errorf("upload is enabled but no object uploader is configured"))
	}

	preflightAt := preparedCommitPreflightTimestamp()
	_, _, errPreflight := s.preflightPreparedCommit(*state, hourKey, prepared, archivePath, preflightAt)
	if errPreflight != nil {
		return s.recordBatchFailure(record, fmt.Errorf("preflight prepared upload commit: %w", errPreflight))
	}

	expectedObject := objectIdentity{Size: prepared.CompressedBytes, SHA256: prepared.ArchiveSHA256}
	uploadStart := s.now()
	errUpload := s.uploader.UploadFile(ctx, s.cfg.Upload.Bucket, prepared.ObjectKey, archivePath, expectedObject)
	if errors.Is(errUpload, ErrObjectConflict) {
		matcher, supportsMatch := s.uploader.(ObjectMatcher)
		if !supportsMatch {
			return s.recordBatchFailure(record, fmt.Errorf("upload %s: verify existing object: uploader does not support checksum matching: %w", prepared.ObjectKey, errUpload))
		}
		matches, errMatch := matcher.MatchObject(ctx, s.cfg.Upload.Bucket, prepared.ObjectKey, expectedObject)
		if errMatch != nil {
			return s.recordBatchFailure(record, fmt.Errorf("upload %s: verify existing object after conflict: %w", prepared.ObjectKey, errMatch))
		}
		if !matches {
			return s.recordBatchFailure(record, fmt.Errorf("upload %s: existing object checksum or size differs; prepared batch retained: %w", prepared.ObjectKey, errUpload))
		}
		log.WithField("object_key", prepared.ObjectKey).Warn("matching remote archive already exists; recovering prepared upload state")
		errUpload = nil
	}
	if errUpload != nil {
		return s.recordBatchFailure(record, fmt.Errorf("upload %s: %w", prepared.ObjectKey, errUpload))
	}
	committedAt := s.now().In(s.location)
	prospective, supabaseEntry, errCommitCandidate := s.preflightPreparedCommit(*state, hourKey, prepared, archivePath, committedAt)
	if errCommitCandidate != nil {
		return s.recordBatchFailure(record, fmt.Errorf("build prepared upload commit after TOS success: %w", errCommitCandidate))
	}
	log.WithFields(log.Fields{
		"hour":            prepared.Hour.Format(time.RFC3339),
		"object_key":      prepared.ObjectKey,
		"archive_size":    info.Size(),
		"upload_duration": committedAt.Sub(uploadStart).String(),
	}).Info("prepared hour uploaded")
	if supabaseEntry != nil {
		record.SupabaseEventID = supabaseEntry.EventID
	}

	needsCleanup := s.cfg.Retention.DeleteSourceAfterUpload || !s.cfg.Retention.KeepLocalArchives
	preCleanupRecord := record
	if needsCleanup {
		preCleanupRecord.Status = "uploaded_cleanup_pending"
	} else {
		preCleanupRecord.Status = "uploaded"
	}
	if errAudit := s.appendAudit(preCleanupRecord); errAudit != nil {
		return fmt.Errorf("record successful upload before committing prepared state: %w", errAudit)
	}

	original := *state
	*state = prospective
	published, errSave := s.saveStateWithResult(*state)
	if errSave != nil {
		if !published {
			*state = original
		}
		return fmt.Errorf("commit uploaded prepared hour: %w", errSave)
	}

	preferredEventID := ""
	if supabaseEntry != nil {
		preferredEventID = supabaseEntry.EventID
	}
	_, errDelivery := s.drainSupabaseOutboxWithPreferredEvent(ctx, state, preferredEventID)
	if errDelivery != nil {
		log.WithError(errDelivery).Warn("Supabase delivery remains pending after TOS upload")
	}

	if !needsCleanup {
		logPreparedUpload(record)
		return errDelivery
	}
	fingerprints := make([]string, 0, len(prepared.Sources))
	for _, source := range prepared.Sources {
		fingerprints = append(fingerprints, source.Fingerprint)
	}
	if errDelivery != nil {
		record.Error = errDelivery.Error()
	}
	if s.cfg.Retention.DeleteSourceAfterUpload {
		changed, deleteErrors := s.deleteUploadedSources(*state, fingerprints, &record.DeletedSources)
		if changed {
			if errSave := s.saveState(*state); errSave != nil {
				deleteErrors = append(deleteErrors, errSave)
			}
		}
		if len(deleteErrors) > 0 {
			record.Status = "uploaded_delete_pending"
			deleteError := errors.Join(deleteErrors...).Error()
			if record.Error == "" {
				record.Error = deleteError
			} else {
				record.Error += "; " + deleteError
			}
			for _, errDelete := range deleteErrors {
				log.WithError(errDelete).Error("failed to finish uploaded source cleanup")
			}
		}
	}
	if !s.cfg.Retention.KeepLocalArchives {
		changed, archiveErrors := s.deleteLocalArchives(*state, []string{prepared.ObjectKey})
		if changed {
			if errSave := s.saveState(*state); errSave != nil {
				archiveErrors = append(archiveErrors, errSave)
			}
		}
		if len(archiveErrors) > 0 {
			if record.Status == "uploaded_delete_pending" {
				record.Status = "uploaded_cleanup_pending"
			} else {
				record.Status = "uploaded_archive_delete_pending"
			}
			archiveError := errors.Join(archiveErrors...).Error()
			if record.Error == "" {
				record.Error = archiveError
			} else {
				record.Error += "; " + archiveError
			}
			for _, errDelete := range archiveErrors {
				log.WithError(errDelete).WithField("archive", archivePath).Warn("failed to remove uploaded local archive")
			}
		}
	}
	if record.Status == "" {
		record.Status = "uploaded"
	}
	if errAudit := s.appendAudit(record); errAudit != nil {
		return errors.Join(errDelivery, errAudit)
	}
	logPreparedUpload(record)
	return errDelivery
}

func (s *Service) preflightPreparedCommit(state uploadState, hourKey string, prepared preparedHour, archivePath string, committedAt time.Time) (uploadState, *supabaseOutboxEntry, error) {
	current, exists := state.PreparedHours[hourKey]
	if !exists || current.ObjectKey != prepared.ObjectKey || current.ArchiveSHA256 != prepared.ArchiveSHA256 || current.ManifestSHA256 != prepared.ManifestSHA256 {
		return uploadState{}, nil, fmt.Errorf("prepared hour changed before upload")
	}
	if _, existsHour := state.Hours[hourKey]; existsHour {
		return uploadState{}, nil, fmt.Errorf("prepared hour is already sealed")
	}
	if _, existsObject := state.Objects[prepared.ObjectKey]; existsObject {
		return uploadState{}, nil, fmt.Errorf("prepared object is already committed")
	}
	for _, source := range prepared.Sources {
		if _, existsSource := state.Uploaded[source.Fingerprint]; existsSource {
			return uploadState{}, nil, fmt.Errorf("prepared source is already committed")
		}
	}

	var entry *supabaseOutboxEntry
	if s.cfg.Supabase.Enabled {
		event, errEvent := s.prepareSupabaseEvent(prepared)
		if errEvent != nil {
			return uploadState{}, nil, errEvent
		}
		if _, duplicateEvent := state.SupabaseOutbox.Entries[event.EventID()]; duplicateEvent {
			return uploadState{}, nil, fmt.Errorf("duplicate Supabase event")
		}
		activeEntries, activePayloadBytes, errCapacity := supabaseOutboxActiveCapacity(state.SupabaseOutbox.Entries)
		if errCapacity != nil {
			return uploadState{}, nil, errCapacity
		}
		if errCapacity = validateSupabaseOutboxCapacity(activeEntries, activePayloadBytes, int64(len(event.rawJSON))); errCapacity != nil {
			return uploadState{}, nil, errCapacity
		}
		payloadSHA256 := sha256.Sum256(event.rawJSON)
		entry = &supabaseOutboxEntry{
			EventID:       event.EventID(),
			HourKey:       hourKey,
			ObjectKey:     prepared.ObjectKey,
			Status:        supabaseOutboxStatusPending,
			Payload:       bytes.Clone(event.rawJSON),
			PayloadSHA256: fmt.Sprintf("%x", payloadSHA256),
			EnqueuedAt:    committedAt,
		}
	}

	candidate, errClone := cloneUploadStateForPreflight(state)
	if errClone != nil {
		return uploadState{}, nil, errClone
	}
	for _, source := range prepared.Sources {
		candidate.Uploaded[source.Fingerprint] = uploadedSource{
			ObjectKey:    prepared.ObjectKey,
			HourKey:      hourKey,
			TargetID:     s.target.ID,
			UploadedAt:   committedAt,
			RelativePath: source.RelativePath,
			Size:         source.Size,
			ModTime:      source.ModTime,
			SHA256:       source.SHA256,
		}
	}
	candidate.Objects[prepared.ObjectKey] = uploadedObject{
		ObjectKey:      prepared.ObjectKey,
		CompressedSize: prepared.CompressedBytes,
		ArchiveSHA256:  prepared.ArchiveSHA256,
		Verification:   "put-success-or-remote-head-match",
		UploadedAt:     committedAt,
		VerifiedAt:     committedAt,
		ArchivePath:    archivePath,
	}
	committedHour := uploadedHour{
		Status:         "sealed",
		ObjectKey:      prepared.ObjectKey,
		ArchiveSHA256:  prepared.ArchiveSHA256,
		ManifestSHA256: prepared.ManifestSHA256,
		UploadedAt:     committedAt,
	}
	if entry != nil {
		committedHour.SupabaseEventID = entry.EventID
	}
	candidate.Hours[hourKey] = committedHour
	delete(candidate.PreparedHours, hourKey)
	if entry != nil {
		candidate.SupabaseOutbox.Entries[entry.EventID] = *entry
	}
	if errValidate := s.validateUploadState(&candidate); errValidate != nil {
		return uploadState{}, nil, fmt.Errorf("validate atomic upload state: %w", errValidate)
	}
	rawCandidate, errMarshal := json.MarshalIndent(candidate, "", "  ")
	if errMarshal != nil {
		return uploadState{}, nil, fmt.Errorf("marshal prospective upload state: %w", errMarshal)
	}
	if int64(len(rawCandidate)) > maxUploadStateBytes {
		return uploadState{}, nil, fmt.Errorf("prospective upload state exceeds the 128 MiB limit")
	}
	return candidate, entry, nil
}

func cloneUploadStateForPreflight(state uploadState) (uploadState, error) {
	raw, errMarshal := json.MarshalIndent(state, "", "  ")
	if errMarshal != nil {
		return uploadState{}, fmt.Errorf("marshal upload state for preflight: %w", errMarshal)
	}
	if int64(len(raw)) > maxUploadStateBytes {
		return uploadState{}, fmt.Errorf("upload state exceeds the 128 MiB limit")
	}
	var cloned uploadState
	if errUnmarshal := json.Unmarshal(raw, &cloned); errUnmarshal != nil {
		return uploadState{}, fmt.Errorf("clone upload state for preflight: %w", errUnmarshal)
	}
	return cloned, nil
}

func (s *Service) auditRecordForPrepared(prepared preparedHour) auditRecord {
	record := auditRecord{
		Timestamp:       s.now().In(s.location),
		Provider:        prepared.Provider,
		Hour:            prepared.Hour,
		SourceCount:     len(prepared.Sources),
		KeyNames:        make(map[string]auditKeyNameSummary),
		JSONLBytes:      prepared.JSONLBytes,
		CompressedBytes: prepared.CompressedBytes,
		ObjectKey:       prepared.ObjectKey,
		ArchivePath:     prepared.ArchivePath,
	}
	for _, source := range prepared.Sources {
		record.SourceBytes += source.Size
		keySummary := record.KeyNames[source.KeyName]
		keySummary.SourceCount++
		keySummary.SourceBytes += source.Size
		if keySummary.Models == nil {
			keySummary.Models = make(map[string]auditModelSummary)
		}
		modelSummary := keySummary.Models[source.Model]
		modelSummary.SourceCount++
		modelSummary.SourceBytes += source.Size
		keySummary.Models[source.Model] = modelSummary
		record.KeyNames[source.KeyName] = keySummary
	}
	return record
}

func logPreparedUpload(record auditRecord) {
	log.WithFields(log.Fields{
		"hour":       record.Hour.Format(time.RFC3339),
		"key_names":  len(record.KeyNames),
		"records":    record.SourceCount,
		"object_key": record.ObjectKey,
	}).Info("log archive uploaded")
}
