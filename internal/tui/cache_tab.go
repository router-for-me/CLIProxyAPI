package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// cacheTabModel displays prompt-cache statistics: a global header, a session
// table, and a per-session request drill-down view.
type cacheTabModel struct {
	client   *Client
	viewport viewport.Model
	width    int
	height   int
	ready    bool

	stats  *CacheStats
	err    error
	cursor int

	// Detail view state
	detail        bool
	detailID      string
	detailLoading bool
	detailData    *CacheSessionDetail
	detailErr     error
}

type cacheDataMsg struct {
	stats *CacheStats
	err   error
}

type cacheSessionMsg struct {
	id     string
	detail *CacheSessionDetail
	err    error
}

func newCacheTabModel(client *Client) cacheTabModel {
	return cacheTabModel{client: client}
}

func (m cacheTabModel) Init() tea.Cmd {
	return m.fetchData
}

func (m cacheTabModel) fetchData() tea.Msg {
	stats, err := m.client.GetCacheStats()
	return cacheDataMsg{stats: stats, err: err}
}

func (m cacheTabModel) fetchSessionCmd(id string) tea.Cmd {
	client := m.client
	return func() tea.Msg {
		detail, err := client.GetCacheSession(id)
		return cacheSessionMsg{id: id, detail: detail, err: err}
	}
}

func (m cacheTabModel) Update(msg tea.Msg) (cacheTabModel, tea.Cmd) {
	switch msg := msg.(type) {
	case localeChangedMsg:
		m.viewport.SetContent(m.renderContent())
		return m, m.fetchData

	case cacheDataMsg:
		if msg.err != nil {
			m.err = msg.err
		} else {
			m.err = nil
			m.stats = msg.stats
			if n := m.sessionCount(); n == 0 {
				m.cursor = 0
			} else if m.cursor >= n {
				m.cursor = n - 1
			}
		}
		m.viewport.SetContent(m.renderContent())
		return m, nil

	case cacheSessionMsg:
		if msg.id != m.detailID {
			return m, nil
		}
		m.detailLoading = false
		if msg.err != nil {
			m.detailErr = msg.err
			m.detailData = nil
		} else {
			m.detailErr = nil
			m.detailData = msg.detail
		}
		m.viewport.SetContent(m.renderContent())
		return m, nil

	case tea.KeyMsg:
		if m.detail {
			switch msg.String() {
			case "esc":
				m.detail = false
				m.detailID = ""
				m.detailData = nil
				m.detailErr = nil
				m.detailLoading = false
				m.viewport.SetContent(m.renderContent())
				m.viewport.GotoTop()
				return m, nil
			case "r":
				if m.detailID != "" {
					m.detailLoading = true
					m.viewport.SetContent(m.renderContent())
					return m, m.fetchSessionCmd(m.detailID)
				}
				return m, nil
			case "j", "down":
				m.viewport.LineDown(1)
				return m, nil
			case "k", "up":
				m.viewport.LineUp(1)
				return m, nil
			default:
				var cmd tea.Cmd
				m.viewport, cmd = m.viewport.Update(msg)
				return m, cmd
			}
		}

		switch msg.String() {
		case "j", "down":
			if n := m.sessionCount(); n > 0 {
				m.cursor = (m.cursor + 1) % n
				m.viewport.SetContent(m.renderContent())
			}
			return m, nil
		case "k", "up":
			if n := m.sessionCount(); n > 0 {
				m.cursor = (m.cursor - 1 + n) % n
				m.viewport.SetContent(m.renderContent())
			}
			return m, nil
		case "enter":
			sess := m.selectedSession()
			if sess == nil {
				return m, nil
			}
			m.detail = true
			m.detailID = sess.ID
			m.detailData = nil
			m.detailErr = nil
			m.detailLoading = true
			m.viewport.SetContent(m.renderContent())
			m.viewport.GotoTop()
			return m, m.fetchSessionCmd(sess.ID)
		case "r":
			return m, m.fetchData
		default:
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			return m, cmd
		}
	}

	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	return m, cmd
}

func (m *cacheTabModel) SetSize(w, h int) {
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

func (m cacheTabModel) View() string {
	if !m.ready {
		return T("loading")
	}
	return m.viewport.View()
}

func (m cacheTabModel) sessionCount() int {
	if m.stats == nil {
		return 0
	}
	return len(m.stats.Sessions)
}

func (m cacheTabModel) selectedSession() *CacheSession {
	if m.stats == nil || m.cursor < 0 || m.cursor >= len(m.stats.Sessions) {
		return nil
	}
	return &m.stats.Sessions[m.cursor]
}

func (m cacheTabModel) renderContent() string {
	if m.detail {
		return m.renderDetail()
	}
	return m.renderList()
}

// ──────────────────────────────────────────
// List view
// ──────────────────────────────────────────

func (m cacheTabModel) renderList() string {
	var sb strings.Builder

	sb.WriteString(titleStyle.Render(T("cache_title")))
	sb.WriteString("\n")
	sb.WriteString(helpStyle.Render(T("cache_help")))
	sb.WriteString("\n\n")

	if m.err != nil {
		sb.WriteString(errorStyle.Render(T("error_prefix") + m.err.Error()))
		sb.WriteString("\n\n")
		sb.WriteString(subtitleStyle.Render(T("cache_unreachable")))
		sb.WriteString("\n")
		return sb.String()
	}

	if m.stats == nil {
		sb.WriteString(subtitleStyle.Render(T("loading")))
		sb.WriteString("\n")
		return sb.String()
	}

	if !m.stats.Enabled {
		sb.WriteString(warningStyle.Render(T("cache_disabled")))
		sb.WriteString("\n\n")
	}

	sb.WriteString(m.renderGlobalHeader(m.stats.Global))
	sb.WriteString("\n")

	sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(colorHighlight).Render(T("cache_sessions_section")))
	sb.WriteString("\n")

	if len(m.stats.Sessions) == 0 {
		sb.WriteString(subtitleStyle.Render(T("cache_no_sessions")))
		sb.WriteString("\n")
		return sb.String()
	}

	header := cacheJoinRow([]string{
		cachePad(T("cache_col_session"), cacheColSession),
		cachePad(T("cache_col_model"), cacheColModel),
		cachePadLeft(T("cache_col_requests"), cacheColReq),
		cachePadLeft(T("cache_col_hitrate"), cacheColHit),
		cachePadLeft(T("cache_col_misses"), cacheColMiss),
		cachePadLeft(T("cache_col_t0"), cacheColT0),
		cachePadLeft(T("cache_col_lost"), cacheColLost),
		cachePad(T("cache_col_last_seen"), cacheColSeen),
	})
	sb.WriteString(tableHeaderStyle.Render("  " + header))
	sb.WriteString("\n")

	for i, s := range m.stats.Sessions {
		row := cacheJoinRow([]string{
			cachePad(cacheShortID(s), cacheColSession),
			cachePad(s.Model, cacheColModel),
			cachePadLeft(fmt.Sprintf("%d", s.Requests), cacheColReq),
			cachePadLeft(cachePercent(s.HitRate), cacheColHit),
			cachePadLeft(fmt.Sprintf("%d", s.Misses), cacheColMiss),
			cachePadLeft(fmt.Sprintf("%d", s.T0s), cacheColT0),
			cachePadLeft(formatLargeNumber(s.LostTokens), cacheColLost),
			cachePad(cacheRelative(s.LastSeen), cacheColSeen),
		})
		if i == m.cursor {
			sb.WriteString(tableSelectedStyle.Render("▸ " + row))
		} else {
			sb.WriteString(tableCellStyle.Render("  " + row))
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

func (m cacheTabModel) renderGlobalHeader(g CacheAggregate) string {
	var sb strings.Builder

	sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(colorHighlight).Render(T("cache_overview")))
	sb.WriteString("\n")
	sb.WriteString(strings.Repeat("─", minInt(maxInt(m.width, 20), 78)))
	sb.WriteString("\n")

	rateStyle := successStyle
	if g.HitRate < 0.5 {
		rateStyle = warningStyle
	}
	if g.HitRate < 0.2 {
		rateStyle = errorStyle
	}

	sb.WriteString(fmt.Sprintf("  %s %s\n",
		labelStyle.Render(T("cache_hit_rate")+":"),
		rateStyle.Render(cachePercent(g.HitRate))))
	sb.WriteString(fmt.Sprintf("  %s %s\n",
		labelStyle.Render(T("cache_requests")+":"),
		valueStyle.Render(fmt.Sprintf("%d", g.Requests))))
	sb.WriteString(fmt.Sprintf("  %s %s\n",
		labelStyle.Render(T("cache_breakdown")+":"),
		valueStyle.Render(fmt.Sprintf("%s %d  •  %s %d  •  %s %d  •  %s %d",
			T("cache_hits"), g.Hits,
			T("cache_misses"), g.Misses,
			T("cache_t0"), g.T0s,
			T("cache_probes"), g.Probes))))
	sb.WriteString(fmt.Sprintf("  %s %s\n",
		labelStyle.Render(T("cache_read_tokens")+":"),
		valueStyle.Render(formatLargeNumber(g.CacheReadTokens))))
	sb.WriteString(fmt.Sprintf("  %s %s\n",
		labelStyle.Render(T("cache_lost_tokens")+":"),
		warningStyle.Render(formatLargeNumber(g.LostTokens))))

	dominant := T("cache_regime_none")
	switch {
	case g.CacheCreation5mTokens > g.CacheCreation1hTokens:
		dominant = "5m"
	case g.CacheCreation1hTokens > g.CacheCreation5mTokens:
		dominant = "1h"
	case g.CacheCreation5mTokens > 0:
		dominant = T("cache_regime_even")
	}
	sb.WriteString(fmt.Sprintf("  %s %s\n",
		labelStyle.Render(T("cache_creation_split")+":"),
		valueStyle.Render(fmt.Sprintf("5m %s  •  1h %s  •  %s %s  (%s %s)",
			formatLargeNumber(g.CacheCreation5mTokens),
			formatLargeNumber(g.CacheCreation1hTokens),
			T("cache_total"), formatLargeNumber(g.CacheCreationTokens),
			T("cache_dominant"), dominant))))
	sb.WriteString(fmt.Sprintf("  %s %s\n",
		labelStyle.Render(T("cache_sessions_count")+":"),
		valueStyle.Render(fmt.Sprintf("%d", g.Sessions))))
	if g.LastSeen != "" {
		sb.WriteString(fmt.Sprintf("  %s %s\n",
			labelStyle.Render(T("cache_last_activity")+":"),
			valueStyle.Render(cacheRelative(g.LastSeen))))
	}

	sb.WriteString("\n")
	return sb.String()
}

// ──────────────────────────────────────────
// Detail view
// ──────────────────────────────────────────

func (m cacheTabModel) renderDetail() string {
	var sb strings.Builder

	sb.WriteString(titleStyle.Render(T("cache_detail_title")))
	sb.WriteString("\n")
	sb.WriteString(helpStyle.Render(T("cache_detail_help")))
	sb.WriteString("\n\n")

	if m.detailErr != nil {
		sb.WriteString(errorStyle.Render(T("error_prefix") + m.detailErr.Error()))
		sb.WriteString("\n")
		return sb.String()
	}
	if m.detailLoading || m.detailData == nil {
		sb.WriteString(subtitleStyle.Render(T("cache_loading_session")))
		sb.WriteString("\n")
		return sb.String()
	}

	s := m.detailData.Session
	sb.WriteString(fmt.Sprintf("  %s %s\n",
		labelStyle.Render(T("cache_col_session")+":"),
		valueStyle.Render(cacheShortID(s))))
	sb.WriteString(fmt.Sprintf("  %s %s\n",
		labelStyle.Render(T("model")+":"),
		valueStyle.Render(s.Model)))
	sb.WriteString(fmt.Sprintf("  %s %s\n",
		labelStyle.Render(T("cache_auth")+":"),
		valueStyle.Render(s.AuthID)))
	sb.WriteString(fmt.Sprintf("  %s %s\n",
		labelStyle.Render(T("cache_hit_rate")+":"),
		valueStyle.Render(fmt.Sprintf("%s (%d/%d)", cachePercent(s.HitRate), s.Hits, s.Requests))))
	sb.WriteString(fmt.Sprintf("  %s %s\n",
		labelStyle.Render(T("cache_lost_tokens")+":"),
		warningStyle.Render(formatLargeNumber(s.LostTokens))))
	regime := s.Regime
	if regime == "" {
		regime = T("cache_regime_none")
	}
	sb.WriteString(fmt.Sprintf("  %s %s\n",
		labelStyle.Render(T("cache_regime")+":"),
		valueStyle.Render(regime)))
	sb.WriteString(fmt.Sprintf("  %s %s\n",
		labelStyle.Render(T("cache_last_activity")+":"),
		valueStyle.Render(cacheRelative(s.LastSeen))))
	sb.WriteString("\n")

	sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(colorHighlight).Render(T("cache_sequence")))
	sb.WriteString("\n")

	if len(m.detailData.Requests) == 0 {
		sb.WriteString(subtitleStyle.Render(T("cache_no_requests")))
		sb.WriteString("\n")
		return sb.String()
	}

	header := cacheJoinRow([]string{
		cachePadLeft(T("cache_col_seq"), cacheColSeq),
		cachePad(T("cache_col_ago"), cacheColAgo),
		cachePadLeft(T("cache_col_read"), cacheColTok),
		cachePadLeft(T("cache_col_c5m"), cacheColTok),
		cachePadLeft(T("cache_col_c1h"), cacheColTok),
		cachePad(T("cache_col_tier"), cacheColTier),
		cachePad(T("cache_col_reason"), cacheColReason),
		cachePad(T("cache_col_probe"), cacheColProbe),
	})
	sb.WriteString(tableHeaderStyle.Render("  " + header))
	sb.WriteString("\n")

	for _, r := range m.detailData.Requests {
		probe := T("cache_probe_no")
		if r.IsProbe {
			probe = T("cache_probe_yes")
		}
		reason := r.MissReason
		if reason == "" {
			reason = "—"
		}
		row := cacheJoinRow([]string{
			cachePadLeft(fmt.Sprintf("%d", r.Seq), cacheColSeq),
			cachePad(cacheRelative(r.At), cacheColAgo),
			cachePadLeft(formatLargeNumber(r.CacheReadTokens), cacheColTok),
			cachePadLeft(formatLargeNumber(r.CacheCreation5mTokens), cacheColTok),
			cachePadLeft(formatLargeNumber(r.CacheCreation1hTokens), cacheColTok),
			cachePad(r.Tier, cacheColTier),
			cachePad(reason, cacheColReason),
			cachePad(probe, cacheColProbe),
		})

		switch strings.ToLower(r.Tier) {
		case "miss":
			sb.WriteString(errorStyle.Render("✗ " + row))
		case "t0":
			sb.WriteString(warningStyle.Render("○ " + row))
		default:
			sb.WriteString(tableCellStyle.Render("  " + row))
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

// ──────────────────────────────────────────
// Formatting helpers
// ──────────────────────────────────────────

// List column widths.
const (
	cacheColSession = 10
	cacheColModel   = 22
	cacheColReq     = 5
	cacheColHit     = 7
	cacheColMiss    = 6
	cacheColT0      = 4
	cacheColLost    = 8
	cacheColSeen    = 10
)

// Detail column widths.
const (
	cacheColSeq    = 4
	cacheColAgo    = 8
	cacheColTok    = 8
	cacheColTier   = 5
	cacheColReason = 20
	cacheColProbe  = 5
)

func cacheJoinRow(cells []string) string {
	return strings.Join(cells, " ")
}

// cachePad truncates or left-aligns s to exactly w display columns.
func cachePad(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = fitStringWidth(s, w)
	if pad := w - lipgloss.Width(s); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
}

// cachePadLeft truncates or right-aligns s to exactly w display columns.
func cachePadLeft(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = fitStringWidth(s, w)
	if pad := w - lipgloss.Width(s); pad > 0 {
		s = strings.Repeat(" ", pad) + s
	}
	return s
}

func cachePercent(rate float64) string {
	return fmt.Sprintf("%.1f%%", rate*100)
}

func cacheShortID(s CacheSession) string {
	if strings.TrimSpace(s.ShortID) != "" {
		return s.ShortID
	}
	if len(s.ID) > 8 {
		return s.ID[:8]
	}
	return s.ID
}

// cacheRelative renders an RFC3339 timestamp as a localized "2m ago" string.
func cacheRelative(ts string) string {
	ts = strings.TrimSpace(ts)
	if ts == "" {
		return "—"
	}
	parsed, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		parsed, err = time.Parse(time.RFC3339Nano, ts)
		if err != nil {
			return ts
		}
	}
	d := time.Since(parsed)
	if d < 0 {
		d = 0
	}
	var amount string
	switch {
	case d < time.Minute:
		amount = fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		amount = fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		amount = fmt.Sprintf("%dh", int(d.Hours()))
	default:
		amount = fmt.Sprintf("%dd", int(d.Hours()/24))
	}
	return fmt.Sprintf(T("cache_ago_fmt"), amount)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
