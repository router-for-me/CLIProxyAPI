package loguploader

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	supabaseEventSchemaVersion = 1
	maxSafeJSONInteger         = int64(1<<53 - 1)
	maxSupabaseUsageRows       = 10_000

	supabaseUsagePrecisionExact     = "exact"
	supabaseUsagePrecisionBatchOnly = "batch_only"
)

var (
	supabaseEventIDPattern  = regexp.MustCompile(`^cliproxy-v1\.[0-9a-f]{64}$`)
	supabaseTargetIDPattern = regexp.MustCompile(`^tos:[0-9a-f]{64}$`)
	sha256Pattern           = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)
	explicitTimezonePattern = regexp.MustCompile(`(?:Z|[+-][0-9]{2}:[0-9]{2})$`)
	uriSchemePattern        = regexp.MustCompile(`(?i)^[a-z][a-z0-9+.-]*:`)
	dotSegmentPattern       = regexp.MustCompile(`(?i)^(?:\.|%2e){2}$`)
	keyJWTSecretPattern     = regexp.MustCompile(`^[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}$`)
	keyTokenAlphabetPattern = regexp.MustCompile(`^[A-Za-z0-9_+/=.\-]+$`)
)

type supabaseEventPayload struct {
	SchemaVersion   int                  `json:"schema_version"`
	EventID         string               `json:"event_id"`
	TargetID        string               `json:"target_id"`
	ObjectKey       string               `json:"object_key"`
	ArchiveSHA256   string               `json:"archive_sha256"`
	ManifestSHA256  string               `json:"manifest_sha256"`
	HourStart       string               `json:"hour_start"`
	Timezone        string               `json:"timezone"`
	UsageDate       string               `json:"usage_date"`
	SourceCount     int64                `json:"source_count"`
	SourceBytes     int64                `json:"source_bytes"`
	JSONLBytes      int64                `json:"jsonl_bytes"`
	CompressedBytes int64                `json:"compressed_bytes"`
	TestMode        bool                 `json:"test_mode"`
	UsagePrecision  string               `json:"usage_precision,omitempty"`
	Usage           []supabaseEventUsage `json:"usage"`
}

type supabaseEventUsage struct {
	KeyName     string `json:"key_name"`
	Provider    string `json:"provider"`
	SourceCount int64  `json:"source_count"`
	SourceBytes int64  `json:"source_bytes"`
	JSONLBytes  *int64 `json:"jsonl_bytes,omitempty"`
}

type supabaseEventResult struct {
	eventID string
	rawJSON []byte
}

func (result supabaseEventResult) EventID() string {
	return result.eventID
}

func (result supabaseEventResult) RawJSON() []byte {
	return bytes.Clone(result.rawJSON)
}

func (s *Service) prepareSupabaseEvent(prepared preparedHour) (supabaseEventResult, error) {
	payload, errPayload := s.buildSupabaseEventPayload(prepared)
	if errPayload != nil {
		return supabaseEventResult{}, errPayload
	}
	rawJSON, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return supabaseEventResult{}, fmt.Errorf("marshal Supabase event: %w", errMarshal)
	}
	return supabaseEventResult{eventID: payload.EventID, rawJSON: bytes.Clone(rawJSON)}, nil
}

func (s *Service) buildSupabaseEventPayload(prepared preparedHour) (supabaseEventPayload, error) {
	if prepared.TargetID != s.target.ID {
		return supabaseEventPayload{}, fmt.Errorf("prepare Supabase event: prepared target does not match configured target")
	}
	if !isSupabaseProvider(prepared.Provider) {
		return supabaseEventPayload{}, fmt.Errorf("prepare Supabase event: unsupported archive provider")
	}
	if errHourBoundary := s.validatePreparedHourBoundary(prepared.Hour); errHourBoundary != nil {
		return supabaseEventPayload{}, fmt.Errorf("prepare Supabase event: %w", errHourBoundary)
	}
	if errUsageIntegrity := s.validatePreparedUsageIntegrity(prepared, true); errUsageIntegrity != nil {
		return supabaseEventPayload{}, fmt.Errorf("prepare Supabase event: %w", errUsageIntegrity)
	}

	preparedUsageRows := append([]preparedUsage(nil), prepared.Usage...)
	sort.Slice(preparedUsageRows, func(i, j int) bool {
		if preparedUsageRows[i].KeyName != preparedUsageRows[j].KeyName {
			return preparedUsageRows[i].KeyName < preparedUsageRows[j].KeyName
		}
		return preparedUsageRows[i].Provider < preparedUsageRows[j].Provider
	})
	usage := make([]supabaseEventUsage, 0, len(preparedUsageRows))
	for _, row := range preparedUsageRows {
		jsonlBytes := row.JSONLBytes
		usage = append(usage, supabaseEventUsage{
			KeyName:     row.KeyName,
			Provider:    row.Provider,
			SourceCount: row.SourceCount,
			SourceBytes: row.SourceBytes,
			JSONLBytes:  &jsonlBytes,
		})
	}
	expectedUsage, errExpectedUsage := aggregatePreparedSourceUsage(prepared.Provider, prepared.Sources)
	if errExpectedUsage != nil {
		return supabaseEventPayload{}, fmt.Errorf("prepare Supabase event: %w", errExpectedUsage)
	}
	if !preparedUsageEqual(preparedUsageRows, expectedUsage) {
		return supabaseEventPayload{}, fmt.Errorf("prepare Supabase event: exact usage does not match prepared sources")
	}

	sourceBytes, errSourceBytes := sumPreparedSourceBytes(prepared.Sources)
	if errSourceBytes != nil {
		return supabaseEventPayload{}, fmt.Errorf("prepare Supabase event: %w", errSourceBytes)
	}
	hourStart := prepared.Hour.In(s.location).Format(time.RFC3339)
	usageDate := prepared.Hour.In(s.location).Format("2006-01-02")
	targetID := "tos:" + s.target.ID
	archiveSHA256 := strings.ToLower(prepared.ArchiveSHA256)
	manifestSHA256 := strings.ToLower(prepared.ManifestSHA256)

	payload := supabaseEventPayload{
		SchemaVersion:   supabaseEventSchemaVersion,
		TargetID:        targetID,
		ObjectKey:       prepared.ObjectKey,
		ArchiveSHA256:   archiveSHA256,
		ManifestSHA256:  manifestSHA256,
		HourStart:       hourStart,
		Timezone:        s.policy.Timezone,
		UsageDate:       usageDate,
		SourceCount:     int64(len(prepared.Sources)),
		SourceBytes:     sourceBytes,
		JSONLBytes:      prepared.JSONLBytes,
		CompressedBytes: prepared.CompressedBytes,
		TestMode:        false,
		Usage:           usage,
	}
	payload.EventID = supabaseEventID(payload, prepared.Provider)
	if errValidate := validateSupabaseEventPayload(payload); errValidate != nil {
		return supabaseEventPayload{}, fmt.Errorf("prepare Supabase event: %w", errValidate)
	}
	return payload, nil
}

func aggregatePreparedSourceUsage(provider string, sources []preparedSource) ([]preparedUsage, error) {
	usageByKey := make(map[string]preparedUsage)
	for _, source := range sources {
		if source.JSONLBytes == nil {
			return nil, fmt.Errorf("exact per-source Supabase usage metadata is missing")
		}
		if errSize := validateSafeJSONInteger("per-source source_bytes", source.Size); errSize != nil {
			return nil, errSize
		}
		if errJSONLBytes := validateSafeJSONInteger("per-source jsonl_bytes", *source.JSONLBytes); errJSONLBytes != nil {
			return nil, errJSONLBytes
		}
		usage := usageByKey[source.KeyName]
		usage.KeyName = source.KeyName
		usage.Provider = provider
		var errAdd error
		usage.SourceCount, errAdd = addSafeJSONInteger(usage.SourceCount, 1)
		if errAdd != nil {
			return nil, fmt.Errorf("per-source source_count total exceeds the safe JSON integer range")
		}
		usage.SourceBytes, errAdd = addSafeJSONInteger(usage.SourceBytes, source.Size)
		if errAdd != nil {
			return nil, fmt.Errorf("per-source source_bytes total exceeds the safe JSON integer range")
		}
		usage.JSONLBytes, errAdd = addSafeJSONInteger(usage.JSONLBytes, *source.JSONLBytes)
		if errAdd != nil {
			return nil, fmt.Errorf("per-source jsonl_bytes total exceeds the safe JSON integer range")
		}
		usageByKey[source.KeyName] = usage
	}

	usage := make([]preparedUsage, 0, len(usageByKey))
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

func preparedUsageEqual(first, second []preparedUsage) bool {
	if len(first) != len(second) {
		return false
	}
	for index := range first {
		if first[index] != second[index] {
			return false
		}
	}
	return true
}

func supabaseEventID(payload supabaseEventPayload, provider string) string {
	fields := []string{
		"cliproxy-supabase-event-v1",
		payload.TargetID,
		payload.ObjectKey,
		payload.ArchiveSHA256,
		payload.ManifestSHA256,
		payload.HourStart,
		payload.Timezone,
		payload.UsageDate,
		provider,
		strconv.FormatInt(payload.SourceCount, 10),
		strconv.FormatInt(payload.SourceBytes, 10),
		strconv.FormatInt(payload.JSONLBytes, 10),
		strconv.FormatInt(payload.CompressedBytes, 10),
		strconv.FormatBool(payload.TestMode),
	}
	for _, usage := range payload.Usage {
		jsonlBytes := int64(0)
		if usage.JSONLBytes != nil {
			jsonlBytes = *usage.JSONLBytes
		}
		fields = append(fields,
			usage.KeyName,
			usage.Provider,
			strconv.FormatInt(usage.SourceCount, 10),
			strconv.FormatInt(usage.SourceBytes, 10),
			strconv.FormatInt(jsonlBytes, 10),
		)
	}

	hash := sha256.New()
	var length [8]byte
	for _, field := range fields {
		binary.BigEndian.PutUint64(length[:], uint64(len(field)))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write([]byte(field))
	}
	return fmt.Sprintf("cliproxy-v1.%x", hash.Sum(nil))
}

func supabaseHistoryEventID(payload supabaseEventPayload) string {
	fields := []string{
		"cliproxy-supabase-history-batch-only-v1",
		payload.TargetID,
		payload.ObjectKey,
		payload.ArchiveSHA256,
		payload.ManifestSHA256,
		payload.HourStart,
		payload.Timezone,
		payload.UsageDate,
		strconv.FormatInt(payload.SourceCount, 10),
		strconv.FormatInt(payload.SourceBytes, 10),
		strconv.FormatInt(payload.JSONLBytes, 10),
		strconv.FormatInt(payload.CompressedBytes, 10),
		strconv.FormatBool(payload.TestMode),
		supabaseUsagePrecisionBatchOnly,
	}
	for _, usage := range payload.Usage {
		fields = append(fields,
			usage.KeyName,
			usage.Provider,
			strconv.FormatInt(usage.SourceCount, 10),
			strconv.FormatInt(usage.SourceBytes, 10),
			"unknown-jsonl-bytes",
		)
	}
	return "cliproxy-v1." + canonicalSHA256(fields)
}

func validateSupabaseEventPayload(payload supabaseEventPayload) error {
	if payload.SchemaVersion != supabaseEventSchemaVersion {
		return fmt.Errorf("schema_version must be %d", supabaseEventSchemaVersion)
	}
	if !supabaseEventIDPattern.MatchString(payload.EventID) {
		return fmt.Errorf("event_id must use the cliproxy-v1.<sha256> format")
	}
	if !supabaseTargetIDPattern.MatchString(payload.TargetID) {
		return fmt.Errorf("target_id must use the tos:<sha256> format")
	}
	if errObjectKey := validateSupabaseObjectKey(payload.ObjectKey); errObjectKey != nil {
		return errObjectKey
	}
	if !sha256Pattern.MatchString(payload.ArchiveSHA256) {
		return fmt.Errorf("archive_sha256 must be a 64-character hexadecimal checksum")
	}
	if !sha256Pattern.MatchString(payload.ManifestSHA256) {
		return fmt.Errorf("manifest_sha256 must be a 64-character hexadecimal checksum")
	}
	if payload.Timezone == "" || utf8.RuneCountInString(payload.Timezone) > 100 {
		return fmt.Errorf("timezone must be a valid IANA timezone name")
	}
	location, errLocation := time.LoadLocation(payload.Timezone)
	if errLocation != nil {
		return fmt.Errorf("timezone must be a valid IANA timezone name")
	}
	if !explicitTimezonePattern.MatchString(payload.HourStart) {
		return fmt.Errorf("hour_start must include an explicit timezone offset")
	}
	hourStart, errHour := time.Parse(time.RFC3339Nano, payload.HourStart)
	if errHour != nil {
		return fmt.Errorf("hour_start must be a valid RFC3339 timestamp")
	}
	if _, errUsageDate := time.Parse("2006-01-02", payload.UsageDate); errUsageDate != nil ||
		hourStart.In(location).Format("2006-01-02") != payload.UsageDate {
		return fmt.Errorf("usage_date must match hour_start in timezone")
	}

	if errInteger := validateSafeJSONInteger("source_count", payload.SourceCount); errInteger != nil {
		return errInteger
	}
	if errInteger := validateSafeJSONInteger("source_bytes", payload.SourceBytes); errInteger != nil {
		return errInteger
	}
	if errInteger := validateSafeJSONInteger("jsonl_bytes", payload.JSONLBytes); errInteger != nil {
		return errInteger
	}
	if errInteger := validateSafeJSONInteger("compressed_bytes", payload.CompressedBytes); errInteger != nil {
		return errInteger
	}
	if len(payload.Usage) > maxSupabaseUsageRows {
		return fmt.Errorf("usage contains more than %d rows", maxSupabaseUsageRows)
	}
	precision := payload.UsagePrecision
	if precision == "" {
		precision = supabaseUsagePrecisionExact
	}
	if precision != supabaseUsagePrecisionExact && precision != supabaseUsagePrecisionBatchOnly {
		return fmt.Errorf("usage_precision must be exact or batch_only")
	}

	seen := make(map[string]struct{}, len(payload.Usage))
	var sourceCount int64
	var sourceBytes int64
	var jsonlBytes int64
	for index, usage := range payload.Usage {
		if errKeyName := validateSupabaseKeyName(usage.KeyName); errKeyName != nil {
			return fmt.Errorf("usage[%d].key_name: %w", index, errKeyName)
		}
		if !isSupabaseProvider(usage.Provider) {
			return fmt.Errorf("usage[%d].provider is unsupported", index)
		}
		pair := usage.KeyName + "\x00" + usage.Provider
		if _, duplicate := seen[pair]; duplicate {
			return fmt.Errorf("usage contains a duplicate key_name and provider pair")
		}
		seen[pair] = struct{}{}
		if errInteger := validateSafeJSONInteger(fmt.Sprintf("usage[%d].source_count", index), usage.SourceCount); errInteger != nil {
			return errInteger
		}
		if errInteger := validateSafeJSONInteger(fmt.Sprintf("usage[%d].source_bytes", index), usage.SourceBytes); errInteger != nil {
			return errInteger
		}
		if precision == supabaseUsagePrecisionExact {
			if usage.JSONLBytes == nil {
				return fmt.Errorf("exact usage[%d].jsonl_bytes is required", index)
			}
			if errInteger := validateSafeJSONInteger(fmt.Sprintf("usage[%d].jsonl_bytes", index), *usage.JSONLBytes); errInteger != nil {
				return errInteger
			}
		} else if usage.JSONLBytes != nil {
			return fmt.Errorf("batch-only usage[%d].jsonl_bytes must be unknown", index)
		}
		var errTotal error
		sourceCount, errTotal = addSafeJSONInteger(sourceCount, usage.SourceCount)
		if errTotal != nil {
			return fmt.Errorf("usage source_count totals exceed the safe JSON integer range")
		}
		sourceBytes, errTotal = addSafeJSONInteger(sourceBytes, usage.SourceBytes)
		if errTotal != nil {
			return fmt.Errorf("usage source_bytes totals exceed the safe JSON integer range")
		}
		if usage.JSONLBytes != nil {
			jsonlBytes, errTotal = addSafeJSONInteger(jsonlBytes, *usage.JSONLBytes)
			if errTotal != nil {
				return fmt.Errorf("usage jsonl_bytes totals exceed the safe JSON integer range")
			}
		}
	}
	if sourceCount != payload.SourceCount || sourceBytes != payload.SourceBytes {
		return fmt.Errorf("usage totals must equal batch source_count and source_bytes")
	}
	if precision == supabaseUsagePrecisionExact && jsonlBytes != payload.JSONLBytes {
		return fmt.Errorf("exact usage totals must equal batch jsonl_bytes")
	}
	return nil
}

func validateSafeJSONInteger(name string, value int64) error {
	if value < 0 || value > maxSafeJSONInteger {
		return fmt.Errorf("%s must be a nonnegative safe JSON integer", name)
	}
	return nil
}

func addSafeJSONInteger(total, value int64) (int64, error) {
	if total > maxSafeJSONInteger-value {
		return 0, fmt.Errorf("safe JSON integer overflow")
	}
	return total + value, nil
}

func sumPreparedSourceBytes(sources []preparedSource) (int64, error) {
	var total int64
	for _, source := range sources {
		if errSize := validateSafeJSONInteger("source_bytes", source.Size); errSize != nil {
			return 0, errSize
		}
		var errAdd error
		total, errAdd = addSafeJSONInteger(total, source.Size)
		if errAdd != nil {
			return 0, fmt.Errorf("source_bytes total exceeds the safe JSON integer range")
		}
	}
	return total, nil
}

func isSupabaseProvider(provider string) bool {
	return provider == providerCodex || provider == providerClaude || provider == providerGrok
}

func validateSupabaseObjectKey(objectKey string) error {
	if strings.TrimSpace(objectKey) == "" || utf8.RuneCountInString(objectKey) > 2048 {
		return fmt.Errorf("object_key must be a nonempty relative object key of at most 2048 characters")
	}
	if strings.HasPrefix(objectKey, "/") || strings.ContainsAny(objectKey, `\?#`) || uriSchemePattern.MatchString(objectKey) {
		return fmt.Errorf("object_key must be a relative object key without URI components")
	}
	for _, segment := range strings.Split(objectKey, "/") {
		if dotSegmentPattern.MatchString(segment) {
			return fmt.Errorf("object_key must not contain parent-directory segments")
		}
	}
	return nil
}

func validateSupabaseKeyName(keyName string) error {
	trimmed := strings.TrimSpace(keyName)
	if trimmed == "" || utf8.RuneCountInString(keyName) > 48 {
		return fmt.Errorf("must be nonempty and at most 48 characters")
	}
	lower := strings.ToLower(trimmed)
	if (strings.HasPrefix(lower, "sk-") && len(strings.Fields(trimmed)) == 1) ||
		(strings.HasPrefix(lower, "bearer ") && len(strings.Fields(trimmed)) >= 2) ||
		keyJWTSecretPattern.MatchString(trimmed) || strings.HasPrefix(lower, "cpa_") {
		return fmt.Errorf("must not contain secret-like data")
	}
	if keyTokenAlphabetPattern.MatchString(trimmed) {
		length := utf8.RuneCountInString(trimmed)
		if length >= 48 || (length >= 32 && keyNameTokenClasses(trimmed) >= 3) {
			return fmt.Errorf("must not contain secret-like data")
		}
	}
	return nil
}

func keyNameTokenClasses(value string) int {
	var lower bool
	var upper bool
	var digit bool
	var symbol bool
	for _, character := range value {
		switch {
		case character >= 'a' && character <= 'z':
			lower = true
		case character >= 'A' && character <= 'Z':
			upper = true
		case character >= '0' && character <= '9':
			digit = true
		case strings.ContainsRune("_+/=.-", character):
			symbol = true
		}
	}
	classes := 0
	for _, present := range []bool{lower, upper, digit, symbol} {
		if present {
			classes++
		}
	}
	return classes
}
