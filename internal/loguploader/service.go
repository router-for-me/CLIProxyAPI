package loguploader

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
	log "github.com/sirupsen/logrus"
)

type ObjectUploader interface {
	UploadFile(ctx context.Context, bucket, objectKey, path string) error
}

type ObjectMatcher interface {
	MatchObject(ctx context.Context, bucket, objectKey, path string) (bool, error)
}

type Service struct {
	cfg      Config
	uploader ObjectUploader
	location *time.Location
	target   uploadTarget
	policy   uploadPolicy
	now      func() time.Time
}

type uploadState struct {
	SchemaVersion int                       `json:"schema_version"`
	Target        uploadTarget              `json:"target"`
	Policy        uploadPolicy              `json:"policy"`
	Uploaded      map[string]uploadedSource `json:"uploaded"`
	Objects       map[string]uploadedObject `json:"objects"`
	Hours         map[string]uploadedHour   `json:"hours"`
	PreparedHours map[string]preparedHour   `json:"prepared_hours"`
	dirty         bool                      `json:"-"`
}

type uploadTarget struct {
	Provider     string `json:"provider"`
	Endpoint     string `json:"endpoint"`
	Region       string `json:"region"`
	Bucket       string `json:"bucket"`
	ObjectPrefix string `json:"object_prefix"`
	ID           string `json:"id"`
}

type uploadPolicy struct {
	Timezone string `json:"timezone"`
	Grouping string `json:"grouping"`
	Naming   string `json:"naming"`
}

type uploadedSource struct {
	ObjectKey       string    `json:"object_key"`
	HourKey         string    `json:"hour_key"`
	TargetID        string    `json:"target_id"`
	UploadedAt      time.Time `json:"uploaded_at"`
	RelativePath    string    `json:"relative_path"`
	Size            int64     `json:"size_bytes"`
	ModTime         time.Time `json:"mod_time"`
	SHA256          string    `json:"source_sha256"`
	PendingDeleteAt string    `json:"pending_delete_path,omitempty"`
}

type uploadedObject struct {
	ObjectKey      string    `json:"object_key"`
	CompressedSize int64     `json:"compressed_size"`
	ArchiveSHA256  string    `json:"archive_sha256"`
	Verification   string    `json:"verification"`
	UploadedAt     time.Time `json:"uploaded_at"`
	VerifiedAt     time.Time `json:"verified_at"`
	ArchivePath    string    `json:"archive_path,omitempty"`
}

type uploadedHour struct {
	Status         string    `json:"status"`
	ObjectKey      string    `json:"object_key"`
	ArchiveSHA256  string    `json:"archive_sha256"`
	ManifestSHA256 string    `json:"manifest_sha256"`
	UploadedAt     time.Time `json:"uploaded_at"`
}

type preparedHour struct {
	TargetID        string           `json:"target_id"`
	Hour            time.Time        `json:"hour"`
	Provider        string           `json:"provider"`
	ObjectKey       string           `json:"object_key"`
	ArchivePath     string           `json:"archive_path"`
	JSONLBytes      int64            `json:"jsonl_bytes"`
	CompressedBytes int64            `json:"compressed_bytes"`
	ArchiveSHA256   string           `json:"archive_sha256"`
	ManifestSHA256  string           `json:"manifest_sha256"`
	Sources         []preparedSource `json:"sources"`
	PreparedAt      time.Time        `json:"prepared_at"`
}

type preparedSource struct {
	Fingerprint  string    `json:"fingerprint"`
	RelativePath string    `json:"relative_path"`
	KeyName      string    `json:"key_name"`
	Model        string    `json:"model"`
	Size         int64     `json:"size_bytes"`
	ModTime      time.Time `json:"mod_time"`
	SHA256       string    `json:"source_sha256"`
}

type auditKeyNameSummary struct {
	SourceCount int                          `json:"source_count"`
	SourceBytes int64                        `json:"source_bytes"`
	Models      map[string]auditModelSummary `json:"models,omitempty"`
}

type auditModelSummary struct {
	SourceCount int   `json:"source_count"`
	SourceBytes int64 `json:"source_bytes"`
}

type auditRecord struct {
	Timestamp       time.Time                      `json:"timestamp"`
	Status          string                         `json:"status"`
	Provider        string                         `json:"provider,omitempty"`
	Hour            time.Time                      `json:"hour"`
	SourceCount     int                            `json:"source_count"`
	SourceBytes     int64                          `json:"source_bytes"`
	KeyNames        map[string]auditKeyNameSummary `json:"key_names"`
	JSONLBytes      int64                          `json:"jsonl_bytes"`
	CompressedBytes int64                          `json:"compressed_bytes"`
	ObjectKey       string                         `json:"object_key,omitempty"`
	ArchivePath     string                         `json:"archive_path"`
	DeletedSources  int                            `json:"deleted_sources"`
	Error           string                         `json:"error,omitempty"`
}

func NewService(cfg Config, uploader ObjectUploader) (*Service, error) {
	location, errLocation := time.LoadLocation(cfg.Timezone)
	if errLocation != nil {
		return nil, fmt.Errorf("load uploader timezone: %w", errLocation)
	}
	target, errTarget := canonicalUploadTarget(cfg.Upload)
	if errTarget != nil {
		return nil, errTarget
	}
	policy := uploadPolicy{Timezone: cfg.Timezone, Grouping: "completion-modtime-hour-v1", Naming: archiveNamingPolicy}
	return &Service{cfg: cfg, uploader: uploader, location: location, target: target, policy: policy, now: time.Now}, nil
}

// Run starts the hourly scheduler and blocks until ctx is cancelled.
func (s *Service) Run(ctx context.Context, dryRun bool) (runErr error) {
	if !s.cfg.Upload.Enabled && !dryRun {
		return fmt.Errorf("upload is disabled; use dry-run for local conversion testing")
	}
	lock, errLock := s.acquireProcessLock()
	if errLock != nil {
		return errLock
	}
	defer func() {
		runErr = errors.Join(runErr, lock.Close())
	}()
	return s.run(ctx, dryRun)
}

func (s *Service) run(ctx context.Context, dryRun bool) error {
	if s.cfg.Schedule.RunOnStart {
		s.runCatchUp(ctx, dryRun, "initial")
	}
	for {
		delay := s.nextDelay()
		nextRun := s.now().Add(delay)
		log.WithFields(log.Fields{
			"delay":    delay.String(),
			"next_run": nextRun.Format(time.RFC3339),
		}).Info("scheduler waiting for next run")
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return nil
		case <-timer.C:
			s.runCatchUp(ctx, dryRun, "scheduled")
		}
	}
}

// runCatchUp keeps calling runOnce until there is no settled backlog and no
// prepared hours, so hour N+1 starts immediately after hour N without waiting
// for the next clock boundary. On error it stops the burst and lets the outer
// scheduler retry after catch-up delay (when backlog remains).
func (s *Service) runCatchUp(ctx context.Context, dryRun bool, reason string) {
	for {
		if ctx.Err() != nil {
			return
		}
		errRun := s.runOnce(ctx, dryRun)
		if errRun != nil {
			if reason == "initial" {
				log.WithError(errRun).Error("log uploader initial run failed")
			} else {
				log.WithError(errRun).Error("scheduled log upload failed")
			}
			return
		}
		hasWork, errHas := s.hasCatchUpWork()
		if errHas != nil {
			log.WithError(errHas).Warn("catch-up work check failed; waiting for next schedule")
			return
		}
		if !hasWork {
			return
		}
		log.WithField("reason", reason).Info("settled backlog remains; continuing catch-up immediately")
	}
}

// hasCatchUpWork reports whether another runOnce should start without waiting
// for the next hourly boundary: prepared uploads still open, or settled source
// .log files still waiting to be archived.
func (s *Service) hasCatchUpWork() (bool, error) {
	state, errLoad := s.loadState()
	if errLoad != nil {
		return false, errLoad
	}
	if len(state.PreparedHours) > 0 {
		return true, nil
	}
	sources, errScan := s.scanSources(state)
	if errScan != nil {
		return false, errScan
	}
	return len(sources) > 0, nil
}

// nextDelay returns the wait duration until the next run.
// When prepared hours or settled source backlog remain, it uses the shorter
// catch-up delay instead of waiting for the next hour boundary.
func (s *Service) nextDelay() time.Duration {
	now := s.now().In(s.location)
	hasWork, errHas := s.hasCatchUpWork()
	if errHas != nil {
		log.WithError(errHas).Warn("failed to inspect backlog for next delay; using hourly schedule")
		return durationUntil(now, nextScheduledRun(now, s.cfg.Schedule.Interval, s.cfg.Schedule.SettleDelay))
	}
	if hasWork {
		log.Info("pending backlog detected; using catch-up delay")
		return s.cfg.Schedule.CatchUpDelay
	}
	return durationUntil(now, nextScheduledRun(now, s.cfg.Schedule.Interval, s.cfg.Schedule.SettleDelay))
}

// durationUntil returns target-now, or 0 if target is not after now.
func durationUntil(now, target time.Time) time.Duration {
	d := target.Sub(now)
	if d < 0 {
		return 0
	}
	return d
}

func nextScheduledRun(now time.Time, interval, settleDelay time.Duration) time.Time {
	boundary := now.Truncate(interval)
	candidate := boundary.Add(settleDelay)
	if !candidate.After(now) {
		candidate = boundary.Add(interval).Add(settleDelay)
	}
	return candidate
}

// RunOnce processes all settled request logs that have not already been uploaded.
func (s *Service) RunOnce(ctx context.Context, dryRun bool) (runErr error) {
	lock, errLock := s.acquireProcessLock()
	if errLock != nil {
		return errLock
	}
	defer func() {
		runErr = errors.Join(runErr, lock.Close())
	}()
	return s.runOnce(ctx, dryRun)
}

func (s *Service) runOnce(ctx context.Context, dryRun bool) error {
	runStart := s.now()
	log.WithField("dry_run", dryRun).Info("log uploader run started")
	if !s.cfg.Upload.Enabled && !dryRun {
		return fmt.Errorf("upload is disabled; use dry-run for local conversion testing")
	}
	if errMkdir := os.MkdirAll(s.cfg.WorkDir, 0o750); errMkdir != nil {
		return fmt.Errorf("create uploader work directory: %w", errMkdir)
	}
	state := s.newUploadState()
	if !dryRun {
		var errState error
		state, errState = s.loadState()
		if errState != nil {
			return errState
		}
		if state.dirty {
			if errSave := s.saveState(state); errSave != nil {
				return errSave
			}
		}
	}
	log.WithFields(log.Fields{
		"prepared_hours": len(state.PreparedHours),
		"sealed_hours":   len(state.Hours),
	}).Info("state loaded")
	var runErrors []error
	if !dryRun {
		if errResume := s.resumePreparedHours(ctx, state); errResume != nil {
			log.WithError(errResume).Error("resume prepared hours failed")
			runErrors = append(runErrors, errResume)
		}
	}
	if !dryRun && s.cfg.Retention.DeleteSourceAfterUpload {
		changed, deleteErrors := s.retryUploadedDeletes(state)
		if changed {
			if errSave := s.saveState(state); errSave != nil {
				return errSave
			}
		}
		for _, errDelete := range deleteErrors {
			log.WithError(errDelete).Warn("uploaded source log is still pending deletion")
		}
	}
	if !dryRun && !s.cfg.Retention.KeepLocalArchives {
		changed, archiveErrors := s.retryLocalArchiveDeletes(state)
		if changed {
			if errSave := s.saveState(state); errSave != nil {
				return errSave
			}
		}
		for _, errDelete := range archiveErrors {
			log.WithError(errDelete).Warn("uploaded local archive is still pending deletion")
		}
	}
	scanStart := s.now()
	sources, errScan := s.scanSources(state)
	if errScan != nil {
		return errScan
	}
	if len(sources) == 0 {
		log.WithField("scan_duration", s.now().Sub(scanStart).String()).Debug("no settled request logs are ready for upload")
		log.WithField("total_duration", s.now().Sub(runStart).String()).Info("log uploader run completed (no work)")
		return errors.Join(runErrors...)
	}

	groups := groupSources(sources)
	groupKeys := make([]providerGroupKey, 0, len(groups))
	for key := range groups {
		groupKeys = append(groupKeys, key)
	}
	sort.Slice(groupKeys, func(i, j int) bool {
		if !groupKeys[i].Hour.Equal(groupKeys[j].Hour) {
			return groupKeys[i].Hour.Before(groupKeys[j].Hour)
		}
		return groupKeys[i].Provider < groupKeys[j].Provider
	})

	log.WithFields(log.Fields{
		"source_files":  len(sources),
		"pending_hours": len(groupKeys),
		"first_hour":    groupKeys[0].Hour.Format(time.RFC3339),
		"last_hour":     groupKeys[len(groupKeys)-1].Hour.Format(time.RFC3339),
	}).Info("processing pending hours")

	for _, key := range groupKeys {
		if errProcess := s.processBatch(ctx, key.Hour, key.Provider, groups[key], state, dryRun); errProcess != nil {
			runErrors = append(runErrors, errProcess)
		}
	}
	log.WithFields(log.Fields{
		"total_duration": s.now().Sub(runStart).String(),
		"errors":         len(runErrors),
	}).Info("log uploader run completed")
	return errors.Join(runErrors...)
}

func (s *Service) scanSources(state uploadState) ([]sourceLog, error) {
	now := s.now().In(s.location)
	cutoff := now.Add(-s.cfg.Schedule.SettleDelay)
	var sources []sourceLog
	errWalk := filepath.Walk(s.cfg.LogsRoot, func(path string, info os.FileInfo, errWalk error) error {
		if errWalk != nil {
			if filepath.Clean(path) == filepath.Clean(s.cfg.LogsRoot) {
				return errWalk
			}
			log.WithError(errWalk).WithField("source", path).Warn("skipping inaccessible request log path")
			return nil
		}
		if info.IsDir() {
			relative, errRelative := filepath.Rel(s.cfg.LogsRoot, path)
			if errRelative == nil && relative != "." && strings.Contains(filepath.ToSlash(relative), "/") {
				return filepath.SkipDir
			}
			return nil
		}
		if !info.Mode().IsRegular() || !strings.EqualFold(filepath.Ext(info.Name()), ".log") {
			return nil
		}
		if info.ModTime().After(cutoff) {
			return nil
		}
		source, errInspect := inspectSourceLog(s.cfg.LogsRoot, path, info, s.location)
		if errInspect != nil {
			log.WithError(errInspect).WithField("source", path).Warn("skipping unreadable request log")
			return nil
		}
		hourReadyAt := source.ArchiveHour.Add(time.Hour).Add(s.cfg.Schedule.SettleDelay)
		if hourReadyAt.After(now) {
			return nil
		}
		if _, uploaded := state.Uploaded[source.Fingerprint]; uploaded {
			return nil
		}
		for _, prepared := range state.PreparedHours {
			for _, preparedSource := range prepared.Sources {
				if preparedSource.Fingerprint == source.Fingerprint {
					return nil
				}
			}
		}
		sources = append(sources, source)
		return nil
	})
	if errors.Is(errWalk, os.ErrNotExist) {
		return nil, nil
	}
	if errWalk != nil {
		return nil, fmt.Errorf("scan request logs: %w", errWalk)
	}
	sort.Slice(sources, func(i, j int) bool {
		if !sources[i].ArchiveHour.Equal(sources[j].ArchiveHour) {
			return sources[i].ArchiveHour.Before(sources[j].ArchiveHour)
		}
		if !sources[i].Timestamp.Equal(sources[j].Timestamp) {
			return sources[i].Timestamp.Before(sources[j].Timestamp)
		}
		return sources[i].Relative < sources[j].Relative
	})
	return sources, nil
}

type providerGroupKey struct {
	Hour     time.Time
	Provider string
}

func groupSources(sources []sourceLog) map[providerGroupKey][]sourceLog {
	groups := make(map[providerGroupKey][]sourceLog)
	for _, source := range sources {
		key := providerGroupKey{Hour: source.ArchiveHour, Provider: source.Provider}
		groups[key] = append(groups[key], source)
	}
	return groups
}

func (s *Service) processBatch(ctx context.Context, hour time.Time, provider string, sources []sourceLog, state uploadState, dryRun bool) error {
	record := auditRecord{
		Timestamp:   s.now().In(s.location),
		Provider:    provider,
		Hour:        hour,
		SourceCount: len(sources),
		KeyNames:    make(map[string]auditKeyNameSummary),
	}
	for _, source := range sources {
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
	if finalized, exists := state.Hours[hourStateKey(hour, provider)]; exists && !dryRun {
		cause := fmt.Errorf("archive hour %s is already finalized as %s; retaining %d late source logs", hour.Format(time.RFC3339), finalized.ObjectKey, len(sources))
		record.Status = "late_logs_retained"
		record.ObjectKey = finalized.ObjectKey
		record.Error = cause.Error()
		return errors.Join(cause, s.appendAudit(record))
	}
	if prepared, exists := state.PreparedHours[hourStateKey(hour, provider)]; exists && !dryRun {
		cause := fmt.Errorf("archive hour %s already has prepared object %s; retaining newly discovered source logs", hour.Format(time.RFC3339), prepared.ObjectKey)
		record.Status = "prepared_hour_blocked"
		record.ObjectKey = prepared.ObjectKey
		record.Error = cause.Error()
		return errors.Join(cause, s.appendAudit(record))
	}

	archiveStart := s.now()
	archivePath, jsonlSize, compressedSize, errArchive := s.buildArchive(ctx, hour, provider, sources, dryRun)
	if errArchive != nil {
		return s.recordBatchFailure(record, fmt.Errorf("build archive for hour %s: %w", hour.Format(time.RFC3339), errArchive))
	}
	log.WithFields(log.Fields{
		"hour":             hour.Format(time.RFC3339),
		"source_files":     len(sources),
		"jsonl_bytes":      jsonlSize,
		"compressed_bytes": compressedSize,
		"archive_duration": s.now().Sub(archiveStart).String(),
	}).Info("archive built")
	record.JSONLBytes = jsonlSize
	record.ArchivePath = archivePath
	record.CompressedBytes = compressedSize
	record.ObjectKey = s.objectKey(hour, filepath.Base(archivePath))

	shouldUpload := s.cfg.Upload.Enabled && !dryRun
	if !shouldUpload {
		record.Status = "dry_run"
		if errAudit := s.appendAudit(record); errAudit != nil {
			return errAudit
		}
		log.WithFields(log.Fields{
			"hour":      hour.Format(time.RFC3339),
			"key_names": len(record.KeyNames),
			"records":   len(sources),
			"archive":   archivePath,
		}).Info("log archive produced without upload")
		return nil
	}
	if s.uploader == nil {
		return s.recordBatchFailure(record, fmt.Errorf("upload is enabled but no object uploader is configured"))
	}
	shaStart := s.now()
	archiveSHA256, archiveSize, errChecksum := fileSHA256(archivePath)
	if errChecksum != nil {
		return s.recordBatchFailure(record, errChecksum)
	}
	log.WithFields(log.Fields{
		"hour":            hour.Format(time.RFC3339),
		"archive_size":    archiveSize,
		"sha256_duration": s.now().Sub(shaStart).String(),
	}).Info("archive checksum computed")
	if archiveSize != compressedSize {
		return s.recordBatchFailure(record, fmt.Errorf("compressed archive size changed before preparation: got %d, want %d", archiveSize, compressedSize))
	}
	prepared := preparedHour{
		TargetID:        s.target.ID,
		Hour:            hour,
		Provider:        provider,
		ObjectKey:       record.ObjectKey,
		ArchivePath:     archivePath,
		JSONLBytes:      jsonlSize,
		CompressedBytes: compressedSize,
		ArchiveSHA256:   archiveSHA256,
		PreparedAt:      s.now().In(s.location),
		Sources:         make([]preparedSource, 0, len(sources)),
	}
	for _, source := range sources {
		if source.SHA256 == "" {
			return s.recordBatchFailure(record, fmt.Errorf("source checksum is missing after archive construction: %s", source.Relative))
		}
		prepared.Sources = append(prepared.Sources, preparedSource{
			Fingerprint:  source.Fingerprint,
			RelativePath: source.Relative,
			KeyName:      source.KeyName,
			Model:        source.Model,
			Size:         source.Size,
			ModTime:      source.ModTime,
			SHA256:       source.SHA256,
		})
	}
	prepared.ManifestSHA256 = manifestSHA256(prepared.Sources)
	hourKey := hourStateKey(hour, provider)
	state.PreparedHours[hourKey] = prepared
	if errSave := s.saveState(state); errSave != nil {
		delete(state.PreparedHours, hourKey)
		return s.recordBatchFailure(record, fmt.Errorf("persist prepared hourly batch: %w", errSave))
	}
	return s.completePreparedHour(ctx, hourKey, prepared, state)
}

func (s *Service) recordBatchFailure(record auditRecord, cause error) error {
	record.Status = "failed"
	record.Error = cause.Error()
	return errors.Join(cause, s.appendAudit(record))
}

func (s *Service) buildArchive(ctx context.Context, hour time.Time, provider string, sources []sourceLog, dryRun bool) (string, int64, int64, error) {
	archiveRoot := "archives"
	if dryRun {
		archiveRoot = "dry-run-archives"
	}
	archiveDir := filepath.Join(
		s.cfg.WorkDir,
		archiveRoot,
		hour.Format("2006"),
		hour.Format("01"),
		hour.Format("02"),
	)
	if errMkdir := os.MkdirAll(archiveDir, 0o750); errMkdir != nil {
		return "", 0, 0, fmt.Errorf("create archive directory: %w", errMkdir)
	}
	destination, errCreate := os.CreateTemp(archiveDir, ".batch-*.jsonl.zst.tmp")
	if errCreate != nil {
		return "", 0, 0, fmt.Errorf("create compressed archive: %w", errCreate)
	}
	tmpPath := destination.Name()
	if errChmod := destination.Chmod(0o640); errChmod != nil {
		_ = destination.Close()
		_ = os.Remove(tmpPath)
		return "", 0, 0, fmt.Errorf("set compressed archive permissions: %w", errChmod)
	}
	encoder, errEncoder := zstd.NewWriter(destination, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if errEncoder != nil {
		_ = destination.Close()
		_ = os.Remove(tmpPath)
		return "", 0, 0, fmt.Errorf("create Zstandard encoder: %w", errEncoder)
	}

	var jsonlSize int64
	var errWrite error
	for index := range sources {
		if errContext := ctx.Err(); errContext != nil {
			errWrite = errContext
			break
		}
		written, sourceSHA256, errRecord := writeJSONLRecordWithHash(encoder, sources[index])
		jsonlSize += written
		if errRecord != nil {
			errWrite = errRecord
			break
		}
		sources[index].SHA256 = sourceSHA256
	}
	errEncoderClose := encoder.Close()
	errDestinationSync := destination.Sync()
	errDestinationClose := destination.Close()
	if errCombined := errors.Join(errWrite, errEncoderClose, errDestinationSync, errDestinationClose); errCombined != nil {
		_ = os.Remove(tmpPath)
		return "", 0, 0, fmt.Errorf("write Zstandard archive: %w", errCombined)
	}

	archiveFilename := makeArchiveFilename(hour, provider, jsonlSize)
	archivePath := filepath.Join(archiveDir, archiveFilename)
	if dryRun {
		for _, label := range []string{archiveNameLabel, claudeArchiveNameLabel, grokArchiveNameLabel, legacyArchiveNameLabel} {
			stalePattern := filepath.Join(archiveDir, fmt.Sprintf("%s-%s-*.jsonl.zst", hour.Format("2006-01-02-15"), label))
			staleArchives, errGlob := filepath.Glob(stalePattern)
			if errGlob != nil {
				_ = os.Remove(tmpPath)
				return "", 0, 0, fmt.Errorf("find stale dry-run hourly archives: %w", errGlob)
			}
			for _, staleArchive := range staleArchives {
				if errRemove := os.Remove(staleArchive); errRemove != nil && !errors.Is(errRemove, os.ErrNotExist) {
					_ = os.Remove(tmpPath)
					return "", 0, 0, fmt.Errorf("remove stale dry-run hourly archive %s: %w", staleArchive, errRemove)
				}
			}
		}
	}
	if errRemove := os.Remove(archivePath); errRemove != nil && !errors.Is(errRemove, os.ErrNotExist) {
		_ = os.Remove(tmpPath)
		return "", 0, 0, fmt.Errorf("replace existing local archive: %w", errRemove)
	}
	if errRename := os.Rename(tmpPath, archivePath); errRename != nil {
		_ = os.Remove(tmpPath)
		return "", 0, 0, fmt.Errorf("publish compressed archive: %w", errRename)
	}
	if errSyncDir := syncParentDirectory(archivePath); errSyncDir != nil {
		return "", 0, 0, errSyncDir
	}
	info, errStat := os.Stat(archivePath)
	if errStat != nil {
		return "", 0, 0, fmt.Errorf("stat compressed archive: %w", errStat)
	}
	return archivePath, jsonlSize, info.Size(), nil
}

func (s *Service) objectKey(hour time.Time, filename string) string {
	parts := []string{
		strings.Trim(s.cfg.Upload.ObjectPrefix, "/"),
		hour.Format("2006"),
		hour.Format("01"),
		hour.Format("02"),
		filename,
	}
	var clean []string
	for _, part := range parts {
		if part != "" {
			clean = append(clean, part)
		}
	}
	return strings.Join(clean, "/")
}

func hourStateKey(hour time.Time, provider string) string {
	return hour.Format("2006-01-02-15") + ":" + provider
}

func (s *Service) statePath() string {
	return filepath.Join(s.cfg.WorkDir, "state.json")
}

func (s *Service) loadState() (uploadState, error) {
	state := s.newUploadState()
	raw, errRead := os.ReadFile(s.statePath())
	if errors.Is(errRead, os.ErrNotExist) {
		return state, nil
	}
	if errRead != nil {
		return state, fmt.Errorf("read upload state: %w", errRead)
	}
	state = uploadState{}
	if errUnmarshal := json.Unmarshal(raw, &state); errUnmarshal != nil {
		return state, fmt.Errorf("parse upload state: %w", errUnmarshal)
	}
	if errValidate := s.validateUploadState(&state); errValidate != nil {
		return state, errValidate
	}
	return state, nil
}

func (s *Service) retryUploadedDeletes(state uploadState) (bool, []error) {
	fingerprints := make([]string, 0, len(state.Uploaded))
	for fingerprint := range state.Uploaded {
		fingerprints = append(fingerprints, fingerprint)
	}
	sort.Strings(fingerprints)
	deleted := 0
	return s.deleteUploadedSources(state, fingerprints, &deleted)
}

func (s *Service) deleteUploadedSources(state uploadState, fingerprints []string, deleted *int) (bool, []error) {
	changed := false
	var deleteErrors []error
	for _, fingerprint := range fingerprints {
		uploaded, exists := state.Uploaded[fingerprint]
		if !exists {
			continue
		}
		if uploaded.TargetID != s.target.ID || uploaded.SHA256 == "" {
			deleteErrors = append(deleteErrors, fmt.Errorf("uploaded source %s lacks trusted target or checksum metadata", uploaded.RelativePath))
			continue
		}
		expectedPendingPath := s.pendingDeletePath(fingerprint)
		if uploaded.PendingDeleteAt != "" && uploaded.PendingDeleteAt != expectedPendingPath {
			deleteErrors = append(deleteErrors, fmt.Errorf("uploaded source %s has invalid pending delete path %q", uploaded.RelativePath, uploaded.PendingDeleteAt))
			continue
		}
		path, errPath := safeSourcePath(s.cfg.LogsRoot, uploaded.RelativePath)
		if errPath != nil {
			deleteErrors = append(deleteErrors, errPath)
			continue
		}
		pendingPath := expectedPendingPath
		pendingInfo, errPending := os.Stat(pendingPath)
		if errPending == nil && pendingInfo.Mode().IsRegular() {
			matches, errMatch := sourceFileMatches(pendingPath, uploaded)
			if errMatch != nil {
				deleteErrors = append(deleteErrors, fmt.Errorf("verify pending source deletion %s: %w", uploaded.RelativePath, errMatch))
				continue
			}
			if !matches {
				deleteErrors = append(deleteErrors, fmt.Errorf("pending source deletion no longer matches uploaded content: %s", uploaded.RelativePath))
				continue
			}
			if errRemove := os.Remove(pendingPath); errRemove != nil {
				deleteErrors = append(deleteErrors, fmt.Errorf("delete pending uploaded source %s: %w", uploaded.RelativePath, errRemove))
				continue
			}
			delete(state.Uploaded, fingerprint)
			changed = true
			(*deleted)++
			continue
		}
		if errPending != nil && !errors.Is(errPending, os.ErrNotExist) {
			deleteErrors = append(deleteErrors, fmt.Errorf("stat pending uploaded source %s: %w", uploaded.RelativePath, errPending))
			continue
		}
		if errPending == nil {
			deleteErrors = append(deleteErrors, fmt.Errorf("pending deletion path is not a regular file: %s", pendingPath))
			continue
		}

		_, errStat := os.Stat(path)
		if errors.Is(errStat, os.ErrNotExist) {
			delete(state.Uploaded, fingerprint)
			changed = true
			continue
		}
		if errStat != nil {
			deleteErrors = append(deleteErrors, fmt.Errorf("stat uploaded source %s: %w", uploaded.RelativePath, errStat))
			continue
		}
		matches, errMatch := sourceFileMatches(path, uploaded)
		if errMatch != nil {
			deleteErrors = append(deleteErrors, fmt.Errorf("verify uploaded source %s: %w", uploaded.RelativePath, errMatch))
			continue
		}
		if !matches {
			delete(state.Uploaded, fingerprint)
			changed = true
			log.WithField("source", uploaded.RelativePath).Warn("uploaded source identity changed before deletion; leaving the new version for the next run")
			continue
		}
		if errMkdir := os.MkdirAll(filepath.Dir(pendingPath), 0o700); errMkdir != nil {
			deleteErrors = append(deleteErrors, fmt.Errorf("create pending deletion directory: %w", errMkdir))
			continue
		}
		if errRename := os.Rename(path, pendingPath); errRename != nil {
			deleteErrors = append(deleteErrors, fmt.Errorf("stage uploaded source deletion %s: %w", uploaded.RelativePath, errRename))
			continue
		}
		uploaded.PendingDeleteAt = pendingPath
		state.Uploaded[fingerprint] = uploaded
		changed = true
		matches, errMatch = sourceFileMatches(pendingPath, uploaded)
		if errMatch != nil || !matches {
			if _, errOriginal := os.Stat(path); errors.Is(errOriginal, os.ErrNotExist) {
				if errRestore := os.Rename(pendingPath, path); errRestore == nil {
					delete(state.Uploaded, fingerprint)
					log.WithField("source", uploaded.RelativePath).Warn("staged source changed identity; restored it for reprocessing")
					continue
				}
			}
			if errMatch != nil {
				deleteErrors = append(deleteErrors, fmt.Errorf("verify staged uploaded source %s: %w", uploaded.RelativePath, errMatch))
			} else {
				deleteErrors = append(deleteErrors, fmt.Errorf("staged uploaded source identity mismatch for %s", uploaded.RelativePath))
			}
			continue
		}
		if errRemove := os.Remove(pendingPath); errRemove != nil {
			deleteErrors = append(deleteErrors, fmt.Errorf("delete staged uploaded source %s: %w", uploaded.RelativePath, errRemove))
			continue
		}
		delete(state.Uploaded, fingerprint)
		(*deleted)++
	}
	return changed, deleteErrors
}

func safeSourcePath(root, relative string) (string, error) {
	rootAbsolute, errRoot := filepath.Abs(root)
	if errRoot != nil {
		return "", fmt.Errorf("resolve logs root: %w", errRoot)
	}
	path := filepath.Join(rootAbsolute, filepath.FromSlash(relative))
	relativeCheck, errRelative := filepath.Rel(rootAbsolute, path)
	if errRelative != nil || relativeCheck == ".." || strings.HasPrefix(relativeCheck, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("uploaded source path escapes logs root: %s", relative)
	}
	return path, nil
}

func (s *Service) retryLocalArchiveDeletes(state uploadState) (bool, []error) {
	objectKeys := make([]string, 0, len(state.Objects))
	for objectKey, object := range state.Objects {
		if object.ArchivePath != "" {
			objectKeys = append(objectKeys, objectKey)
		}
	}
	sort.Strings(objectKeys)
	return s.deleteLocalArchives(state, objectKeys)
}

func (s *Service) deleteLocalArchives(state uploadState, objectKeys []string) (bool, []error) {
	changed := false
	var deleteErrors []error
	archiveRoot := filepath.Join(s.cfg.WorkDir, "archives")
	for _, objectKey := range objectKeys {
		object, exists := state.Objects[objectKey]
		if !exists || object.ArchivePath == "" {
			continue
		}
		archivePath, errPath := safeExistingPath(archiveRoot, object.ArchivePath)
		if errPath != nil {
			deleteErrors = append(deleteErrors, errPath)
			continue
		}
		if errRemove := os.Remove(archivePath); errRemove != nil && !errors.Is(errRemove, os.ErrNotExist) {
			deleteErrors = append(deleteErrors, fmt.Errorf("delete local archive %s: %w", archivePath, errRemove))
			continue
		}
		object.ArchivePath = ""
		state.Objects[objectKey] = object
		changed = true
	}
	return changed, deleteErrors
}

func safeExistingPath(root, path string) (string, error) {
	rootAbsolute, errRoot := filepath.Abs(root)
	if errRoot != nil {
		return "", fmt.Errorf("resolve safe path root: %w", errRoot)
	}
	pathAbsolute, errPath := filepath.Abs(path)
	if errPath != nil {
		return "", fmt.Errorf("resolve safe path: %w", errPath)
	}
	relativeCheck, errRelative := filepath.Rel(rootAbsolute, pathAbsolute)
	if errRelative != nil || relativeCheck == ".." || strings.HasPrefix(relativeCheck, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes expected root: %s", path)
	}
	return pathAbsolute, nil
}

func (s *Service) saveState(state uploadState) error {
	state.dirty = false
	raw, errMarshal := json.MarshalIndent(state, "", "  ")
	if errMarshal != nil {
		return fmt.Errorf("marshal upload state: %w", errMarshal)
	}
	tmpPath := s.statePath() + ".tmp"
	file, errOpen := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if errOpen != nil {
		return fmt.Errorf("open temporary upload state: %w", errOpen)
	}
	_, errWrite := file.Write(raw)
	errSync := file.Sync()
	errClose := file.Close()
	if errCombined := errors.Join(errWrite, errSync, errClose); errCombined != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write temporary upload state: %w", errCombined)
	}
	if errRename := os.Rename(tmpPath, s.statePath()); errRename != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("publish upload state: %w", errRename)
	}
	if errSyncDir := syncParentDirectory(s.statePath()); errSyncDir != nil {
		return errSyncDir
	}
	return nil
}

func (s *Service) appendAudit(record auditRecord) error {
	path := filepath.Join(s.cfg.WorkDir, "audit.jsonl")
	file, errOpen := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if errOpen != nil {
		return fmt.Errorf("open uploader audit log: %w", errOpen)
	}
	errEncode := json.NewEncoder(file).Encode(record)
	errSync := file.Sync()
	errClose := file.Close()
	if errCombined := errors.Join(errEncode, errSync, errClose); errCombined != nil {
		return fmt.Errorf("write uploader audit log: %w", errCombined)
	}
	return nil
}
