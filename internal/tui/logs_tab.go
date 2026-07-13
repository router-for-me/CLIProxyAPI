package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
)

// logsTabModel displays real-time log lines from hook/API source.
type logsTabModel struct {
	client                    *Client
	hook                      *LogHook
	viewport                  viewport.Model
	lines                     []string
	maxLines                  int
	autoScroll                bool
	width                     int
	height                    int
	ready                     bool
	filter                    string // "", "debug", "info", "warn", "error"
	after                     int64
	lastErr                   error
	observabilityOnly         bool
	lastObservabilitySequence uint64
	lastObservabilityGapTo    uint64
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
		m.viewport.SetContent(m.renderLogs())
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
		m.viewport.SetContent(m.renderLogs())
		if m.autoScroll {
			m.viewport.GotoBottom()
		}
		return m, m.waitForNextPoll()
	case logLineMsg:
		m.lines = append(m.lines, string(msg))
		if len(m.lines) > m.maxLines {
			m.lines = m.lines[len(m.lines)-m.maxLines:]
		}
		m.viewport.SetContent(m.renderLogs())
		if m.autoScroll {
			m.viewport.GotoBottom()
		}
		return m, m.waitForLog

	case tea.KeyMsg:
		switch msg.String() {
		case "a":
			m.autoScroll = !m.autoScroll
			if m.autoScroll {
				m.viewport.GotoBottom()
			}
			return m, nil
		case "c":
			m.lines = nil
			m.lastErr = nil
			m.viewport.SetContent(m.renderLogs())
			return m, nil
		case "1":
			m.filter = ""
			m.viewport.SetContent(m.renderLogs())
			return m, nil
		case "2":
			m.filter = "info"
			m.viewport.SetContent(m.renderLogs())
			return m, nil
		case "3":
			m.filter = "warn"
			m.viewport.SetContent(m.renderLogs())
			return m, nil
		case "4":
			m.filter = "error"
			m.viewport.SetContent(m.renderLogs())
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
	if m == nil || !m.observabilityOnly || len(events) == 0 {
		return
	}
	for _, event := range events {
		if event.Sequence <= m.lastObservabilitySequence {
			continue
		}
		m.lines = append(m.lines, formatObservabilityEventLine(event))
		m.lastObservabilitySequence = event.Sequence
	}
	if len(m.lines) > m.maxLines {
		m.lines = m.lines[len(m.lines)-m.maxLines:]
	}
	if m.ready {
		m.viewport.SetContent(m.renderLogs())
		if m.autoScroll {
			m.viewport.GotoBottom()
		}
	}
}

// ResetObservabilityCursor marks a server restart and permits the new process
// to reuse sequence numbers starting at one.
func (m *logsTabModel) ResetObservabilityCursor(bootID string) {
	if m == nil {
		return
	}
	m.lastObservabilitySequence = 0
	m.lastObservabilityGapTo = 0
	if !m.observabilityOnly {
		return
	}
	m.lines = append(m.lines, fmt.Sprintf("[warn] request_event_stream_restart boot_id=%q cursor_reset=true", bootID))
	m.refreshObservabilityView()
}

// AddObservabilityGap makes bounded-buffer loss explicit in the log stream.
func (m *logsTabModel) AddObservabilityGap(from, to uint64) {
	if m == nil || !m.observabilityOnly || from == 0 || to < from || to <= m.lastObservabilityGapTo {
		return
	}
	m.lines = append(m.lines, fmt.Sprintf("[warn] request_event_gap missing_sequences=%d-%d", from, to))
	m.lastObservabilityGapTo = to
	m.refreshObservabilityView()
}

func (m *logsTabModel) refreshObservabilityView() {
	if len(m.lines) > m.maxLines {
		m.lines = m.lines[len(m.lines)-m.maxLines:]
	}
	if m.ready {
		m.viewport.SetContent(m.renderLogs())
		if m.autoScroll {
			m.viewport.GotoBottom()
		}
	}
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
	if event.EstimatedCostAvailable {
		cost = fmt.Sprintf("%.8f", event.EstimatedCostUSD)
	}
	return fmt.Sprintf(
		"[info] request_event operation=%s provider=%q model=%q input_tokens=%d output_tokens=%d cache_read_tokens=%d cache_write_tokens=%d uncached_input_tokens=%d cache_read_percent=%.2f cache_low_reuse=%t cache_outcome=%s cache_miss=%t estimated_cost_usd=%s estimated_cost_tier=%s failed=%t",
		event.Operation,
		event.Provider,
		event.Model,
		event.InputTokens,
		event.OutputTokens,
		event.CacheReadTokens,
		event.CacheWriteTokens,
		event.UncachedInputTokens,
		event.CacheReadPercent,
		event.CacheLowReuse,
		event.CacheOutcome,
		event.CacheMiss,
		cost,
		event.EstimatedCostTier,
		event.Failed,
	)
}

func (m *logsTabModel) SetSize(w, h int) {
	m.width = w
	m.height = h
	if !m.ready {
		m.viewport = viewport.New(w, h)
		m.viewport.SetContent(m.renderLogs())
		m.ready = true
	} else {
		m.viewport.Width = w
		m.viewport.Height = h
	}
}

func (m logsTabModel) View() string {
	if !m.ready {
		return T("loading")
	}
	return m.viewport.View()
}

func (m logsTabModel) renderLogs() string {
	var sb strings.Builder

	scrollStatus := successStyle.Render(T("logs_auto_scroll"))
	if !m.autoScroll {
		scrollStatus = warningStyle.Render(T("logs_paused"))
	}
	filterLabel := "ALL"
	if m.filter != "" {
		filterLabel = strings.ToUpper(m.filter) + "+"
	}

	header := fmt.Sprintf(" %s  %s  %s: %s  %s: %d",
		T("logs_title"), scrollStatus, T("logs_filter"), filterLabel, T("logs_lines"), len(m.lines))
	sb.WriteString(titleStyle.Render(header))
	sb.WriteString("\n")
	sb.WriteString(helpStyle.Render(T("logs_help")))
	sb.WriteString("\n")
	sb.WriteString(strings.Repeat("─", m.width))
	sb.WriteString("\n")

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
		styled := m.styleLine(line)
		sb.WriteString(styled)
		sb.WriteString("\n")
	}

	return sb.String()
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
