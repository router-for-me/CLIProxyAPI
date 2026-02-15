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
}

type dashboardDataMsg struct {
	config    map[string]any
	usage     map[string]any
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
	usage, usageErr := m.client.GetUsage()
	authFiles, authErr := m.client.GetAuthFiles()
	apiKeys, keysErr := m.client.GetAPIKeys()

	var err error
	for _, e := range []error{cfgErr, usageErr, authErr, keysErr} {
		if e != nil {
			err = e
			break
		}
	}
	return dashboardDataMsg{config: cfg, usage: usage, authFiles: authFiles, apiKeys: apiKeys, err: err}
}

func (m dashboardModel) Update(msg tea.Msg) (dashboardModel, tea.Cmd) {
	switch msg := msg.(type) {
	case dashboardDataMsg:
		if msg.err != nil {
			m.err = msg.err
			m.content = errorStyle.Render("⚠ Error: " + msg.err.Error())
		} else {
			m.err = nil
			m.content = m.renderDashboard(msg.config, msg.usage, msg.authFiles, msg.apiKeys)
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
		return "Loading..."
	}
	return m.viewport.View()
}

func (m dashboardModel) renderDashboard(cfg, usage map[string]any, authFiles []map[string]any, apiKeys []string) string {
	var sb strings.Builder

	sb.WriteString(titleStyle.Render("📊 Dashboard"))
	sb.WriteString("\n")
	sb.WriteString(helpStyle.Render(" [r] refresh • [↑↓] scroll"))
	sb.WriteString("\n\n")

	// ━━━ Connection Status ━━━
	port := 0.0
	if cfg != nil {
		port = getFloat(cfg, "port")
	}
	connStyle := lipgloss.NewStyle().Bold(true).Foreground(colorSuccess)
	sb.WriteString(connStyle.Render("● 已连接"))
	sb.WriteString(fmt.Sprintf("  http://127.0.0.1:%.0f", port))
	sb.WriteString("\n\n")

	// ━━━ Stats Cards ━━━
	cardWidth := 25
	if m.width > 0 {
		cardWidth = (m.width - 6) / 4
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
		lipgloss.NewStyle().Foreground(colorMuted).Render("管理密钥"),
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
		lipgloss.NewStyle().Foreground(colorMuted).Render(fmt.Sprintf("认证文件 (%d active)", activeAuth)),
	))

	// Card 3: Total Requests
	totalReqs := int64(0)
	successReqs := int64(0)
	failedReqs := int64(0)
	totalTokens := int64(0)
	if usage != nil {
		if usageMap, ok := usage["usage"].(map[string]any); ok {
			totalReqs = int64(getFloat(usageMap, "total_requests"))
			successReqs = int64(getFloat(usageMap, "success_count"))
			failedReqs = int64(getFloat(usageMap, "failure_count"))
			totalTokens = int64(getFloat(usageMap, "total_tokens"))
		}
	}
	card3 := cardStyle.Render(fmt.Sprintf(
		"%s\n%s",
		lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("214")).Render(fmt.Sprintf("📈 %d", totalReqs)),
		lipgloss.NewStyle().Foreground(colorMuted).Render(fmt.Sprintf("请求 (✓%d ✗%d)", successReqs, failedReqs)),
	))

	// Card 4: Total Tokens
	tokenStr := formatLargeNumber(totalTokens)
	card4 := cardStyle.Render(fmt.Sprintf(
		"%s\n%s",
		lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("170")).Render(fmt.Sprintf("🔤 %s", tokenStr)),
		lipgloss.NewStyle().Foreground(colorMuted).Render("总 Tokens"),
	))

	sb.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, card1, " ", card2, " ", card3, " ", card4))
	sb.WriteString("\n\n")

	// ━━━ Current Config ━━━
	sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(colorHighlight).Render("当前配置"))
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
			{"启用调试模式", boolEmoji(debug)},
			{"启用使用统计", boolEmoji(usageEnabled)},
			{"启用日志记录到文件", boolEmoji(loggingToFile)},
			{"重试次数", fmt.Sprintf("%.0f", retry)},
		}
		if proxyURL != "" {
			configItems = append(configItems, struct {
				label string
				value string
			}{"代理 URL", proxyURL})
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
			labelStyle.Render("路由策略:"),
			valueStyle.Render(strategy)))
	}

	sb.WriteString("\n")

	// ━━━ Per-Model Usage ━━━
	if usage != nil {
		if usageMap, ok := usage["usage"].(map[string]any); ok {
			if apis, ok := usageMap["apis"].(map[string]any); ok && len(apis) > 0 {
				sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(colorHighlight).Render("模型统计"))
				sb.WriteString("\n")
				sb.WriteString(strings.Repeat("─", minInt(m.width, 60)))
				sb.WriteString("\n")

				header := fmt.Sprintf("  %-40s %10s %12s", "Model", "Requests", "Tokens")
				sb.WriteString(tableHeaderStyle.Render(header))
				sb.WriteString("\n")

				for _, apiSnap := range apis {
					if apiMap, ok := apiSnap.(map[string]any); ok {
						if models, ok := apiMap["models"].(map[string]any); ok {
							for model, v := range models {
								if stats, ok := v.(map[string]any); ok {
									reqs := int64(getFloat(stats, "total_requests"))
									toks := int64(getFloat(stats, "total_tokens"))
									row := fmt.Sprintf("  %-40s %10d %12s", truncate(model, 40), reqs, formatLargeNumber(toks))
									sb.WriteString(tableCellStyle.Render(row))
									sb.WriteString("\n")
								}
							}
						}
					}
				}
			}
		}
	}

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
		return "是 ✓"
	}
	return "否"
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
