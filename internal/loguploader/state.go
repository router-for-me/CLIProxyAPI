package loguploader

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	uploadStateSchemaVersion   = 3
	preparedUsageSchemaVersion = 2
)

func canonicalUploadTarget(cfg UploadConfig) (uploadTarget, error) {
	endpoint, errEndpoint := parseTOSEndpoint(cfg.Endpoint)
	if errEndpoint != nil {
		return uploadTarget{}, fmt.Errorf("canonicalize upload target: %w", errEndpoint)
	}
	target := uploadTarget{
		Provider:     "volcengine-tos",
		Endpoint:     strings.ToLower(strings.TrimRight(endpoint, "/")),
		Region:       strings.TrimSpace(cfg.Region),
		Bucket:       strings.TrimSpace(cfg.Bucket),
		ObjectPrefix: strings.Trim(strings.TrimSpace(cfg.ObjectPrefix), "/"),
	}
	identity := strings.Join([]string{
		target.Provider,
		target.Endpoint,
		target.Region,
		target.Bucket,
		target.ObjectPrefix,
	}, "\x00")
	target.ID = fmt.Sprintf("%x", sha256.Sum256([]byte(identity)))
	return target, nil
}

func (s *Service) newUploadState() uploadState {
	return uploadState{
		SchemaVersion:   uploadStateSchemaVersion,
		Target:          s.target,
		Policy:          s.policy,
		Uploaded:        make(map[string]uploadedSource),
		Objects:         make(map[string]uploadedObject),
		Hours:           make(map[string]uploadedHour),
		PreparedHours:   make(map[string]preparedHour),
		SupabaseOutbox:  newSupabaseOutboxState(s),
		SupabaseHistory: make(map[string]supabaseHistoryCheckpoint),
	}
}

func (s *Service) validateUploadState(state *uploadState) error {
	if state.SchemaVersion == 0 {
		return fmt.Errorf("legacy upload state is not trusted for automatic cleanup; migrate it explicitly before starting the uploader")
	}
	if state.SchemaVersion == 2 {
		if !state.SupabaseOutbox.empty() || len(state.SupabaseHistory) != 0 {
			return fmt.Errorf("schema-v2 upload state must not contain Supabase outbox or history data")
		}
		state.SchemaVersion = uploadStateSchemaVersion
		state.SupabaseOutbox = newSupabaseOutboxState(s)
		state.SupabaseHistory = make(map[string]supabaseHistoryCheckpoint)
		state.dirty = true
	}
	if state.SchemaVersion != uploadStateSchemaVersion {
		return fmt.Errorf("unsupported upload state schema version %d", state.SchemaVersion)
	}
	if state.SupabaseHistory == nil {
		state.SupabaseHistory = make(map[string]supabaseHistoryCheckpoint)
	}
	if state.Target != s.target {
		return fmt.Errorf("upload state target mismatch: state target %s does not match configured target %s", state.Target.ID, s.target.ID)
	}
	if state.Policy != s.policy {
		// Migration path: all-models-jsonl-size-v1 -> codex56sol-jsonl-size-v1 -> provider-jsonl-size-v2
		legacyV1 := s.policy
		legacyV1.Naming = legacyAllModelsNamingPolicy
		legacyV2 := s.policy
		legacyV2.Naming = legacyArchiveNamingPolicy
		if state.Policy != legacyV1 && state.Policy != legacyV2 {
			return fmt.Errorf("upload state policy mismatch: state policy %+v does not match configured policy %+v", state.Policy, s.policy)
		}
		state.Policy = s.policy
		state.dirty = true
	}
	if state.Uploaded == nil {
		state.Uploaded = make(map[string]uploadedSource)
		state.dirty = true
	}
	if state.Objects == nil {
		state.Objects = make(map[string]uploadedObject)
		state.dirty = true
	}
	if state.Hours == nil {
		state.Hours = make(map[string]uploadedHour)
		state.dirty = true
	}
	if state.PreparedHours == nil {
		state.PreparedHours = make(map[string]preparedHour)
		state.dirty = true
	}
	if state.SessionGate != nil && state.SessionGate.Sessions == nil {
		state.SessionGate.Sessions = make(map[string]sessionHoldRecord)
		state.dirty = true
	}

	// Migrate legacy hour keys (without provider suffix) to the new format.
	for hourKey, hour := range state.Hours {
		if !strings.Contains(hourKey, ":") {
			newKey := hourKey + ":" + providerCodex
			state.Hours[newKey] = hour
			delete(state.Hours, hourKey)
			state.dirty = true
		}
	}
	for hourKey, prepared := range state.PreparedHours {
		if !strings.Contains(hourKey, ":") {
			newKey := hourKey + ":" + providerCodex
			if prepared.Provider == "" {
				prepared.Provider = providerCodex
			}
			state.PreparedHours[newKey] = prepared
			delete(state.PreparedHours, hourKey)
			state.dirty = true
		}
	}
	for fingerprint, source := range state.Uploaded {
		if source.HourKey != "" && !strings.Contains(source.HourKey, ":") {
			source.HourKey = source.HourKey + ":" + providerCodex
			state.Uploaded[fingerprint] = source
			state.dirty = true
		}
	}

	objectHours := make(map[string]string, len(state.Hours))
	for objectKey, object := range state.Objects {
		if strings.TrimSpace(objectKey) == "" || object.ObjectKey != objectKey {
			return fmt.Errorf("uploaded object map key %q does not match object key %q", objectKey, object.ObjectKey)
		}
		if !isSHA256(object.ArchiveSHA256) {
			return fmt.Errorf("uploaded object %s has an invalid archive checksum", objectKey)
		}
		if object.ArchivePath != "" {
			archiveRoot := filepath.Join(s.cfg.WorkDir, "archives")
			if _, errPath := safeExistingPath(archiveRoot, object.ArchivePath); errPath != nil {
				return fmt.Errorf("uploaded object %s has an unsafe archive path: %w", objectKey, errPath)
			}
		}
	}
	for hourKey, hour := range state.Hours {
		if errHourKey := s.validateHourStateKey(hourKey); errHourKey != nil {
			return errHourKey
		}
		if hour.Status != "sealed" {
			return fmt.Errorf("uploaded hour %s has unsupported status %q", hourKey, hour.Status)
		}
		object, exists := state.Objects[hour.ObjectKey]
		if !exists {
			return fmt.Errorf("uploaded hour %s references missing object %s", hourKey, hour.ObjectKey)
		}
		if hour.ArchiveSHA256 == "" || hour.ArchiveSHA256 != object.ArchiveSHA256 {
			return fmt.Errorf("uploaded hour %s archive checksum does not match object %s", hourKey, hour.ObjectKey)
		}
		if strings.TrimSpace(hour.ManifestSHA256) == "" {
			return fmt.Errorf("uploaded hour %s has an empty manifest checksum", hourKey)
		}
		if hour.SupabaseEventID != "" && !supabaseEventIDPattern.MatchString(hour.SupabaseEventID) {
			return fmt.Errorf("uploaded hour %s has an invalid Supabase event ID", hourKey)
		}
		if previousHour, duplicate := objectHours[hour.ObjectKey]; duplicate {
			return fmt.Errorf("uploaded object %s is referenced by hours %s and %s", hour.ObjectKey, previousHour, hourKey)
		}
		objectHours[hour.ObjectKey] = hourKey
	}
	for objectKey := range state.Objects {
		if _, referenced := objectHours[objectKey]; !referenced {
			return fmt.Errorf("uploaded object %s is not referenced by a sealed hour", objectKey)
		}
	}

	fingerprintOwners := make(map[string]string, len(state.Uploaded))
	for fingerprint, source := range state.Uploaded {
		if source.TargetID != s.target.ID || !isSHA256(source.SHA256) || source.HourKey == "" {
			return fmt.Errorf("uploaded source %s lacks trusted target, hour, or checksum metadata", fingerprint)
		}
		if fingerprint == "" || fingerprint != sourceFingerprint(source.RelativePath, source.Size, source.ModTime) {
			return fmt.Errorf("uploaded source map key %q does not match its source identity", fingerprint)
		}
		if _, errPath := safeSourcePath(s.cfg.LogsRoot, source.RelativePath); errPath != nil {
			return fmt.Errorf("uploaded source %s has an unsafe relative path: %w", fingerprint, errPath)
		}
		if source.PendingDeleteAt != "" && source.PendingDeleteAt != s.pendingDeletePath(fingerprint) {
			return fmt.Errorf("uploaded source %s has invalid pending delete path %q", fingerprint, source.PendingDeleteAt)
		}
		hour, exists := state.Hours[source.HourKey]
		if !exists || hour.Status != "sealed" || hour.ObjectKey != source.ObjectKey {
			return fmt.Errorf("uploaded source %s does not reference a sealed hour and object", fingerprint)
		}
		if _, exists := state.Objects[source.ObjectKey]; !exists {
			return fmt.Errorf("uploaded source %s references missing object %s", fingerprint, source.ObjectKey)
		}
		fingerprintOwners[fingerprint] = "uploaded source"
	}
	preparedObjects := make(map[string]string, len(state.PreparedHours))
	for hourKey, prepared := range state.PreparedHours {
		if errHourKey := s.validateHourStateKey(hourKey); errHourKey != nil {
			return errHourKey
		}
		if prepared.TargetID != s.target.ID || prepared.ObjectKey == "" || !isSHA256(prepared.ArchiveSHA256) || len(prepared.Sources) == 0 {
			return fmt.Errorf("prepared hour %s is missing trusted batch metadata", hourKey)
		}
		if s.cfg.Supabase.Enabled && len(prepared.Usage) == 0 &&
			(len(prepared.Sources) > 0 || prepared.JSONLBytes > 0 || prepared.CompressedBytes > 0) {
			return fmt.Errorf("prepared hour %s lacks exact Supabase usage metadata required while Supabase is enabled", hourKey)
		}
		legacyUsageMetadata := prepared.UsageSchemaVersion == 0 && prepared.UsageSHA256 == ""
		if s.cfg.Supabase.Enabled || !legacyUsageMetadata {
			if errHourBoundary := s.validatePreparedHourBoundary(prepared.Hour); errHourBoundary != nil {
				return fmt.Errorf("prepared hour %s: %w", hourKey, errHourBoundary)
			}
		}
		if errUsageIntegrity := s.validatePreparedUsageIntegrity(prepared, s.cfg.Supabase.Enabled); errUsageIntegrity != nil {
			return fmt.Errorf("prepared hour %s has invalid exact Supabase usage metadata: %w", hourKey, errUsageIntegrity)
		}
		if hourStateKey(prepared.Hour.In(s.location), prepared.Provider) != hourKey {
			return fmt.Errorf("prepared hour %s does not match its state key", hourKey)
		}
		if _, sealed := state.Hours[hourKey]; sealed {
			return fmt.Errorf("hour %s is both prepared and sealed", hourKey)
		}
		if _, committed := state.Objects[prepared.ObjectKey]; committed {
			return fmt.Errorf("prepared hour %s reuses committed object %s", hourKey, prepared.ObjectKey)
		}
		if previousHour, duplicate := preparedObjects[prepared.ObjectKey]; duplicate {
			return fmt.Errorf("prepared object %s is shared by hours %s and %s", prepared.ObjectKey, previousHour, hourKey)
		}
		preparedObjects[prepared.ObjectKey] = hourKey
		archiveRoot := filepath.Join(s.cfg.WorkDir, "archives")
		if _, errPath := safeExistingPath(archiveRoot, prepared.ArchivePath); errPath != nil {
			return fmt.Errorf("prepared hour %s has an unsafe archive path: %w", hourKey, errPath)
		}
		if gotManifest := manifestSHA256(prepared.Sources); gotManifest != prepared.ManifestSHA256 {
			return fmt.Errorf("prepared hour %s manifest checksum mismatch: got %s, want %s", hourKey, gotManifest, prepared.ManifestSHA256)
		}
		for _, source := range prepared.Sources {
			if source.Fingerprint == "" || source.Fingerprint != sourceFingerprint(source.RelativePath, source.Size, source.ModTime) || !isSHA256(source.SHA256) {
				return fmt.Errorf("prepared hour %s has invalid source identity for %s", hourKey, source.RelativePath)
			}
			if _, errPath := safeSourcePath(s.cfg.LogsRoot, source.RelativePath); errPath != nil {
				return fmt.Errorf("prepared hour %s has an unsafe source path: %w", hourKey, errPath)
			}
			if owner, duplicate := fingerprintOwners[source.Fingerprint]; duplicate {
				return fmt.Errorf("source fingerprint %s is shared by %s and prepared hour %s", source.Fingerprint, owner, hourKey)
			}
			fingerprintOwners[source.Fingerprint] = "prepared hour " + hourKey
		}
		if s.cfg.Supabase.Enabled {
			if _, errPayload := s.buildSupabaseEventPayload(prepared); errPayload != nil {
				return fmt.Errorf("prepared hour %s has invalid exact Supabase usage metadata: %w", hourKey, errPayload)
			}
		}
	}
	if errOutbox := s.validateSupabaseOutboxState(state); errOutbox != nil {
		return errOutbox
	}
	return nil
}

func (s *Service) validateHourStateKey(hourKey string) error {
	// New format: "2006-01-02-15:provider"
	if idx := strings.LastIndex(hourKey, ":"); idx > 0 {
		hourPart := hourKey[:idx]
		providerPart := hourKey[idx+1:]
		if providerPart != providerCodex && providerPart != providerClaude && providerPart != providerGrok {
			return fmt.Errorf("invalid provider in hour key %q", hourKey)
		}
		hour, errParse := time.ParseInLocation("2006-01-02-15", hourPart, s.location)
		if errParse != nil || hourStateKey(hour, providerPart) != hourKey {
			return fmt.Errorf("invalid upload state hour key %q", hourKey)
		}
		return nil
	}
	// Legacy format: "2006-01-02-15" (treated as codex)
	hour, errParse := time.ParseInLocation("2006-01-02-15", hourKey, s.location)
	if errParse != nil || hour.Format("2006-01-02-15") != hourKey {
		return fmt.Errorf("invalid upload state hour key %q", hourKey)
	}
	return nil
}

func (s *Service) validatePreparedHourBoundary(hour time.Time) error {
	if hour.IsZero() || !hour.Equal(hour.Truncate(time.Hour)) {
		return fmt.Errorf("must use the canonical hour boundary")
	}
	return nil
}

func sourceFingerprint(relativePath string, size int64, modTime time.Time) string {
	return fmt.Sprintf("%s|%d|%d", filepath.ToSlash(relativePath), size, modTime.UnixNano())
}

func isSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, errDecode := hex.DecodeString(value)
	return errDecode == nil
}

func manifestSHA256(sources []preparedSource) string {
	entries := make([]string, 0, len(sources))
	for _, source := range sources {
		entries = append(entries, strings.Join([]string{
			source.Fingerprint,
			source.RelativePath,
			fmt.Sprintf("%d", source.Size),
			source.ModTime.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
			source.SHA256,
		}, "\x00"))
	}
	sort.Strings(entries)
	return fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(entries, "\n"))))
}

func (s *Service) validatePreparedUsageIntegrity(prepared preparedHour, required bool) error {
	if prepared.UsageSchemaVersion == 0 && prepared.UsageSHA256 == "" {
		if required {
			return fmt.Errorf("exact Supabase usage metadata checksum is missing")
		}
		return nil
	}
	if prepared.UsageSchemaVersion != preparedUsageSchemaVersion {
		return fmt.Errorf("unsupported exact Supabase usage schema version %d", prepared.UsageSchemaVersion)
	}
	if !isSHA256(prepared.UsageSHA256) {
		return fmt.Errorf("exact Supabase usage checksum is invalid")
	}
	gotUsageSHA256, errUsageSHA := s.preparedUsageSHA256(prepared)
	if errUsageSHA != nil {
		return errUsageSHA
	}
	if gotUsageSHA256 != prepared.UsageSHA256 {
		return fmt.Errorf("usage checksum mismatch")
	}
	return nil
}

func (s *Service) preparedUsageSHA256(prepared preparedHour) (string, error) {
	if errHourBoundary := s.validatePreparedHourBoundary(prepared.Hour); errHourBoundary != nil {
		return "", errHourBoundary
	}
	if !isSupabaseProvider(prepared.Provider) {
		return "", fmt.Errorf("unsupported exact usage provider")
	}
	entries := make([]string, 0, len(prepared.Sources))
	for _, source := range prepared.Sources {
		if source.JSONLBytes == nil {
			return "", fmt.Errorf("exact per-source Supabase usage metadata is missing")
		}
		if errSize := validateSafeJSONInteger("per-source source_bytes", source.Size); errSize != nil {
			return "", errSize
		}
		if errJSONLBytes := validateSafeJSONInteger("per-source jsonl_bytes", *source.JSONLBytes); errJSONLBytes != nil {
			return "", errJSONLBytes
		}
		entries = append(entries, canonicalSHA256([]string{
			source.Fingerprint,
			source.RelativePath,
			source.KeyName,
			strconv.FormatInt(source.Size, 10),
			source.ModTime.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
			source.SHA256,
			strconv.FormatInt(*source.JSONLBytes, 10),
		}))
	}
	sort.Strings(entries)
	canonicalHour := prepared.Hour.In(s.location).Format(time.RFC3339)
	fields := append([]string{"prepared-usage-v2", canonicalHour, prepared.Provider}, entries...)
	return canonicalSHA256(fields), nil
}

func canonicalSHA256(fields []string) string {
	hash := sha256.New()
	var length [8]byte
	for _, field := range fields {
		binary.BigEndian.PutUint64(length[:], uint64(len(field)))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write([]byte(field))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func syncParentDirectory(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	directory, errOpen := os.Open(filepath.Dir(path))
	if errOpen != nil {
		return fmt.Errorf("open parent directory for sync: %w", errOpen)
	}
	errSync := directory.Sync()
	errClose := directory.Close()
	if errSync != nil {
		return fmt.Errorf("sync parent directory: %w", errSync)
	}
	if errClose != nil {
		return fmt.Errorf("close parent directory: %w", errClose)
	}
	return nil
}
