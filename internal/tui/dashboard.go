package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// dashboardModel displays server info, stats cards, and config overview.
type dashboardModel struct {
	client   *Client
	viewport viewport.Model
	content  string
	err      error
	width    int
	height   int
	ready    bool

	// Cached data for re-rendering on locale change
	lastConfig    map[string]any
	lastAuthFiles []map[string]any
	lastAPIKeys   []string
}

type dashboardDataMsg struct {
	config    map[string]any
	authFiles []map[string]any
	apiKeys   []string
	err       error
}

func newDashboardModel(client *Client) dashboardModel {
	return dashboardModel{
		client: client,
	}
}

func (m dashboardModel) Init() tea.Cmd {
	return m.fetchData
}

func (m dashboardModel) fetchData() tea.Msg {
	cfg, cfgErr := m.client.GetConfig()
	authFiles, authErr := m.client.GetAuthFiles()
	apiKeys, keysErr := m.client.GetAPIKeys()

	var err error
	for _, e := range []error{cfgErr, authErr, keysErr} {
		if e != nil {
			err = e
			break
		}
	}
	return dashboardDataMsg{config: cfg, authFiles: authFiles, apiKeys: apiKeys, err: err}
}

func (m dashboardModel) Update(msg tea.Msg) (dashboardModel, tea.Cmd) {
	switch msg := msg.(type) {
	case localeChangedMsg:
		// Re-render immediately with cached data using new locale
		m.content = m.renderDashboard(m.lastConfig, m.lastAuthFiles, m.lastAPIKeys)
		m.viewport.SetContent(m.content)
		// Also fetch fresh data in background
		return m, m.fetchData

	case dashboardDataMsg:
		if msg.err != nil {
			m.err = msg.err
			m.content = errorStyle.Render("⚠ Error: " + msg.err.Error())
		} else {
			m.err = nil
			// Cache data for locale switching
			m.lastConfig = msg.config
			m.lastAuthFiles = msg.authFiles
			m.lastAPIKeys = msg.apiKeys

			m.content = m.renderDashboard(msg.config, msg.authFiles, msg.apiKeys)
		}
		m.viewport.SetContent(m.content)
		return m, nil

	case tea.KeyMsg:
		if msg.String() == "r" {
			return m, m.fetchData
		}
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd
	}

	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	return m, cmd
}

func (m *dashboardModel) SetSize(w, h int) {
	m.width = w
	m.height = h
	if !m.ready {
		m.viewport = viewport.New(w, h)
		m.viewport.SetContent(m.content)
		m.ready = true
	} else {
		m.viewport.Width = w
		m.viewport.Height = h
	}
}

func (m dashboardModel) View() string {
	if !m.ready {
		return T("loading")
	}
	return m.viewport.View()
}

func (m dashboardModel) renderDashboard(cfg map[string]any, authFiles []map[string]any, apiKeys []string) string {
	var sb strings.Builder

	sb.WriteString(titleStyle.Render(T("dashboard_title")))
	sb.WriteString("\n")
	sb.WriteString(helpStyle.Render(T("dashboard_help")))
	sb.WriteString("\n\n")

	// ━━━ Connection Status ━━━
	connStyle := lipgloss.NewStyle().Bold(true).Foreground(colorSuccess)
	sb.WriteString(connStyle.Render(T("connected")))
	sb.WriteString(fmt.Sprintf("  %s", m.client.baseURL))
	sb.WriteString("\n\n")

	// ━━━ Stats Cards ━━━
	cardWidth := 25
	if m.width > 0 {
		cardWidth = (m.width - 2) / 2
		if cardWidth < 18 {
			cardWidth = 18
		}
	}

	cardStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("240")).
		Padding(0, 1).
		Width(cardWidth).
		Height(2)

	// Card 1: API Keys
	keyCount := len(apiKeys)
	card1 := cardStyle.Render(fmt.Sprintf(
		"%s\n%s",
		lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("111")).Render(fmt.Sprintf("🔑 %d", keyCount)),
		lipgloss.NewStyle().Foreground(colorMuted).Render(T("mgmt_keys")),
	))

	// Card 2: Auth Files
	authCount := len(authFiles)
	activeAuth := 0
	for _, f := range authFiles {
		if !getBool(f, "disabled") {
			activeAuth++
		}
	}
	card2 := cardStyle.Render(fmt.Sprintf(
		"%s\n%s",
		lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("76")).Render(fmt.Sprintf("📄 %d", authCount)),
		lipgloss.NewStyle().Foreground(colorMuted).Render(fmt.Sprintf("%s (%d %s)", T("auth_files_label"), activeAuth, T("active_suffix"))),
	))

	sb.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, card1, " ", card2))
	sb.WriteString("\n\n")

	// ━━━ Current Config ━━━
	sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(colorHighlight).Render(T("current_config")))
	sb.WriteString("\n")
	sb.WriteString(strings.Repeat("─", minInt(m.width, 60)))
	sb.WriteString("\n")

	if cfg != nil {
		debug := getBool(cfg, "debug")
		retry := getFloat(cfg, "request-retry")
		proxyURL := getString(cfg, "proxy-url")
		loggingToFile := getBool(cfg, "logging-to-file")
		usageEnabled := true
		if v, ok := cfg["usage-statistics-enabled"]; ok {
			if b, ok2 := v.(bool); ok2 {
				usageEnabled = b
			}
		}

		configItems := []struct {
			label string
			value string
		}{
			{T("debug_mode"), boolEmoji(debug)},
			{T("usage_stats"), boolEmoji(usageEnabled)},
			{T("log_to_file"), boolEmoji(loggingToFile)},
			{T("retry_count"), fmt.Sprintf("%.0f", retry)},
		}
		if proxyURL != "" {
			configItems = append(configItems, struct {
				label string
				value string
			}{T("proxy_url"), proxyURL})
		}

		// Render config items as a compact row
		for _, item := range configItems {
			sb.WriteString(fmt.Sprintf("  %s %s\n",
				labelStyle.Render(item.label+":"),
				valueStyle.Render(item.value)))
		}

		// Routing strategy
		strategy := "round-robin"
		if routing, ok := cfg["routing"].(map[string]any); ok {
			if s := getString(routing, "strategy"); s != "" {
				strategy = s
			}
		}
		sb.WriteString(fmt.Sprintf("  %s %s\n",
			labelStyle.Render(T("routing_strategy")+":"),
			valueStyle.Render(strategy)))
	}

	sb.WriteString("\n")

	return sb.String()
}

func formatKV(key, value string) string {
	return fmt.Sprintf("  %s %s\n", labelStyle.Render(key+":"), valueStyle.Render(value))
}

func getString(m map[string]any, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func getFloat(m map[string]any, key string) float64 {
	if v, ok := m[key]; ok {
		switch n := v.(type) {
		case float64:
			return n
		case json.Number:
			f, _ := n.Float64()
			return f
		}
	}
	return 0
}

func getBool(m map[string]any, key string) bool {
	if v, ok := m[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}

func boolEmoji(b bool) string {
	if b {
		return T("bool_yes")
	}
	return T("bool_no")
}

func formatLargeNumber(n int64) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 1_000 {
		return fmt.Sprintf("%.1fK", float64(n)/1_000)
	}
	return fmt.Sprintf("%d", n)
}

func truncate(s string, maxLen int) string {
	if len(s) > maxLen {
		return s[:maxLen-3] + "..."
	}
	return s
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
