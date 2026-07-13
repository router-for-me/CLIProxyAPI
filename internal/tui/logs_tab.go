package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// logsTabModel displays real-time log lines from hook/API source.
type logsTabModel struct {
	client                    *Client
	hook                      *LogHook
	viewport                  viewport.Model
	lines                     []string
	requestEvents             []observabilityEvent
	maxLines                  int
	autoScroll                bool
	showRaw                   bool
	width                     int
	height                    int
	ready                     bool
	filter                    string // "", "debug", "info", "warn", "error"
	after                     int64
	lastErr                   error
	observabilityOnly         bool
	lastObservabilitySequence uint64
	lastObservabilityGapTo    uint64
	observabilityGapWarning   string
}

type logsPollMsg struct {
	lines  []string
	latest int64
	err    error
}

type logsTickMsg struct{}
type logLineMsg string

func newLogsTabModel(client *Client, hook *LogHook) logsTabModel {
	return logsTabModel{
		client:     client,
		hook:       hook,
		maxLines:   5000,
		autoScroll: true,
	}
}

func (m logsTabModel) Init() tea.Cmd {
	if m.hook != nil {
		return m.waitForLog
	}
	if m.observabilityOnly {
		return nil
	}
	return m.fetchLogs
}

func (m logsTabModel) fetchLogs() tea.Msg {
	lines, latest, err := m.client.GetLogs(m.after, 200)
	return logsPollMsg{
		lines:  lines,
		latest: latest,
		err:    err,
	}
}

func (m logsTabModel) waitForNextPoll() tea.Cmd {
	return tea.Tick(2*time.Second, func(_ time.Time) tea.Msg {
		return logsTickMsg{}
	})
}

func (m logsTabModel) waitForLog() tea.Msg {
	if m.hook == nil {
		return nil
	}
	line, ok := <-m.hook.Chan()
	if !ok {
		return nil
	}
	return logLineMsg(line)
}

func (m logsTabModel) Update(msg tea.Msg) (logsTabModel, tea.Cmd) {
	switch msg := msg.(type) {
	case localeChangedMsg:
		m.refreshViewport()
		return m, nil
	case logsTickMsg:
		if m.hook != nil || m.observabilityOnly {
			return m, nil
		}
		return m, m.fetchLogs
	case logsPollMsg:
		if m.hook != nil {
			return m, nil
		}
		if msg.err != nil {
			m.lastErr = msg.err
		} else {
			m.lastErr = nil
			m.after = msg.latest
			if len(msg.lines) > 0 {
				m.lines = append(m.lines, msg.lines...)
				if len(m.lines) > m.maxLines {
					m.lines = m.lines[len(m.lines)-m.maxLines:]
				}
			}
		}
		m.refreshViewport()
		return m, m.waitForNextPoll()
	case logLineMsg:
		m.lines = append(m.lines, string(msg))
		if len(m.lines) > m.maxLines {
			m.lines = m.lines[len(m.lines)-m.maxLines:]
		}
		m.refreshViewport()
		return m, m.waitForLog

	case tea.KeyMsg:
		switch msg.String() {
		case "a":
			m.autoScroll = !m.autoScroll
			if m.autoScroll {
				m.viewport.GotoBottom()
			}
			return m, nil
		case "v":
			m.showRaw = !m.showRaw
			m.refreshViewport()
			return m, nil
		case "c":
			m.lines = nil
			m.requestEvents = nil
			m.lastErr = nil
			m.observabilityGapWarning = ""
			m.refreshViewport()
			return m, nil
		case "1":
			m.filter = ""
			m.refreshViewport()
			return m, nil
		case "2":
			m.filter = "info"
			m.refreshViewport()
			return m, nil
		case "3":
			m.filter = "warn"
			m.refreshViewport()
			return m, nil
		case "4":
			m.filter = "error"
			m.refreshViewport()
			return m, nil
		default:
			wasAtBottom := m.viewport.AtBottom()
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			// If user scrolls up, disable auto-scroll
			if !m.viewport.AtBottom() && wasAtBottom {
				m.autoScroll = false
			}
			// If user scrolls to bottom, re-enable auto-scroll
			if m.viewport.AtBottom() {
				m.autoScroll = true
			}
			return m, cmd
		}
	}

	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	return m, cmd
}

func (m *logsTabModel) SetObservabilityOnly(enabled bool) {
	if m == nil || m.hook != nil {
		return
	}
	m.observabilityOnly = enabled
	if enabled {
		m.lastErr = nil
	}
}

func (m *logsTabModel) AddObservabilityEvents(events []observabilityEvent) {
	if m == nil || len(events) == 0 {
		return
	}
	for _, event := range events {
		if event.Sequence <= m.lastObservabilitySequence {
			continue
		}
		m.requestEvents = append(m.requestEvents, event)
		if m.observabilityOnly {
			// Preserve the safe, text-only diagnostic stream for API-only TUI
			// clients while the visible monitor uses structured table rows.
			m.lines = append(m.lines, formatObservabilityEventLine(event))
		}
		m.lastObservabilitySequence = event.Sequence
	}
	if len(m.requestEvents) > m.maxLines {
		m.requestEvents = m.requestEvents[len(m.requestEvents)-m.maxLines:]
	}
	if len(m.lines) > m.maxLines {
		m.lines = m.lines[len(m.lines)-m.maxLines:]
	}
	m.refreshViewport()
}

// ResetObservabilityCursor marks a server restart and permits the new process
// to reuse sequence numbers starting at one.
func (m *logsTabModel) ResetObservabilityCursor(bootID string) {
	if m == nil {
		return
	}
	m.lastObservabilitySequence = 0
	m.lastObservabilityGapTo = 0
	m.observabilityGapWarning = ""
	m.requestEvents = nil
	if !m.observabilityOnly {
		return
	}
	m.lines = append(m.lines, fmt.Sprintf("[warn] request_event_stream_restart boot_id=%q cursor_reset=true", bootID))
	m.refreshObservabilityView()
}

// AddObservabilityGap makes bounded-buffer loss explicit in the log stream.
func (m *logsTabModel) AddObservabilityGap(from, to uint64) {
	if m == nil || from == 0 || to < from || to <= m.lastObservabilityGapTo {
		return
	}
	warning := fmt.Sprintf("request event gap: missing sequences %d-%d", from, to)
	m.observabilityGapWarning = warning
	if m.observabilityOnly {
		m.lines = append(m.lines, fmt.Sprintf("[warn] request_event_gap missing_sequences=%d-%d", from, to))
	}
	m.lastObservabilityGapTo = to
	m.refreshObservabilityView()
}

func (m *logsTabModel) refreshObservabilityView() {
	if len(m.lines) > m.maxLines {
		m.lines = m.lines[len(m.lines)-m.maxLines:]
	}
	m.refreshViewport()
}

func formatObservabilityEventLine(event observabilityEvent) string {
	if event.Operation == "compaction_reset" {
		return fmt.Sprintf(
			"[warn] compaction_event operation=compaction_reset provider=%q model=%q reason=%q lane_id=%q agent_id=%q previous_envelope_id=%q envelope_id=%q",
			event.Provider,
			event.Model,
			event.ResetReason,
			event.LaneID,
			event.AgentID,
			event.PreviousEnvelopeID,
			event.EnvelopeID,
		)
	}
	cost := "unavailable"
	inputCost := "unavailable"
	outputCost := "unavailable"
	cacheCost := "unavailable"
	if event.EstimatedCostAvailable {
		cost = fmt.Sprintf("%.8f", event.EstimatedCostUSD)
		inputCost = fmt.Sprintf("%.8f", event.EstimatedInputCostUSD)
		outputCost = fmt.Sprintf("%.8f", event.EstimatedOutputCostUSD)
		cacheCost = fmt.Sprintf("%.8f", event.EstimatedCacheCostUSD)
	}
	return fmt.Sprintf(
		"[info] request_event operation=%s provider=%q model=%q effort=%q input_tokens=%d output_tokens=%d cache_read_tokens=%d cache_write_tokens=%d cache_write_estimated=%t uncached_input_tokens=%d cache_read_percent=%.2f cache_low_reuse=%t cache_outcome=%s cache_miss=%t estimated_input_cost_usd=%s estimated_output_cost_usd=%s estimated_cache_cost_usd=%s estimated_cost_usd=%s estimated_cost_tier=%s failed=%t",
		event.Operation,
		event.Provider,
		event.Model,
		event.Effort,
		event.InputTokens,
		event.OutputTokens,
		event.CacheReadTokens,
		event.CacheWriteTokens,
		event.CacheWriteEstimated,
		event.UncachedInputTokens,
		event.CacheReadPercent,
		event.CacheLowReuse,
		event.CacheOutcome,
		event.CacheMiss,
		inputCost,
		outputCost,
		cacheCost,
		cost,
		event.EstimatedCostTier,
		event.Failed,
	)
}

func (m *logsTabModel) SetSize(w, h int) {
	m.width = w
	m.height = h
	viewportHeight := h - lipgloss.Height(m.renderMonitorHeader()) - 1
	if viewportHeight < 1 {
		viewportHeight = 1
	}
	if !m.ready {
		m.viewport = viewport.New(w, viewportHeight)
		m.viewport.SetContent(m.renderLogs())
		m.ready = true
	} else {
		m.viewport.Width = w
		m.viewport.Height = viewportHeight
		m.viewport.SetContent(m.renderLogs())
	}
}

func (m logsTabModel) View() string {
	if !m.ready {
		return T("loading")
	}
	return m.renderMonitorHeader() + "\n" + m.viewport.View()
}

func (m logsTabModel) renderLogs() string {
	if m.showRaw {
		return m.renderRawLogs()
	}
	return m.renderRequestRows()
}

func (m logsTabModel) renderRequestRows() string {
	var sb strings.Builder

	if m.lastErr != nil {
		sb.WriteString(errorStyle.Render("⚠ Error: " + m.lastErr.Error()))
		sb.WriteString("\n")
	}
	if m.observabilityGapWarning != "" {
		sb.WriteString(warningStyle.Render("⚠ " + m.observabilityGapWarning))
		sb.WriteString("\n")
	}

	if len(m.requestEvents) == 0 {
		sb.WriteString(subtitleStyle.Render(T("logs_waiting")))
		return sb.String()
	}

	matched := 0
	for _, event := range m.requestEvents {
		if !m.matchRequestEvent(event) {
			continue
		}
		line := formatRequestTableRow(event, m.width)
		sb.WriteString(styleRequestTableRow(event, line))
		sb.WriteString("\n")
		matched++
	}
	if matched == 0 {
		sb.WriteString(subtitleStyle.Render(T("logs_no_matches")))
	}

	return sb.String()
}

func (m logsTabModel) renderRawLogs() string {
	var sb strings.Builder
	if m.lastErr != nil {
		sb.WriteString(errorStyle.Render("⚠ Error: " + m.lastErr.Error()))
		sb.WriteString("\n")
	}
	if len(m.lines) == 0 {
		sb.WriteString(subtitleStyle.Render(T("logs_waiting")))
		return sb.String()
	}
	for _, line := range m.lines {
		if m.filter != "" && !m.matchLevel(line) {
			continue
		}
		sb.WriteString(m.styleLine(line))
		sb.WriteString("\n")
	}
	return sb.String()
}

func (m logsTabModel) renderMonitorHeader() string {
	scrollStatus := successStyle.Render(T("logs_auto_scroll"))
	if !m.autoScroll {
		scrollStatus = warningStyle.Render(T("logs_paused"))
	}
	filterLabel := "ALL"
	if m.filter != "" {
		filterLabel = strings.ToUpper(m.filter) + "+"
	}
	viewLabel := T("logs_view_requests")
	countLabel := T("logs_requests")
	count := len(m.requestEvents)
	if m.showRaw {
		viewLabel = T("logs_view_raw")
		countLabel = T("logs_lines")
		count = len(m.lines)
	}
	header := fmt.Sprintf(" %s  %s  %s: %s  %s: %s  %s: %d",
		T("logs_title"), scrollStatus, T("logs_view"), viewLabel, T("logs_filter"), filterLabel, countLabel, count)
	parts := []string{
		titleStyle.Render(header),
		helpStyle.Render(T("logs_help")),
	}
	if !m.showRaw {
		parts = append(parts, renderRequestTableHeader(m.width))
	}
	return strings.Join(parts, "\n")
}

func (m logsTabModel) matchRequestEvent(event observabilityEvent) bool {
	switch m.filter {
	case "error":
		return event.Failed || event.CacheMiss || event.CacheLowReuse
	case "warn":
		return event.Failed || event.CacheMiss || event.CacheLowReuse || event.Operation == "compaction_reset"
	default:
		return true
	}
}

func (m *logsTabModel) refreshViewport() {
	if m == nil || !m.ready {
		return
	}
	viewportHeight := m.height - lipgloss.Height(m.renderMonitorHeader()) - 1
	if viewportHeight < 1 {
		viewportHeight = 1
	}
	m.viewport.Width = m.width
	m.viewport.Height = viewportHeight
	m.viewport.SetContent(m.renderLogs())
	if m.autoScroll {
		m.viewport.GotoBottom()
	}
}

func (m logsTabModel) matchLevel(line string) bool {
	switch m.filter {
	case "error":
		return strings.Contains(line, "[error]") || strings.Contains(line, "[fatal]") || strings.Contains(line, "[panic]")
	case "warn":
		return strings.Contains(line, "[warn") || strings.Contains(line, "[error]") || strings.Contains(line, "[fatal]")
	case "info":
		return !strings.Contains(line, "[debug]")
	default:
		return true
	}
}

func (m logsTabModel) styleLine(line string) string {
	lower := strings.ToLower(line)
	if strings.Contains(lower, "request_event") &&
		(strings.Contains(lower, "cache_outcome=miss") || strings.Contains(lower, "cache_miss=true") || strings.Contains(lower, "cache_low_reuse=true")) {
		return logErrorStyle.Render(line)
	}
	if strings.Contains(lower, "compaction_event") {
		return logCompactionStyle.Render(line)
	}
	if strings.Contains(lower, "request_event") && strings.Contains(lower, "operation=compaction") {
		return logCompactionStyle.Render(line)
	}
	if strings.Contains(line, "[error]") || strings.Contains(line, "[fatal]") {
		return logErrorStyle.Render(line)
	}
	if strings.Contains(line, "[warn") {
		return logWarnStyle.Render(line)
	}
	if strings.Contains(line, "[info") {
		return logInfoStyle.Render(line)
	}
	if strings.Contains(line, "[debug]") {
		return logDebugStyle.Render(line)
	}
	return line
}
