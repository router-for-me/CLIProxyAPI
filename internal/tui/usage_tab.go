package tui

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const (
	availabilitySampleInterval   = 10 * time.Second
	availabilityHistoryMaxPoints = 30
)

// usageTabModel displays usage statistics with charts and breakdowns.
type usageTabModel struct {
	client              *Client
	viewport            viewport.Model
	usage               map[string]any
	authFiles           []map[string]any
	availability        []providerAvailabilitySnapshot
	availabilityHistory map[string][]float64
	err                 error
	width               int
	height              int
	ready               bool
	tickVersion         int
}

type oauthCredentialStatus struct {
	Name          string
	Email         string
	Status        string
	StatusMessage string
	Disabled      bool
	Unavailable   bool
}

type providerAvailabilitySnapshot struct {
	Provider        string
	DisplayName     string
	Total           int
	Available       int
	Unavailable     int
	Disabled        int
	AvailabilityPct float64
	Credentials     []oauthCredentialStatus
}

type usageDataMsg struct {
	usage map[string]any
	err   error
}

type authFilesDataMsg struct {
	files []map[string]any
	err   error
}

type usageSamplingTickMsg struct {
	version int
}

type usageResumeMsg struct{}

func newUsageTabModel(client *Client) usageTabModel {
	return usageTabModel{
		client:              client,
		availabilityHistory: make(map[string][]float64),
	}
}

func (m usageTabModel) Init() tea.Cmd {
	return func() tea.Msg {
		return usageResumeMsg{}
	}
}

func (m usageTabModel) fetchUsageData() tea.Msg {
	usage, err := m.client.GetUsage()
	return usageDataMsg{usage: usage, err: err}
}

func (m usageTabModel) fetchAuthFilesData() tea.Msg {
	files, err := m.client.GetAuthFiles()
	return authFilesDataMsg{files: files, err: err}
}

func (m usageTabModel) fetchAllData() tea.Cmd {
	return tea.Batch(m.fetchUsageData, m.fetchAuthFilesData)
}

func (m usageTabModel) scheduleSamplingTick(version int) tea.Cmd {
	return tea.Tick(availabilitySampleInterval, func(_ time.Time) tea.Msg {
		return usageSamplingTickMsg{version: version}
	})
}

func (m usageTabModel) Update(msg tea.Msg) (usageTabModel, tea.Cmd) {
	switch msg := msg.(type) {
	case localeChangedMsg:
		m.viewport.SetContent(m.renderContent())
		return m, nil
	case usageResumeMsg:
		m.tickVersion++
		version := m.tickVersion
		return m, tea.Batch(m.fetchAllData(), m.scheduleSamplingTick(version))
	case usageSamplingTickMsg:
		if msg.version != m.tickVersion {
			return m, nil
		}
		return m, tea.Batch(m.fetchAllData(), m.scheduleSamplingTick(msg.version))
	case usageDataMsg:
		if msg.err != nil {
			m.err = msg.err
		} else {
			m.err = nil
			m.usage = msg.usage
		}
		m.viewport.SetContent(m.renderContent())
		return m, nil
	case authFilesDataMsg:
		if msg.err == nil {
			m.authFiles = msg.files
			m.availability = buildProviderAvailabilitySnapshots(msg.files)
			m.recordAvailabilitySample(m.availability)
		}
		m.viewport.SetContent(m.renderContent())
		return m, nil
	case tea.KeyMsg:
		if msg.String() == "r" {
			return m, m.fetchAllData()
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
		sb.WriteString("\n")
		sb.WriteString(m.renderOAuthAvailabilitySection())
		sb.WriteString("\n")
		return sb.String()
	}

	if m.usage == nil {
		sb.WriteString(subtitleStyle.Render(T("usage_no_data")))
		sb.WriteString("\n")
		sb.WriteString("\n")
		sb.WriteString(m.renderOAuthAvailabilitySection())
		sb.WriteString("\n")
		return sb.String()
	}

	usageMap, _ := m.usage["usage"].(map[string]any)
	if usageMap == nil {
		sb.WriteString(subtitleStyle.Render(T("usage_no_data")))
		sb.WriteString("\n")
		sb.WriteString("\n")
		sb.WriteString(m.renderOAuthAvailabilitySection())
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

	sb.WriteString(m.renderOAuthAvailabilitySection())
	sb.WriteString("\n")

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

func (m *usageTabModel) recordAvailabilitySample(snapshots []providerAvailabilitySnapshot) {
	if len(snapshots) == 0 {
		return
	}
	if m.availabilityHistory == nil {
		m.availabilityHistory = make(map[string][]float64)
	}
	for _, snapshot := range snapshots {
		key := normalizeProviderKey(snapshot.Provider)
		if key == "" {
			continue
		}
		history := append(m.availabilityHistory[key], snapshot.AvailabilityPct)
		if len(history) > availabilityHistoryMaxPoints {
			history = history[len(history)-availabilityHistoryMaxPoints:]
		}
		m.availabilityHistory[key] = history
	}
}

func (m usageTabModel) renderOAuthAvailabilitySection() string {
	var sb strings.Builder
	sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(colorHighlight).Render(T("usage_oauth_availability_title")))
	sb.WriteString("\n")
	sb.WriteString(helpStyle.Render(T("usage_oauth_availability_hint")))
	sb.WriteString("\n")
	sb.WriteString(strings.Repeat("─", minInt(m.width, 80)))
	sb.WriteString("\n")

	if len(m.availability) == 0 {
		sb.WriteString(subtitleStyle.Render(T("usage_oauth_availability_empty")))
		sb.WriteString("\n")
		return sb.String()
	}

	percentData := make(map[string]any, len(m.availability))
	for _, snapshot := range m.availability {
		percentData[snapshot.DisplayName] = snapshot.AvailabilityPct
	}
	sb.WriteString(renderBarChart(percentData, m.width-6, lipgloss.Color("76")))

	sb.WriteString(helpStyle.Render(T("usage_oauth_availability_legend")))
	sb.WriteString("\n")
	for _, snapshot := range m.availability {
		sb.WriteString(fmt.Sprintf("  %-12s %d/%d available (%.1f%%)\n",
			snapshot.DisplayName, snapshot.Available, snapshot.Total, snapshot.AvailabilityPct))
	}
	sb.WriteString("\n")

	sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(colorHighlight).Render(T("usage_oauth_trend_title")))
	sb.WriteString("\n")
	for _, snapshot := range m.availability {
		history := m.availabilityHistory[normalizeProviderKey(snapshot.Provider)]
		line := renderSparkline(history)
		if line == "" {
			line = "-"
		}
		sb.WriteString(fmt.Sprintf("  %-12s %s  %5.1f%%\n", snapshot.DisplayName, line, snapshot.AvailabilityPct))
	}
	sb.WriteString("\n")

	sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(colorHighlight).Render(T("usage_oauth_credential_title")))
	sb.WriteString("\n")
	sb.WriteString(helpStyle.Render(T("usage_oauth_credential_hint")))
	sb.WriteString("\n")
	for _, snapshot := range m.availability {
		sb.WriteString(fmt.Sprintf("  %s\n", lipgloss.NewStyle().Bold(true).Render(snapshot.DisplayName)))
		for _, credential := range snapshot.Credentials {
			status := T("status_active")
			statusStyle := successStyle
			if credential.Disabled {
				status = T("status_disabled")
				statusStyle = warningStyle
			} else if credential.Unavailable {
				status = T("usage_oauth_status_unavailable")
				statusStyle = warningStyle
			}
			message := strings.TrimSpace(credential.StatusMessage)
			if message == "" {
				message = strings.TrimSpace(credential.Status)
			}
			if message == "" {
				message = T("not_set")
			}
			displayName := strings.TrimSpace(credential.Name)
			if displayName == "" {
				displayName = T("not_set")
			}
			email := strings.TrimSpace(credential.Email)
			if email == "" {
				email = T("not_set")
			}
			sb.WriteString(fmt.Sprintf("    • %-20s %-28s %-12s %s\n",
				truncate(displayName, 20), truncate(email, 28), statusStyle.Render(status), truncate(message, 44)))
		}
	}

	return sb.String()
}

func buildProviderAvailabilitySnapshots(files []map[string]any) []providerAvailabilitySnapshot {
	type accumulator struct {
		provider    string
		displayName string
		total       int
		available   int
		unavailable int
		disabled    int
		credentials []oauthCredentialStatus
	}

	accumulators := make(map[string]*accumulator)
	for _, file := range files {
		if !isOAuthCredential(file) {
			continue
		}
		provider := normalizeProviderKey(stringValue(file["provider"]))
		if provider == "" {
			provider = normalizeProviderKey(stringValue(file["type"]))
		}
		if provider == "" {
			provider = "unknown"
		}

		acc := accumulators[provider]
		if acc == nil {
			acc = &accumulator{
				provider:    provider,
				displayName: providerDisplayName(provider),
			}
			accumulators[provider] = acc
		}

		disabled := boolValue(file["disabled"])
		unavailable := boolValue(file["unavailable"])
		acc.total++
		switch {
		case disabled:
			acc.disabled++
		case unavailable:
			acc.unavailable++
		default:
			acc.available++
		}
		acc.credentials = append(acc.credentials, oauthCredentialStatus{
			Name:          strings.TrimSpace(stringValue(file["name"])),
			Email:         strings.TrimSpace(stringValue(file["email"])),
			Status:        strings.TrimSpace(stringValue(file["status"])),
			StatusMessage: strings.TrimSpace(stringValue(file["status_message"])),
			Disabled:      disabled,
			Unavailable:   unavailable,
		})
	}

	snapshots := make([]providerAvailabilitySnapshot, 0, len(accumulators))
	for _, acc := range accumulators {
		if acc.total <= 0 {
			continue
		}
		sort.Slice(acc.credentials, func(i, j int) bool {
			return strings.ToLower(acc.credentials[i].Name) < strings.ToLower(acc.credentials[j].Name)
		})
		snapshots = append(snapshots, providerAvailabilitySnapshot{
			Provider:        acc.provider,
			DisplayName:     acc.displayName,
			Total:           acc.total,
			Available:       acc.available,
			Unavailable:     acc.unavailable,
			Disabled:        acc.disabled,
			AvailabilityPct: clampPercentage(float64(acc.available) / float64(acc.total) * 100),
			Credentials:     acc.credentials,
		})
	}
	sort.Slice(snapshots, func(i, j int) bool {
		if snapshots[i].AvailabilityPct == snapshots[j].AvailabilityPct {
			return snapshots[i].DisplayName < snapshots[j].DisplayName
		}
		return snapshots[i].AvailabilityPct > snapshots[j].AvailabilityPct
	})
	return snapshots
}

func renderSparkline(history []float64) string {
	if len(history) == 0 {
		return ""
	}
	const glyphs = "▁▂▃▄▅▆▇█"
	levels := []rune(glyphs)
	var sb strings.Builder
	for _, value := range history {
		clamped := clampPercentage(value)
		index := int(math.Round(clamped / 100 * float64(len(levels)-1)))
		if index < 0 {
			index = 0
		}
		if index >= len(levels) {
			index = len(levels) - 1
		}
		sb.WriteRune(levels[index])
	}
	return sb.String()
}

func clampPercentage(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}

func isOAuthCredential(entry map[string]any) bool {
	accountType := strings.ToLower(strings.TrimSpace(stringValue(entry["account_type"])))
	if accountType != "" {
		return accountType == "oauth"
	}
	authType := strings.ToLower(strings.TrimSpace(stringValue(entry["auth_type"])))
	if authType != "" {
		return authType == "oauth"
	}
	provider := normalizeProviderKey(stringValue(entry["provider"]))
	if provider == "" {
		provider = normalizeProviderKey(stringValue(entry["type"]))
	}
	switch provider {
	case "gemini-cli", "claude", "codex", "antigravity", "qwen", "kimi", "iflow", "vertex", "aistudio":
		return true
	default:
		return false
	}
}

func normalizeProviderKey(provider string) string {
	key := strings.ToLower(strings.TrimSpace(provider))
	if key == "" {
		return ""
	}
	switch key {
	case "anthropic":
		return "claude"
	default:
		return key
	}
}

func providerDisplayName(provider string) string {
	switch normalizeProviderKey(provider) {
	case "gemini-cli":
		return "Gemini"
	case "claude":
		return "Claude"
	case "codex":
		return "Codex"
	case "antigravity":
		return "Antigravity"
	case "qwen":
		return "Qwen"
	case "kimi":
		return "Kimi"
	case "iflow":
		return "IFlow"
	case "vertex":
		return "Vertex"
	case "aistudio":
		return "AI Studio"
	default:
		trimmed := strings.TrimSpace(provider)
		if trimmed == "" {
			return "Unknown"
		}
		return strings.ToUpper(trimmed[:1]) + trimmed[1:]
	}
}

func stringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", typed)
	}
}

func boolValue(value any) bool {
	typed, ok := value.(bool)
	if !ok {
		return false
	}
	return typed
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
