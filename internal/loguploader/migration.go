package loguploader

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type legacyManifestEntry struct {
	Hour            time.Time `json:"hour"`
	ObjectKey       string    `json:"object_key"`
	SHA256          string    `json:"sha256"`
	SourceCount     int       `json:"source_count"`
	JSONLBytes      int64     `json:"jsonl_bytes"`
	CompressedBytes int64     `json:"compressed_bytes"`
}

// MigrateLegacyState verifies a backed-up archive manifest against local files,
// TOS metadata, and the audit log before replacing an untrusted legacy state.
func (s *Service) MigrateLegacyState(ctx context.Context, manifestPath, archivesRoot string, trustVerifiedLocal bool) (runErr error) {
	lock, errLock := s.acquireProcessLock()
	if errLock != nil {
		return errLock
	}
	defer func() {
		runErr = errors.Join(runErr, lock.Close())
	}()
	if s.uploader == nil {
		return fmt.Errorf("legacy state migration requires an enabled object uploader")
	}
	matcher, supportsMatch := s.uploader.(ObjectMatcher)
	if !supportsMatch && !trustVerifiedLocal {
		return fmt.Errorf("legacy state migration requires remote checksum matching")
	}
	rawLegacy, errRead := os.ReadFile(s.statePath())
	if errRead != nil {
		return fmt.Errorf("read legacy upload state: %w", errRead)
	}
	legacy := uploadState{}
	if errUnmarshal := json.Unmarshal(rawLegacy, &legacy); errUnmarshal != nil {
		return fmt.Errorf("parse legacy upload state: %w", errUnmarshal)
	}
	if legacy.SchemaVersion != 0 {
		return fmt.Errorf("state schema version is %d, not legacy", legacy.SchemaVersion)
	}
	if len(legacy.Uploaded) != 0 {
		return fmt.Errorf("legacy state has %d pending uploaded source entries; refusing automatic migration", len(legacy.Uploaded))
	}
	for objectKey, object := range legacy.Objects {
		if object.ArchivePath != "" {
			return fmt.Errorf("legacy object %s still has a pending local archive", objectKey)
		}
	}

	manifest, errManifest := readLegacyManifest(manifestPath)
	if errManifest != nil {
		return errManifest
	}
	if len(manifest) != len(legacy.Objects) {
		return fmt.Errorf("manifest objects=%d do not match legacy state objects=%d", len(manifest), len(legacy.Objects))
	}
	audit, errAudit := readMigrationAudit(filepath.Join(s.cfg.WorkDir, "audit.jsonl"))
	if errAudit != nil {
		return errAudit
	}

	nextState := s.newUploadState()
	seenHours := make(map[string]struct{}, len(manifest))
	for _, entry := range manifest {
		if entry.Hour.IsZero() || entry.ObjectKey == "" || entry.SHA256 == "" || entry.CompressedBytes <= 0 {
			return fmt.Errorf("manifest entry for %q is incomplete", entry.ObjectKey)
		}
		if _, exists := legacy.Objects[entry.ObjectKey]; !exists {
			return fmt.Errorf("manifest object is absent from legacy state: %s", entry.ObjectKey)
		}
		if s.target.ObjectPrefix != "" && !strings.HasPrefix(entry.ObjectKey, s.target.ObjectPrefix+"/") {
			return fmt.Errorf("manifest object is outside configured prefix: %s", entry.ObjectKey)
		}
		hour := entry.Hour.In(s.location).Truncate(time.Hour)
		hourKey := hourStateKey(hour, providerCodex)
		if _, duplicate := seenHours[hourKey]; duplicate {
			return fmt.Errorf("manifest contains more than one object for hour %s", hourKey)
		}
		seenHours[hourKey] = struct{}{}
		hourPrefix := hour.Format("2006-01-02-15")
		if !strings.HasPrefix(filepath.Base(entry.ObjectKey), hourPrefix+"-"+legacyArchiveNameLabel+"-") || strings.Contains(filepath.Base(entry.ObjectKey), "-part") {
			return fmt.Errorf("manifest object does not use the strict hourly name: %s", entry.ObjectKey)
		}
		auditRecord, existsAudit := audit[entry.ObjectKey]
		if !existsAudit || auditRecord.SourceCount != entry.SourceCount || auditRecord.JSONLBytes != entry.JSONLBytes || auditRecord.CompressedBytes != entry.CompressedBytes {
			return fmt.Errorf("manifest object does not match its audit record: %s", entry.ObjectKey)
		}
		archivePath := filepath.Join(archivesRoot, hour.Format("2006"), hour.Format("01"), hour.Format("02"), filepath.Base(entry.ObjectKey))
		archivePath, errSafe := safeExistingPath(archivesRoot, archivePath)
		if errSafe != nil {
			return errSafe
		}
		localSHA256, localSize, errChecksum := fileSHA256(archivePath)
		if errChecksum != nil {
			return fmt.Errorf("verify migration archive %s: %w", archivePath, errChecksum)
		}
		if localSHA256 != entry.SHA256 || localSize != entry.CompressedBytes {
			return fmt.Errorf("migration archive mismatch for %s", entry.ObjectKey)
		}
		verification := "verified-local-archive-and-upload-audit"
		if !trustVerifiedLocal {
			expectedObject := objectIdentity{Size: entry.CompressedBytes, SHA256: entry.SHA256}
			matches, errMatch := matcher.MatchObject(ctx, s.cfg.Upload.Bucket, entry.ObjectKey, expectedObject)
			if errMatch != nil {
				return fmt.Errorf("verify remote migration object %s: %w", entry.ObjectKey, errMatch)
			}
			if !matches {
				return fmt.Errorf("remote migration object differs from verified local archive: %s", entry.ObjectKey)
			}
			verification = "remote-head-match"
		}
		legacyObject := legacy.Objects[entry.ObjectKey]
		verifiedAt := s.now().In(s.location)
		nextState.Objects[entry.ObjectKey] = uploadedObject{
			ObjectKey:      entry.ObjectKey,
			CompressedSize: entry.CompressedBytes,
			ArchiveSHA256:  entry.SHA256,
			Verification:   verification,
			UploadedAt:     legacyObject.UploadedAt,
			VerifiedAt:     verifiedAt,
		}
		nextState.Hours[hourKey] = uploadedHour{
			Status:         "sealed",
			ObjectKey:      entry.ObjectKey,
			ArchiveSHA256:  entry.SHA256,
			ManifestSHA256: "legacy-verified:" + entry.SHA256,
			UploadedAt:     legacyObject.UploadedAt,
		}
	}

	backupPath := s.statePath() + ".legacy-" + s.now().In(s.location).Format("20060102T150405")
	if errBackup := writeDurableFile(backupPath, rawLegacy, 0o600); errBackup != nil {
		return fmt.Errorf("back up legacy state: %w", errBackup)
	}
	if errSave := s.saveState(nextState); errSave != nil {
		return fmt.Errorf("publish migrated state: %w", errSave)
	}
	return nil
}

func readLegacyManifest(path string) ([]legacyManifestEntry, error) {
	file, errOpen := os.Open(path)
	if errOpen != nil {
		return nil, fmt.Errorf("open legacy migration manifest: %w", errOpen)
	}
	defer func() { _ = file.Close() }()
	var entries []legacyManifestEntry
	decoder := json.NewDecoder(bufio.NewReader(file))
	for {
		var entry legacyManifestEntry
		if errDecode := decoder.Decode(&entry); errors.Is(errDecode, io.EOF) {
			break
		} else if errDecode != nil {
			return nil, fmt.Errorf("decode legacy migration manifest: %w", errDecode)
		}
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Hour.Before(entries[j].Hour) })
	return entries, nil
}

func readMigrationAudit(path string) (map[string]auditRecord, error) {
	file, errOpen := os.Open(path)
	if errOpen != nil {
		return nil, fmt.Errorf("open migration audit: %w", errOpen)
	}
	defer func() { _ = file.Close() }()
	records := make(map[string]auditRecord)
	decoder := json.NewDecoder(bufio.NewReader(file))
	for {
		var record auditRecord
		if errDecode := decoder.Decode(&record); errors.Is(errDecode, io.EOF) {
			break
		} else if errDecode != nil {
			return nil, fmt.Errorf("decode migration audit: %w", errDecode)
		}
		if record.Status == "uploaded" && record.ObjectKey != "" {
			if _, duplicate := records[record.ObjectKey]; duplicate {
				return nil, fmt.Errorf("migration audit contains duplicate uploaded object %s", record.ObjectKey)
			}
			records[record.ObjectKey] = record
		}
	}
	return records, nil
}

func writeDurableFile(path string, data []byte, mode os.FileMode) error {
	file, errOpen := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, mode)
	if errOpen != nil {
		return errOpen
	}
	_, errWrite := file.Write(data)
	errSync := file.Sync()
	errClose := file.Close()
	if errCombined := errors.Join(errWrite, errSync, errClose); errCombined != nil {
		_ = os.Remove(path)
		return errCombined
	}
	return syncParentDirectory(path)
}
