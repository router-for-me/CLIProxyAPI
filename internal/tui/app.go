package tui

import (
	"io"
	"os"
	"strings"

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

var tabNames = []string{"Dashboard", "Config", "Auth Files", "API Keys", "OAuth", "Usage", "Logs"}

// App is the root bubbletea model that contains all tab sub-models.
type App struct {
	activeTab int
	tabs      []string

	dashboard dashboardModel
	config    configTabModel
	auth      authTabModel
	keys      keysTabModel
	oauth     oauthTabModel
	usage     usageTabModel
	logs      logsTabModel

	client *Client
	hook   *LogHook
	width  int
	height int
	ready  bool

	// Track which tabs have been initialized (fetched data)
	initialized [7]bool
}

// NewApp creates the root TUI application model.
func NewApp(port int, secretKey string, hook *LogHook) App {
	client := NewClient(port, secretKey)
	return App{
		activeTab: tabDashboard,
		tabs:      tabNames,
		dashboard: newDashboardModel(client),
		config:    newConfigTabModel(client),
		auth:      newAuthTabModel(client),
		keys:      newKeysTabModel(client),
		oauth:     newOAuthTabModel(client),
		usage:     newUsageTabModel(client),
		logs:      newLogsTabModel(hook),
		client:    client,
		hook:      hook,
	}
}

func (a App) Init() tea.Cmd {
	// Initialize dashboard and logs on start
	a.initialized[tabDashboard] = true
	a.initialized[tabLogs] = true
	return tea.Batch(
		a.dashboard.Init(),
		a.logs.Init(),
	)
}

func (a App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width = msg.Width
		a.height = msg.Height
		a.ready = true
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

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			return a, tea.Quit
		case "q":
			// Only quit if not in logs tab (where 'q' might be useful)
			if a.activeTab != tabLogs {
				return a, tea.Quit
			}
		case "tab":
			prevTab := a.activeTab
			a.activeTab = (a.activeTab + 1) % len(a.tabs)
			return a, a.initTabIfNeeded(prevTab)
		case "shift+tab":
			prevTab := a.activeTab
			a.activeTab = (a.activeTab - 1 + len(a.tabs)) % len(a.tabs)
			return a, a.initTabIfNeeded(prevTab)
		}
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

	// Always route logLineMsg to logs tab even if not active,
	// AND capture the returned cmd to maintain the waitForLog chain.
	if _, ok := msg.(logLineMsg); ok && a.activeTab != tabLogs {
		var logCmd tea.Cmd
		a.logs, logCmd = a.logs.Update(msg)
		if logCmd != nil {
			cmd = logCmd
		}
	}

	return a, cmd
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
		return a.logs.Init()
	}
	return nil
}

func (a App) View() string {
	if !a.ready {
		return "Initializing TUI..."
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
		sb.WriteString(a.logs.View())
	}

	// Status bar
	sb.WriteString("\n")
	sb.WriteString(a.renderStatusBar())

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
	left := " CLIProxyAPI Management TUI"
	right := "Tab/Shift+Tab: switch • q/Ctrl+C: quit "
	gap := a.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 0 {
		gap = 0
	}
	return statusBarStyle.Width(a.width).Render(left + strings.Repeat(" ", gap) + right)
}

// Run starts the TUI application.
// output specifies where bubbletea renders. If nil, defaults to os.Stdout.
// Pass the real terminal stdout here when os.Stdout has been redirected.
func Run(port int, secretKey string, hook *LogHook, output io.Writer) error {
	if output == nil {
		output = os.Stdout
	}
	app := NewApp(port, secretKey, hook)
	p := tea.NewProgram(app, tea.WithAltScreen(), tea.WithOutput(output))
	_, err := p.Run()
	return err
}
