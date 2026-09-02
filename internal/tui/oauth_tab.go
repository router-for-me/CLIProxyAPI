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
	name       string
	apiPath    string // management API path
	emoji      string
	deviceFlow bool // true for RFC 8628 device-code providers
}

var oauthProviders = []oauthProvider{
	{"Claude (Anthropic)", "anthropic-auth-url", "🟧", false},
	{"Codex (OpenAI)", "codex-auth-url", "🟩", false},
	{"Antigravity", "antigravity-auth-url", "🟪", false},
	{"Kimi", "kimi-auth-url", "🟫", true},
	{"xAI", "xai-auth-url", "⬛", true},
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

	// Remote browser / device-code mode
	authURL       string // auth URL to display
	authState     string // OAuth state parameter
	providerName  string // current provider name
	userCode      string // device-code user_code (optional)
	deviceFlow    bool   // true when waiting on device authorization
	expiresIn     int    // device-code / poll timeout in seconds
	callbackInput textinput.Model
	inputActive   bool // true when user is typing callback URL

	// pollGeneration invalidates in-flight start/poll commands after cancel or restart.
	pollGeneration int
}

type oauthState int

const (
	oauthIdle oauthState = iota
	oauthPending
	oauthRemote // remote browser mode: waiting for manual callback or device auth
	oauthSuccess
	oauthError
)

const (
	defaultOAuthPollTimeout  = 5 * time.Minute
	deviceOAuthPollTimeout   = 30 * time.Minute
	maxOAuthStatusPollErrors = 5
	oauthStatusPollInterval  = 2 * time.Second
)

// Messages
type oauthStartMsg struct {
	url          string
	state        string
	providerName string
	userCode     string
	deviceFlow   bool
	expiresIn    int
	generation   int
	err          error
}

type oauthPollMsg struct {
	state      string
	generation int
	done       bool
	message    string
	err        error
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
	case localeChangedMsg:
		m.viewport.SetContent(m.renderContent())
		return m, nil
	case oauthStartMsg:
		if !shouldAcceptOAuthStart(msg, m.pollGeneration) {
			// Stale start after Esc/restart: cancel server session so credentials are not saved.
			if msg.err == nil && strings.TrimSpace(msg.state) != "" {
				return m, m.cancelOAuthSession(msg.state)
			}
			return m, nil
		}
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
		m.userCode = msg.userCode
		m.deviceFlow = msg.deviceFlow
		m.expiresIn = msg.expiresIn
		m.state = oauthRemote
		m.callbackInput.SetValue("")
		m.message = ""
		if m.deviceFlow {
			m.inputActive = false
			m.callbackInput.Blur()
			m.viewport.SetContent(m.renderContent())
			return m, m.pollOAuthStatus(msg.state, msg.expiresIn, true, msg.generation)
		}
		m.callbackInput.Focus()
		m.inputActive = true
		m.viewport.SetContent(m.renderContent())
		return m, tea.Batch(textinput.Blink, m.pollOAuthStatus(msg.state, msg.expiresIn, false, msg.generation))

	case oauthPollMsg:
		if !shouldAcceptOAuthPoll(msg, m.authState, m.pollGeneration, m.state) {
			return m, nil
		}
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
			m.message = errorStyle.Render(T("oauth_submit_fail") + ": " + msg.err.Error())
		} else {
			m.message = successStyle.Render(T("oauth_submit_ok"))
		}
		m.viewport.SetContent(m.renderContent())
		return m, nil

	case tea.KeyMsg:
		// ---- Input active: typing callback URL (web flow only) ----
		if m.inputActive && !m.deviceFlow {
			switch msg.String() {
			case "enter":
				callbackURL := m.callbackInput.Value()
				if callbackURL == "" {
					return m, nil
				}
				m.inputActive = false
				m.callbackInput.Blur()
				m.message = warningStyle.Render(T("oauth_submitting"))
				m.viewport.SetContent(m.renderContent())
				return m, m.submitCallback(callbackURL)
			case "esc":
				// Cancel the remote OAuth session even while the callback input is focused.
				return m, m.cancelRemoteOAuth()
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
				if m.deviceFlow {
					return m, nil
				}
				// Re-activate input
				m.inputActive = true
				m.callbackInput.Focus()
				m.viewport.SetContent(m.renderContent())
				return m, textinput.Blink
			case "esc":
				return m, m.cancelRemoteOAuth()
			}
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			return m, cmd
		}

		// ---- Pending (auto polling) ----
		if m.state == oauthPending {
			if msg.String() == "esc" {
				m.pollGeneration++
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
				m.pollGeneration++
				m.state = oauthPending
				m.message = warningStyle.Render(fmt.Sprintf(T("oauth_initiating"), provider.name))
				m.viewport.SetContent(m.renderContent())
				return m, m.startOAuth(provider, m.pollGeneration)
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

func (m oauthTabModel) startOAuth(provider oauthProvider, generation int) tea.Cmd {
	return func() tea.Msg {
		// Call the auth URL endpoint with is_webui=true
		data, err := m.client.getJSON("/v0/management/" + provider.apiPath + "?is_webui=true")
		if err != nil {
			return oauthStartMsg{generation: generation, err: fmt.Errorf("failed to start %s login: %w", provider.name, err)}
		}

		authURL := getString(data, "url")
		state := getString(data, "state")
		if authURL == "" {
			return oauthStartMsg{generation: generation, err: fmt.Errorf("no auth URL returned for %s", provider.name)}
		}

		userCode := getString(data, "user_code")
		flow := strings.ToLower(strings.TrimSpace(getString(data, "flow")))
		expiresIn := int(getFloat(data, "expires_in"))
		deviceFlow := provider.deviceFlow || flow == "device" || userCode != ""

		// Try to open browser (best effort)
		_ = openBrowser(authURL)

		return oauthStartMsg{
			url:          authURL,
			state:        state,
			providerName: provider.name,
			userCode:     userCode,
			deviceFlow:   deviceFlow,
			expiresIn:    expiresIn,
			generation:   generation,
		}
	}
}

// cancelRemoteOAuth clears local remote/device UI state and cancels the server session.
func (m *oauthTabModel) cancelRemoteOAuth() tea.Cmd {
	state := m.authState
	m.pollGeneration++
	m.state = oauthIdle
	m.message = ""
	m.authURL = ""
	m.authState = ""
	m.userCode = ""
	m.deviceFlow = false
	m.expiresIn = 0
	m.inputActive = false
	m.callbackInput.Blur()
	m.callbackInput.SetValue("")
	m.viewport.SetContent(m.renderContent())
	return m.cancelOAuthSession(state)
}

func (m oauthTabModel) cancelOAuthSession(state string) tea.Cmd {
	state = strings.TrimSpace(state)
	if state == "" || m.client == nil {
		return nil
	}
	return func() tea.Msg {
		_ = m.client.CancelAuthSession(state)
		return nil
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
				case "anthropic-auth-url":
					providerKey = "anthropic"
				case "codex-auth-url":
					providerKey = "codex"
				case "antigravity-auth-url":
					providerKey = "antigravity"
				case "kimi-auth-url":
					providerKey = "kimi"
				case "xai-auth-url":
					providerKey = "xai"
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

func (m oauthTabModel) pollOAuthStatus(state string, expiresIn int, deviceFlow bool, generation int) tea.Cmd {
	return func() tea.Msg {
		timeout := defaultOAuthPollTimeout
		if expiresIn > 0 {
			timeout = time.Duration(expiresIn) * time.Second
		} else if deviceFlow {
			timeout = deviceOAuthPollTimeout
		}
		deadline := time.Now().Add(timeout)
		consecutiveErrors := 0
		for {
			if time.Now().After(deadline) {
				return oauthPollMsg{
					state:      state,
					generation: generation,
					done:       false,
					err:        fmt.Errorf("%s", T("oauth_timeout")),
				}
			}

			time.Sleep(oauthStatusPollInterval)

			status, errMsg, err := m.client.GetAuthStatus(state)
			if err != nil {
				consecutiveErrors++
				if shouldFailOAuthStatusPoll(consecutiveErrors, maxOAuthStatusPollErrors) {
					return oauthPollMsg{
						state:      state,
						generation: generation,
						done:       false,
						err:        fmt.Errorf("%s: %w", T("oauth_status_error"), err),
					}
				}
				continue
			}
			consecutiveErrors = 0

			switch status {
			case "ok":
				return oauthPollMsg{
					state:      state,
					generation: generation,
					done:       true,
					message:    T("oauth_success"),
				}
			case "error":
				return oauthPollMsg{
					state:      state,
					generation: generation,
					done:       false,
					err:        fmt.Errorf("%s: %s", T("oauth_failed"), errMsg),
				}
			case "wait":
				continue
			default:
				return oauthPollMsg{
					state:      state,
					generation: generation,
					done:       true,
					message:    T("oauth_completed"),
				}
			}
		}
	}
}

// shouldAcceptOAuthStart reports whether a start result belongs to the current flow.
func shouldAcceptOAuthStart(msg oauthStartMsg, generation int) bool {
	return msg.generation == generation
}

// shouldAcceptOAuthPoll reports whether a poll result belongs to the active remote flow.
func shouldAcceptOAuthPoll(msg oauthPollMsg, authState string, generation int, state oauthState) bool {
	if msg.generation != generation {
		return false
	}
	if msg.state == "" || msg.state != authState {
		return false
	}
	return state == oauthRemote
}

// shouldFailOAuthStatusPoll reports whether consecutive status request errors should fail the flow.
func shouldFailOAuthStatusPoll(consecutiveErrors, maxErrors int) bool {
	if maxErrors <= 0 {
		return consecutiveErrors > 0
	}
	return consecutiveErrors >= maxErrors
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
		return T("loading")
	}
	return m.viewport.View()
}

func (m oauthTabModel) renderContent() string {
	var sb strings.Builder

	sb.WriteString(titleStyle.Render(T("oauth_title")))
	sb.WriteString("\n\n")

	if m.message != "" {
		sb.WriteString("  " + m.message)
		sb.WriteString("\n\n")
	}

	// ---- Remote browser / device-code mode ----
	if m.state == oauthRemote {
		if m.deviceFlow {
			sb.WriteString(m.renderDeviceMode())
		} else {
			sb.WriteString(m.renderRemoteMode())
		}
		return sb.String()
	}

	if m.state == oauthPending {
		sb.WriteString(helpStyle.Render(T("oauth_press_esc")))
		return sb.String()
	}

	sb.WriteString(helpStyle.Render(T("oauth_select")))
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
	sb.WriteString(helpStyle.Render(T("oauth_help")))

	return sb.String()
}

func (m oauthTabModel) renderRemoteMode() string {
	var sb strings.Builder

	providerStyle := lipgloss.NewStyle().Bold(true).Foreground(colorHighlight)
	sb.WriteString(providerStyle.Render(fmt.Sprintf("  ✦ %s OAuth", m.providerName)))
	sb.WriteString("\n\n")

	// Auth URL section
	sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(colorInfo).Render(T("oauth_auth_url")))
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

	sb.WriteString(helpStyle.Render(T("oauth_remote_hint")))
	sb.WriteString("\n\n")

	// Callback URL input
	sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(colorInfo).Render(T("oauth_callback_url")))
	sb.WriteString("\n")

	if m.inputActive {
		sb.WriteString(m.callbackInput.View())
		sb.WriteString("\n")
		sb.WriteString(helpStyle.Render("  " + T("enter_submit") + " • " + T("esc_cancel")))
	} else {
		sb.WriteString(helpStyle.Render(T("oauth_press_c")))
	}

	sb.WriteString("\n\n")
	sb.WriteString(warningStyle.Render(T("oauth_waiting")))

	return sb.String()
}

func (m oauthTabModel) renderDeviceMode() string {
	var sb strings.Builder

	providerStyle := lipgloss.NewStyle().Bold(true).Foreground(colorHighlight)
	sb.WriteString(providerStyle.Render(fmt.Sprintf("  ✦ %s OAuth", m.providerName)))
	sb.WriteString("\n\n")

	sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(colorInfo).Render(T("oauth_auth_url")))
	sb.WriteString("\n")

	urlStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	maxURLWidth := m.width - 6
	if maxURLWidth < 40 {
		maxURLWidth = 40
	}
	for _, line := range wrapText(m.authURL, maxURLWidth) {
		sb.WriteString("  " + urlStyle.Render(line) + "\n")
	}
	sb.WriteString("\n")

	if strings.TrimSpace(m.userCode) != "" {
		sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(colorInfo).Render(T("oauth_user_code")))
		sb.WriteString("\n")
		codeStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FFFFFF")).Background(colorPrimary).Padding(0, 1)
		sb.WriteString("  " + codeStyle.Render(m.userCode) + "\n\n")
	}

	sb.WriteString(helpStyle.Render(T("oauth_device_hint")))
	sb.WriteString("\n")
	if m.expiresIn > 0 {
		sb.WriteString(helpStyle.Render(fmt.Sprintf(T("oauth_device_expires"), m.expiresIn)))
		sb.WriteString("\n")
	}
	sb.WriteString("\n")
	sb.WriteString(warningStyle.Render(T("oauth_waiting")))
	sb.WriteString("\n")
	sb.WriteString(helpStyle.Render(T("oauth_press_esc")))

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
