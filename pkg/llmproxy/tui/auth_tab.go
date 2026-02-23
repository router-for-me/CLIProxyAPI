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

// editableField represents an editable field on an auth file.
type editableField struct {
	label string
	key   string // API field key: "prefix", "proxy_url", "priority"
}

var authEditableFields = []editableField{
	{label: "Prefix", key: "prefix"},
	{label: "Proxy URL", key: "proxy_url"},
	{label: "Priority", key: "priority"},
}

// authTabModel displays auth credential files with interactive management.
type authTabModel struct {
	client   *Client
	viewport viewport.Model
	files    []map[string]any
	err      error
	width    int
	height   int
	ready    bool
	cursor   int
	expanded int // -1 = none expanded, >=0 = expanded index
	confirm  int // -1 = no confirmation, >=0 = confirm delete for index
	status   string

	// Editing state
	editing      bool            // true when editing a field
	editField    int             // index into authEditableFields
	editInput    textinput.Model // text input for editing
	editFileName string          // name of file being edited
}

type authFilesMsg struct {
	files []map[string]any
	err   error
}

type authActionMsg struct {
	action string // "deleted", "toggled", "updated"
	err    error
}

func newAuthTabModel(client *Client) authTabModel {
	ti := textinput.New()
	ti.CharLimit = 256
	return authTabModel{
		client:    client,
		expanded:  -1,
		confirm:   -1,
		editInput: ti,
	}
}

func (m authTabModel) Init() tea.Cmd {
	return m.fetchFiles
}

func (m authTabModel) fetchFiles() tea.Msg {
	files, err := m.client.GetAuthFiles()
	return authFilesMsg{files: files, err: err}
}

func (m authTabModel) Update(msg tea.Msg) (authTabModel, tea.Cmd) {
	switch msg := msg.(type) {
	case localeChangedMsg:
		m.viewport.SetContent(m.renderContent())
		return m, nil
	case authFilesMsg:
		if msg.err != nil {
			m.err = msg.err
		} else {
			m.err = nil
			m.files = msg.files
			if m.cursor >= len(m.files) {
				m.cursor = max(0, len(m.files)-1)
			}
			m.status = ""
		}
		m.viewport.SetContent(m.renderContent())
		return m, nil

	case authActionMsg:
		if msg.err != nil {
			m.status = errorStyle.Render("✗ " + msg.err.Error())
		} else {
			m.status = successStyle.Render("✓ " + msg.action)
		}
		m.confirm = -1
		m.viewport.SetContent(m.renderContent())
		return m, m.fetchFiles

	case tea.KeyMsg:
		// ---- Editing mode ----
		if m.editing {
			return m.handleEditInput(msg)
		}

		// ---- Delete confirmation mode ----
		if m.confirm >= 0 {
			return m.handleConfirmInput(msg)
		}

		// ---- Normal mode ----
		return m.handleNormalInput(msg)
	}

	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	return m, cmd
}

// startEdit activates inline editing for a field on the currently selected auth file.
func (m *authTabModel) startEdit(fieldIdx int) tea.Cmd {
	if m.cursor >= len(m.files) {
		return nil
	}
	f := m.files[m.cursor]
	m.editFileName = getString(f, "name")
	m.editField = fieldIdx
	m.editing = true

	// Pre-populate with current value
	key := authEditableFields[fieldIdx].key
	currentVal := getAnyString(f, key)
	m.editInput.SetValue(currentVal)
	m.editInput.Focus()
	m.editInput.Prompt = fmt.Sprintf("  %s: ", authEditableFields[fieldIdx].label)
	m.viewport.SetContent(m.renderContent())
	return textinput.Blink
}

func (m *authTabModel) SetSize(w, h int) {
	m.width = w
	m.height = h
	m.editInput.Width = w - 20
	if !m.ready {
		m.viewport = viewport.New(w, h)
		m.viewport.SetContent(m.renderContent())
		m.ready = true
	} else {
		m.viewport.Width = w
		m.viewport.Height = h
	}
}

func (m authTabModel) View() string {
	if !m.ready {
		return T("loading")
	}
	return m.viewport.View()
}

func (m authTabModel) renderContent() string {
	var sb strings.Builder

	sb.WriteString(titleStyle.Render(T("auth_title")))
	sb.WriteString("\n")
	sb.WriteString(helpStyle.Render(T("auth_help1")))
	sb.WriteString("\n")
	sb.WriteString(helpStyle.Render(T("auth_help2")))
	sb.WriteString("\n")
	sb.WriteString(strings.Repeat("─", m.width))
	sb.WriteString("\n")

	if m.err != nil {
		sb.WriteString(errorStyle.Render("⚠ Error: " + m.err.Error()))
		sb.WriteString("\n")
		return sb.String()
	}

	if len(m.files) == 0 {
		sb.WriteString(subtitleStyle.Render(T("no_auth_files")))
		sb.WriteString("\n")
		return sb.String()
	}

	for i, f := range m.files {
		name := getString(f, "name")
		channel := getString(f, "channel")
		email := getString(f, "email")
		disabled := getBool(f, "disabled")

		statusIcon := successStyle.Render("●")
		statusText := T("status_active")
		if disabled {
			statusIcon = lipgloss.NewStyle().Foreground(colorMuted).Render("○")
			statusText = T("status_disabled")
		}

		cursor := "  "
		rowStyle := lipgloss.NewStyle()
		if i == m.cursor {
			cursor = "▸ "
			rowStyle = lipgloss.NewStyle().Bold(true)
		}

		displayName := name
		if len(displayName) > 24 {
			displayName = displayName[:21] + "..."
		}
		displayEmail := email
		if len(displayEmail) > 28 {
			displayEmail = displayEmail[:25] + "..."
		}

		row := fmt.Sprintf("%s%s %-24s %-12s %-28s %s",
			cursor, statusIcon, displayName, channel, displayEmail, statusText)
		sb.WriteString(rowStyle.Render(row))
		sb.WriteString("\n")

		// Delete confirmation
		if m.confirm == i {
			sb.WriteString(warningStyle.Render(fmt.Sprintf("    "+T("confirm_delete"), name)))
			sb.WriteString("\n")
		}

		// Inline edit input
		if m.editing && i == m.cursor {
			sb.WriteString(m.editInput.View())
			sb.WriteString("\n")
			sb.WriteString(helpStyle.Render("    " + T("enter_save") + " • " + T("esc_cancel")))
			sb.WriteString("\n")
		}

		// Expanded detail view
		if m.expanded == i {
			sb.WriteString(m.renderDetail(f))
		}
	}

	if m.status != "" {
		sb.WriteString("\n")
		sb.WriteString(m.status)
		sb.WriteString("\n")
	}

	return sb.String()
}

func (m authTabModel) renderDetail(f map[string]any) string {
	var sb strings.Builder

	labelStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("111")).
		Bold(true)
	valueStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("252"))
	editableMarker := lipgloss.NewStyle().
		Foreground(lipgloss.Color("214")).
		Render(" ✎")

	sb.WriteString("    ┌─────────────────────────────────────────────\n")

	fields := []struct {
		label    string
		key      string
		editable bool
	}{
		{"Name", "name", false},
		{"Channel", "channel", false},
		{"Email", "email", false},
		{"Status", "status", false},
		{"Status Msg", "status_message", false},
		{"File Name", "file_name", false},
		{"Auth Type", "auth_type", false},
		{"Prefix", "prefix", true},
		{"Proxy URL", "proxy_url", true},
		{"Priority", "priority", true},
		{"Project ID", "project_id", false},
		{"Disabled", "disabled", false},
		{"Created", "created_at", false},
		{"Updated", "updated_at", false},
	}

	for _, field := range fields {
		val := getAnyString(f, field.key)
		if val == "" || val == "<nil>" {
			if field.editable {
				val = T("not_set")
			} else {
				continue
			}
		}
		editMark := ""
		if field.editable {
			editMark = editableMarker
		}
		line := fmt.Sprintf("    │ %s %s%s",
			labelStyle.Render(fmt.Sprintf("%-12s:", field.label)),
			valueStyle.Render(val),
			editMark)
		sb.WriteString(line)
		sb.WriteString("\n")
	}

	sb.WriteString("    └─────────────────────────────────────────────\n")
	return sb.String()
}

// getAnyString converts any value to its string representation.
func getAnyString(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	return fmt.Sprintf("%v", v)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (m authTabModel) handleEditInput(msg tea.KeyMsg) (authTabModel, tea.Cmd) {
	switch msg.String() {
	case "enter":
		value := m.editInput.Value()
		fieldKey := authEditableFields[m.editField].key
		fileName := m.editFileName
		m.editing = false
		m.editInput.Blur()
		fields := map[string]any{}
		if fieldKey == "priority" {
			p, err := strconv.Atoi(value)
			if err != nil {
				return m, func() tea.Msg {
					return authActionMsg{err: fmt.Errorf("%s: %s", T("invalid_int"), value)}
				}
			}
			fields[fieldKey] = p
		} else {
			fields[fieldKey] = value
		}
		return m, func() tea.Msg {
			err := m.client.PatchAuthFileFields(fileName, fields)
			if err != nil {
				return authActionMsg{err: err}
			}
			return authActionMsg{action: fmt.Sprintf(T("updated_field"), fieldKey, fileName)}
		}
	case "esc":
		m.editing = false
		m.editInput.Blur()
		m.viewport.SetContent(m.renderContent())
		return m, nil
	default:
		var cmd tea.Cmd
		m.editInput, cmd = m.editInput.Update(msg)
		m.viewport.SetContent(m.renderContent())
		return m, cmd
	}
}

func (m authTabModel) handleConfirmInput(msg tea.KeyMsg) (authTabModel, tea.Cmd) {
	switch msg.String() {
	case "y", "Y":
		idx := m.confirm
		m.confirm = -1
		if idx < len(m.files) {
			name := getString(m.files[idx], "name")
			return m, func() tea.Msg {
				err := m.client.DeleteAuthFile(name)
				if err != nil {
					return authActionMsg{err: err}
				}
				return authActionMsg{action: fmt.Sprintf(T("deleted"), name)}
			}
		}
		m.viewport.SetContent(m.renderContent())
		return m, nil
	case "n", "N", "esc":
		m.confirm = -1
		m.viewport.SetContent(m.renderContent())
		return m, nil
	}
	return m, nil
}

func (m authTabModel) handleNormalInput(msg tea.KeyMsg) (authTabModel, tea.Cmd) {
	switch msg.String() {
	case "j", "down":
		if len(m.files) > 0 {
			m.cursor = (m.cursor + 1) % len(m.files)
			m.viewport.SetContent(m.renderContent())
		}
		return m, nil
	case "k", "up":
		if len(m.files) > 0 {
			m.cursor = (m.cursor - 1 + len(m.files)) % len(m.files)
			m.viewport.SetContent(m.renderContent())
		}
		return m, nil
	case "enter", " ":
		if m.expanded == m.cursor {
			m.expanded = -1
		} else {
			m.expanded = m.cursor
		}
		m.viewport.SetContent(m.renderContent())
		return m, nil
	case "d", "D":
		if m.cursor < len(m.files) {
			m.confirm = m.cursor
			m.viewport.SetContent(m.renderContent())
		}
		return m, nil
	case "e", "E":
		if m.cursor < len(m.files) {
			f := m.files[m.cursor]
			name := getString(f, "name")
			disabled := getBool(f, "disabled")
			newDisabled := !disabled
			return m, func() tea.Msg {
				err := m.client.ToggleAuthFile(name, newDisabled)
				if err != nil {
					return authActionMsg{err: err}
				}
				action := T("enabled")
				if newDisabled {
					action = T("disabled")
				}
				return authActionMsg{action: fmt.Sprintf("%s %s", action, name)}
			}
		}
		return m, nil
	case "1":
		return m, m.startEdit(0) // prefix
	case "2":
		return m, m.startEdit(1) // proxy_url
	case "3":
		return m, m.startEdit(2) // priority
	case "r":
		m.status = ""
		return m, m.fetchFiles
	default:
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd
	}
}
