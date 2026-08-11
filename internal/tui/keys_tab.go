package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// keysTabModel displays and manages API keys.
type keysTabModel struct {
	client       *Client
	viewport     viewport.Model
	keys         []string
	profiles     []APIKeyProfile
	usage        ClientKeyUsageReport
	usageByKey   map[string]ClientUsageSummary
	gemini       []map[string]any
	interactions []map[string]any
	claude       []map[string]any
	codex        []map[string]any
	xai          []map[string]any
	vertex       []map[string]any
	openai       []map[string]any
	err          error
	width        int
	height       int
	ready        bool
	cursor       int
	confirm      int // -1 = no deletion pending
	status       string

	// Editing / Adding
	editing   bool
	adding    bool
	aliasing  bool
	editIdx   int
	editInput textinput.Model
}

type keysDataMsg struct {
	apiKeys      []string
	profiles     []APIKeyProfile
	usage        ClientKeyUsageReport
	gemini       []map[string]any
	interactions []map[string]any
	claude       []map[string]any
	codex        []map[string]any
	xai          []map[string]any
	vertex       []map[string]any
	openai       []map[string]any
	err          error
}

type keyActionMsg struct {
	action string
	err    error
}

func newKeysTabModel(client *Client) keysTabModel {
	ti := textinput.New()
	ti.CharLimit = 512
	ti.Prompt = "  Key: "
	return keysTabModel{
		client:    client,
		confirm:   -1,
		editInput: ti,
	}
}

func (m keysTabModel) Init() tea.Cmd {
	return m.fetchKeys
}

func (m keysTabModel) fetchKeys() tea.Msg {
	result := keysDataMsg{}
	apiKeys, profiles, err := m.client.GetAPIKeysAndProfiles()
	if err != nil {
		result.err = err
		return result
	}
	result.apiKeys = apiKeys
	result.profiles = profiles
	result.usage, err = m.client.GetClientKeyUsage()
	if err != nil {
		result.err = err
		return result
	}
	result.gemini, _ = m.client.GetGeminiKeys()
	result.interactions, _ = m.client.GetInteractionsKeys()
	result.claude, _ = m.client.GetClaudeKeys()
	result.codex, _ = m.client.GetCodexKeys()
	result.xai, _ = m.client.GetXAIKeys()
	result.vertex, _ = m.client.GetVertexKeys()
	result.openai, _ = m.client.GetOpenAICompat()
	return result
}

func (m keysTabModel) Update(msg tea.Msg) (keysTabModel, tea.Cmd) {
	switch msg := msg.(type) {
	case localeChangedMsg:
		m.viewport.SetContent(m.renderContent())
		return m, nil
	case keysDataMsg:
		if msg.err != nil {
			m.err = msg.err
		} else {
			m.err = nil
			m.keys = msg.apiKeys
			m.profiles = msg.profiles
			m.usage = msg.usage
			m.usageByKey = make(map[string]ClientUsageSummary, len(msg.usage.Keys))
			for _, keyUsage := range msg.usage.Keys {
				m.usageByKey[keyUsage.KeyID] = keyUsage.Summary
			}
			m.gemini = msg.gemini
			m.interactions = msg.interactions
			m.claude = msg.claude
			m.codex = msg.codex
			m.xai = msg.xai
			m.vertex = msg.vertex
			m.openai = msg.openai
			if m.cursor >= len(m.keys) {
				m.cursor = max(0, len(m.keys)-1)
			}
		}
		m.viewport.SetContent(m.renderContent())
		return m, nil

	case keyActionMsg:
		if msg.err != nil {
			m.status = errorStyle.Render("✗ " + msg.err.Error())
		} else {
			m.status = successStyle.Render("✓ " + msg.action)
		}
		m.confirm = -1
		m.viewport.SetContent(m.renderContent())
		return m, m.fetchKeys

	case tea.KeyMsg:
		// ---- Editing / Adding mode ----
		if m.editing || m.adding || m.aliasing {
			switch msg.String() {
			case "enter":
				value := strings.TrimSpace(m.editInput.Value())
				if m.aliasing {
					editIdx := m.editIdx
					expectedID := m.profileAt(editIdx).ID
					m.aliasing = false
					m.editInput.Blur()
					return m, func() tea.Msg {
						err := m.client.PatchAPIKeyProfileExpected(editIdx, expectedID, nil, &value, nil)
						if err != nil {
							return keyActionMsg{err: err}
						}
						return keyActionMsg{action: T("alias_updated")}
					}
				}
				if value == "" {
					m.editing = false
					m.adding = false
					m.editInput.Blur()
					m.viewport.SetContent(m.renderContent())
					return m, nil
				}
				isAdding := m.adding
				editIdx := m.editIdx
				profile := m.profileAt(editIdx)
				expectedID := profile.ID
				expectedRevision := profile.Revision
				m.editing = false
				m.adding = false
				m.editInput.Blur()
				if isAdding {
					return m, func() tea.Msg {
						err := m.client.AddAPIKey(value)
						if err != nil {
							return keyActionMsg{err: err}
						}
						return keyActionMsg{action: T("key_added")}
					}
				}
				return m, func() tea.Msg {
					err := m.client.EditAPIKeyExpectedRevision(editIdx, expectedID, expectedRevision, value)
					if err != nil {
						return keyActionMsg{err: err}
					}
					return keyActionMsg{action: T("key_updated")}
				}
			case "esc":
				m.editing = false
				m.adding = false
				m.aliasing = false
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

		// ---- Delete confirmation ----
		if m.confirm >= 0 {
			switch msg.String() {
			case "y", "Y":
				idx := m.confirm
				profile := m.profileAt(idx)
				expectedID := profile.ID
				expectedRevision := profile.Revision
				m.confirm = -1
				return m, func() tea.Msg {
					err := m.client.DeleteAPIKeyExpectedRevision(idx, expectedID, expectedRevision)
					if err != nil {
						return keyActionMsg{err: err}
					}
					return keyActionMsg{action: T("key_deleted")}
				}
			case "n", "N", "esc":
				m.confirm = -1
				m.viewport.SetContent(m.renderContent())
				return m, nil
			}
			return m, nil
		}

		// ---- Normal mode ----
		switch msg.String() {
		case "j", "down":
			if len(m.keys) > 0 {
				m.cursor = (m.cursor + 1) % len(m.keys)
				m.viewport.SetContent(m.renderContent())
			}
			return m, nil
		case "k", "up":
			if len(m.keys) > 0 {
				m.cursor = (m.cursor - 1 + len(m.keys)) % len(m.keys)
				m.viewport.SetContent(m.renderContent())
			}
			return m, nil
		case "a":
			// Add new key
			m.adding = true
			m.editing = false
			m.aliasing = false
			m.editInput.SetValue("")
			m.editInput.Prompt = T("new_key_prompt")
			m.editInput.Focus()
			m.viewport.SetContent(m.renderContent())
			return m, textinput.Blink
		case "e":
			// Edit selected key
			if m.cursor < len(m.keys) {
				m.editing = true
				m.adding = false
				m.aliasing = false
				m.editIdx = m.cursor
				m.editInput.SetValue(m.keys[m.cursor])
				m.editInput.Prompt = T("edit_key_prompt")
				m.editInput.Focus()
				m.viewport.SetContent(m.renderContent())
				return m, textinput.Blink
			}
			return m, nil
		case "n":
			// Edit the selected key's display alias.
			if m.cursor < len(m.keys) {
				profile := m.profileAt(m.cursor)
				if profile.Issue != "" {
					return m, nil
				}
				m.aliasing = true
				m.editing = false
				m.adding = false
				m.editIdx = m.cursor
				m.editInput.SetValue(profile.Alias)
				m.editInput.Prompt = T("edit_alias_prompt")
				m.editInput.Focus()
				m.viewport.SetContent(m.renderContent())
				return m, textinput.Blink
			}
			return m, nil
		case "t":
			// Toggle whether the selected client key may authenticate.
			if m.cursor < len(m.keys) {
				index := m.cursor
				profile := m.profileAt(index)
				if profile.Issue != "" {
					return m, nil
				}
				disabled := !profile.Disabled
				return m, func() tea.Msg {
					err := m.client.PatchAPIKeyProfileExpected(index, profile.ID, nil, nil, &disabled)
					if err != nil {
						return keyActionMsg{err: err}
					}
					action := T("key_enabled")
					if disabled {
						action = T("key_disabled")
					}
					return keyActionMsg{action: action}
				}
			}
			return m, nil
		case "d":
			// Delete selected key
			if m.cursor < len(m.keys) {
				m.confirm = m.cursor
				m.viewport.SetContent(m.renderContent())
			}
			return m, nil
		case "c":
			// Copy selected key to clipboard
			if m.cursor < len(m.keys) {
				key := m.keys[m.cursor]
				if err := clipboard.WriteAll(key); err != nil {
					m.status = errorStyle.Render(T("copy_failed") + ": " + err.Error())
				} else {
					m.status = successStyle.Render(T("copied"))
				}
				m.viewport.SetContent(m.renderContent())
			}
			return m, nil
		case "r":
			m.status = ""
			return m, m.fetchKeys
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

func (m *keysTabModel) SetSize(w, h int) {
	m.width = w
	m.height = h
	m.editInput.Width = w - 16
	if !m.ready {
		m.viewport = viewport.New(w, h)
		m.viewport.SetContent(m.renderContent())
		m.ready = true
	} else {
		m.viewport.Width = w
		m.viewport.Height = h
	}
}

func (m keysTabModel) View() string {
	if !m.ready {
		return T("loading")
	}
	return m.viewport.View()
}

func (m keysTabModel) renderContent() string {
	var sb strings.Builder

	sb.WriteString(titleStyle.Render(T("keys_title")))
	sb.WriteString("\n")
	sb.WriteString(helpStyle.Render(T("keys_help") + T("keys_profile_help")))
	sb.WriteString("\n")
	sb.WriteString(strings.Repeat("─", m.width))
	sb.WriteString("\n")

	if m.err != nil {
		sb.WriteString(errorStyle.Render(T("error_prefix") + m.err.Error()))
		sb.WriteString("\n")
		return sb.String()
	}

	// ━━━ Access API Keys (interactive) ━━━
	sb.WriteString(tableHeaderStyle.Render(fmt.Sprintf("  %s (%d)", T("access_keys"), len(m.keys))))
	sb.WriteString("\n")

	if len(m.keys) == 0 {
		sb.WriteString(subtitleStyle.Render(T("no_keys")))
		sb.WriteString("\n")
	}
	if !m.usage.Enabled {
		sb.WriteString(helpStyle.Render("  " + T("usage_collection_disabled")))
		sb.WriteString("\n")
	}
	if m.usage.PersistenceError != "" {
		sb.WriteString(warningStyle.Render("  " + T("usage_persistence_warning")))
		sb.WriteString("\n")
	}

	for i, key := range m.keys {
		profile := m.profileAt(i)
		cursor := "  "
		rowStyle := lipgloss.NewStyle()
		if i == m.cursor {
			cursor = "▸ "
			rowStyle = lipgloss.NewStyle().Bold(true)
		}

		maskedKey := profile.MaskedKey
		if maskedKey == "" {
			maskedKey = maskKey(key)
		}
		alias := profile.Alias
		if alias == "" {
			alias = T("not_set")
		}
		state := T("key_active")
		if profile.Issue != "" {
			switch profile.Issue {
			case "empty":
				state = T("key_ignored_empty")
			case "duplicate":
				state = T("key_ignored_duplicate")
			default:
				state = T("key_ignored")
			}
		} else if profile.Disabled {
			state = T("key_disabled_state")
		}
		row := fmt.Sprintf("%s%d. %s  %s: %s  %s: %s  [%s]", cursor, i+1, maskedKey, T("alias_label"), alias, T("id_label"), profile.ID, state)
		sb.WriteString(rowStyle.Render(row))
		sb.WriteString("\n")
		if m.usage.Enabled {
			usage := m.usageForKey(profile.ID)
			usageLine := fmt.Sprintf("    %s: %d  %s: %d", T("usage_attempts"), usage.Attempts, T("tokens"), usage.Tokens.TotalTokens)
			if costs := formatEstimatedCosts(usage.EstimatedCostMicrosByCurrency); costs != "" {
				usageLine += "  " + T("usage_estimated_cost") + ": " + costs
			}
			if usage.UnpricedAttempts > 0 || usage.UnpricedTokens > 0 {
				usageLine += fmt.Sprintf("  ⚠ %s: %d/%d", T("usage_unpriced"), usage.UnpricedAttempts, usage.UnpricedTokens)
			}
			sb.WriteString(helpStyle.Render(usageLine))
			sb.WriteString("\n")
		}

		// Delete confirmation
		if m.confirm == i {
			sb.WriteString(warningStyle.Render(fmt.Sprintf("    "+T("confirm_delete_key"), maskKey(key))))
			sb.WriteString("\n")
		}

		// Edit input
		if m.editing && m.editIdx == i {
			sb.WriteString(m.editInput.View())
			sb.WriteString("\n")
			sb.WriteString(helpStyle.Render(T("enter_save_esc")))
			sb.WriteString("\n")
		}

		if m.aliasing && m.editIdx == i {
			sb.WriteString(m.editInput.View())
			sb.WriteString("\n")
			sb.WriteString(helpStyle.Render(T("enter_save_esc")))
			sb.WriteString("\n")
		}
	}

	// Add input
	if m.adding {
		sb.WriteString("\n")
		sb.WriteString(m.editInput.View())
		sb.WriteString("\n")
		sb.WriteString(helpStyle.Render(T("enter_add")))
		sb.WriteString("\n")
	}

	sb.WriteString("\n")

	// ━━━ Provider Keys (read-only display) ━━━
	renderProviderKeys(&sb, "Gemini API Keys", m.gemini)
	renderProviderKeys(&sb, "Interactions API Keys", m.interactions)
	renderProviderKeys(&sb, "Claude API Keys", m.claude)
	renderProviderKeys(&sb, "Codex API Keys", m.codex)
	renderProviderKeys(&sb, "xAI API Keys", m.xai)
	renderProviderKeys(&sb, "Vertex API Keys", m.vertex)

	if len(m.openai) > 0 {
		renderSection(&sb, "OpenAI Compatibility", len(m.openai))
		for i, entry := range m.openai {
			name := getString(entry, "name")
			baseURL := getString(entry, "base-url")
			prefix := getString(entry, "prefix")
			info := name
			if prefix != "" {
				info += " (prefix: " + prefix + ")"
			}
			if baseURL != "" {
				info += " → " + baseURL
			}
			sb.WriteString(fmt.Sprintf("  %d. %s\n", i+1, info))
		}
		sb.WriteString("\n")
	}

	if m.status != "" {
		sb.WriteString(m.status)
		sb.WriteString("\n")
	}

	return sb.String()
}

func renderSection(sb *strings.Builder, title string, count int) {
	header := fmt.Sprintf("%s (%d)", title, count)
	sb.WriteString(tableHeaderStyle.Render("  " + header))
	sb.WriteString("\n")
}

func renderProviderKeys(sb *strings.Builder, title string, keys []map[string]any) {
	if len(keys) == 0 {
		return
	}
	renderSection(sb, title, len(keys))
	for i, key := range keys {
		apiKey := getString(key, "api-key")
		prefix := getString(key, "prefix")
		baseURL := getString(key, "base-url")
		info := maskKey(apiKey)
		if prefix != "" {
			info += " (prefix: " + prefix + ")"
		}
		if baseURL != "" {
			info += " → " + baseURL
		}
		sb.WriteString(fmt.Sprintf("  %d. %s\n", i+1, info))
	}
	sb.WriteString("\n")
}

func maskKey(key string) string {
	runes := []rune(key)
	if len(runes) <= 8 {
		return strings.Repeat("*", len(runes))
	}
	return string(runes[:4]) + strings.Repeat("*", len(runes)-8) + string(runes[len(runes)-4:])
}

func (m keysTabModel) profileAt(index int) APIKeyProfile {
	for _, profile := range m.profiles {
		if profile.Index == index {
			return profile
		}
	}
	profile := APIKeyProfile{Index: index}
	if index >= 0 && index < len(m.keys) {
		profile.MaskedKey = maskKey(m.keys[index])
	}
	return profile
}

func (m keysTabModel) usageForKey(id string) ClientUsageSummary {
	if m.usageByKey != nil {
		return m.usageByKey[id]
	}
	for _, usage := range m.usage.Keys {
		if usage.KeyID == id {
			return usage.Summary
		}
	}
	return ClientUsageSummary{}
}

func formatEstimatedCosts(costs map[string]int64) string {
	if len(costs) == 0 {
		return ""
	}
	currencies := make([]string, 0, len(costs))
	for currency := range costs {
		currencies = append(currencies, currency)
	}
	sort.Strings(currencies)
	formatted := make([]string, 0, len(currencies))
	for _, currency := range currencies {
		micros := costs[currency]
		if micros < 0 {
			micros = 0
		}
		formatted = append(formatted, fmt.Sprintf("%s %d.%06d", currency, micros/1_000_000, micros%1_000_000))
	}
	return strings.Join(formatted, ", ")
}
