package multiuser

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

type AuditEvent struct {
	ID     string    `json:"id"`
	At     time.Time `json:"at"`
	Actor  string    `json:"actor"`
	Action string    `json:"action"`
	Target string    `json:"target"`
	Status int       `json:"status"`
	IP     string    `json:"ip"`
	Detail string    `json:"detail,omitempty"`
}

type UsageEvent struct {
	ID                  string               `json:"id"`
	RequestID           string               `json:"request_id"`
	TraceID             string               `json:"trace_id"`
	At                  time.Time            `json:"at"`
	Consumer            string               `json:"consumer"`
	KeyID               string               `json:"key_id"`
	KeyName             string               `json:"key_name"`
	Owner               string               `json:"owner"`
	ChannelID           string               `json:"channel_id"`
	Channel             string               `json:"channel"`
	CredentialID        string               `json:"credential_id"`
	CredentialName      string               `json:"credential_name"`
	Provider            string               `json:"provider"`
	Model               string               `json:"model"`
	UpstreamModel       string               `json:"upstream_model"`
	Endpoint            string               `json:"endpoint"`
	Stream              bool                 `json:"stream"`
	Failed              bool                 `json:"failed"`
	Status              int                  `json:"status"`
	LatencyMS           int64                `json:"latency_ms"`
	TTFTMS              int64                `json:"ttft_ms"`
	InputTokens         int64                `json:"input_tokens"`
	OutputTokens        int64                `json:"output_tokens"`
	ReasoningTokens     int64                `json:"reasoning_tokens"`
	CacheReadTokens     int64                `json:"cache_read_tokens"`
	CacheCreationTokens int64                `json:"cache_creation_tokens"`
	TotalTokens         int64                `json:"total_tokens"`
	TokensKnown         bool                 `json:"tokens_known"`
	TokenBreakdown      usage.TokenBreakdown `json:"token_breakdown"`
	// ReasoningEffort is the translated upstream thinking level (low/high/…), when known.
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	// Balanced marks attempts made with an even-load-balancing key; one client
	// request may produce several attempts sharing the trace ID.
	Balanced bool `json:"balanced,omitempty"`
	// Error is the upstream failure body, truncated to maxUsageErrorBytes.
	Error string `json:"error,omitempty"`
	// Attempts is query-time only: the number of balanced attempts in this
	// record's trace that match the same filter. It is never persisted.
	Attempts int `json:"attempts,omitempty"`
}

const maxUsageErrorBytes = 4096

// truncateUTF8 cuts s to at most limit bytes without splitting a rune.
func truncateUTF8(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

type CallEvent struct {
	ID        string    `json:"id"`
	At        time.Time `json:"at"`
	Consumer  string    `json:"consumer"`
	KeyID     string    `json:"key_id"`
	KeyName   string    `json:"key_name"`
	Endpoint  string    `json:"endpoint"`
	Model     string    `json:"model"`
	Status    int       `json:"status"`
	LatencyMS int64     `json:"latency_ms"`
}

type journalEntry struct {
	Usage *UsageEvent `json:"usage,omitempty"`
	Audit *AuditEvent `json:"audit,omitempty"`
	Call  *CallEvent  `json:"call,omitempty"`
}

type Filter struct {
	Consumer     string
	Owner        string
	ChannelID    string
	CredentialID string
	KeyID        string
	Model        string
	// Outcome narrows usage to "ok" or "failed" attempts; empty keeps both.
	Outcome string
	// Provider matches the upstream type ("kimi", "openai-compatible").
	Provider string
	// Stream is "stream" or "nonstream"; empty keeps both.
	Stream string
	// Status is an exact HTTP status or a class such as "4xx".
	Status   string
	Endpoint string
	// Effort is the translated reasoning level; "none" matches records without one.
	Effort string
	// Balanced is "yes" or "no"; empty keeps both.
	Balanced string
	// Sort orders usage lists: "" or "time" (newest first), "tokens", "latency".
	Sort     string
	Actor    string
	Action   string
	From     time.Time
	To       time.Time
	Page     int
	PageSize int
}

func (s *Store) appendEntry(at time.Time, entry journalEntry) error {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	dir := filepath.Join(s.dir, "journal")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(dir, at.UTC().Format("2006-01-02")+".jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	errEncode := json.NewEncoder(file).Encode(entry)
	if errEncode == nil {
		errEncode = file.Sync()
	}
	errClose := file.Close()
	if errEncode != nil {
		return errEncode
	}
	return errClose
}

func (s *Store) Audit(event AuditEvent) error {
	event.ID = uuid.NewString()
	if event.At.IsZero() {
		event.At = s.now().UTC()
	}
	return s.appendEntry(event.At, journalEntry{Audit: &event})
}

func (s *Store) RecordCall(event CallEvent) error {
	if event.At.IsZero() {
		event.At = s.now().UTC()
	}
	return s.appendEntry(event.At, journalEntry{Call: &event})
}

func (s *Store) HandleUsage(ctx context.Context, record usage.Record) {
	a := record.Attribution
	if a.Consumer == "" || a.SiteID != s.SiteID() {
		return
	}
	at := record.RequestedAt
	if at.IsZero() {
		at = s.now().UTC()
	}
	status := record.Fail.StatusCode
	if !record.Failed {
		status = 200
	} else if status == 0 {
		status = 502
	}
	d := record.Detail
	event := UsageEvent{
		ID: uuid.NewString(), RequestID: record.RequestID, TraceID: record.TraceID, At: at.UTC(),
		Consumer: a.Consumer, KeyID: a.KeyID, KeyName: a.KeyName, Owner: a.Owner,
		ChannelID: a.ChannelID, Channel: a.Channel, CredentialID: record.AuthID, CredentialName: a.CredentialName,
		Provider: record.Provider, Model: record.Alias, UpstreamModel: record.Model,
		Endpoint: logging.GetEndpoint(ctx), Stream: record.Stream, Failed: record.Failed, Status: status,
		LatencyMS: record.Latency.Milliseconds(), TTFTMS: record.TTFT.Milliseconds(),
		InputTokens: d.InputTokens, OutputTokens: d.OutputTokens, ReasoningTokens: d.ReasoningTokens,
		CacheReadTokens: d.CacheReadTokens, CacheCreationTokens: d.CacheCreationTokens, TotalTokens: d.TotalTokens,
		TokensKnown:     d.TotalTokens != 0 || d.InputTokens != 0 || d.OutputTokens != 0,
		TokenBreakdown:  d.TokenBreakdown,
		ReasoningEffort: record.ReasoningEffort,
		Balanced:        a.Balanced,
	}
	if record.Failed {
		event.Error = truncateUTF8(strings.TrimSpace(record.Fail.Body), maxUsageErrorBytes)
	}
	if d.TokenBreakdown.Valid() && d.TokenBreakdown.Quality == usage.TokenAccountingQualityComplete {
		b := d.TokenBreakdown
		event.InputTokens = b.Input.TotalTokens
		event.OutputTokens = b.Output.TotalTokens
		event.CacheReadTokens = b.Input.CacheReadTokens
		event.CacheCreationTokens = b.Input.CacheWriteTokens
		event.ReasoningTokens = b.Output.ReasoningTokens
		event.TotalTokens = b.TotalTokens
	}
	if event.Model == "" {
		event.Model = a.Channel + "/" + record.Model
	}
	if err := s.appendEntry(at, journalEntry{Usage: &event}); err != nil {
		log.WithError(err).Error("multi-user: failed to persist usage")
	}
}

func within(at time.Time, filter Filter) bool {
	return (filter.From.IsZero() || !at.Before(filter.From)) && (filter.To.IsZero() || at.Before(filter.To))
}

func matchUsage(event UsageEvent, filter Filter) bool {
	return within(event.At, filter) && (filter.Consumer == "" || event.Consumer == filter.Consumer) &&
		(filter.Owner == "" || event.Owner == filter.Owner) && (filter.ChannelID == "" || event.ChannelID == filter.ChannelID) &&
		(filter.CredentialID == "" || event.CredentialID == filter.CredentialID) && (filter.KeyID == "" || event.KeyID == filter.KeyID) &&
		matchModelQuery(event, filter.Model) &&
		(filter.Outcome == "" || event.Failed == (filter.Outcome == "failed")) &&
		(filter.Provider == "" || providerBase(event.Provider) == filter.Provider) &&
		(filter.Stream == "" || event.Stream == (filter.Stream == "stream")) &&
		matchStatus(event.Status, filter.Status) &&
		(filter.Endpoint == "" || event.Endpoint == filter.Endpoint) &&
		(filter.Effort == "" || event.ReasoningEffort == effortValue(filter.Effort)) &&
		(filter.Balanced == "" || event.Balanced == (filter.Balanced == "yes"))
}

// effortValue maps the "none" sentinel to records without a reasoning level.
func effortValue(effort string) string {
	if effort == "none" {
		return ""
	}
	return effort
}

// statusRange parses "200" into [200,200] and "4xx" into [400,499].
func statusRange(status string) (int, int, bool) {
	status = strings.ToLower(strings.TrimSpace(status))
	if len(status) == 3 && strings.HasSuffix(status, "xx") && status[0] >= '1' && status[0] <= '5' {
		base := int(status[0]-'0') * 100
		return base, base + 99, true
	}
	code, err := strconv.Atoi(status)
	if err != nil || code < 100 || code > 599 {
		return 0, 0, false
	}
	return code, code, true
}

func matchStatus(status int, filter string) bool {
	if filter == "" {
		return true
	}
	low, high, ok := statusRange(filter)
	return ok && status >= low && status <= high
}

// Parenthesized channel/model suffixes pin bare-model calls to their serving
// channel. Plain queries retain the existing requested-model substring match.
func matchModelQuery(event UsageEvent, query string) bool {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return true
	}
	model := strings.ToLower(event.Model)
	serving := model
	if event.Channel != "" && !strings.HasPrefix(model, strings.ToLower(event.Channel)+"/") {
		serving = strings.ToLower(event.Channel) + "/" + model
	}
	if pin, ok := modelQueryPin(q); ok {
		return serving == pin || model == pin
	}
	return strings.Contains(model, q)
}

// modelQueryPin extracts a "channel/model" pin from a trailing parenthesized
// segment of the query, accepting both ASCII and full-width parentheses.
func modelQueryPin(q string) (string, bool) {
	for _, pair := range [][2]string{{"(", ")"}, {"（", "）"}} {
		open := strings.Index(q, pair[0])
		if open < 0 || !strings.HasSuffix(q, pair[1]) {
			continue
		}
		inner := strings.TrimSpace(q[open+len(pair[0]) : len(q)-len(pair[1])])
		if strings.Contains(inner, "/") {
			return inner, true
		}
	}
	return "", false
}

func pageBounds(filter Filter, length int) (int, int) {
	page, size := filter.Page, filter.PageSize
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 50
	}
	if size > 200 {
		size = 200
	}
	start := (page - 1) * size
	if start > length {
		start = length
	}
	end := start + size
	if end > length {
		end = length
	}
	return start, end
}

func (s *Store) Usage(filter Filter) ([]UsageEvent, int, error) {
	return s.UsageContext(context.Background(), filter)
}

func (s *Store) Calls(filter Filter) ([]CallEvent, int, error) {
	return s.CallsContext(context.Background(), filter)
}

func (s *Store) Audits(filter Filter) ([]AuditEvent, int, error) {
	return s.AuditsContext(context.Background(), filter)
}

type Summary struct {
	Consumer            string `json:"consumer,omitempty"`
	Owner               string `json:"owner,omitempty"`
	Channel             string `json:"channel,omitempty"`
	Model               string `json:"model,omitempty"`
	CredentialID        string `json:"credential_id,omitempty"`
	Day                 string `json:"day,omitempty"`
	Calls               int64  `json:"calls"`
	Executions          int64  `json:"executions"`
	Failures            int64  `json:"failures"`
	InputTokens         int64  `json:"input_tokens"`
	OutputTokens        int64  `json:"output_tokens"`
	ReasoningTokens     int64  `json:"reasoning_tokens"`
	CacheReadTokens     int64  `json:"cache_read_tokens"`
	CacheCreationTokens int64  `json:"cache_creation_tokens"`
	TotalTokens         int64  `json:"total_tokens"`
	UnknownTokenRecords int64  `json:"unknown_token_records"`
	// Latency and TTFT sums cover successful attempts with a measured value;
	// callers divide by the matching count, so groups stay mergeable.
	LatencyMS    int64 `json:"latency_ms"`
	LatencyCount int64 `json:"latency_count"`
	TTFTMS       int64 `json:"ttft_ms"`
	TTFTCount    int64 `json:"ttft_count"`
	// Failovers counts earlier failed attempts folded into balanced requests;
	// only the overall total carries it.
	Failovers int64 `json:"failovers,omitempty"`
}

// SummaryBucket is one hour of usage for the trend chart.
type SummaryBucket struct {
	Start        int64 `json:"start"`
	Calls        int64 `json:"calls"`
	Executions   int64 `json:"executions"`
	Failures     int64 `json:"failures"`
	TotalTokens  int64 `json:"total_tokens"`
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

func (s *Store) Summary(filter Filter) (Summary, []Summary, error) {
	return s.SummaryContext(context.Background(), filter)
}

// PerformanceBucket holds raw aggregates for one hour of usage. Rates and
// averages are derived by callers so buckets can be merged across routes.
type PerformanceBucket struct {
	Start        int64 `json:"start"`
	Requests     int64 `json:"requests"`
	Failures     int64 `json:"failures"`
	LatencyMS    int64 `json:"latency_ms"`
	LatencyCount int64 `json:"latency_count"`
	TTFTMS       int64 `json:"ttft_ms"`
	TTFTCount    int64 `json:"ttft_count"`
	OutputTokens int64 `json:"output_tokens"`
	GenerationMS int64 `json:"generation_ms"`
}

func (b *PerformanceBucket) add(u *UsageEvent) {
	b.Requests++
	if u.Failed {
		b.Failures++
		return
	}
	if u.LatencyMS > 0 {
		b.LatencyMS += u.LatencyMS
		b.LatencyCount++
		generation := u.LatencyMS
		if u.TTFTMS > 0 && u.TTFTMS < u.LatencyMS {
			generation = u.LatencyMS - u.TTFTMS
		}
		b.GenerationMS += generation
		b.OutputTokens += u.OutputTokens
	}
	if u.TTFTMS > 0 {
		b.TTFTMS += u.TTFTMS
		b.TTFTCount++
	}
}

// ModelPerformance aggregates the trailing 24 hours of usage for one pinned
// model ID: totals plus one bucket per hour, oldest first.
type ModelPerformance struct {
	Model   string              `json:"model"`
	Totals  PerformanceBucket   `json:"totals"`
	Buckets []PerformanceBucket `json:"buckets"`
}

const performanceBucketCount = 24

// ModelPerformance aggregates the 24 hourly buckets ending at the hour containing now.
func (s *Store) ModelPerformance(now time.Time) (map[string]*ModelPerformance, error) {
	return s.ModelPerformanceContext(context.Background(), now)
}
