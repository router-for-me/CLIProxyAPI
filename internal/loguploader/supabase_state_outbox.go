package loguploader

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

const (
	supabaseOutboxSchemaVersion = 1
	supabaseOutboxStatusPending = "pending"
	supabaseOutboxStatusBlocked = "blocked"

	maxSupabaseOutboxPayloadBytes            = 4 * 1024 * 1024
	maxSupabaseOutboxEntries                 = 10_000
	maxSupabaseOutboxTotalPayloadBytes       = 64 * 1024 * 1024
	maxUploadStateBytes                int64 = 128 * 1024 * 1024
	maxSupabaseDeliveryResponseBytes         = 64 * 1024
)

var (
	errSupabaseDeliveryRetryable     = errors.New("Supabase delivery is retryable")
	errSupabaseDeliveryBlocked       = errors.New("Supabase delivery is blocked")
	errSupabaseDeliveryConfiguration = errors.New("Supabase delivery configuration error")
	errSupabaseDeliveryState         = errors.New("Supabase delivery state persistence error")
)

type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type supabaseDeliverySummary struct {
	Attempted int
	Inserted  int
	Duplicate int
	Blocked   int
	Retryable int
}

type supabaseOutboxState struct {
	SchemaVersion int                            `json:"schema_version"`
	DestinationID string                         `json:"destination_id"`
	Entries       map[string]supabaseOutboxEntry `json:"entries"`
}

type supabaseOutboxEntry struct {
	EventID       string    `json:"event_id"`
	HourKey       string    `json:"hour_key"`
	ObjectKey     string    `json:"object_key"`
	Status        string    `json:"status"`
	Payload       []byte    `json:"payload"`
	PayloadSHA256 string    `json:"payload_sha256"`
	EnqueuedAt    time.Time `json:"enqueued_at"`
	BlockCategory string    `json:"block_category,omitempty"`
	BlockStatus   int       `json:"block_status,omitempty"`
}

type supabaseHistoryCheckpoint struct {
	DestinationID string    `json:"destination_id"`
	ObjectKey     string    `json:"object_key"`
	ArchiveSHA256 string    `json:"archive_sha256"`
	EventID       string    `json:"event_id"`
	CommittedAt   time.Time `json:"committed_at"`
}

func (s *Service) drainSupabaseOutbox(ctx context.Context, state *uploadState) (supabaseDeliverySummary, error) {
	return s.drainSupabaseOutboxWithPreferredEvent(ctx, state, "")
}

func (s *Service) drainSupabaseOutboxWithPreferredEvent(ctx context.Context, state *uploadState, preferredEventID string) (supabaseDeliverySummary, error) {
	var summary supabaseDeliverySummary
	if !s.cfg.Supabase.Enabled {
		return summary, nil
	}

	type deliveryEntry struct {
		mapKey string
		entry  supabaseOutboxEntry
	}
	entries := make([]deliveryEntry, 0, len(state.SupabaseOutbox.Entries))
	for mapKey, entry := range state.SupabaseOutbox.Entries {
		if entry.Status == supabaseOutboxStatusPending || entry.Status == supabaseOutboxStatusBlocked {
			entries = append(entries, deliveryEntry{mapKey: mapKey, entry: entry})
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].entry.HourKey != entries[j].entry.HourKey {
			return entries[i].entry.HourKey < entries[j].entry.HourKey
		}
		return entries[i].entry.EventID < entries[j].entry.EventID
	})
	if preferredEventID != "" {
		preferred := entries[:0]
		for index := range entries {
			if entries[index].entry.EventID != preferredEventID {
				continue
			}
			preferred = append(preferred, entries[index])
			break
		}
		entries = preferred
	}

	doer := s.supabaseHTTPDoer
	if doer == nil {
		doer = newSupabaseHTTPClient()
	}
	for _, deliveryEntry := range entries {
		select {
		case <-ctx.Done():
			return summary, supabaseDeliveryError(summary, errSupabaseDeliveryRetryable)
		default:
		}
		token := strings.TrimSpace(os.Getenv(s.cfg.Supabase.IngestTokenEnv))
		if token == "" {
			return summary, supabaseDeliveryError(summary, errSupabaseDeliveryConfiguration)
		}
		request, errRequest := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.Supabase.IngestURL, bytes.NewReader(deliveryEntry.entry.Payload))
		if errRequest != nil {
			return summary, supabaseDeliveryError(summary, errSupabaseDeliveryConfiguration)
		}
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "application/json")
		request.Header["User-Agent"] = nil

		summary.Attempted++
		response, errDo := doer.Do(request)
		if ctx.Err() != nil {
			closeSupabaseDeliveryResponse(response)
			summary.Retryable++
			return summary, supabaseDeliveryError(summary, nil)
		}
		if errDo != nil {
			closeSupabaseDeliveryResponse(response)
			summary.Retryable++
			continue
		}
		if response == nil {
			summary.Retryable++
			continue
		}
		if response.StatusCode != http.StatusOK {
			closeSupabaseDeliveryResponse(response)
		}

		switch response.StatusCode {
		case http.StatusOK:
			if response.Body == nil {
				summary.Retryable++
				continue
			}
			body, oversized, bodyOK := readAndCloseSupabaseDeliveryResponse(response.Body)
			if !bodyOK {
				summary.Retryable++
				continue
			}
			status, eventID, ok := decodeSupabaseDeliveryAcknowledgement(body)
			if !ok || eventID != deliveryEntry.entry.EventID || oversized {
				summary.Retryable++
				continue
			}
			delete(state.SupabaseOutbox.Entries, deliveryEntry.mapKey)
			published, errSave := s.saveStateWithResult(*state)
			if errSave != nil {
				if !published {
					state.SupabaseOutbox.Entries[deliveryEntry.mapKey] = deliveryEntry.entry
				}
				return summary, supabaseDeliveryError(summary, errSupabaseDeliveryState)
			}
			if status == "inserted" {
				summary.Inserted++
			} else {
				summary.Duplicate++
			}
		case http.StatusBadRequest, http.StatusConflict, http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity:
			blocked := deliveryEntry.entry
			blocked.Status = supabaseOutboxStatusBlocked
			blocked.BlockStatus = response.StatusCode
			switch response.StatusCode {
			case http.StatusBadRequest:
				blocked.BlockCategory = "invalid-request"
			case http.StatusConflict:
				blocked.BlockCategory = "conflict"
			case http.StatusRequestEntityTooLarge:
				blocked.BlockCategory = "payload-too-large"
			case http.StatusUnprocessableEntity:
				blocked.BlockCategory = "unprocessable"
			}
			state.SupabaseOutbox.Entries[deliveryEntry.mapKey] = blocked
			published, errSave := s.saveStateWithResult(*state)
			if errSave != nil {
				if !published {
					state.SupabaseOutbox.Entries[deliveryEntry.mapKey] = deliveryEntry.entry
				}
				return summary, supabaseDeliveryError(summary, errSupabaseDeliveryState)
			}
			summary.Blocked++
		case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusMethodNotAllowed:
			return summary, supabaseDeliveryError(summary, errSupabaseDeliveryConfiguration)
		default:
			summary.Retryable++
		}
	}
	return summary, supabaseDeliveryError(summary, nil)
}

func supabaseDeliveryError(summary supabaseDeliverySummary, terminal error) error {
	deliveryErrors := make([]error, 0, 3)
	if summary.Blocked > 0 {
		deliveryErrors = append(deliveryErrors, errSupabaseDeliveryBlocked)
	}
	if summary.Retryable > 0 {
		deliveryErrors = append(deliveryErrors, errSupabaseDeliveryRetryable)
	}
	if terminal != nil {
		alreadyIncluded := terminal == errSupabaseDeliveryBlocked && summary.Blocked > 0 ||
			terminal == errSupabaseDeliveryRetryable && summary.Retryable > 0
		if !alreadyIncluded {
			deliveryErrors = append(deliveryErrors, terminal)
		}
	}
	return errors.Join(deliveryErrors...)
}

func closeSupabaseDeliveryResponse(response *http.Response) {
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
}

func readAndCloseSupabaseDeliveryResponse(body io.ReadCloser) (raw []byte, oversized, ok bool) {
	raw, errRead := io.ReadAll(io.LimitReader(body, maxSupabaseDeliveryResponseBytes+1))
	errClose := body.Close()
	if errRead != nil || errClose != nil {
		return nil, false, false
	}
	return raw, len(raw) > maxSupabaseDeliveryResponseBytes, true
}

func newSupabaseHTTPClient() *http.Client {
	dialer := &net.Dialer{}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		DisableCompression:    true,
		TLSHandshakeTimeout:   0,
		ResponseHeaderTimeout: 0,
		IdleConnTimeout:       0,
		ExpectContinueTimeout: 0,
	}
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Timeout: 0,
	}
}

func decodeSupabaseDeliveryAcknowledgement(raw []byte) (status, eventID string, ok bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	opening, errOpening := decoder.Token()
	if errOpening != nil || opening != json.Delim('{') {
		return "", "", false
	}
	seen := make(map[string]struct{}, 2)
	for decoder.More() {
		keyToken, errKey := decoder.Token()
		key, keyOK := keyToken.(string)
		if errKey != nil || !keyOK {
			return "", "", false
		}
		if _, duplicate := seen[key]; duplicate {
			return "", "", false
		}
		seen[key] = struct{}{}
		switch key {
		case "status":
			if errStatus := decoder.Decode(&status); errStatus != nil {
				return "", "", false
			}
		case "event_id":
			if errEventID := decoder.Decode(&eventID); errEventID != nil {
				return "", "", false
			}
		default:
			return "", "", false
		}
	}
	closing, errClosing := decoder.Token()
	if errClosing != nil || closing != json.Delim('}') {
		return "", "", false
	}
	if errTrailing := decoder.Decode(&struct{}{}); errTrailing != io.EOF {
		return "", "", false
	}
	if len(seen) != 2 || eventID == "" || (status != "inserted" && status != "duplicate") {
		return "", "", false
	}
	return status, eventID, true
}

func newSupabaseOutboxState(service *Service) supabaseOutboxState {
	destinationID := ""
	if service.cfg.Supabase.Enabled && strings.TrimSpace(service.cfg.Supabase.IngestURL) != "" {
		destinationID, _ = supabaseDestinationID(service.cfg.Supabase.IngestURL)
	}
	return supabaseOutboxState{
		SchemaVersion: supabaseOutboxSchemaVersion,
		DestinationID: destinationID,
		Entries:       make(map[string]supabaseOutboxEntry),
	}
}

func (state supabaseOutboxState) empty() bool {
	return state.SchemaVersion == 0 && state.DestinationID == "" && len(state.Entries) == 0
}

func (s *Service) validateSupabaseOutboxState(state *uploadState) error {
	outbox := &state.SupabaseOutbox
	if outbox.SchemaVersion != supabaseOutboxSchemaVersion {
		return fmt.Errorf("unsupported Supabase outbox schema version")
	}
	if outbox.Entries == nil {
		outbox.Entries = make(map[string]supabaseOutboxEntry)
		state.dirty = true
	}
	if outbox.DestinationID != "" && !isLowercaseSHA256(outbox.DestinationID) {
		return fmt.Errorf("Supabase outbox has an invalid destination identity")
	}
	if len(outbox.Entries) != 0 && outbox.DestinationID == "" {
		return fmt.Errorf("non-empty Supabase outbox has an invalid destination identity")
	}
	configuredDestinationID := ""
	if s.cfg.Supabase.Enabled && strings.TrimSpace(s.cfg.Supabase.IngestURL) != "" {
		destinationID, errDestination := supabaseDestinationID(s.cfg.Supabase.IngestURL)
		if errDestination != nil {
			return fmt.Errorf("configured Supabase outbox destination is invalid")
		}
		configuredDestinationID = destinationID
	}

	for _, entry := range outbox.Entries {
		if errPayloadSize := validateSupabaseOutboxPayloadSize(int64(len(entry.Payload))); errPayloadSize != nil {
			return fmt.Errorf("Supabase outbox capacity validation failed: %w", errPayloadSize)
		}
	}
	if _, _, errCapacity := supabaseOutboxActiveCapacity(outbox.Entries); errCapacity != nil {
		return fmt.Errorf("Supabase outbox capacity validation failed: %w", errCapacity)
	}
	for mapKey, entry := range outbox.Entries {
		if errEntry := s.validateSupabaseOutboxEntry(state, mapKey, entry); errEntry != nil {
			return fmt.Errorf("invalid Supabase outbox entry: %w", errEntry)
		}
	}
	for mapKey, checkpoint := range state.SupabaseHistory {
		if !isLowercaseSHA256(checkpoint.DestinationID) || mapKey != supabaseHistoryCheckpointKey(checkpoint.DestinationID, checkpoint.ObjectKey) {
			return fmt.Errorf("Supabase history map key does not match its destination and object")
		}
		object, exists := state.Objects[checkpoint.ObjectKey]
		if !exists || checkpoint.ArchiveSHA256 != object.ArchiveSHA256 {
			return fmt.Errorf("Supabase history checkpoint does not match a trusted uploaded object")
		}
		if !supabaseEventIDPattern.MatchString(checkpoint.EventID) {
			return fmt.Errorf("Supabase history checkpoint has an invalid event ID")
		}
		if checkpoint.CommittedAt.IsZero() {
			return fmt.Errorf("Supabase history checkpoint has an empty commit time")
		}
	}
	if configuredDestinationID != "" && outbox.DestinationID != configuredDestinationID {
		outbox.DestinationID = configuredDestinationID
		state.dirty = true
	}
	return nil
}

func isLowercaseSHA256(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, errDecode := hex.DecodeString(value)
	return errDecode == nil
}

func (s *Service) validateSupabaseOutboxEntry(state *uploadState, mapKey string, entry supabaseOutboxEntry) error {
	if mapKey == "" || entry.EventID != mapKey {
		return fmt.Errorf("map key does not match entry event ID")
	}
	if !supabaseEventIDPattern.MatchString(entry.EventID) {
		return fmt.Errorf("event ID must use the cliproxy-v1.<sha256> format")
	}
	if len(entry.PayloadSHA256) != sha256.Size*2 || strings.ToLower(entry.PayloadSHA256) != entry.PayloadSHA256 {
		return fmt.Errorf("payload checksum must be 64 lowercase hexadecimal characters")
	}
	decodedPayloadSHA256, errDecodeSHA := hex.DecodeString(entry.PayloadSHA256)
	if errDecodeSHA != nil {
		return fmt.Errorf("payload checksum must be hexadecimal")
	}
	payloadSHA256 := sha256.Sum256(entry.Payload)
	if !bytes.Equal(decodedPayloadSHA256, payloadSHA256[:]) {
		return fmt.Errorf("payload checksum does not match exact payload bytes")
	}
	if entry.EnqueuedAt.IsZero() {
		return fmt.Errorf("enqueue time must be nonzero")
	}
	switch entry.Status {
	case supabaseOutboxStatusPending:
		if entry.BlockCategory != "" || entry.BlockStatus != 0 {
			return fmt.Errorf("pending entry must not contain blocked metadata")
		}
	case supabaseOutboxStatusBlocked:
		if !validSupabaseBlockMetadata(entry.BlockCategory, entry.BlockStatus) {
			return fmt.Errorf("blocked entry has invalid category or HTTP status")
		}
	default:
		return fmt.Errorf("unsupported status")
	}

	payload, errPayload := decodeSupabaseOutboxPayload(entry.Payload)
	if errPayload != nil {
		return errPayload
	}
	if errContract := validateSupabaseEventPayload(payload); errContract != nil {
		return fmt.Errorf("payload violates Supabase event contract: %w", errContract)
	}
	if payload.EventID != entry.EventID {
		return fmt.Errorf("payload event_id does not match entry event ID")
	}
	hour, exists := state.Hours[entry.HourKey]
	if !exists || hour.Status != "sealed" {
		return fmt.Errorf("hour key does not reference a sealed hour")
	}
	providerSeparator := strings.LastIndex(entry.HourKey, ":")
	if providerSeparator <= 0 {
		return fmt.Errorf("hour key does not identify an event provider")
	}
	provider := entry.HourKey[providerSeparator+1:]
	if !isSupabaseProvider(provider) {
		return fmt.Errorf("hour key has an unsupported event provider")
	}
	precision := payload.UsagePrecision
	if precision == "" {
		precision = supabaseUsagePrecisionExact
	}
	if precision == supabaseUsagePrecisionExact {
		for _, usage := range payload.Usage {
			if usage.Provider != provider {
				return fmt.Errorf("payload usage provider does not match its hour key")
			}
		}
		if payload.EventID != supabaseEventID(payload, provider) {
			return fmt.Errorf("payload does not have its deterministic event ID")
		}
	} else if payload.EventID != supabaseHistoryEventID(payload) {
		return fmt.Errorf("history payload does not have its deterministic event ID")
	}
	object, exists := state.Objects[hour.ObjectKey]
	if !exists {
		return fmt.Errorf("sealed hour references a missing uploaded object")
	}
	if entry.ObjectKey != hour.ObjectKey || payload.ObjectKey != entry.ObjectKey || object.ObjectKey != entry.ObjectKey {
		return fmt.Errorf("entry, payload, sealed hour, and uploaded object key do not match")
	}
	if payload.TargetID != "tos:"+state.Target.ID {
		return fmt.Errorf("payload target identity does not match upload state")
	}
	if payload.ArchiveSHA256 != hour.ArchiveSHA256 || payload.ArchiveSHA256 != object.ArchiveSHA256 {
		return fmt.Errorf("payload archive checksum does not match sealed state")
	}
	if payload.ManifestSHA256 != hour.ManifestSHA256 {
		return fmt.Errorf("payload manifest checksum does not match sealed state")
	}
	if payload.CompressedBytes != object.CompressedSize {
		return fmt.Errorf("payload compressed bytes do not match uploaded object")
	}
	if payload.Timezone != state.Policy.Timezone {
		return fmt.Errorf("payload timezone does not match upload policy")
	}
	hourStart, errHourStart := time.Parse(time.RFC3339Nano, payload.HourStart)
	if errHourStart != nil {
		return fmt.Errorf("payload hour does not use a valid RFC3339 timestamp")
	}
	if errBoundary := s.validatePreparedHourBoundary(hourStart); errBoundary != nil {
		return fmt.Errorf("payload hour: %w", errBoundary)
	}
	canonicalHourStart := hourStart.In(s.location)
	if canonicalHourStart.Format(time.RFC3339) != payload.HourStart || hourStateKey(canonicalHourStart, provider) != entry.HourKey {
		return fmt.Errorf("payload hour does not match outbox hour key")
	}
	return nil
}

func decodeSupabaseOutboxPayload(raw []byte) (supabaseEventPayload, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var payload supabaseEventPayload
	if errDecode := decoder.Decode(&payload); errDecode != nil {
		return supabaseEventPayload{}, fmt.Errorf("strictly decode payload: invalid JSON object")
	}
	if errTrailing := decoder.Decode(&struct{}{}); errTrailing != io.EOF {
		return supabaseEventPayload{}, fmt.Errorf("strictly decode payload: expected exactly one JSON object")
	}
	return payload, nil
}

func validSupabaseBlockMetadata(category string, status int) bool {
	switch status {
	case 400:
		return category == "invalid-request"
	case 409:
		return category == "conflict"
	case 413:
		return category == "payload-too-large"
	case 422:
		return category == "unprocessable"
	default:
		return false
	}
}

func validateSupabaseOutboxCapacity(activeEntries int, totalPayloadBytes, nextPayloadBytes int64) error {
	if errPayloadSize := validateSupabaseOutboxPayloadSize(nextPayloadBytes); errPayloadSize != nil {
		return errPayloadSize
	}
	if activeEntries < 0 || activeEntries >= maxSupabaseOutboxEntries {
		return fmt.Errorf("active entries exceed the 10,000 entry limit")
	}
	if totalPayloadBytes < 0 || totalPayloadBytes > maxSupabaseOutboxTotalPayloadBytes-nextPayloadBytes {
		return fmt.Errorf("total payload bytes exceed the 64 MiB limit")
	}
	return nil
}

func validateSupabaseOutboxPayloadSize(payloadBytes int64) error {
	if payloadBytes < 0 || payloadBytes > maxSupabaseOutboxPayloadBytes {
		return fmt.Errorf("single payload exceeds the 4 MiB limit")
	}
	return nil
}

func supabaseOutboxActiveCapacity(entries map[string]supabaseOutboxEntry) (activeEntries int, totalPayloadBytes int64, capacityErr error) {
	for _, entry := range entries {
		if entry.Status != supabaseOutboxStatusPending {
			continue
		}
		if errCapacity := validateSupabaseOutboxCapacity(activeEntries, totalPayloadBytes, int64(len(entry.Payload))); errCapacity != nil {
			return 0, 0, errCapacity
		}
		activeEntries++
		totalPayloadBytes += int64(len(entry.Payload))
	}
	return activeEntries, totalPayloadBytes, nil
}

func supabaseDestinationID(rawURL string) (string, error) {
	normalized, errNormalize := normalizeSupabaseIngestURL(rawURL)
	if errNormalize != nil {
		return "", errNormalize
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(normalized))), nil
}

func normalizeSupabaseIngestURL(rawURL string) (string, error) {
	config := SupabaseConfig{Enabled: true, IngestURL: rawURL, IngestTokenEnv: "unused"}
	if errValidate := config.validate(); errValidate != nil {
		return "", errValidate
	}
	parsedURL, errParse := url.Parse(config.IngestURL)
	if errParse != nil {
		return "", fmt.Errorf("parse normalized Supabase ingest URL: %w", errParse)
	}
	hostname := strings.ToLower(parsedURL.Hostname())
	host := hostname
	if port := parsedURL.Port(); port != "" && port != "443" {
		host = net.JoinHostPort(hostname, port)
	} else if strings.Contains(hostname, ":") {
		host = "[" + hostname + "]"
	}
	return (&url.URL{
		Scheme: strings.ToLower(parsedURL.Scheme),
		Host:   host,
		Path:   parsedURL.Path,
	}).String(), nil
}
