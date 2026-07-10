package usagestats

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

const (
	defaultMaxEvents = 10000
	defaultMaxAge    = 90 * 24 * time.Hour

	// DefaultDays is the default number of calendar days returned by a report.
	DefaultDays = 7
	// MaxDays is the largest report window accepted by the management API.
	MaxDays = 90
	// DefaultModelLimit is the default number of model rows returned by a report.
	DefaultModelLimit = 10
	// MaxModelLimit is the largest model row limit accepted by the management API.
	MaxModelLimit = 100
	// DefaultRecentLimit is the default number of recent requests returned by a report.
	DefaultRecentLimit = 20
	// MaxRecentLimit is the largest recent request limit accepted by the management API.
	MaxRecentLimit = 200
)

var defaultStore = NewStore(defaultMaxEvents, defaultMaxAge)

func init() {
	coreusage.RegisterNamedPlugin("provider-statistics", defaultStore)
}

// Store keeps a bounded in-memory history of sanitized usage events.
type Store struct {
	mu        sync.RWMutex
	events    []Event
	maxEvents int
	maxAge    time.Duration
}

// QueryOptions controls report filtering and result sizes.
type QueryOptions struct {
	Days        int
	Provider    string
	ModelLimit  int
	RecentLimit int
}

// Report is the provider statistics response returned to management clients.
type Report struct {
	GeneratedAt time.Time      `json:"generated_at"`
	Range       ReportRange    `json:"range"`
	Summary     Summary        `json:"summary"`
	Providers   []ProviderStat `json:"providers"`
	Models      []ModelStat    `json:"models"`
	Trend       []DailyStat    `json:"trend"`
	Recent      []Event        `json:"recent"`
}

// ReportRange describes the time and provider filters applied to a report.
type ReportRange struct {
	From     time.Time `json:"from"`
	To       time.Time `json:"to"`
	Days     int       `json:"days"`
	Provider string    `json:"provider,omitempty"`
}

// Summary contains totals for the selected report range.
type Summary struct {
	Requests         int64       `json:"requests"`
	Successful       int64       `json:"successful"`
	Failed           int64       `json:"failed"`
	SuccessRate      float64     `json:"success_rate"`
	ActiveProviders  int         `json:"active_providers"`
	ActiveModels     int         `json:"active_models"`
	AverageLatencyMs float64     `json:"average_latency_ms"`
	Tokens           TokenTotals `json:"tokens"`
}

// ProviderStat contains totals for one upstream provider.
type ProviderStat struct {
	Provider         string      `json:"provider"`
	Requests         int64       `json:"requests"`
	Successful       int64       `json:"successful"`
	Failed           int64       `json:"failed"`
	SuccessRate      float64     `json:"success_rate"`
	ActiveModels     int         `json:"active_models"`
	AverageLatencyMs float64     `json:"average_latency_ms"`
	Tokens           TokenTotals `json:"tokens"`
}

// ModelStat contains totals for one provider and model pair.
type ModelStat struct {
	Provider         string      `json:"provider"`
	Model            string      `json:"model"`
	Requests         int64       `json:"requests"`
	Successful       int64       `json:"successful"`
	Failed           int64       `json:"failed"`
	SuccessRate      float64     `json:"success_rate"`
	AverageLatencyMs float64     `json:"average_latency_ms"`
	Tokens           TokenTotals `json:"tokens"`
}

// DailyStat contains one calendar day's totals.
type DailyStat struct {
	Date       string      `json:"date"`
	Requests   int64       `json:"requests"`
	Successful int64       `json:"successful"`
	Failed     int64       `json:"failed"`
	Tokens     TokenTotals `json:"tokens"`
}

// TokenTotals contains the token usage breakdown used across report sections.
type TokenTotals struct {
	InputTokens         int64 `json:"input_tokens"`
	OutputTokens        int64 `json:"output_tokens"`
	ReasoningTokens     int64 `json:"reasoning_tokens"`
	CachedTokens        int64 `json:"cached_tokens"`
	CacheReadTokens     int64 `json:"cache_read_tokens"`
	CacheCreationTokens int64 `json:"cache_creation_tokens"`
	TotalTokens         int64 `json:"total_tokens"`
}

// Event is the sanitized request detail retained for recent request lists.
type Event struct {
	Timestamp       time.Time   `json:"timestamp"`
	Provider        string      `json:"provider"`
	ExecutorType    string      `json:"executor_type"`
	Model           string      `json:"model"`
	Alias           string      `json:"alias"`
	Source          string      `json:"source,omitempty"`
	AuthType        string      `json:"auth_type"`
	Endpoint        string      `json:"endpoint,omitempty"`
	RequestID       string      `json:"request_id,omitempty"`
	ReasoningEffort string      `json:"reasoning_effort,omitempty"`
	ServiceTier     string      `json:"service_tier"`
	LatencyMs       int64       `json:"latency_ms"`
	TTFTMs          int64       `json:"ttft_ms"`
	Failed          bool        `json:"failed"`
	StatusCode      int         `json:"status_code"`
	Tokens          TokenTotals `json:"tokens"`
}

type accumulator struct {
	requests   int64
	successful int64
	failed     int64
	latencyMs  int64
	tokens     TokenTotals
	models     map[string]struct{}
}

type modelKey struct {
	provider string
	model    string
}

// NewStore constructs a bounded usage statistics store.
func NewStore(maxEvents int, maxAge time.Duration) *Store {
	if maxEvents <= 0 {
		maxEvents = defaultMaxEvents
	}
	if maxAge <= 0 {
		maxAge = defaultMaxAge
	}
	return &Store{
		events:    make([]Event, 0, min(maxEvents, 256)),
		maxEvents: maxEvents,
		maxAge:    maxAge,
	}
}

// DefaultStore returns the process-wide store registered with the usage manager.
func DefaultStore() *Store {
	return defaultStore
}

// HandleUsage converts a runtime usage record into a sanitized statistics event.
func (s *Store) HandleUsage(ctx context.Context, record coreusage.Record) {
	if s == nil || !redisqueue.UsageStatisticsEnabled() {
		return
	}

	now := time.Now()
	timestamp := record.RequestedAt
	if timestamp.IsZero() {
		timestamp = now
	}

	responseStatus := internallogging.GetResponseStatus(ctx)
	failed := record.Failed || responseStatus >= http.StatusBadRequest
	statusCode := record.Fail.StatusCode
	if statusCode <= 0 {
		statusCode = responseStatus
	}
	if statusCode <= 0 {
		if failed {
			statusCode = http.StatusInternalServerError
		} else {
			statusCode = http.StatusOK
		}
	}

	model := normalizedLabel(record.Model)
	alias := strings.TrimSpace(record.Alias)
	if alias == "" {
		alias = model
	}
	serviceTier := strings.TrimSpace(record.ServiceTier)
	if serviceTier == "" {
		serviceTier = coreusage.ServiceTierFromContext(ctx)
	}
	reasoningEffort := strings.TrimSpace(record.ReasoningEffort)
	if reasoningEffort == "" {
		reasoningEffort = coreusage.ReasoningEffortFromContext(ctx)
	}

	event := Event{
		Timestamp:       timestamp,
		Provider:        normalizedLabel(record.Provider),
		ExecutorType:    normalizedLabel(record.ExecutorType),
		Model:           model,
		Alias:           alias,
		Source:          strings.TrimSpace(record.Source),
		AuthType:        normalizedLabel(record.AuthType),
		Endpoint:        strings.TrimSpace(internallogging.GetEndpoint(ctx)),
		RequestID:       strings.TrimSpace(internallogging.GetRequestID(ctx)),
		ReasoningEffort: reasoningEffort,
		ServiceTier:     serviceTier,
		LatencyMs:       nonNegativeDurationMillis(record.Latency),
		TTFTMs:          nonNegativeDurationMillis(record.TTFT),
		Failed:          failed,
		StatusCode:      statusCode,
		Tokens:          normalizeTokens(record.Detail),
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.pruneLocked(now)
	if event.Timestamp.Before(now.Add(-s.maxAge)) {
		return
	}
	s.events = append(s.events, event)
	if overflow := len(s.events) - s.maxEvents; overflow > 0 {
		copy(s.events, s.events[overflow:])
		s.events = s.events[:s.maxEvents]
	}
}

// Report builds an aggregate snapshot for the requested calendar-day range.
func (s *Store) Report(options QueryOptions, now time.Time) Report {
	options = normalizeQueryOptions(options)
	if now.IsZero() {
		now = time.Now()
	}
	location := now.Location()
	startOfToday := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, location)
	from := startOfToday.AddDate(0, 0, -(options.Days - 1))

	report := Report{
		GeneratedAt: now,
		Range: ReportRange{
			From:     from,
			To:       now,
			Days:     options.Days,
			Provider: options.Provider,
		},
		Providers: make([]ProviderStat, 0),
		Models:    make([]ModelStat, 0),
		Trend:     make([]DailyStat, options.Days),
		Recent:    make([]Event, 0),
	}

	dailyByDate := make(map[string]*DailyStat, options.Days)
	for day := 0; day < options.Days; day++ {
		date := from.AddDate(0, 0, day).Format("2006-01-02")
		report.Trend[day].Date = date
		dailyByDate[date] = &report.Trend[day]
	}

	if s == nil {
		return report
	}

	s.mu.Lock()
	s.pruneLocked(now)
	events := append([]Event(nil), s.events...)
	s.mu.Unlock()

	providers := make(map[string]*accumulator)
	models := make(map[modelKey]*accumulator)
	activeModels := make(map[modelKey]struct{})
	matchingEvents := make([]Event, 0, len(events))
	var summaryAccumulator accumulator

	for _, event := range events {
		if event.Timestamp.Before(from) || event.Timestamp.After(now) {
			continue
		}
		if options.Provider != "" && !strings.EqualFold(event.Provider, options.Provider) {
			continue
		}

		matchingEvents = append(matchingEvents, event)
		addEvent(&summaryAccumulator, event)

		providerAccumulator := providers[event.Provider]
		if providerAccumulator == nil {
			providerAccumulator = &accumulator{models: make(map[string]struct{})}
			providers[event.Provider] = providerAccumulator
		}
		addEvent(providerAccumulator, event)
		providerAccumulator.models[event.Model] = struct{}{}

		key := modelKey{provider: event.Provider, model: event.Model}
		modelAccumulator := models[key]
		if modelAccumulator == nil {
			modelAccumulator = &accumulator{}
			models[key] = modelAccumulator
		}
		addEvent(modelAccumulator, event)
		activeModels[key] = struct{}{}

		date := event.Timestamp.In(location).Format("2006-01-02")
		if daily := dailyByDate[date]; daily != nil {
			daily.Requests++
			if event.Failed {
				daily.Failed++
			} else {
				daily.Successful++
			}
			daily.Tokens.add(event.Tokens)
		}
	}

	report.Summary = Summary{
		Requests:         summaryAccumulator.requests,
		Successful:       summaryAccumulator.successful,
		Failed:           summaryAccumulator.failed,
		SuccessRate:      successRate(summaryAccumulator.successful, summaryAccumulator.requests),
		ActiveProviders:  len(providers),
		ActiveModels:     len(activeModels),
		AverageLatencyMs: average(summaryAccumulator.latencyMs, summaryAccumulator.requests),
		Tokens:           summaryAccumulator.tokens,
	}

	for provider, totals := range providers {
		report.Providers = append(report.Providers, ProviderStat{
			Provider:         provider,
			Requests:         totals.requests,
			Successful:       totals.successful,
			Failed:           totals.failed,
			SuccessRate:      successRate(totals.successful, totals.requests),
			ActiveModels:     len(totals.models),
			AverageLatencyMs: average(totals.latencyMs, totals.requests),
			Tokens:           totals.tokens,
		})
	}
	sort.Slice(report.Providers, func(i, j int) bool {
		if report.Providers[i].Requests != report.Providers[j].Requests {
			return report.Providers[i].Requests > report.Providers[j].Requests
		}
		if report.Providers[i].Tokens.TotalTokens != report.Providers[j].Tokens.TotalTokens {
			return report.Providers[i].Tokens.TotalTokens > report.Providers[j].Tokens.TotalTokens
		}
		return report.Providers[i].Provider < report.Providers[j].Provider
	})

	for key, totals := range models {
		report.Models = append(report.Models, ModelStat{
			Provider:         key.provider,
			Model:            key.model,
			Requests:         totals.requests,
			Successful:       totals.successful,
			Failed:           totals.failed,
			SuccessRate:      successRate(totals.successful, totals.requests),
			AverageLatencyMs: average(totals.latencyMs, totals.requests),
			Tokens:           totals.tokens,
		})
	}
	sort.Slice(report.Models, func(i, j int) bool {
		if report.Models[i].Tokens.TotalTokens != report.Models[j].Tokens.TotalTokens {
			return report.Models[i].Tokens.TotalTokens > report.Models[j].Tokens.TotalTokens
		}
		if report.Models[i].Requests != report.Models[j].Requests {
			return report.Models[i].Requests > report.Models[j].Requests
		}
		if report.Models[i].Provider != report.Models[j].Provider {
			return report.Models[i].Provider < report.Models[j].Provider
		}
		return report.Models[i].Model < report.Models[j].Model
	})
	if len(report.Models) > options.ModelLimit {
		report.Models = report.Models[:options.ModelLimit]
	}

	sort.Slice(matchingEvents, func(i, j int) bool {
		return matchingEvents[i].Timestamp.After(matchingEvents[j].Timestamp)
	})
	if len(matchingEvents) > options.RecentLimit {
		matchingEvents = matchingEvents[:options.RecentLimit]
	}
	report.Recent = matchingEvents

	return report
}

func (s *Store) pruneLocked(now time.Time) {
	if len(s.events) == 0 {
		return
	}
	cutoff := now.Add(-s.maxAge)
	kept := s.events[:0]
	for _, event := range s.events {
		if !event.Timestamp.Before(cutoff) {
			kept = append(kept, event)
		}
	}
	s.events = kept
}

func normalizeQueryOptions(options QueryOptions) QueryOptions {
	if options.Days <= 0 {
		options.Days = DefaultDays
	} else if options.Days > MaxDays {
		options.Days = MaxDays
	}
	if options.ModelLimit <= 0 {
		options.ModelLimit = DefaultModelLimit
	} else if options.ModelLimit > MaxModelLimit {
		options.ModelLimit = MaxModelLimit
	}
	if options.RecentLimit <= 0 {
		options.RecentLimit = DefaultRecentLimit
	} else if options.RecentLimit > MaxRecentLimit {
		options.RecentLimit = MaxRecentLimit
	}
	options.Provider = strings.TrimSpace(options.Provider)
	return options
}

func normalizedLabel(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	return value
}

func nonNegativeDurationMillis(value time.Duration) int64 {
	if value <= 0 {
		return 0
	}
	return value.Milliseconds()
}

func normalizeTokens(detail coreusage.Detail) TokenTotals {
	tokens := TokenTotals{
		InputTokens:         nonNegative(detail.InputTokens),
		OutputTokens:        nonNegative(detail.OutputTokens),
		ReasoningTokens:     nonNegative(detail.ReasoningTokens),
		CachedTokens:        nonNegative(detail.CachedTokens),
		CacheReadTokens:     nonNegative(detail.CacheReadTokens),
		CacheCreationTokens: nonNegative(detail.CacheCreationTokens),
		TotalTokens:         nonNegative(detail.TotalTokens),
	}
	if tokens.TotalTokens == 0 {
		tokens.TotalTokens = tokens.InputTokens + tokens.OutputTokens + tokens.ReasoningTokens
	}
	return tokens
}

func nonNegative(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

func addEvent(totals *accumulator, event Event) {
	totals.requests++
	if event.Failed {
		totals.failed++
	} else {
		totals.successful++
	}
	totals.latencyMs += event.LatencyMs
	totals.tokens.add(event.Tokens)
}

func (tokens *TokenTotals) add(other TokenTotals) {
	tokens.InputTokens += other.InputTokens
	tokens.OutputTokens += other.OutputTokens
	tokens.ReasoningTokens += other.ReasoningTokens
	tokens.CachedTokens += other.CachedTokens
	tokens.CacheReadTokens += other.CacheReadTokens
	tokens.CacheCreationTokens += other.CacheCreationTokens
	tokens.TotalTokens += other.TotalTokens
}

func successRate(successful, requests int64) float64 {
	if requests == 0 {
		return 0
	}
	return float64(successful) * 100 / float64(requests)
}

func average(total, count int64) float64 {
	if count == 0 {
		return 0
	}
	return float64(total) / float64(count)
}
