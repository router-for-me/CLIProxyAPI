package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// cacheTabModel displays prompt-cache statistics: a global header, a
// per-provider breakdown, a session table, and a per-session drill-down.
type cacheTabModel struct {
	client   *Client
	viewport viewport.Model
	width    int
	height   int
	ready    bool

	stats  *CacheStats
	err    error
	cursor int

	// Provider filter. Empty string means "all"; providerOpts always starts
	// with the empty string followed by the keys seen in stats.Providers.
	providerFilter string
	providerOpts   []string

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
	return cacheTabModel{client: client, providerOpts: []string{""}}
}

func (m cacheTabModel) Init() tea.Cmd {
	return m.fetchData
}

func (m cacheTabModel) fetchData() tea.Msg {
	stats, err := m.client.GetCacheStats(m.providerFilter)
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
			m.syncProviderOpts()
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
		case "p":
			m.providerFilter = m.nextProvider()
			m.cursor = 0
			m.viewport.SetContent(m.renderContent())
			return m, m.fetchData
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

// syncProviderOpts rebuilds the filter cycle from the providers the server
// reports, keeping the active filter selectable even when a filtered response
// only carries its own provider.
func (m *cacheTabModel) syncProviderOpts() {
	opts := []string{""}
	seen := map[string]bool{"": true}
	if m.providerFilter != "" {
		opts = append(opts, m.providerFilter)
		seen[m.providerFilter] = true
	}
	if m.stats != nil {
		for _, p := range m.stats.Providers {
			if p.Key == "" || seen[p.Key] {
				continue
			}
			seen[p.Key] = true
			opts = append(opts, p.Key)
		}
	}
	m.providerOpts = opts
}

func (m cacheTabModel) nextProvider() string {
	if len(m.providerOpts) < 2 {
		return ""
	}
	for i, opt := range m.providerOpts {
		if opt == m.providerFilter {
			return m.providerOpts[(i+1)%len(m.providerOpts)]
		}
	}
	return ""
}

func (m cacheTabModel) filterLabel() string {
	if m.providerFilter == "" {
		return T("cache_filter_all")
	}
	return m.providerFilter
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

	sb.WriteString(titleStyle.Render(fmt.Sprintf("%s  [%s: %s]",
		T("cache_title"), T("cache_provider_filter"), m.filterLabel())))
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
	sb.WriteString(m.renderProviderBreakdown())

	sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(colorHighlight).Render(T("cache_sessions_section")))
	sb.WriteString("\n")
	sb.WriteString(helpStyle.Render(T("cache_keyed_legend")))
	sb.WriteString("\n")

	if len(m.stats.Sessions) == 0 {
		sb.WriteString(subtitleStyle.Render(T("cache_no_sessions")))
		sb.WriteString("\n")
		return sb.String()
	}

	header := cacheJoinRow([]string{
		cachePad(T("cache_col_session"), cacheColSession),
		cachePad(T("cache_col_provider"), cacheColProvider),
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
		lost := s.LostTokens
		if s.Alerting && s.LostTokensInWindow > 0 {
			lost = s.LostTokensInWindow
		}
		row := cacheJoinRow([]string{
			cachePad(cacheSessionLabel(s), cacheColSession),
			cachePad(cacheOrDash(s.Provider), cacheColProvider),
			cachePad(s.Model, cacheColModel),
			cachePadLeft(fmt.Sprintf("%d", s.Requests), cacheColReq),
			cachePadLeft(cacheSessionRate(s), cacheColHit),
			cachePadLeft(fmt.Sprintf("%d", s.Misses), cacheColMiss),
			cachePadLeft(fmt.Sprintf("%d", s.T0s), cacheColT0),
			cachePadLeft(formatLargeNumber(lost), cacheColLost),
			cachePad(cacheRelative(s.LastSeen), cacheColSeen),
		})

		switch {
		case i == m.cursor && s.Alerting:
			sb.WriteString(alertStyle.Render("▸!" + row))
		case i == m.cursor:
			sb.WriteString(tableSelectedStyle.Render("▸ " + row))
		case s.Alerting:
			sb.WriteString(alertStyle.Render(" !" + row))
		default:
			sb.WriteString(tableCellStyle.Render("  " + row))
		}
		sb.WriteString("\n")
	}

	if m.anyAlerting() {
		sb.WriteString("\n")
		sb.WriteString(alertStyle.Render(" " + T("cache_alert_legend") + " "))
		sb.WriteString("\n")
	}

	return sb.String()
}

func (m cacheTabModel) anyAlerting() bool {
	if m.stats == nil {
		return false
	}
	for _, s := range m.stats.Sessions {
		if s.Alerting {
			return true
		}
	}
	return false
}

func (m cacheTabModel) renderGlobalHeader(g CacheAggregate) string {
	var sb strings.Builder

	sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(colorHighlight).Render(T("cache_overview")))
	sb.WriteString("\n")
	sb.WriteString(strings.Repeat("─", minInt(maxInt(m.width, 20), 86)))
	sb.WriteString("\n")

	// A zero classified count means the provider reports no cache accounting.
	// That is emphatically not a 0% hit rate, so no percentage is shown.
	if g.Classified <= 0 {
		sb.WriteString(fmt.Sprintf("  %s %s\n",
			labelStyle.Render(T("cache_hit_rate")+":"),
			mutedStyle.Render(T("cache_no_cache_data"))))
	} else {
		rateStyle := successStyle
		if g.HitRate < 0.5 {
			rateStyle = warningStyle
		}
		if g.HitRate < 0.2 {
			rateStyle = errorStyle
		}
		sb.WriteString(fmt.Sprintf("  %s %s %s\n",
			labelStyle.Render(T("cache_hit_rate")+":"),
			rateStyle.Render(cachePercent(g.HitRate)),
			subtitleStyle.Render(fmt.Sprintf(T("cache_classified_fmt"), g.Hits, g.Classified))))
	}

	sb.WriteString(fmt.Sprintf("  %s %s\n",
		labelStyle.Render(T("cache_requests")+":"),
		valueStyle.Render(fmt.Sprintf("%d", g.Requests))))
	sb.WriteString(fmt.Sprintf("  %s %s\n",
		labelStyle.Render(T("cache_breakdown")+":"),
		valueStyle.Render(fmt.Sprintf("%s %d  •  %s %d  •  %s %d  •  %s %d  •  %s %d",
			T("cache_hits"), g.Hits,
			T("cache_misses"), g.Misses,
			T("cache_t0"), g.T0s,
			T("cache_probes"), g.Probes,
			T("cache_rebinds"), g.Rebinds))))
	sb.WriteString(fmt.Sprintf("  %s %s\n",
		labelStyle.Render(T("cache_t0_breakdown")+":"),
		valueStyle.Render(fmt.Sprintf("%s %d  •  %s %d",
			T("cache_t0_rebind"), g.T0Rebinds,
			T("cache_t0_expiry"), g.T0Expiries))))
	sb.WriteString(fmt.Sprintf("  %s %s\n",
		labelStyle.Render(T("cache_cached_share")+":"),
		valueStyle.Render(fmt.Sprintf("%s  (%s %s)",
			cacheShareCell(g.Classified, g.PromptTokens, g.CachedShare),
			T("cache_prompt_tokens"), formatLargeNumber(g.PromptTokens)))))
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

func (m cacheTabModel) renderProviderBreakdown() string {
	if m.stats == nil || len(m.stats.Providers) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(colorHighlight).Render(T("cache_by_provider")))
	sb.WriteString("\n")

	header := cacheJoinRow([]string{
		cachePad(T("cache_col_provider"), cacheColProvider),
		cachePadLeft(T("cache_col_requests"), cacheColReq),
		cachePadLeft(T("cache_col_hitrate"), cacheColHit),
		cachePadLeft(T("cache_col_share"), cacheColShare),
		cachePadLeft(T("cache_col_rebinds"), cacheColReb),
		cachePadLeft(T("cache_col_lost"), cacheColLost),
	})
	sb.WriteString(tableHeaderStyle.Render("  " + header))
	sb.WriteString("\n")

	for _, p := range m.stats.Providers {
		row := cacheJoinRow([]string{
			cachePad(p.Key, cacheColProvider),
			cachePadLeft(fmt.Sprintf("%d", p.Requests), cacheColReq),
			cachePadLeft(cacheRateCell(p.Classified, p.HitRate), cacheColHit),
			cachePadLeft(cacheShareCell(p.Classified, p.PromptTokens, p.CachedShare), cacheColShare),
			cachePadLeft(fmt.Sprintf("%d", p.Rebinds), cacheColReb),
			cachePadLeft(formatLargeNumber(p.LostTokens), cacheColLost),
		})
		if p.Classified <= 0 {
			sb.WriteString(mutedStyle.Render("  " + row + "  " + T("cache_no_cache_data")))
		} else {
			sb.WriteString(tableCellStyle.Render("  " + row))
		}
		sb.WriteString("\n")
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
	reqs := m.detailData.Requests
	readOnly := strings.EqualFold(s.CacheSignal, "read")

	if s.Alerting {
		sb.WriteString(alertStyle.Render(fmt.Sprintf(" %s  %s: %s ",
			T("cache_alerting"), T("cache_lost_in_window"), formatLargeNumber(s.LostTokensInWindow))))
		sb.WriteString("\n\n")
	}

	sb.WriteString(fmt.Sprintf("  %s %s\n",
		labelStyle.Render(T("cache_col_session")+":"),
		valueStyle.Render(cacheSessionLabel(s))))
	sb.WriteString(fmt.Sprintf("  %s %s\n",
		labelStyle.Render(T("cache_keyed_by")+":"),
		valueStyle.Render(cacheKeyedByLabel(s.KeyedBy))))
	sb.WriteString(fmt.Sprintf("  %s %s\n",
		labelStyle.Render(T("cache_col_provider")+":"),
		valueStyle.Render(cacheOrDash(s.Provider))))
	sb.WriteString(fmt.Sprintf("  %s %s\n",
		labelStyle.Render(T("model")+":"),
		valueStyle.Render(s.Model)))
	sb.WriteString(fmt.Sprintf("  %s %s\n",
		labelStyle.Render(T("cache_auth")+":"),
		valueStyle.Render(cacheOrDash(s.AuthID))))
	sb.WriteString(fmt.Sprintf("  %s %s\n",
		labelStyle.Render(T("cache_signal")+":"),
		valueStyle.Render(cacheSignalLabel(s.CacheSignal))))

	if rate := cacheSessionRate(s); rate == cacheDash {
		sb.WriteString(fmt.Sprintf("  %s %s\n",
			labelStyle.Render(T("cache_hit_rate")+":"),
			mutedStyle.Render(T("cache_no_cache_data"))))
	} else {
		sb.WriteString(fmt.Sprintf("  %s %s %s\n",
			labelStyle.Render(T("cache_hit_rate")+":"),
			valueStyle.Render(rate),
			subtitleStyle.Render(fmt.Sprintf(T("cache_classified_fmt"), s.Hits, s.Classified))))
	}
	sb.WriteString(fmt.Sprintf("  %s %s\n",
		labelStyle.Render(T("cache_lost_tokens")+":"),
		warningStyle.Render(formatLargeNumber(s.LostTokens))))
	sb.WriteString(fmt.Sprintf("  %s %s\n",
		labelStyle.Render(T("cache_lost_in_window")+":"),
		warningStyle.Render(formatLargeNumber(s.LostTokensInWindow))))

	// A read-only provider never reports cache creation, so the 5m/1h split
	// would always read as zeroes. Show the cached share of prompt tokens.
	if readOnly {
		sb.WriteString(fmt.Sprintf("  %s %s\n",
			labelStyle.Render(T("cache_cached_share")+":"),
			valueStyle.Render(fmt.Sprintf("%s  (%s / %s %s)",
				cacheShareCell(s.Classified, s.PromptTokens, s.CachedShare),
				formatLargeNumber(s.CacheReadTokens),
				formatLargeNumber(s.PromptTokens),
				T("cache_prompt_tokens")))))
	} else {
		regime := s.Regime
		if regime == "" {
			regime = T("cache_regime_none")
		}
		sb.WriteString(fmt.Sprintf("  %s %s\n",
			labelStyle.Render(T("cache_creation_split")+":"),
			valueStyle.Render(fmt.Sprintf("5m %s  •  1h %s  (%s %s)",
				formatLargeNumber(s.CacheCreation5mTokens),
				formatLargeNumber(s.CacheCreation1hTokens),
				T("cache_regime"), regime))))
	}

	sb.WriteString(fmt.Sprintf("  %s %s\n",
		labelStyle.Render(T("cache_last_activity")+":"),
		valueStyle.Render(cacheRelative(s.LastSeen))))
	sb.WriteString("\n")

	sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(colorHighlight).Render(T("cache_sequence")))
	sb.WriteString("\n")

	if len(reqs) == 0 {
		sb.WriteString(subtitleStyle.Render(T("cache_no_requests")))
		sb.WriteString("\n")
		return sb.String()
	}

	headerCells := []string{
		cachePadLeft(T("cache_col_seq"), cacheColSeq),
		cachePad(T("cache_col_ago"), cacheColAgo),
		cachePad(T("cache_col_provider"), cacheColProvider),
	}
	if readOnly {
		headerCells = append(headerCells,
			cachePadLeft(T("cache_col_prompt"), cacheColTok),
			cachePadLeft(T("cache_col_read"), cacheColTok),
			cachePadLeft(T("cache_col_share"), cacheColShare))
	} else {
		headerCells = append(headerCells,
			cachePadLeft(T("cache_col_read"), cacheColTok),
			cachePadLeft(T("cache_col_c5m"), cacheColTok),
			cachePadLeft(T("cache_col_c1h"), cacheColTok))
	}
	headerCells = append(headerCells,
		cachePad(T("cache_col_tier"), cacheColTier),
		cachePad(T("cache_col_reason"), cacheColReason),
		cachePad(T("cache_col_rebind"), cacheColReb),
		cachePad(T("cache_col_probe"), cacheColProbe))
	sb.WriteString(tableHeaderStyle.Render("  " + cacheJoinRow(headerCells)))
	sb.WriteString("\n")

	for _, r := range reqs {
		cells := []string{
			cachePadLeft(fmt.Sprintf("%d", r.Seq), cacheColSeq),
			cachePad(cacheRelative(r.At), cacheColAgo),
			cachePad(cacheOrDash(r.Provider), cacheColProvider),
		}
		if readOnly {
			share := cacheDash
			if r.PromptTokens > 0 {
				share = cachePercent(float64(r.CacheReadTokens) / float64(r.PromptTokens))
			}
			cells = append(cells,
				cachePadLeft(formatLargeNumber(r.PromptTokens), cacheColTok),
				cachePadLeft(formatLargeNumber(r.CacheReadTokens), cacheColTok),
				cachePadLeft(share, cacheColShare))
		} else {
			cells = append(cells,
				cachePadLeft(formatLargeNumber(r.CacheReadTokens), cacheColTok),
				cachePadLeft(formatLargeNumber(r.CacheCreation5mTokens), cacheColTok),
				cachePadLeft(formatLargeNumber(r.CacheCreation1hTokens), cacheColTok))
		}
		cells = append(cells,
			cachePad(cacheTierLabel(r.Tier), cacheColTier),
			cachePad(cacheReasonLabel(r), cacheColReason),
			cachePad(cacheBoolMark(r.Rebind, "⇄"), cacheColReb),
			cachePad(cacheProbeLabel(r.IsProbe), cacheColProbe))
		row := cacheJoinRow(cells)

		switch {
		case strings.EqualFold(r.Tier, "miss"):
			sb.WriteString(errorStyle.Render("✗ " + row))
		case strings.EqualFold(r.Tier, "t0") && strings.EqualFold(r.T0Cause, "rebind"):
			// Not a lost cache: the cache is alive on another credential.
			sb.WriteString(rebindStyle.Render("⇄ " + row))
		case strings.EqualFold(r.Tier, "t0"):
			sb.WriteString(warningStyle.Render("○ " + row))
		case strings.EqualFold(r.Tier, "n/a"):
			// The provider reports no cache accounting; this is not an error.
			sb.WriteString(mutedStyle.Render("· " + row))
		default:
			sb.WriteString(tableCellStyle.Render("  " + row))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("\n")
	sb.WriteString(helpStyle.Render(T("cache_tier_legend")))
	sb.WriteString("\n")

	return sb.String()
}

// ──────────────────────────────────────────
// Formatting helpers
// ──────────────────────────────────────────

// cacheDash marks a value that must not be rendered as a number.
const cacheDash = "—"

// List / breakdown column widths.
const (
	cacheColSession  = 11
	cacheColProvider = 9
	cacheColModel    = 18
	cacheColReq      = 5
	cacheColHit      = 7
	cacheColMiss     = 5
	cacheColT0       = 4
	cacheColLost     = 8
	cacheColSeen     = 9
	cacheColShare    = 7
	cacheColReb      = 4
)

// Detail column widths.
const (
	cacheColSeq    = 4
	cacheColAgo    = 8
	cacheColTok    = 8
	cacheColTier   = 5
	cacheColReason = 17
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

func cacheOrDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return cacheDash
	}
	return s
}

// cacheRateCell renders a hit rate only when the bucket classified at least one
// request. Zero classified means "no cache accounting", not "0%".
func cacheRateCell(classified int64, rate float64) string {
	if classified <= 0 {
		return cacheDash
	}
	return cachePercent(rate)
}

// cacheShareCell renders the cached prompt share, suppressed when the bucket
// classified nothing or has no prompt tokens to divide by.
func cacheShareCell(classified, promptTokens int64, share float64) string {
	if classified <= 0 || promptTokens <= 0 {
		return cacheDash
	}
	return cachePercent(share)
}

// cacheSessionRate suppresses the hit rate for sessions whose provider reports
// no cache accounting.
func cacheSessionRate(s CacheSession) string {
	if strings.EqualFold(s.CacheSignal, "none") {
		return cacheDash
	}
	return cacheRateCell(s.Classified, s.HitRate)
}

// cacheSessionLabel is the short id, suffixed with a marker when the session is
// keyed by API key + model rather than by a real agent session id.
func cacheSessionLabel(s CacheSession) string {
	label := s.ShortID
	if strings.TrimSpace(label) == "" {
		if len(s.ID) > 8 {
			label = s.ID[:8]
		} else {
			label = s.ID
		}
	}
	if strings.EqualFold(s.KeyedBy, "apikey-model") {
		label += "†"
	}
	return label
}

func cacheKeyedByLabel(keyedBy string) string {
	switch strings.ToLower(strings.TrimSpace(keyedBy)) {
	case "apikey-model":
		return T("cache_keyed_apikey_model")
	case "session":
		return T("cache_keyed_session")
	case "":
		return cacheDash
	default:
		return keyedBy
	}
}

func cacheSignalLabel(signal string) string {
	switch strings.ToLower(strings.TrimSpace(signal)) {
	case "full":
		return T("cache_signal_full")
	case "read":
		return T("cache_signal_read")
	case "none":
		return T("cache_signal_none")
	case "":
		return cacheDash
	default:
		return signal
	}
}

func cacheTierLabel(tier string) string {
	if strings.TrimSpace(tier) == "" {
		return cacheDash
	}
	return tier
}

// cacheReasonLabel shows the T0 cause for T0 rows and the miss reason for
// misses, so a rebind T0 never reads like a lost cache.
func cacheReasonLabel(r CacheRequest) string {
	if strings.EqualFold(r.Tier, "t0") {
		switch strings.ToLower(strings.TrimSpace(r.T0Cause)) {
		case "first":
			return T("cache_t0_first")
		case "rebind":
			return T("cache_t0_rebind")
		case "expiry":
			return T("cache_t0_expiry")
		case "":
			return cacheDash
		default:
			return r.T0Cause
		}
	}
	if strings.TrimSpace(r.MissReason) != "" {
		return r.MissReason
	}
	return cacheDash
}

func cacheBoolMark(v bool, mark string) string {
	if v {
		return mark
	}
	return "-"
}

func cacheProbeLabel(isProbe bool) string {
	if isProbe {
		return T("cache_probe_yes")
	}
	return T("cache_probe_no")
}

// cacheRelative renders an RFC3339 timestamp as a localized "2m ago" string.
func cacheRelative(ts string) string {
	ts = strings.TrimSpace(ts)
	if ts == "" {
		return cacheDash
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
