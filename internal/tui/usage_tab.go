package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// usageTabModel displays usage statistics with charts and breakdowns.
type usageTabModel struct {
	client   *Client
	viewport viewport.Model
	usage    map[string]any
	err      error
	width    int
	height   int
	ready    bool
}

type usageDataMsg struct {
	usage map[string]any
	err   error
}

func newUsageTabModel(client *Client) usageTabModel {
	return usageTabModel{
		client: client,
	}
}

func (m usageTabModel) Init() tea.Cmd {
	return m.fetchData
}

func (m usageTabModel) fetchData() tea.Msg {
	usage, err := m.client.GetUsage()
	return usageDataMsg{usage: usage, err: err}
}

func (m usageTabModel) Update(msg tea.Msg) (usageTabModel, tea.Cmd) {
	switch msg := msg.(type) {
	case localeChangedMsg:
		m.viewport.SetContent(m.renderContent())
		return m, nil
	case usageDataMsg:
		if msg.err != nil {
			m.err = msg.err
		} else {
			m.err = nil
			m.usage = msg.usage
		}
		m.viewport.SetContent(m.renderContent())
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

func (m *usageTabModel) SetSize(w, h int) {
	m.width = w
	m.height = h
	if !m.ready {
		m.viewport = viewport.New(w, h)
		m.viewport.SetContent(m.renderContent())
		m.ready = true
	} else {
		m.viewport.Width = w
		m.viewport.Height = h
	}
}

func (m usageTabModel) View() string {
	if !m.ready {
		return T("loading")
	}
	return m.viewport.View()
}

func (m usageTabModel) renderContent() string {
	var sb strings.Builder

	sb.WriteString(titleStyle.Render(T("usage_title")))
	sb.WriteString("\n")
	sb.WriteString(helpStyle.Render(T("usage_help")))
	sb.WriteString("\n\n")

	if m.err != nil {
		sb.WriteString(errorStyle.Render("⚠ Error: " + m.err.Error()))
		sb.WriteString("\n")
		return sb.String()
	}

	if m.usage == nil {
		sb.WriteString(subtitleStyle.Render(T("usage_no_data")))
		sb.WriteString("\n")
		return sb.String()
	}

	usageMap, _ := m.usage["usage"].(map[string]any)
	if usageMap == nil {
		sb.WriteString(subtitleStyle.Render(T("usage_no_data")))
		sb.WriteString("\n")
		return sb.String()
	}

	totalReqs := int64(getFloat(usageMap, "total_requests"))
	successCnt := int64(getFloat(usageMap, "success_count"))
	failureCnt := int64(getFloat(usageMap, "failure_count"))
	totalTokens := int64(getFloat(usageMap, "total_tokens"))

	// ━━━ Overview Cards ━━━
	cardWidth := 20
	if m.width > 0 {
		cardWidth = (m.width - 6) / 4
		if cardWidth < 16 {
			cardWidth = 16
		}
	}
	cardStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("240")).
		Padding(0, 1).
		Width(cardWidth).
		Height(3)

	// Total Requests
	card1 := cardStyle.Copy().BorderForeground(lipgloss.Color("111")).Render(fmt.Sprintf(
		"%s\n%s\n%s",
		lipgloss.NewStyle().Foreground(colorMuted).Render(T("usage_total_reqs")),
		lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("111")).Render(fmt.Sprintf("%d", totalReqs)),
		lipgloss.NewStyle().Foreground(colorMuted).Render(fmt.Sprintf("● %s: %d  ● %s: %d", T("usage_success"), successCnt, T("usage_failure"), failureCnt)),
	))

	// Total Tokens
	card2 := cardStyle.Copy().BorderForeground(lipgloss.Color("214")).Render(fmt.Sprintf(
		"%s\n%s\n%s",
		lipgloss.NewStyle().Foreground(colorMuted).Render(T("usage_total_tokens")),
		lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("214")).Render(formatLargeNumber(totalTokens)),
		lipgloss.NewStyle().Foreground(colorMuted).Render(fmt.Sprintf("%s: %s", T("usage_total_token_l"), formatLargeNumber(totalTokens))),
	))

	// RPM
	rpm := float64(0)
	if totalReqs > 0 {
		if rByH, ok := usageMap["requests_by_hour"].(map[string]any); ok && len(rByH) > 0 {
			rpm = float64(totalReqs) / float64(len(rByH)) / 60.0
		}
	}
	card3 := cardStyle.Copy().BorderForeground(lipgloss.Color("76")).Render(fmt.Sprintf(
		"%s\n%s\n%s",
		lipgloss.NewStyle().Foreground(colorMuted).Render(T("usage_rpm")),
		lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("76")).Render(fmt.Sprintf("%.2f", rpm)),
		lipgloss.NewStyle().Foreground(colorMuted).Render(fmt.Sprintf("%s: %d", T("usage_total_reqs"), totalReqs)),
	))

	// TPM
	tpm := float64(0)
	if totalTokens > 0 {
		if tByH, ok := usageMap["tokens_by_hour"].(map[string]any); ok && len(tByH) > 0 {
			tpm = float64(totalTokens) / float64(len(tByH)) / 60.0
		}
	}
	card4 := cardStyle.Copy().BorderForeground(lipgloss.Color("170")).Render(fmt.Sprintf(
		"%s\n%s\n%s",
		lipgloss.NewStyle().Foreground(colorMuted).Render(T("usage_tpm")),
		lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("170")).Render(fmt.Sprintf("%.2f", tpm)),
		lipgloss.NewStyle().Foreground(colorMuted).Render(fmt.Sprintf("%s: %s", T("usage_total_tokens"), formatLargeNumber(totalTokens))),
	))

	sb.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, card1, " ", card2, " ", card3, " ", card4))
	sb.WriteString("\n\n")

	// ━━━ Requests by Hour (ASCII bar chart) ━━━
	if rByH, ok := usageMap["requests_by_hour"].(map[string]any); ok && len(rByH) > 0 {
		sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(colorHighlight).Render(T("usage_req_by_hour")))
		sb.WriteString("\n")
		sb.WriteString(strings.Repeat("─", minInt(m.width, 60)))
		sb.WriteString("\n")
		sb.WriteString(renderBarChart(rByH, m.width-6, lipgloss.Color("111")))
		sb.WriteString("\n")
	}

	// ━━━ Tokens by Hour ━━━
	if tByH, ok := usageMap["tokens_by_hour"].(map[string]any); ok && len(tByH) > 0 {
		sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(colorHighlight).Render(T("usage_tok_by_hour")))
		sb.WriteString("\n")
		sb.WriteString(strings.Repeat("─", minInt(m.width, 60)))
		sb.WriteString("\n")
		sb.WriteString(renderBarChart(tByH, m.width-6, lipgloss.Color("214")))
		sb.WriteString("\n")
	}

	// ━━━ Requests by Day ━━━
	if rByD, ok := usageMap["requests_by_day"].(map[string]any); ok && len(rByD) > 0 {
		sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(colorHighlight).Render(T("usage_req_by_day")))
		sb.WriteString("\n")
		sb.WriteString(strings.Repeat("─", minInt(m.width, 60)))
		sb.WriteString("\n")
		sb.WriteString(renderBarChart(rByD, m.width-6, lipgloss.Color("76")))
		sb.WriteString("\n")
	}

	// ━━━ API Detail Stats ━━━
	if apis, ok := usageMap["apis"].(map[string]any); ok && len(apis) > 0 {
		sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(colorHighlight).Render(T("usage_api_detail")))
		sb.WriteString("\n")
		sb.WriteString(strings.Repeat("─", minInt(m.width, 80)))
		sb.WriteString("\n")

		header := fmt.Sprintf("  %-30s %10s %12s", "API", T("requests"), T("tokens"))
		sb.WriteString(tableHeaderStyle.Render(header))
		sb.WriteString("\n")

		for apiName, apiSnap := range apis {
			if apiMap, ok := apiSnap.(map[string]any); ok {
				apiReqs := int64(getFloat(apiMap, "total_requests"))
				apiToks := int64(getFloat(apiMap, "total_tokens"))

				row := fmt.Sprintf("  %-30s %10d %12s",
					truncate(maskKey(apiName), 30), apiReqs, formatLargeNumber(apiToks))
				sb.WriteString(lipgloss.NewStyle().Bold(true).Render(row))
				sb.WriteString("\n")

				// Per-model breakdown
				if models, ok := apiMap["models"].(map[string]any); ok {
					for model, v := range models {
						if stats, ok := v.(map[string]any); ok {
							mReqs := int64(getFloat(stats, "total_requests"))
							mToks := int64(getFloat(stats, "total_tokens"))
							mRow := fmt.Sprintf("    ├─ %-28s %10d %12s",
								truncate(model, 28), mReqs, formatLargeNumber(mToks))
							sb.WriteString(tableCellStyle.Render(mRow))
							sb.WriteString("\n")

							// Token type breakdown from details
							sb.WriteString(m.renderTokenBreakdown(stats))
						}
					}
				}
			}
		}
	}

	sb.WriteString("\n")
	return sb.String()
}

// renderTokenBreakdown aggregates input/output/cached/reasoning tokens from model details.
func (m usageTabModel) renderTokenBreakdown(modelStats map[string]any) string {
	details, ok := modelStats["details"]
	if !ok {
		return ""
	}
	detailList, ok := details.([]any)
	if !ok || len(detailList) == 0 {
		return ""
	}

	var inputTotal, outputTotal, cachedTotal, reasoningTotal int64
	for _, d := range detailList {
		dm, ok := d.(map[string]any)
		if !ok {
			continue
		}
		tokens, ok := dm["tokens"].(map[string]any)
		if !ok {
			continue
		}
		inputTotal += int64(getFloat(tokens, "input_tokens"))
		outputTotal += int64(getFloat(tokens, "output_tokens"))
		cachedTotal += int64(getFloat(tokens, "cached_tokens"))
		reasoningTotal += int64(getFloat(tokens, "reasoning_tokens"))
	}

	if inputTotal == 0 && outputTotal == 0 && cachedTotal == 0 && reasoningTotal == 0 {
		return ""
	}

	parts := []string{}
	if inputTotal > 0 {
		parts = append(parts, fmt.Sprintf("%s:%s", T("usage_input"), formatLargeNumber(inputTotal)))
	}
	if outputTotal > 0 {
		parts = append(parts, fmt.Sprintf("%s:%s", T("usage_output"), formatLargeNumber(outputTotal)))
	}
	if cachedTotal > 0 {
		parts = append(parts, fmt.Sprintf("%s:%s", T("usage_cached"), formatLargeNumber(cachedTotal)))
	}
	if reasoningTotal > 0 {
		parts = append(parts, fmt.Sprintf("%s:%s", T("usage_reasoning"), formatLargeNumber(reasoningTotal)))
	}

	return fmt.Sprintf("    │  %s\n",
		lipgloss.NewStyle().Foreground(colorMuted).Render(strings.Join(parts, "  ")))
}

// renderBarChart renders a simple ASCII horizontal bar chart.
func renderBarChart(data map[string]any, maxBarWidth int, barColor lipgloss.Color) string {
	if maxBarWidth < 10 {
		maxBarWidth = 10
	}

	// Sort keys
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// Find max value
	maxVal := float64(0)
	for _, k := range keys {
		v := getFloat(data, k)
		if v > maxVal {
			maxVal = v
		}
	}
	if maxVal == 0 {
		return ""
	}

	barStyle := lipgloss.NewStyle().Foreground(barColor)
	var sb strings.Builder

	labelWidth := 12
	barAvail := maxBarWidth - labelWidth - 12
	if barAvail < 5 {
		barAvail = 5
	}

	for _, k := range keys {
		v := getFloat(data, k)
		barLen := int(v / maxVal * float64(barAvail))
		if barLen < 1 && v > 0 {
			barLen = 1
		}
		bar := strings.Repeat("█", barLen)
		label := k
		if len(label) > labelWidth {
			label = label[:labelWidth]
		}
		sb.WriteString(fmt.Sprintf("  %-*s %s %s\n",
			labelWidth, label,
			barStyle.Render(bar),
			lipgloss.NewStyle().Foreground(colorMuted).Render(fmt.Sprintf("%.0f", v)),
		))
	}

	return sb.String()
}
