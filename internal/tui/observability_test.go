package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestRequestEventLineStyles(t *testing.T) {
	model := logsTabModel{}
	cacheMiss := `[info] request_event operation=inference cache_outcome=miss cache_miss=true`
	if got, want := model.styleLine(cacheMiss), logErrorStyle.Render(cacheMiss); got != want {
		t.Fatalf("cache miss style = %q, want red style %q", got, want)
	}

	compaction := `[info] request_event operation=compaction cache_outcome=hit cache_miss=false`
	if got, want := model.styleLine(compaction), logCompactionStyle.Render(compaction); got != want {
		t.Fatalf("compaction style = %q, want magenta style %q", got, want)
	}

	lowReuse := `[info] request_event operation=inference cache_outcome=hit cache_miss=false cache_low_reuse=true`
	if got, want := model.styleLine(lowReuse), logErrorStyle.Render(lowReuse); got != want {
		t.Fatalf("low cache reuse style = %q, want red style %q", got, want)
	}

	reset := `[warn] compaction_event operation=compaction_reset lane_id="0123456789abcdef"`
	if got, want := model.styleLine(reset), logCompactionStyle.Render(reset); got != want {
		t.Fatalf("compaction reset style = %q, want magenta style %q", got, want)
	}
}

func TestRequestMonitorTableHasRequestedColumnsAndValues(t *testing.T) {
	model := newLogsTabModel(nil, nil)
	model.SetSize(180, 20)
	model.AddObservabilityEvents([]observabilityEvent{{
		Sequence:               1,
		Provider:               "codex",
		Model:                  "gpt-5.6-sol",
		Effort:                 "xhigh",
		Operation:              "inference",
		InputTokens:            240000,
		OutputTokens:           1234,
		CacheReadTokens:        179200,
		CacheWriteTokens:       32768,
		CacheWriteEstimated:    true,
		CacheReadPercent:       74.666,
		CacheTelemetryPresent:  true,
		EstimatedCostAvailable: true,
		EstimatedInputCostUSD:  0.304,
		EstimatedOutputCostUSD: 0.03702,
		EstimatedCacheCostUSD:  0.0896,
	}})

	view := model.View()
	for _, header := range []string{
		"PROVIDER", "MODEL", "EFFORT", "INPUT", "OUTPUT", "CACHE_READ",
		"CACHE_WRITE", "CACHE READ %", "COST IN", "COST OUT", "COST CACHE",
	} {
		if !strings.Contains(view, header) {
			t.Fatalf("request monitor view missing header %q:\n%s", header, view)
		}
	}
	for _, value := range []string{
		"codex", "gpt-5.6-sol", "xhigh", "240,000", "1,234", "179,200",
		"~32,768", "74.7%", "$0.3040", "$0.0370", "$0.0896",
	} {
		if !strings.Contains(view, value) {
			t.Fatalf("request monitor view missing value %q:\n%s", value, view)
		}
	}
}

func TestRequestMonitorKeepsHeaderFixedWhileRowsScroll(t *testing.T) {
	model := newLogsTabModel(nil, nil)
	model.SetSize(140, 10)
	events := make([]observabilityEvent, 40)
	for index := range events {
		events[index] = observabilityEvent{
			Sequence:  uint64(index + 1),
			Provider:  "codex",
			Model:     "gpt-5.6-sol",
			Operation: "inference",
		}
	}
	model.AddObservabilityEvents(events)
	model.viewport.GotoBottom()

	view := model.View()
	if !strings.Contains(view, "PROVIDER") || !strings.Contains(view, "COST CACHE") {
		t.Fatalf("scrolled request monitor lost fixed table header:\n%s", view)
	}
}

func TestRequestMonitorStylesCompactionsAndCacheMisses(t *testing.T) {
	compaction := observabilityEvent{Operation: "compaction"}
	line := formatRequestTableRow(compaction, 140)
	if got, want := styleRequestTableRow(compaction, line), logCompactionStyle.Render(line); got != want {
		t.Fatalf("compaction table row style = %q, want %q", got, want)
	}
	miss := observabilityEvent{Operation: "compaction", CacheMiss: true}
	line = formatRequestTableRow(miss, 140)
	if got, want := styleRequestTableRow(miss, line), logErrorStyle.Render(line); got != want {
		t.Fatalf("cache-miss table row style = %q, want %q", got, want)
	}
}

func TestRequestTableFitsVeryNarrowViewports(t *testing.T) {
	event := observabilityEvent{
		Provider:              "codex",
		Model:                 "gpt-5.6-sol",
		Effort:                "xhigh",
		InputTokens:           240000,
		OutputTokens:          1234,
		CacheReadTokens:       179200,
		CacheWriteTokens:      32768,
		CacheReadPercent:      74.666,
		CacheTelemetryPresent: true,
	}

	for _, width := range []int{70, 40, 20, 7, 0} {
		t.Run(fmt.Sprintf("width_%d", width), func(t *testing.T) {
			columns := requestTableColumns(width)
			if got := requestTableWidth(columns); got > width {
				t.Fatalf("calculated table width = %d, exceeds viewport %d", got, width)
			}

			headers := make([]string, len(columns))
			for index, column := range columns {
				headers[index] = column.header
			}
			if got := lipgloss.Width(formatRequestTableCells(columns, headers)); got > width {
				t.Fatalf("rendered header width = %d, exceeds viewport %d", got, width)
			}
			if got := lipgloss.Width(formatRequestTableRow(event, width)); got > width {
				t.Fatalf("rendered row width = %d, exceeds viewport %d", got, width)
			}
		})
	}
}

func TestRequestMonitorUsesStructuredEventsWithFileLogsActive(t *testing.T) {
	model := newLogsTabModel(nil, &LogHook{})
	model.AddObservabilityEvents([]observabilityEvent{{Sequence: 1, Operation: "inference"}})
	if len(model.requestEvents) != 1 {
		t.Fatalf("structured request rows = %d, want 1", len(model.requestEvents))
	}
	if len(model.lines) != 0 {
		t.Fatalf("text diagnostic lines = %d, want 0 while file logs are active", len(model.lines))
	}
}

func TestRequestMonitorCanToggleToRawServerLogs(t *testing.T) {
	model := newLogsTabModel(nil, &LogHook{})
	model.SetSize(140, 12)
	model, _ = model.Update(logLineMsg(`[error] upstream connection failed`))
	if strings.Contains(model.View(), "upstream connection failed") {
		t.Fatalf("request view unexpectedly rendered raw log:\n%s", model.View())
	}

	model, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
	view := model.View()
	if !strings.Contains(view, "View: RAW") || !strings.Contains(view, "upstream connection failed") {
		t.Fatalf("raw log view missing toggle label or server error:\n%s", view)
	}
}

func TestRequestMonitorShowsObservabilityGapWithFileLogsActive(t *testing.T) {
	model := newLogsTabModel(nil, &LogHook{})
	model.SetSize(140, 12)
	model.AddObservabilityGap(21, 24)

	view := model.View()
	if !strings.Contains(view, "missing sequences 21-24") {
		t.Fatalf("request monitor did not expose event gap:\n%s", view)
	}
	if len(model.lines) != 0 {
		t.Fatalf("gap warning was duplicated into raw file logs: %q", model.lines)
	}
}

func TestRequestTableHidesUnavailableComponentCostsFromOlderServer(t *testing.T) {
	event := observabilityEvent{
		Provider:               "codex",
		Model:                  "gpt-5.6-sol",
		EstimatedCostAvailable: true,
		EstimatedCostUSD:       1.25,
	}
	row := formatRequestTableRow(event, 180)
	if strings.Contains(row, "$0.0000") {
		t.Fatalf("mixed-version row rendered unavailable component costs as zero: %q", row)
	}
}

func TestFormatRequestCostKeepsMicroCostsCompact(t *testing.T) {
	got := formatRequestCost(0.00005)
	if got != "$5.00e-5" {
		t.Fatalf("micro cost = %q, want compact scientific value", got)
	}
	if len(got) > 8 {
		t.Fatalf("micro cost %q does not fit narrow cost column", got)
	}
	row := formatRequestTableRow(observabilityEvent{
		EstimatedCostAvailable: true,
		EstimatedCostUSD:       0.00005,
		EstimatedInputCostUSD:  0.00005,
	}, 120)
	if !strings.Contains(row, got) {
		t.Fatalf("narrow request row truncated micro cost %q: %q", got, row)
	}
}

func TestRenderObservabilitySummaryIncludesFixedCounters(t *testing.T) {
	app := App{
		width: 180,
		observability: observabilitySnapshot{
			Requests:              12,
			InputTokens:           345,
			OutputTokens:          67,
			CacheMisses:           3,
			CacheLowReuseRequests: 4,
			Compactions:           2,
			CompactionResets:      1,
			EstimatedCostUSD:      1.25,
			PricedRequests:        12,
		},
	}
	rendered := app.renderObservabilitySummary()
	for _, want := range []string{"Requests 12", "In 345", "Out 67", "Cache misses 3", "Low cache reuse 4", "Compactions 2", "Compaction resets 1", "Est. cost $1.2500"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("summary %q missing %q", rendered, want)
		}
	}
}

func TestRenderObservabilitySummaryUsesCompactLabelsWhenNarrow(t *testing.T) {
	app := App{
		width: 80,
		observability: observabilitySnapshot{
			Requests:              12,
			InputTokens:           345,
			OutputTokens:          67,
			CacheMisses:           3,
			CacheLowReuseRequests: 4,
			Compactions:           2,
			CompactionResets:      1,
			EstimatedCostUSD:      1.25,
			PricedRequests:        12,
		},
	}
	rendered := app.renderObservabilitySummary()
	for _, want := range []string{"Req 12", "In 345 Out 67", "Miss 3/Low 4", "Cmp 2/Rst 1", "$1.2500"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("compact summary %q missing %q", rendered, want)
		}
	}
}

func TestFormatObservabilityCostShowsCompleteness(t *testing.T) {
	tests := []struct {
		name     string
		snapshot observabilitySnapshot
		want     string
	}{
		{name: "none priced", snapshot: observabilitySnapshot{UnpricedRequests: 2}, want: "—"},
		{name: "all priced", snapshot: observabilitySnapshot{PricedRequests: 2, EstimatedCostUSD: 1.25}, want: "$1.2500"},
		{name: "partially priced", snapshot: observabilitySnapshot{PricedRequests: 1, UnpricedRequests: 1, EstimatedCostUSD: 1.25}, want: "partial $1.2500"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatObservabilityCost(tt.snapshot); got != tt.want {
				t.Fatalf("cost = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestObservabilityOnlyLogsAppendEachRequestOnce(t *testing.T) {
	model := newLogsTabModel(nil, nil)
	model.SetObservabilityOnly(true)
	model.SetSize(140, 20)
	events := []observabilityEvent{{
		Sequence:               1,
		Provider:               "codex",
		Model:                  "gpt-5.6-sol",
		Effort:                 "xhigh",
		Operation:              "compaction",
		InputTokens:            240000,
		OutputTokens:           1024,
		CacheReadTokens:        17920,
		CacheWriteTokens:       222080,
		CacheWriteEstimated:    true,
		UncachedInputTokens:    222080,
		CacheReadPercent:       7.4666667,
		CacheLowReuse:          true,
		CacheOutcome:           "miss",
		CacheMiss:              true,
		EstimatedCostUSD:       1.23,
		EstimatedInputCostUSD:  1.1,
		EstimatedOutputCostUSD: 0.03,
		EstimatedCacheCostUSD:  0.1,
		EstimatedCostAvailable: true,
	}}
	model.AddObservabilityEvents(events)
	model.AddObservabilityEvents(events)
	if len(model.lines) != 1 {
		t.Fatalf("observability lines = %d, want 1", len(model.lines))
	}
	for _, want := range []string{
		"operation=compaction", `provider="codex"`, `effort="xhigh"`,
		"cache_write_tokens=222080", "cache_write_estimated=true", "uncached_input_tokens=222080",
		"cache_read_percent=7.47", "cache_low_reuse=true", "cache_outcome=miss", "cache_miss=true",
		"estimated_input_cost_usd=1.10000000", "estimated_output_cost_usd=0.03000000",
		"estimated_cache_cost_usd=0.10000000",
	} {
		if !strings.Contains(model.lines[0], want) {
			t.Fatalf("request event %q missing %q", model.lines[0], want)
		}
	}
	if got, want := model.styleLine(model.lines[0]), logErrorStyle.Render(model.lines[0]); got != want {
		t.Fatalf("cache-miss compaction style = %q, want red %q", got, want)
	}
}

func TestObservabilityCompactionResetUsesSafeDiagnosticLine(t *testing.T) {
	model := newLogsTabModel(nil, nil)
	model.SetObservabilityOnly(true)
	event := observabilityEvent{
		Sequence:           1,
		Provider:           "codex",
		Model:              "gpt-5.6-sol",
		Operation:          "compaction_reset",
		ResetReason:        "request_envelope_changed",
		LaneID:             "0123456789abcdef",
		AgentID:            "fedcba9876543210",
		PreviousEnvelopeID: "1111111111111111",
		EnvelopeID:         "2222222222222222",
	}
	model.AddObservabilityEvents([]observabilityEvent{event})
	if len(model.lines) != 1 {
		t.Fatalf("observability lines = %d, want 1", len(model.lines))
	}
	for _, want := range []string{"compaction_event", "operation=compaction_reset", `reason="request_envelope_changed"`, `lane_id="0123456789abcdef"`, `agent_id="fedcba9876543210"`} {
		if !strings.Contains(model.lines[0], want) {
			t.Fatalf("compaction reset line %q missing %q", model.lines[0], want)
		}
	}
	if strings.Contains(model.lines[0], "input_tokens=") || strings.Contains(model.lines[0], "cache_outcome=") {
		t.Fatalf("compaction reset line %q looks like a request", model.lines[0])
	}
	if got, want := model.styleLine(model.lines[0]), logCompactionStyle.Render(model.lines[0]); got != want {
		t.Fatalf("compaction reset style = %q, want magenta %q", got, want)
	}
}

func TestObservabilityLogResetsSequenceOnRestartAndShowsGap(t *testing.T) {
	model := newLogsTabModel(nil, nil)
	model.SetObservabilityOnly(true)
	model.AddObservabilityEvents([]observabilityEvent{{Sequence: 10, Operation: "inference"}})
	model.AddObservabilityGap(11, 14)
	model.AddObservabilityGap(11, 14)
	model.ResetObservabilityCursor("new-boot")
	model.AddObservabilityEvents([]observabilityEvent{{Sequence: 1, Operation: "inference"}})

	if len(model.lines) != 4 {
		t.Fatalf("observability lines = %d, want event + gap + restart + new event", len(model.lines))
	}
	for _, want := range []string{"request_event_gap missing_sequences=11-14", `request_event_stream_restart boot_id="new-boot"`, "operation=inference"} {
		if !strings.Contains(strings.Join(model.lines, "\n"), want) {
			t.Fatalf("observability log lines %q missing %q", model.lines, want)
		}
	}
	if got, want := model.styleLine(model.lines[1]), logWarnStyle.Render(model.lines[1]); got != want {
		t.Fatalf("gap style = %q, want warning %q", got, want)
	}
}

func TestObservabilityCursorResetsWhileFileLogsAreActive(t *testing.T) {
	model := newLogsTabModel(nil, &LogHook{})
	model.lines = []string{"existing file log"}
	model.lastObservabilitySequence = 42
	model.lastObservabilityGapTo = 41

	model.ResetObservabilityCursor("new-boot")

	if model.lastObservabilitySequence != 0 || model.lastObservabilityGapTo != 0 {
		t.Fatalf("cursor/gap = %d/%d, want 0/0", model.lastObservabilitySequence, model.lastObservabilityGapTo)
	}
	if len(model.lines) != 1 || model.lines[0] != "existing file log" {
		t.Fatalf("file log lines = %q, want unchanged without restart marker", model.lines)
	}
}

func TestObservabilityPollErrorPreservesSnapshotAndShowsStaleIndicator(t *testing.T) {
	app := App{
		width:                    180,
		observability:            observabilitySnapshot{Requests: 7, PricedRequests: 1, EstimatedCostUSD: 0.5},
		observabilityLastSuccess: time.Now().Add(-time.Second),
		logs:                     newLogsTabModel(nil, nil),
	}
	updatedModel, _ := app.Update(observabilityPollMsg{err: errors.New("management API offline")})
	updated := updatedModel.(App)
	if updated.observability.Requests != 7 || updated.observability.EstimatedCostUSD != 0.5 {
		t.Fatalf("last snapshot was replaced on error: %+v", updated.observability)
	}
	rendered := updated.renderObservabilitySummary()
	for _, want := range []string{"Requests 7", "$0.5000", "stale", "management API offline"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("stale summary %q missing %q", rendered, want)
		}
	}
}

func TestObservabilityPollResetsCursorAcrossProcessRestart(t *testing.T) {
	logs := newLogsTabModel(nil, nil)
	logs.SetObservabilityOnly(true)
	app := App{
		observabilityBootID: "old-boot",
		observabilityCursor: 99,
		logs:                logs,
	}
	updatedModel, _ := app.Update(observabilityPollMsg{snapshot: observabilitySnapshot{
		BootID:      "new-boot",
		CursorReset: true,
		NextAfter:   1,
		RecentEvents: []observabilityEvent{{
			Sequence:  1,
			Operation: "inference",
		}},
	}})
	updated := updatedModel.(App)
	if updated.observabilityBootID != "new-boot" || updated.observabilityCursor != 1 {
		t.Fatalf("boot/cursor = %q/%d, want new-boot/1", updated.observabilityBootID, updated.observabilityCursor)
	}
	if len(updated.logs.lines) != 2 || !strings.Contains(updated.logs.lines[0], "request_event_stream_restart") || !strings.Contains(updated.logs.lines[1], "operation=inference") {
		t.Fatalf("restart log lines = %q, want restart marker then new event", updated.logs.lines)
	}
}
