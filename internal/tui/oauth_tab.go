package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// oauthProvider represents an OAuth provider option.
type oauthProvider struct {
	name    string
	apiPath string // management API path
	emoji   string
}

var oauthProviders = []oauthProvider{
	{"Gemini CLI", "gemini-cli-auth-url", "🟦"},
	{"Claude (Anthropic)", "anthropic-auth-url", "🟧"},
	{"Codex (OpenAI)", "codex-auth-url", "🟩"},
	{"Antigravity", "antigravity-auth-url", "🟪"},
	{"Qwen", "qwen-auth-url", "🟨"},
	{"Kimi", "kimi-auth-url", "🟫"},
	{"IFlow", "iflow-auth-url", "⬜"},
}

// oauthTabModel handles OAuth login flows.
type oauthTabModel struct {
	client   *Client
	viewport viewport.Model
	cursor   int
	state    oauthState
	message  string
	err      error
	width    int
	height   int
	ready    bool

	// Remote browser mode
	authURL       string // auth URL to display
	authState     string // OAuth state parameter
	providerName  string // current provider name
	callbackInput textinput.Model
	inputActive   bool // true when user is typing callback URL
}

type oauthState int

const (
	oauthIdle oauthState = iota
	oauthPending
	oauthRemote // remote browser mode: waiting for manual callback
	oauthSuccess
	oauthError
)

// Messages
type oauthStartMsg struct {
	url          string
	state        string
	providerName string
	err          error
}

type oauthPollMsg struct {
	done    bool
	message string
	err     error
}

type oauthCallbackSubmitMsg struct {
	err error
}

func newOAuthTabModel(client *Client) oauthTabModel {
	ti := textinput.New()
	ti.Placeholder = "http://localhost:.../auth/callback?code=...&state=..."
	ti.CharLimit = 2048
	ti.Prompt = "  回调 URL: "
	return oauthTabModel{
		client:        client,
		callbackInput: ti,
	}
}

func (m oauthTabModel) Init() tea.Cmd {
	return nil
}

func (m oauthTabModel) Update(msg tea.Msg) (oauthTabModel, tea.Cmd) {
	switch msg := msg.(type) {
	case oauthStartMsg:
		if msg.err != nil {
			m.state = oauthError
			m.err = msg.err
			m.message = errorStyle.Render("✗ " + msg.err.Error())
			m.viewport.SetContent(m.renderContent())
			return m, nil
		}
		m.authURL = msg.url
		m.authState = msg.state
		m.providerName = msg.providerName
		m.state = oauthRemote
		m.callbackInput.SetValue("")
		m.callbackInput.Focus()
		m.inputActive = true
		m.message = ""
		m.viewport.SetContent(m.renderContent())
		// Also start polling in the background
		return m, tea.Batch(textinput.Blink, m.pollOAuthStatus(msg.state))

	case oauthPollMsg:
		if msg.err != nil {
			m.state = oauthError
			m.err = msg.err
			m.message = errorStyle.Render("✗ " + msg.err.Error())
			m.inputActive = false
			m.callbackInput.Blur()
		} else if msg.done {
			m.state = oauthSuccess
			m.message = successStyle.Render("✓ " + msg.message)
			m.inputActive = false
			m.callbackInput.Blur()
		} else {
			m.message = warningStyle.Render("⏳ " + msg.message)
		}
		m.viewport.SetContent(m.renderContent())
		return m, nil

	case oauthCallbackSubmitMsg:
		if msg.err != nil {
			m.message = errorStyle.Render("✗ 提交回调失败: " + msg.err.Error())
		} else {
			m.message = successStyle.Render("✓ 回调已提交，等待处理...")
		}
		m.viewport.SetContent(m.renderContent())
		return m, nil

	case tea.KeyMsg:
		// ---- Input active: typing callback URL ----
		if m.inputActive {
			switch msg.String() {
			case "enter":
				callbackURL := m.callbackInput.Value()
				if callbackURL == "" {
					return m, nil
				}
				m.inputActive = false
				m.callbackInput.Blur()
				m.message = warningStyle.Render("⏳ 提交回调中...")
				m.viewport.SetContent(m.renderContent())
				return m, m.submitCallback(callbackURL)
			case "esc":
				m.inputActive = false
				m.callbackInput.Blur()
				m.viewport.SetContent(m.renderContent())
				return m, nil
			default:
				var cmd tea.Cmd
				m.callbackInput, cmd = m.callbackInput.Update(msg)
				m.viewport.SetContent(m.renderContent())
				return m, cmd
			}
		}

		// ---- Remote mode but not typing ----
		if m.state == oauthRemote {
			switch msg.String() {
			case "c", "C":
				// Re-activate input
				m.inputActive = true
				m.callbackInput.Focus()
				m.viewport.SetContent(m.renderContent())
				return m, textinput.Blink
			case "esc":
				m.state = oauthIdle
				m.message = ""
				m.authURL = ""
				m.authState = ""
				m.viewport.SetContent(m.renderContent())
				return m, nil
			}
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			return m, cmd
		}

		// ---- Pending (auto polling) ----
		if m.state == oauthPending {
			if msg.String() == "esc" {
				m.state = oauthIdle
				m.message = ""
				m.viewport.SetContent(m.renderContent())
			}
			return m, nil
		}

		// ---- Idle ----
		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
				m.viewport.SetContent(m.renderContent())
			}
			return m, nil
		case "down", "j":
			if m.cursor < len(oauthProviders)-1 {
				m.cursor++
				m.viewport.SetContent(m.renderContent())
			}
			return m, nil
		case "enter":
			if m.cursor >= 0 && m.cursor < len(oauthProviders) {
				provider := oauthProviders[m.cursor]
				m.state = oauthPending
				m.message = warningStyle.Render("⏳ 正在初始化 " + provider.name + " 登录...")
				m.viewport.SetContent(m.renderContent())
				return m, m.startOAuth(provider)
			}
			return m, nil
		case "esc":
			m.state = oauthIdle
			m.message = ""
			m.err = nil
			m.viewport.SetContent(m.renderContent())
			return m, nil
		}

		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd
	}

	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	return m, cmd
}

func (m oauthTabModel) startOAuth(provider oauthProvider) tea.Cmd {
	return func() tea.Msg {
		// Call the auth URL endpoint with is_webui=true
		data, err := m.client.getJSON("/v0/management/" + provider.apiPath + "?is_webui=true")
		if err != nil {
			return oauthStartMsg{err: fmt.Errorf("failed to start %s login: %w", provider.name, err)}
		}

		authURL := getString(data, "url")
		state := getString(data, "state")
		if authURL == "" {
			return oauthStartMsg{err: fmt.Errorf("no auth URL returned for %s", provider.name)}
		}

		// Try to open browser (best effort)
		_ = openBrowser(authURL)

		return oauthStartMsg{url: authURL, state: state, providerName: provider.name}
	}
}

func (m oauthTabModel) submitCallback(callbackURL string) tea.Cmd {
	return func() tea.Msg {
		// Determine provider from current context
		providerKey := ""
		for _, p := range oauthProviders {
			if p.name == m.providerName {
				// Map provider name to the canonical key the API expects
				switch p.apiPath {
				case "gemini-cli-auth-url":
					providerKey = "gemini"
				case "anthropic-auth-url":
					providerKey = "anthropic"
				case "codex-auth-url":
					providerKey = "codex"
				case "antigravity-auth-url":
					providerKey = "antigravity"
				case "qwen-auth-url":
					providerKey = "qwen"
				case "kimi-auth-url":
					providerKey = "kimi"
				case "iflow-auth-url":
					providerKey = "iflow"
				}
				break
			}
		}

		body := map[string]string{
			"provider":     providerKey,
			"redirect_url": callbackURL,
			"state":        m.authState,
		}
		err := m.client.postJSON("/v0/management/oauth-callback", body)
		if err != nil {
			return oauthCallbackSubmitMsg{err: err}
		}
		return oauthCallbackSubmitMsg{}
	}
}

func (m oauthTabModel) pollOAuthStatus(state string) tea.Cmd {
	return func() tea.Msg {
		// Poll session status for up to 5 minutes
		deadline := time.Now().Add(5 * time.Minute)
		for {
			if time.Now().After(deadline) {
				return oauthPollMsg{done: false, err: fmt.Errorf("OAuth flow timed out (5 minutes)")}
			}

			time.Sleep(2 * time.Second)

			status, errMsg, err := m.client.GetAuthStatus(state)
			if err != nil {
				continue // Ignore transient errors
			}

			switch status {
			case "ok":
				return oauthPollMsg{
					done:    true,
					message: "认证成功! 请刷新 Auth Files 标签查看新凭证。",
				}
			case "error":
				return oauthPollMsg{
					done: false,
					err:  fmt.Errorf("认证失败: %s", errMsg),
				}
			case "wait":
				continue
			default:
				return oauthPollMsg{
					done:    true,
					message: "认证流程已完成。",
				}
			}
		}
	}
}

func (m *oauthTabModel) SetSize(w, h int) {
	m.width = w
	m.height = h
	m.callbackInput.Width = w - 16
	if !m.ready {
		m.viewport = viewport.New(w, h)
		m.viewport.SetContent(m.renderContent())
		m.ready = true
	} else {
		m.viewport.Width = w
		m.viewport.Height = h
	}
}

func (m oauthTabModel) View() string {
	if !m.ready {
		return "Loading..."
	}
	return m.viewport.View()
}

func (m oauthTabModel) renderContent() string {
	var sb strings.Builder

	sb.WriteString(titleStyle.Render("🔐 OAuth 登录"))
	sb.WriteString("\n\n")

	if m.message != "" {
		sb.WriteString("  " + m.message)
		sb.WriteString("\n\n")
	}

	// ---- Remote browser mode ----
	if m.state == oauthRemote {
		sb.WriteString(m.renderRemoteMode())
		return sb.String()
	}

	if m.state == oauthPending {
		sb.WriteString(helpStyle.Render("  Press [Esc] to cancel"))
		return sb.String()
	}

	sb.WriteString(helpStyle.Render("  选择提供商并按 [Enter] 开始 OAuth 登录:"))
	sb.WriteString("\n\n")

	for i, p := range oauthProviders {
		isSelected := i == m.cursor
		prefix := "  "
		if isSelected {
			prefix = "▸ "
		}

		label := fmt.Sprintf("%s %s", p.emoji, p.name)
		if isSelected {
			label = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FFFFFF")).Background(colorPrimary).Padding(0, 1).Render(label)
		} else {
			label = lipgloss.NewStyle().Foreground(colorText).Padding(0, 1).Render(label)
		}

		sb.WriteString(prefix + label + "\n")
	}

	sb.WriteString("\n")
	sb.WriteString(helpStyle.Render("  [↑↓/jk] 导航 • [Enter] 登录 • [Esc] 清除状态"))

	return sb.String()
}

func (m oauthTabModel) renderRemoteMode() string {
	var sb strings.Builder

	providerStyle := lipgloss.NewStyle().Bold(true).Foreground(colorHighlight)
	sb.WriteString(providerStyle.Render(fmt.Sprintf("  ✦ %s OAuth", m.providerName)))
	sb.WriteString("\n\n")

	// Auth URL section
	sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(colorInfo).Render("  授权链接:"))
	sb.WriteString("\n")

	// Wrap URL to fit terminal width
	urlStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	maxURLWidth := m.width - 6
	if maxURLWidth < 40 {
		maxURLWidth = 40
	}
	wrappedURL := wrapText(m.authURL, maxURLWidth)
	for _, line := range wrappedURL {
		sb.WriteString("  " + urlStyle.Render(line) + "\n")
	}
	sb.WriteString("\n")

	sb.WriteString(helpStyle.Render("  远程浏览器模式：在浏览器中打开上述链接完成授权后，将回调 URL 粘贴到下方。"))
	sb.WriteString("\n\n")

	// Callback URL input
	sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(colorInfo).Render("  回调 URL:"))
	sb.WriteString("\n")

	if m.inputActive {
		sb.WriteString(m.callbackInput.View())
		sb.WriteString("\n")
		sb.WriteString(helpStyle.Render("  Enter: 提交 • Esc: 取消输入"))
	} else {
		sb.WriteString(helpStyle.Render("  按 [c] 输入回调 URL • [Esc] 返回"))
	}

	sb.WriteString("\n\n")
	sb.WriteString(warningStyle.Render("  等待认证中..."))

	return sb.String()
}

// wrapText splits a long string into lines of at most maxWidth characters.
func wrapText(s string, maxWidth int) []string {
	if maxWidth <= 0 {
		return []string{s}
	}
	var lines []string
	for len(s) > maxWidth {
		lines = append(lines, s[:maxWidth])
		s = s[maxWidth:]
	}
	if len(s) > 0 {
		lines = append(lines, s)
	}
	return lines
}
