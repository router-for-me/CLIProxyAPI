package tui

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Tab identifiers
const (
	tabDashboard = iota
	tabConfig
	tabAuthFiles
	tabAPIKeys
	tabOAuth
	tabUsage
	tabLogs
)

// App is the root bubbletea model that contains all tab sub-models.
type App struct {
	activeTab int
	tabs      []string

	standalone  bool
	logsEnabled bool

	authenticated  bool
	authInput      textinput.Model
	authError      string
	authConnecting bool

	dashboard dashboardModel
	config    configTabModel
	auth      authTabModel
	keys      keysTabModel
	oauth     oauthTabModel
	usage     usageTabModel
	logs      logsTabModel

	client *Client

	width  int
	height int
	ready  bool

	// Track which tabs have been initialized (fetched data)
	initialized [7]bool
}

type authConnectMsg struct {
	cfg map[string]any
	err error
}

// NewApp creates the root TUI application model.
func NewApp(port int, secretKey string, hook *LogHook) App {
	standalone := hook != nil
	authRequired := !standalone
	ti := textinput.New()
	ti.CharLimit = 512
	ti.EchoMode = textinput.EchoPassword
	ti.EchoCharacter = '*'
	ti.SetValue(strings.TrimSpace(secretKey))
	ti.Focus()

	client := NewClient(port, secretKey)
	app := App{
		activeTab:     tabDashboard,
		standalone:    standalone,
		logsEnabled:   true,
		authenticated: !authRequired,
		authInput:     ti,
		dashboard:     newDashboardModel(client),
		config:        newConfigTabModel(client),
		auth:          newAuthTabModel(client),
		keys:          newKeysTabModel(client),
		oauth:         newOAuthTabModel(client),
		usage:         newUsageTabModel(client),
		logs:          newLogsTabModel(client, hook),
		client:        client,
		initialized: [7]bool{
			tabDashboard: true,
			tabLogs:      true,
		},
	}

	app.refreshTabs()
	if authRequired {
		app.initialized = [7]bool{}
	}
	app.setAuthInputPrompt()
	return app
}

func (a App) Init() tea.Cmd {
	if !a.authenticated {
		return textinput.Blink
	}
	cmds := []tea.Cmd{a.dashboard.Init()}
	if a.logsEnabled {
		cmds = append(cmds, a.logs.Init())
	}
	return tea.Batch(cmds...)
}

func (a App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width = msg.Width
		a.height = msg.Height
		a.ready = true
		if a.width > 0 {
			a.authInput.Width = a.width - 6
		}
		contentH := a.height - 4 // tab bar + status bar
		if contentH < 1 {
			contentH = 1
		}
		contentW := a.width
		a.dashboard.SetSize(contentW, contentH)
		a.config.SetSize(contentW, contentH)
		a.auth.SetSize(contentW, contentH)
		a.keys.SetSize(contentW, contentH)
		a.oauth.SetSize(contentW, contentH)
		a.usage.SetSize(contentW, contentH)
		a.logs.SetSize(contentW, contentH)
		return a, nil

	case authConnectMsg:
		a.authConnecting = false
		if msg.err != nil {
			a.authError = fmt.Sprintf(T("auth_gate_connect_fail"), msg.err.Error())
			return a, nil
		}
		a.authError = ""
		a.authenticated = true
		a.logsEnabled = a.standalone || isLogsEnabledFromConfig(msg.cfg)
		a.refreshTabs()
		a.initialized = [7]bool{}
		a.initialized[tabDashboard] = true
		cmds := []tea.Cmd{a.dashboard.Init()}
		if a.logsEnabled {
			a.initialized[tabLogs] = true
			cmds = append(cmds, a.logs.Init())
		}
		return a, tea.Batch(cmds...)

	case configUpdateMsg:
		var cmdLogs tea.Cmd
		if !a.standalone && msg.err == nil && msg.path == "logging-to-file" {
			logsEnabledConfig, okConfig := msg.value.(bool)
			if okConfig {
				logsEnabledBefore := a.logsEnabled
				a.logsEnabled = logsEnabledConfig
				if logsEnabledBefore != a.logsEnabled {
					a.refreshTabs()
				}
				if !a.logsEnabled {
					a.initialized[tabLogs] = false
				}
				if !logsEnabledBefore && a.logsEnabled {
					a.initialized[tabLogs] = true
					cmdLogs = a.logs.Init()
				}
			}
		}

		var cmdConfig tea.Cmd
		a.config, cmdConfig = a.config.Update(msg)
		if cmdConfig != nil && cmdLogs != nil {
			return a, tea.Batch(cmdConfig, cmdLogs)
		}
		if cmdConfig != nil {
			return a, cmdConfig
		}
		return a, cmdLogs

	case tea.KeyMsg:
		if !a.authenticated {
			switch msg.String() {
			case "ctrl+c", "q":
				return a, tea.Quit
			case "L":
				ToggleLocale()
				a.refreshTabs()
				a.setAuthInputPrompt()
				return a, nil
			case "enter":
				if a.authConnecting {
					return a, nil
				}
				password := strings.TrimSpace(a.authInput.Value())
				if password == "" {
					a.authError = T("auth_gate_password_required")
					return a, nil
				}
				a.authError = ""
				a.authConnecting = true
				return a, a.connectWithPassword(password)
			default:
				var cmd tea.Cmd
				a.authInput, cmd = a.authInput.Update(msg)
				return a, cmd
			}
		}

		switch msg.String() {
		case "ctrl+c":
			return a, tea.Quit
		case "q":
			// Only quit if not in logs tab (where 'q' might be useful)
			if !a.logsEnabled || a.activeTab != tabLogs {
				return a, tea.Quit
			}
		case "L":
			ToggleLocale()
			a.refreshTabs()
			return a.broadcastToAllTabs(localeChangedMsg{})
		case "tab":
			if len(a.tabs) == 0 {
				return a, nil
			}
			prevTab := a.activeTab
			a.activeTab = (a.activeTab + 1) % len(a.tabs)
			return a, a.initTabIfNeeded(prevTab)
		case "shift+tab":
			if len(a.tabs) == 0 {
				return a, nil
			}
			prevTab := a.activeTab
			a.activeTab = (a.activeTab - 1 + len(a.tabs)) % len(a.tabs)
			return a, a.initTabIfNeeded(prevTab)
		}
	}

	if !a.authenticated {
		var cmd tea.Cmd
		a.authInput, cmd = a.authInput.Update(msg)
		return a, cmd
	}

	// Route msg to active tab
	var cmd tea.Cmd
	switch a.activeTab {
	case tabDashboard:
		a.dashboard, cmd = a.dashboard.Update(msg)
	case tabConfig:
		a.config, cmd = a.config.Update(msg)
	case tabAuthFiles:
		a.auth, cmd = a.auth.Update(msg)
	case tabAPIKeys:
		a.keys, cmd = a.keys.Update(msg)
	case tabOAuth:
		a.oauth, cmd = a.oauth.Update(msg)
	case tabUsage:
		a.usage, cmd = a.usage.Update(msg)
	case tabLogs:
		a.logs, cmd = a.logs.Update(msg)
	}

	// Keep logs polling alive even when logs tab is not active.
	if a.logsEnabled && a.activeTab != tabLogs {
		switch msg.(type) {
		case logsPollMsg, logsTickMsg, logLineMsg:
			var logCmd tea.Cmd
			a.logs, logCmd = a.logs.Update(msg)
			if logCmd != nil {
				cmd = logCmd
			}
		}
	}

	return a, cmd
}

// localeChangedMsg is broadcast to all tabs when the user toggles locale.
type localeChangedMsg struct{}

func (a *App) refreshTabs() {
	names := TabNames()
	if a.logsEnabled {
		a.tabs = names
	} else {
		filtered := make([]string, 0, len(names)-1)
		for idx, name := range names {
			if idx == tabLogs {
				continue
			}
			filtered = append(filtered, name)
		}
		a.tabs = filtered
	}

	if len(a.tabs) == 0 {
		a.activeTab = tabDashboard
		return
	}
	if a.activeTab >= len(a.tabs) {
		a.activeTab = len(a.tabs) - 1
	}
}

func (a *App) initTabIfNeeded(_ int) tea.Cmd {
	if a.initialized[a.activeTab] {
		return nil
	}
	a.initialized[a.activeTab] = true
	switch a.activeTab {
	case tabDashboard:
		return a.dashboard.Init()
	case tabConfig:
		return a.config.Init()
	case tabAuthFiles:
		return a.auth.Init()
	case tabAPIKeys:
		return a.keys.Init()
	case tabOAuth:
		return a.oauth.Init()
	case tabUsage:
		return a.usage.Init()
	case tabLogs:
		if !a.logsEnabled {
			return nil
		}
		return a.logs.Init()
	}
	return nil
}

func (a App) View() string {
	if !a.authenticated {
		return a.renderAuthView()
	}

	if !a.ready {
		return T("initializing_tui")
	}

	var sb strings.Builder

	// Tab bar
	sb.WriteString(a.renderTabBar())
	sb.WriteString("\n")

	// Content
	switch a.activeTab {
	case tabDashboard:
		sb.WriteString(a.dashboard.View())
	case tabConfig:
		sb.WriteString(a.config.View())
	case tabAuthFiles:
		sb.WriteString(a.auth.View())
	case tabAPIKeys:
		sb.WriteString(a.keys.View())
	case tabOAuth:
		sb.WriteString(a.oauth.View())
	case tabUsage:
		sb.WriteString(a.usage.View())
	case tabLogs:
		if a.logsEnabled {
			sb.WriteString(a.logs.View())
		}
	}

	// Status bar
	sb.WriteString("\n")
	sb.WriteString(a.renderStatusBar())

	return sb.String()
}

func (a App) renderAuthView() string {
	var sb strings.Builder

	sb.WriteString(titleStyle.Render(T("auth_gate_title")))
	sb.WriteString("\n")
	sb.WriteString(helpStyle.Render(T("auth_gate_help")))
	sb.WriteString("\n\n")
	if a.authConnecting {
		sb.WriteString(warningStyle.Render(T("auth_gate_connecting")))
		sb.WriteString("\n\n")
	}
	if strings.TrimSpace(a.authError) != "" {
		sb.WriteString(errorStyle.Render(a.authError))
		sb.WriteString("\n\n")
	}
	sb.WriteString(a.authInput.View())
	sb.WriteString("\n")
	sb.WriteString(helpStyle.Render(T("auth_gate_enter")))
	return sb.String()
}

func (a App) renderTabBar() string {
	var tabs []string
	for i, name := range a.tabs {
		if i == a.activeTab {
			tabs = append(tabs, tabActiveStyle.Render(name))
		} else {
			tabs = append(tabs, tabInactiveStyle.Render(name))
		}
	}
	tabBar := lipgloss.JoinHorizontal(lipgloss.Top, tabs...)
	return tabBarStyle.Width(a.width).Render(tabBar)
}

func (a App) renderStatusBar() string {
	left := strings.TrimRight(T("status_left"), " ")
	right := strings.TrimRight(T("status_right"), " ")

	width := a.width
	if width < 1 {
		width = 1
	}

	// statusBarStyle has left/right padding(1), so content area is width-2.
	contentWidth := width - 2
	if contentWidth < 0 {
		contentWidth = 0
	}

	if lipgloss.Width(left) > contentWidth {
		left = fitStringWidth(left, contentWidth)
		right = ""
	}

	remaining := contentWidth - lipgloss.Width(left)
	if remaining < 0 {
		remaining = 0
	}
	if lipgloss.Width(right) > remaining {
		right = fitStringWidth(right, remaining)
	}

	gap := contentWidth - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 0 {
		gap = 0
	}
	return statusBarStyle.Width(width).Render(left + strings.Repeat(" ", gap) + right)
}

func fitStringWidth(text string, maxWidth int) string {
	if maxWidth <= 0 {
		return ""
	}
	if lipgloss.Width(text) <= maxWidth {
		return text
	}

	out := ""
	for _, r := range text {
		next := out + string(r)
		if lipgloss.Width(next) > maxWidth {
			break
		}
		out = next
	}
	return out
}

func isLogsEnabledFromConfig(cfg map[string]any) bool {
	if cfg == nil {
		return true
	}
	value, ok := cfg["logging-to-file"]
	if !ok {
		return true
	}
	enabled, ok := value.(bool)
	if !ok {
		return true
	}
	return enabled
}

func (a *App) setAuthInputPrompt() {
	if a == nil {
		return
	}
	a.authInput.Prompt = fmt.Sprintf("  %s: ", T("auth_gate_password"))
}

func (a App) connectWithPassword(password string) tea.Cmd {
	return func() tea.Msg {
		a.client.SetSecretKey(password)
		cfg, errGetConfig := a.client.GetConfig()
		return authConnectMsg{cfg: cfg, err: errGetConfig}
	}
}

// Run starts the TUI application.
// output specifies where bubbletea renders. If nil, defaults to os.Stdout.
func Run(port int, secretKey string, hook *LogHook, output io.Writer) error {
	if output == nil {
		output = os.Stdout
	}
	app := NewApp(port, secretKey, hook)
	p := tea.NewProgram(app, tea.WithAltScreen(), tea.WithOutput(output))
	_, err := p.Run()
	return err
}

func (a App) broadcastToAllTabs(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	var cmd tea.Cmd

	a.dashboard, cmd = a.dashboard.Update(msg)
	if cmd != nil {
		cmds = append(cmds, cmd)
	}
	a.config, cmd = a.config.Update(msg)
	if cmd != nil {
		cmds = append(cmds, cmd)
	}
	a.auth, cmd = a.auth.Update(msg)
	if cmd != nil {
		cmds = append(cmds, cmd)
	}
	a.keys, cmd = a.keys.Update(msg)
	if cmd != nil {
		cmds = append(cmds, cmd)
	}
	a.oauth, cmd = a.oauth.Update(msg)
	if cmd != nil {
		cmds = append(cmds, cmd)
	}
	a.usage, cmd = a.usage.Update(msg)
	if cmd != nil {
		cmds = append(cmds, cmd)
	}
	a.logs, cmd = a.logs.Update(msg)
	if cmd != nil {
		cmds = append(cmds, cmd)
	}

	return a, tea.Batch(cmds...)
}
