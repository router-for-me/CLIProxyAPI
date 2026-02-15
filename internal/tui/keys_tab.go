package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
)

// keysTabModel displays API keys from all providers.
type keysTabModel struct {
	client   *Client
	viewport viewport.Model
	content  string
	err      error
	width    int
	height   int
	ready    bool
}

type keysDataMsg struct {
	apiKeys []string
	gemini  []map[string]any
	claude  []map[string]any
	codex   []map[string]any
	vertex  []map[string]any
	openai  []map[string]any
	err     error
}

func newKeysTabModel(client *Client) keysTabModel {
	return keysTabModel{
		client: client,
	}
}

func (m keysTabModel) Init() tea.Cmd {
	return m.fetchKeys
}

func (m keysTabModel) fetchKeys() tea.Msg {
	result := keysDataMsg{}

	apiKeys, err := m.client.GetAPIKeys()
	if err != nil {
		result.err = err
		return result
	}
	result.apiKeys = apiKeys

	// Fetch all key types, ignoring individual errors (they may not be configured)
	result.gemini, _ = m.client.GetGeminiKeys()
	result.claude, _ = m.client.GetClaudeKeys()
	result.codex, _ = m.client.GetCodexKeys()
	result.vertex, _ = m.client.GetVertexKeys()
	result.openai, _ = m.client.GetOpenAICompat()

	return result
}

func (m keysTabModel) Update(msg tea.Msg) (keysTabModel, tea.Cmd) {
	switch msg := msg.(type) {
	case keysDataMsg:
		if msg.err != nil {
			m.err = msg.err
			m.content = errorStyle.Render("⚠ Error: " + msg.err.Error())
		} else {
			m.err = nil
			m.content = m.renderKeys(msg)
		}
		m.viewport.SetContent(m.content)
		return m, nil

	case tea.KeyMsg:
		if msg.String() == "r" {
			return m, m.fetchKeys
		}
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd
	}

	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	return m, cmd
}

func (m *keysTabModel) SetSize(w, h int) {
	m.width = w
	m.height = h
	if !m.ready {
		m.viewport = viewport.New(w, h)
		m.viewport.SetContent(m.content)
		m.ready = true
	} else {
		m.viewport.Width = w
		m.viewport.Height = h
	}
}

func (m keysTabModel) View() string {
	if !m.ready {
		return "Loading..."
	}
	return m.viewport.View()
}

func (m keysTabModel) renderKeys(data keysDataMsg) string {
	var sb strings.Builder

	sb.WriteString(titleStyle.Render("🔐 API Keys"))
	sb.WriteString("\n\n")

	// API Keys (access keys)
	renderSection(&sb, "Access API Keys", len(data.apiKeys))
	for i, key := range data.apiKeys {
		sb.WriteString(fmt.Sprintf("  %d. %s\n", i+1, maskKey(key)))
	}
	sb.WriteString("\n")

	// Gemini Keys
	renderProviderKeys(&sb, "Gemini API Keys", data.gemini)

	// Claude Keys
	renderProviderKeys(&sb, "Claude API Keys", data.claude)

	// Codex Keys
	renderProviderKeys(&sb, "Codex API Keys", data.codex)

	// Vertex Keys
	renderProviderKeys(&sb, "Vertex API Keys", data.vertex)

	// OpenAI Compatibility
	if len(data.openai) > 0 {
		renderSection(&sb, "OpenAI Compatibility", len(data.openai))
		for i, entry := range data.openai {
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

	sb.WriteString(helpStyle.Render("Press [r] to refresh • [↑↓] to scroll"))

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
	if len(key) <= 8 {
		return strings.Repeat("*", len(key))
	}
	return key[:4] + strings.Repeat("*", len(key)-8) + key[len(key)-4:]
}
