package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type observabilitySnapshot struct {
	BootID                string               `json:"boot_id"`
	ProcessID             int                  `json:"process_id"`
	Requests              uint64               `json:"requests"`
	InputTokens           int64                `json:"input_tokens"`
	OutputTokens          int64                `json:"output_tokens"`
	CacheMisses           uint64               `json:"cache_misses"`
	CacheLowReuseRequests uint64               `json:"cache_low_reuse_requests"`
	Compactions           uint64               `json:"compactions"`
	CompactionResets      uint64               `json:"compaction_resets"`
	EstimatedCostUSD      float64              `json:"estimated_cost_usd"`
	CostEstimated         bool                 `json:"cost_estimated"`
	PricedRequests        uint64               `json:"priced_requests"`
	UnpricedRequests      uint64               `json:"unpriced_requests"`
	EarliestSequence      uint64               `json:"earliest_sequence"`
	LatestSequence        uint64               `json:"latest_sequence"`
	NextAfter             uint64               `json:"next_after"`
	EventGap              bool                 `json:"event_gap"`
	GapFromSequence       uint64               `json:"gap_from_sequence"`
	GapToSequence         uint64               `json:"gap_to_sequence"`
	CursorReset           bool                 `json:"cursor_reset"`
	RecentEvents          []observabilityEvent `json:"recent_events"`
}

type observabilityEvent struct {
	Sequence               uint64  `json:"sequence"`
	Provider               string  `json:"provider"`
	Model                  string  `json:"model"`
	Operation              string  `json:"operation"`
	InputTokens            int64   `json:"input_tokens"`
	OutputTokens           int64   `json:"output_tokens"`
	CacheReadTokens        int64   `json:"cache_read_tokens"`
	CacheWriteTokens       int64   `json:"cache_write_tokens"`
	UncachedInputTokens    int64   `json:"uncached_input_tokens"`
	CacheReadPercent       float64 `json:"cache_read_percent"`
	CacheLowReuse          bool    `json:"cache_low_reuse"`
	CacheOutcome           string  `json:"cache_outcome"`
	CacheMiss              bool    `json:"cache_miss"`
	Failed                 bool    `json:"failed"`
	EstimatedCostUSD       float64 `json:"estimated_cost_usd"`
	EstimatedCostAvailable bool    `json:"estimated_cost_available"`
	EstimatedCostTier      string  `json:"estimated_cost_tier"`
	ResetReason            string  `json:"reset_reason"`
	LaneID                 string  `json:"lane_id"`
	AgentID                string  `json:"agent_id"`
	PreviousEnvelopeID     string  `json:"previous_envelope_id"`
	EnvelopeID             string  `json:"envelope_id"`
}

type observabilityPollMsg struct {
	snapshot observabilitySnapshot
	err      error
}

type observabilityTickMsg struct{}

func (a App) fetchObservability() tea.Msg {
	snapshot, err := a.client.GetObservabilitySnapshot(a.observabilityCursor, 200, a.observabilityBootID)
	return observabilityPollMsg{snapshot: snapshot, err: err}
}

func waitForObservabilityPoll() tea.Cmd {
	return tea.Tick(2*time.Second, func(_ time.Time) tea.Msg {
		return observabilityTickMsg{}
	})
}

func (a App) renderObservabilitySummary() string {
	width := a.width
	if width < 1 {
		width = 1
	}
	contentWidth := width - summaryBarStyle.GetPaddingLeft() - summaryBarStyle.GetPaddingRight()
	if contentWidth < 0 {
		contentWidth = 0
	}

	cost := formatObservabilityCost(a.observability)
	health := a.observabilityHealthLabel(time.Now())
	healthPrefix := ""
	if health != "" {
		healthPrefix = fitStringWidth(health, 40) + "  │  "
	}
	summary := fmt.Sprintf(
		"%s%s %d  │  %s %s  │  %s %s  │  %s %d  │  %s %d  │  %s %d  │  %s %d  │  %s %s",
		healthPrefix,
		T("summary_requests"), a.observability.Requests,
		T("summary_input"), formatLargeNumber(a.observability.InputTokens),
		T("summary_output"), formatLargeNumber(a.observability.OutputTokens),
		T("summary_cache_misses"), a.observability.CacheMisses,
		T("summary_cache_low_reuse"), a.observability.CacheLowReuseRequests,
		T("summary_compactions"), a.observability.Compactions,
		T("summary_compaction_resets"), a.observability.CompactionResets,
		T("summary_estimated_cost"), cost,
	)
	if lipgloss.Width(summary) > contentWidth {
		summary = fmt.Sprintf(
			"%sReq %d │ In %s Out %s │ Miss %d/Low %d │ Cmp %d/Rst %d │ %s",
			healthPrefix,
			a.observability.Requests,
			formatLargeNumber(a.observability.InputTokens),
			formatLargeNumber(a.observability.OutputTokens),
			a.observability.CacheMisses,
			a.observability.CacheLowReuseRequests,
			a.observability.Compactions,
			a.observability.CompactionResets,
			cost,
		)
	}
	summary = fitStringWidth(summary, contentWidth)
	gap := contentWidth - lipgloss.Width(summary)
	if gap > 0 {
		summary += strings.Repeat(" ", gap)
	}
	return summaryBarStyle.Width(width).Render(summary)
}

func formatObservabilityCost(snapshot observabilitySnapshot) string {
	if snapshot.PricedRequests == 0 {
		return "—"
	}
	prefix := ""
	if snapshot.UnpricedRequests > 0 {
		prefix = "partial "
	}
	return fmt.Sprintf("%s$%.4f", prefix, snapshot.EstimatedCostUSD)
}

func (a App) observabilityHealthLabel(now time.Time) string {
	if a.observabilityErr != nil {
		message := strings.Join(strings.Fields(a.observabilityErr.Error()), " ")
		return "⚠ stale: " + message
	}
	if a.observabilityLastSuccess.IsZero() {
		return "… loading"
	}
	age := now.Sub(a.observabilityLastSuccess)
	if age >= 6*time.Second {
		return fmt.Sprintf("⚠ stale %s", age.Round(time.Second))
	}
	return ""
}
