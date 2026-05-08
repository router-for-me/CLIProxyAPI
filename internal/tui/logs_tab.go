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
	client     *Client
	hook       *LogHook
	viewport   viewport.Model
	lines      []string
	maxLines   int
	autoScroll bool
	width      int
	height     int
	ready      bool
	filter     string // "", "debug", "info", "warn", "error"
	after      int64
	lastErr    error
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
		if m.hook != nil {
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
