package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// configField represents a single editable config field.
type configField struct {
	label    string
	apiPath  string // management API path (e.g. "debug", "proxy-url")
	kind     string // "bool", "int", "string", "readonly"
	value    string // current display value
	rawValue any    // raw value from API
}

// configTabModel displays parsed config with interactive editing.
type configTabModel struct {
	client    *Client
	viewport  viewport.Model
	fields    []configField
	cursor    int
	editing   bool
	textInput textinput.Model
	err       error
	message   string // status message (success/error)
	width     int
	height    int
	ready     bool
}

type configDataMsg struct {
	config map[string]any
	err    error
}

type configUpdateMsg struct {
	path  string
	value any
	err   error
}

func newConfigTabModel(client *Client) configTabModel {
	ti := textinput.New()
	ti.CharLimit = 256
	return configTabModel{
		client:    client,
		textInput: ti,
	}
}

func (m configTabModel) Init() tea.Cmd {
	return m.fetchConfig
}

func (m configTabModel) fetchConfig() tea.Msg {
	cfg, err := m.client.GetConfig()
	return configDataMsg{config: cfg, err: err}
}

func (m configTabModel) Update(msg tea.Msg) (configTabModel, tea.Cmd) {
	switch msg := msg.(type) {
	case localeChangedMsg:
		m.viewport.SetContent(m.renderContent())
		return m, nil
	case configDataMsg:
		if msg.err != nil {
			m.err = msg.err
			m.fields = nil
		} else {
			m.err = nil
			m.fields = m.parseConfig(msg.config)
		}
		m.viewport.SetContent(m.renderContent())
		return m, nil

	case configUpdateMsg:
		if msg.err != nil {
			m.message = errorStyle.Render("✗ " + msg.err.Error())
		} else {
			m.message = successStyle.Render(T("updated_ok"))
		}
		m.viewport.SetContent(m.renderContent())
		// Refresh config from server
		return m, m.fetchConfig

	case tea.KeyMsg:
		if m.editing {
			return m.handleEditingKey(msg)
		}
		return m.handleNormalKey(msg)
	}

	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	return m, cmd
}

func (m configTabModel) handleNormalKey(msg tea.KeyMsg) (configTabModel, tea.Cmd) {
	switch msg.String() {
	case "r":
		m.message = ""
		return m, m.fetchConfig
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
			m.viewport.SetContent(m.renderContent())
			// Ensure cursor is visible
			m.ensureCursorVisible()
		}
		return m, nil
	case "down", "j":
		if m.cursor < len(m.fields)-1 {
			m.cursor++
			m.viewport.SetContent(m.renderContent())
			m.ensureCursorVisible()
		}
		return m, nil
	case "enter", " ":
		if m.cursor >= 0 && m.cursor < len(m.fields) {
			f := m.fields[m.cursor]
			if f.kind == "readonly" {
				return m, nil
			}
			if f.kind == "bool" {
				// Toggle directly
				return m, m.toggleBool(m.cursor)
			}
			// Start editing for int/string
			m.editing = true
			m.textInput.SetValue(configFieldEditValue(f))
			m.textInput.Focus()
			m.viewport.SetContent(m.renderContent())
			return m, textinput.Blink
		}
		return m, nil
	}

	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	return m, cmd
}

func (m configTabModel) handleEditingKey(msg tea.KeyMsg) (configTabModel, tea.Cmd) {
	switch msg.String() {
	case "enter":
		m.editing = false
		m.textInput.Blur()
		return m, m.submitEdit(m.cursor, m.textInput.Value())
	case "esc":
		m.editing = false
		m.textInput.Blur()
		m.viewport.SetContent(m.renderContent())
		return m, nil
	default:
		var cmd tea.Cmd
		m.textInput, cmd = m.textInput.Update(msg)
		m.viewport.SetContent(m.renderContent())
		return m, cmd
	}
}

func (m configTabModel) toggleBool(idx int) tea.Cmd {
	return func() tea.Msg {
		f := m.fields[idx]
		current := f.value == "true"
		newValue := !current
		errPutBool := m.client.PutBoolField(f.apiPath, newValue)
		return configUpdateMsg{
			path:  f.apiPath,
			value: newValue,
			err:   errPutBool,
		}
	}
}

func (m configTabModel) submitEdit(idx int, newValue string) tea.Cmd {
	return func() tea.Msg {
		f := m.fields[idx]
		var err error
		var value any
		switch f.kind {
		case "int":
			valueInt, errAtoi := strconv.Atoi(newValue)
			if errAtoi != nil {
				return configUpdateMsg{
					path: f.apiPath,
					err:  fmt.Errorf("%s: %s", T("invalid_int"), newValue),
				}
			}
			value = valueInt
			err = m.client.PutIntField(f.apiPath, valueInt)
		case "string":
			value = newValue
			err = m.client.PutStringField(f.apiPath, newValue)
		}
		return configUpdateMsg{
			path:  f.apiPath,
			value: value,
			err:   err,
		}
	}
}

func configFieldEditValue(f configField) string {
	if rawString, ok := f.rawValue.(string); ok {
		return rawString
	}
	return f.value
}

func (m *configTabModel) SetSize(w, h int) {
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

func (m *configTabModel) ensureCursorVisible() {
	// Each field takes ~1 line, header takes ~4 lines
	targetLine := m.cursor + 5
	if targetLine < m.viewport.YOffset {
		m.viewport.SetYOffset(targetLine)
	}
	if targetLine >= m.viewport.YOffset+m.viewport.Height {
		m.viewport.SetYOffset(targetLine - m.viewport.Height + 1)
	}
}

func (m configTabModel) View() string {
	if !m.ready {
		return T("loading")
	}
	return m.viewport.View()
}

func (m configTabModel) renderContent() string {
	var sb strings.Builder

	sb.WriteString(titleStyle.Render(T("config_title")))
	sb.WriteString("\n")

	if m.message != "" {
		sb.WriteString("  " + m.message)
		sb.WriteString("\n")
	}

	sb.WriteString(helpStyle.Render(T("config_help1")))
	sb.WriteString("\n")
	sb.WriteString(helpStyle.Render(T("config_help2")))
	sb.WriteString("\n\n")

	if m.err != nil {
		sb.WriteString(errorStyle.Render("  ⚠ Error: " + m.err.Error()))
		return sb.String()
	}

	if len(m.fields) == 0 {
		sb.WriteString(subtitleStyle.Render(T("no_config")))
		return sb.String()
	}

	currentSection := ""
	for i, f := range m.fields {
		// Section headers
		section := fieldSection(f.apiPath)
		if section != currentSection {
			currentSection = section
			sb.WriteString("\n")
			sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(colorHighlight).Render("  ── " + section + " "))
			sb.WriteString("\n")
		}

		isSelected := i == m.cursor
		prefix := "  "
		if isSelected {
			prefix = "▸ "
		}

		labelStr := lipgloss.NewStyle().
			Foreground(colorInfo).
			Bold(isSelected).
			Width(32).
			Render(f.label)

		var valueStr string
		if m.editing && isSelected {
			valueStr = m.textInput.View()
		} else {
			switch f.kind {
			case "bool":
				if f.value == "true" {
					valueStr = successStyle.Render("● ON")
				} else {
					valueStr = lipgloss.NewStyle().Foreground(colorMuted).Render("○ OFF")
				}
			case "readonly":
				valueStr = lipgloss.NewStyle().Foreground(colorSubtext).Render(f.value)
			default:
				valueStr = valueStyle.Render(f.value)
			}
		}

		line := prefix + labelStr + "  " + valueStr
		if isSelected && !m.editing {
			line = lipgloss.NewStyle().Background(colorSurface).Render(line)
		}
		sb.WriteString(line + "\n")
	}

	return sb.String()
}

func (m configTabModel) parseConfig(cfg map[string]any) []configField {
	var fields []configField

	// Server settings
	fields = append(fields, configField{"Port", "port", "readonly", fmt.Sprintf("%.0f", getFloat(cfg, "port")), nil})
	fields = append(fields, configField{"Host", "host", "readonly", getString(cfg, "host"), nil})
	fields = append(fields, configField{"Debug", "debug", "bool", fmt.Sprintf("%v", getBool(cfg, "debug")), nil})
	fields = append(fields, configField{"Proxy URL", "proxy-url", "string", getString(cfg, "proxy-url"), nil})
	fields = append(fields, configField{"Request Retry", "request-retry", "int", fmt.Sprintf("%.0f", getFloat(cfg, "request-retry")), nil})
	fields = append(fields, configField{"Max Retry Interval (s)", "max-retry-interval", "int", fmt.Sprintf("%.0f", getFloat(cfg, "max-retry-interval")), nil})
	fields = append(fields, configField{"Force Model Prefix", "force-model-prefix", "string", getString(cfg, "force-model-prefix"), nil})

	// Logging
	fields = append(fields, configField{"Logging to File", "logging-to-file", "bool", fmt.Sprintf("%v", getBool(cfg, "logging-to-file")), nil})
	fields = append(fields, configField{"Logs Max Total Size (MB)", "logs-max-total-size-mb", "int", fmt.Sprintf("%.0f", getFloat(cfg, "logs-max-total-size-mb")), nil})
	fields = append(fields, configField{"Error Logs Max Files", "error-logs-max-files", "int", fmt.Sprintf("%.0f", getFloat(cfg, "error-logs-max-files")), nil})
	fields = append(fields, configField{"Usage Stats Enabled", "usage-statistics-enabled", "bool", fmt.Sprintf("%v", getBool(cfg, "usage-statistics-enabled")), nil})
	fields = append(fields, configField{"Request Log", "request-log", "bool", fmt.Sprintf("%v", getBool(cfg, "request-log")), nil})

	// Quota exceeded
	fields = append(fields, configField{"Switch Project on Quota", "quota-exceeded/switch-project", "bool", fmt.Sprintf("%v", getBoolNested(cfg, "quota-exceeded", "switch-project")), nil})
	fields = append(fields, configField{"Switch Preview Model", "quota-exceeded/switch-preview-model", "bool", fmt.Sprintf("%v", getBoolNested(cfg, "quota-exceeded", "switch-preview-model")), nil})

	// Routing
	if routing, ok := cfg["routing"].(map[string]any); ok {
		fields = append(fields, configField{"Routing Strategy", "routing/strategy", "string", getString(routing, "strategy"), nil})
	} else {
		fields = append(fields, configField{"Routing Strategy", "routing/strategy", "string", "", nil})
	}

	// WebSocket auth
	fields = append(fields, configField{"WebSocket Auth", "ws-auth", "bool", fmt.Sprintf("%v", getBool(cfg, "ws-auth")), nil})

	// AMP settings
	if amp, ok := cfg["ampcode"].(map[string]any); ok {
		upstreamURL := getString(amp, "upstream-url")
		upstreamAPIKey := getString(amp, "upstream-api-key")
		fields = append(fields, configField{"AMP Upstream URL", "ampcode/upstream-url", "string", upstreamURL, upstreamURL})
		fields = append(fields, configField{"AMP Upstream API Key", "ampcode/upstream-api-key", "string", maskIfNotEmpty(upstreamAPIKey), upstreamAPIKey})
		fields = append(fields, configField{"AMP Restrict Mgmt Localhost", "ampcode/restrict-management-to-localhost", "bool", fmt.Sprintf("%v", getBool(amp, "restrict-management-to-localhost")), nil})
	}

	return fields
}

func fieldSection(apiPath string) string {
	if strings.HasPrefix(apiPath, "ampcode/") {
		return T("section_ampcode")
	}
	if strings.HasPrefix(apiPath, "quota-exceeded/") {
		return T("section_quota")
	}
	if strings.HasPrefix(apiPath, "routing/") {
		return T("section_routing")
	}
	switch apiPath {
	case "port", "host", "debug", "proxy-url", "request-retry", "max-retry-interval", "force-model-prefix":
		return T("section_server")
	case "logging-to-file", "logs-max-total-size-mb", "error-logs-max-files", "usage-statistics-enabled", "request-log":
		return T("section_logging")
	case "ws-auth":
		return T("section_websocket")
	default:
		return T("section_other")
	}
}

func getBoolNested(m map[string]any, keys ...string) bool {
	current := m
	for i, key := range keys {
		if i == len(keys)-1 {
			return getBool(current, key)
		}
		if nested, ok := current[key].(map[string]any); ok {
			current = nested
		} else {
			return false
		}
	}
	return false
}

func maskIfNotEmpty(s string) string {
	if s == "" {
		return T("not_set")
	}
	return maskKey(s)
}
