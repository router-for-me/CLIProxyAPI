package auth

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executionregistry"
	log "github.com/sirupsen/logrus"
)

// HomeInFlightTransport publishes in-flight observation frames for one Home lifetime.
type HomeInFlightTransport interface {
	HeartbeatOK() bool
	LPushInFlightSnapshot(context.Context, []byte) error
}

// HomeInFlightPublisherConfig bounds in-flight observation frames.
type HomeInFlightPublisherConfig struct {
	SnapshotInterval   time.Duration
	MaxPartBytes       int
	MaxPartCount       int
	MaxRevisionBytes   int
	MaxAggregateGroups int
	MaxDetails         int
	MaxStringBytes     int
}

type homeInFlightAggregateKey struct {
	CredentialID string
	Model        string
	Accounted    bool
}

func homeInFlightStatus(accounted bool) home.InFlightAccountedStatus {
	if accounted {
		return home.InFlightAccounted
	}
	return home.InFlightUnaccounted
}

// HomeInFlightPublisherConfigFromConfig converts validated runtime config into publisher bounds.
func HomeInFlightPublisherConfigFromConfig(cfg internalconfig.CredentialInFlightConfig) (HomeInFlightPublisherConfig, error) {
	snapshotInterval, _, _, errDurations := cfg.Durations()
	if errDurations != nil {
		return HomeInFlightPublisherConfig{}, errDurations
	}
	if errValidate := cfg.Validate(); errValidate != nil {
		return HomeInFlightPublisherConfig{}, errValidate
	}
	return HomeInFlightPublisherConfig{
		SnapshotInterval:   snapshotInterval,
		MaxPartBytes:       cfg.MaxPartBytes,
		MaxPartCount:       cfg.MaxPartCount,
		MaxRevisionBytes:   cfg.MaxRevisionBytes,
		MaxAggregateGroups: cfg.MaxAggregateGroups,
		MaxDetails:         cfg.MaxDetails,
		MaxStringBytes:     cfg.MaxStringBytes,
	}, nil
}

// ApplyHomeInFlightPublisherConfig stores an immutable validated publisher config snapshot.
func (m *Manager) ApplyHomeInFlightPublisherConfig(cfg HomeInFlightPublisherConfig) {
	if m == nil || !validHomeInFlightPublisherConfig(cfg) {
		return
	}
	snapshot := cfg
	m.homeInFlightPublisherConfig.Store(&snapshot)
}

// HomeInFlightPublisherConfig returns the current immutable publisher config snapshot.
func (m *Manager) HomeInFlightPublisherConfig() HomeInFlightPublisherConfig {
	if m == nil {
		return HomeInFlightPublisherConfig{}
	}
	cfg := m.homeInFlightPublisherConfig.Load()
	if cfg == nil {
		return HomeInFlightPublisherConfig{}
	}
	return *cfg
}

func validHomeInFlightPublisherConfig(cfg HomeInFlightPublisherConfig) bool {
	if cfg.SnapshotInterval <= 0 || cfg.MaxPartBytes < 1024 || cfg.MaxPartCount <= 0 || cfg.MaxPartCount > internalconfig.DefaultInFlightMaxPartCount ||
		cfg.MaxRevisionBytes < cfg.MaxPartBytes || cfg.MaxRevisionBytes > internalconfig.DefaultInFlightMaxRevisionBytes ||
		cfg.MaxAggregateGroups <= 0 || cfg.MaxAggregateGroups > internalconfig.DefaultInFlightMaxAggregateGroups ||
		cfg.MaxDetails < 0 || cfg.MaxDetails > internalconfig.DefaultInFlightMaxDetails ||
		cfg.MaxStringBytes <= 0 || cfg.MaxStringBytes > internalconfig.DefaultInFlightMaxStringBytes {
		return false
	}
	return (cfg.MaxRevisionBytes+cfg.MaxPartBytes-1)/cfg.MaxPartBytes <= cfg.MaxPartCount
}

func validHomeInFlightPublisherBounds(cfg HomeInFlightPublisherConfig) bool {
	return cfg.MaxPartBytes > 0 && cfg.MaxPartCount > 0 && cfg.MaxRevisionBytes >= cfg.MaxPartBytes &&
		cfg.MaxAggregateGroups > 0 && cfg.MaxDetails >= 0 && cfg.MaxStringBytes > 0
}

// StartHomeInFlightPublisher publishes periodic snapshots for the supplied lifetime registry.
func (m *Manager) StartHomeInFlightPublisher(ctx context.Context, transport HomeInFlightTransport, registry *executionregistry.Registry) {
	if m == nil || transport == nil || registry == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}

	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case observedAt := <-timer.C:
			cfg := m.HomeInFlightPublisherConfig()
			interval := cfg.SnapshotInterval
			if interval <= 0 {
				interval = 2 * time.Second
			}
			timer.Reset(interval)
			if !transport.HeartbeatOK() {
				continue
			}
			freeze := registry.FreezeInFlight(observedAt.UTC())
			frames := encodeHomeInFlightFreeze(freeze, observedAt.UTC(), cfg)
			for index := range frames {
				raw, errMarshal := json.Marshal(frames[index])
				if errMarshal != nil {
					log.Warn("failed to encode in-flight snapshot frame")
					break
				}
				if errPush := transport.LPushInFlightSnapshot(ctx, raw); errPush != nil {
					log.Warn("failed to publish in-flight snapshot frame")
					break
				}
			}
		}
	}
}

func encodeHomeInFlightFreeze(freeze executionregistry.Freeze, observedAt time.Time, cfg HomeInFlightPublisherConfig) []home.InFlightSnapshotFrame {
	observedAt = observedAt.UTC()
	aggregateCounts := make(map[homeInFlightAggregateKey]int64, len(freeze.Executions))
	aggregateKeysValid := true
	for _, observation := range freeze.Executions {
		key := homeInFlightAggregateKey{
			CredentialID: observation.CredentialID,
			Model:        homeInFlightObservationModel(observation),
			Accounted:    observation.Accounted,
		}
		if len(key.CredentialID) > cfg.MaxStringBytes || len(key.Model) > cfg.MaxStringBytes {
			aggregateKeysValid = false
		}
		aggregateCounts[key]++
	}
	aggregates := make([]home.InFlightAggregate, 0, len(aggregateCounts))
	for key, count := range aggregateCounts {
		aggregates = append(aggregates, home.InFlightAggregate{
			CredentialID: key.CredentialID,
			Model:        key.Model,
			Status:       homeInFlightStatus(key.Accounted),
			Count:        count,
		})
	}
	sort.Slice(aggregates, func(left, right int) bool {
		if aggregates[left].CredentialID != aggregates[right].CredentialID {
			return aggregates[left].CredentialID < aggregates[right].CredentialID
		}
		if aggregates[left].Model != aggregates[right].Model {
			return aggregates[left].Model < aggregates[right].Model
		}
		return aggregates[left].Status < aggregates[right].Status
	})
	if !validHomeInFlightPublisherBounds(cfg) || !aggregateKeysValid || len(aggregates) > cfg.MaxAggregateGroups {
		return homeInFlightOverflow(freeze, observedAt, len(aggregates))
	}

	details := make([]home.InFlightRequestDetail, 0, len(freeze.Executions))
	detailsTruncated := false
	for _, observation := range freeze.Executions {
		detail, bounded := homeInFlightBoundDetail(home.InFlightRequestDetail{
			RequestID:    observation.RequestID,
			CredentialID: observation.CredentialID,
			Model:        homeInFlightObservationModel(observation),
			RequestKind:  observation.RequestKind,
			StartedAt:    observation.StartedAt.UTC(),
		}, cfg.MaxStringBytes)
		if !validHomeInFlightDetail(detail, cfg.MaxStringBytes) {
			detailsTruncated = true
			continue
		}
		detailsTruncated = detailsTruncated || bounded
		details = append(details, detail)
	}
	sort.Slice(details, func(left, right int) bool {
		if !details[left].StartedAt.Equal(details[right].StartedAt) {
			return details[left].StartedAt.Before(details[right].StartedAt)
		}
		if details[left].RequestID != details[right].RequestID {
			return details[left].RequestID < details[right].RequestID
		}
		if details[left].CredentialID != details[right].CredentialID {
			return details[left].CredentialID < details[right].CredentialID
		}
		if details[left].Model != details[right].Model {
			return details[left].Model < details[right].Model
		}
		return details[left].RequestKind < details[right].RequestKind
	})

	if len(details) > cfg.MaxDetails {
		details = details[:cfg.MaxDetails]
		detailsTruncated = true
	}

	for {
		frames, aggregatesPacked, includedDetails := packHomeInFlightFrames(freeze, observedAt, cfg, aggregates, details, detailsTruncated)
		if !aggregatesPacked {
			return homeInFlightOverflow(freeze, observedAt, len(aggregates))
		}
		if includedDetails < len(details) {
			details = details[:includedDetails]
			detailsTruncated = true
			continue
		}
		if homeInFlightFramesWithinBounds(frames, cfg) {
			return frames
		}
		if len(details) == 0 {
			return homeInFlightOverflow(freeze, observedAt, len(aggregates))
		}
		details = details[:len(details)-1]
		detailsTruncated = true
	}
}

func homeInFlightObservationModel(observation executionregistry.Observation) string {
	if observation.Accounted {
		return observation.Model
	}
	if model, valid := validCanonicalHomeConcurrencyModelKey(observation.Model); valid {
		return model
	}
	return "unknown"
}

func validHomeInFlightDetail(detail home.InFlightRequestDetail, maxStringBytes int) bool {
	validString := func(value string) bool {
		return utf8.ValidString(value) && strings.TrimSpace(value) != "" && len(value) <= maxStringBytes
	}
	return validString(detail.RequestID) && validString(detail.CredentialID) && validString(detail.Model) && validString(detail.RequestKind) && !detail.StartedAt.IsZero() && detail.StartedAt.Location() == time.UTC
}

func homeInFlightBoundDetail(detail home.InFlightRequestDetail, maxBytes int) (home.InFlightRequestDetail, bool) {
	truncated := false
	bound := func(value string) string {
		bounded := homeInFlightTruncateString(value, maxBytes)
		truncated = truncated || bounded != value
		return bounded
	}
	detail.RequestID = bound(detail.RequestID)
	detail.CredentialID = bound(detail.CredentialID)
	detail.Model = bound(detail.Model)
	detail.RequestKind = bound(detail.RequestKind)
	return detail, truncated
}

func homeInFlightTruncateString(value string, maxBytes int) string {
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func packHomeInFlightFrames(freeze executionregistry.Freeze, observedAt time.Time, cfg HomeInFlightPublisherConfig, aggregates []home.InFlightAggregate, details []home.InFlightRequestDetail, detailsTruncated bool) ([]home.InFlightSnapshotFrame, bool, int) {
	frames := make([]home.InFlightSnapshotFrame, 0, cfg.MaxPartCount)
	current := homeInFlightPartFrame(freeze, observedAt, cfg.MaxPartCount, detailsTruncated)
	appendCurrent := func() bool {
		if len(frames) >= cfg.MaxPartCount {
			return false
		}
		frames = append(frames, current)
		current = homeInFlightPartFrame(freeze, observedAt, cfg.MaxPartCount, detailsTruncated)
		return true
	}
	for _, aggregate := range aggregates {
		candidate := current
		candidate.Aggregates = append(candidate.Aggregates, aggregate)
		if homeInFlightFrameWithinPartLimit(candidate, cfg.MaxPartBytes) {
			current = candidate
			continue
		}
		if len(current.Aggregates) == 0 && len(current.Details) == 0 {
			return nil, false, 0
		}
		if !appendCurrent() {
			return nil, false, 0
		}
		candidate = current
		candidate.Aggregates = append(candidate.Aggregates, aggregate)
		if !homeInFlightFrameWithinPartLimit(candidate, cfg.MaxPartBytes) {
			return nil, false, 0
		}
		current = candidate
	}

	includedDetails := 0
	for _, detail := range details {
		candidate := current
		candidate.Details = append(candidate.Details, detail)
		if homeInFlightFrameWithinPartLimit(candidate, cfg.MaxPartBytes) {
			current = candidate
			includedDetails++
			continue
		}
		if len(current.Aggregates) == 0 && len(current.Details) == 0 {
			return frames, true, includedDetails
		}
		if !appendCurrent() {
			return frames, true, includedDetails - len(current.Details)
		}
		candidate = current
		candidate.Details = append(candidate.Details, detail)
		if !homeInFlightFrameWithinPartLimit(candidate, cfg.MaxPartBytes) {
			return frames, true, includedDetails
		}
		current = candidate
		includedDetails++
	}
	if len(current.Aggregates) != 0 || len(current.Details) != 0 || len(frames) == 0 {
		if !appendCurrent() {
			if len(current.Aggregates) != 0 {
				return nil, false, 0
			}
			return frames, true, includedDetails - len(current.Details)
		}
	}
	for index := range frames {
		partIndex, partCount := index, len(frames)
		frames[index].PartIndex = &partIndex
		frames[index].PartCount = &partCount
	}
	return frames, true, includedDetails
}

func homeInFlightPartFrame(freeze executionregistry.Freeze, observedAt time.Time, partCount int, detailsTruncated bool) home.InFlightSnapshotFrame {
	partIndex := 0
	return home.InFlightSnapshotFrame{
		Kind:             home.InFlightFramePart,
		Revision:         freeze.Revision,
		ObservedAt:       observedAt,
		BarrierRevision:  freeze.BarrierRevision,
		PartIndex:        &partIndex,
		PartCount:        &partCount,
		DetailsTruncated: detailsTruncated,
	}
}

func homeInFlightFrameWithinPartLimit(frame home.InFlightSnapshotFrame, maxPartBytes int) bool {
	raw, errMarshal := json.Marshal(frame)
	return errMarshal == nil && len(raw) <= maxPartBytes
}

func homeInFlightFramesWithinBounds(frames []home.InFlightSnapshotFrame, cfg HomeInFlightPublisherConfig) bool {
	if len(frames) == 0 || len(frames) > cfg.MaxPartCount {
		return false
	}
	totalBytes := 0
	for _, frame := range frames {
		raw, errMarshal := json.Marshal(frame)
		if errMarshal != nil || len(raw) > cfg.MaxPartBytes {
			return false
		}
		totalBytes += len(raw)
		if totalBytes > cfg.MaxRevisionBytes {
			return false
		}
	}
	return true
}

func homeInFlightOverflow(freeze executionregistry.Freeze, observedAt time.Time, aggregateGroupCount int) []home.InFlightSnapshotFrame {
	return []home.InFlightSnapshotFrame{{
		Kind:                home.InFlightFrameOverflow,
		Revision:            freeze.Revision,
		ObservedAt:          observedAt,
		BarrierRevision:     freeze.BarrierRevision,
		AggregateGroupCount: aggregateGroupCount,
	}}
}
