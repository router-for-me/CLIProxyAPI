package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestKeysTabRendersProfileMetadata(t *testing.T) {
	previousLocale := CurrentLocale()
	SetLocale("en")
	defer SetLocale(previousLocale)

	m := newKeysTabModel(nil)
	m.width = 120
	m.keys = []string{"abcdefghijk"}
	m.profiles = []APIKeyProfile{{
		Index:     0,
		ID:        "team-a",
		Alias:     "Production",
		Disabled:  true,
		MaskedKey: "abcd***hijk",
	}}
	m.usage = ClientKeyUsageReport{
		Enabled: true,
		Keys: []ClientKeyUsage{{
			KeyID: "team-a",
			Summary: ClientUsageSummary{
				Attempts:                      3,
				Tokens:                        ClientUsageTokens{TotalTokens: 42},
				EstimatedCostMicrosByCurrency: map[string]int64{"USD": 1250},
				UnpricedAttempts:              1,
				UnpricedTokens:                7,
			},
		}},
	}

	content := m.renderContent()
	for _, expected := range []string{
		"abcd***hijk", "Alias: Production", "ID: team-a", "[disabled]",
		"[n] Edit alias", "[t] Enable/Disable", "Upstream attempts: 3",
		"Tokens: 42", "Estimated cost: USD 0.001250", "Unpriced attempts/tokens: 1/7",
	} {
		if !strings.Contains(content, expected) {
			t.Fatalf("rendered content missing %q:\n%s", expected, content)
		}
	}
	if strings.Contains(content, "abcdefghijk") {
		t.Fatalf("rendered content exposed the raw API key:\n%s", content)
	}
}

func TestMaskKeyIsRuneSafe(t *testing.T) {
	if got, want := maskKey("密钥甲乙丙丁戊己庚辛壬癸"), "密钥甲乙****庚辛壬癸"; got != want {
		t.Fatalf("maskKey() = %q, want %q", got, want)
	}
}

func TestKeysTabAliasShortcutStartsAliasEditor(t *testing.T) {
	previousLocale := CurrentLocale()
	SetLocale("en")
	defer SetLocale(previousLocale)

	m := newKeysTabModel(nil)
	m.keys = []string{"key-one"}
	m.profiles = []APIKeyProfile{{Index: 0, Alias: "Production"}}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if !updated.aliasing || updated.editing || updated.adding {
		t.Fatalf("unexpected editor state: aliasing=%v editing=%v adding=%v", updated.aliasing, updated.editing, updated.adding)
	}
	if got := updated.editInput.Value(); got != "Production" {
		t.Fatalf("alias input = %q, want %q", got, "Production")
	}
	if got := updated.editInput.Prompt; got != "  Edit Alias: " {
		t.Fatalf("alias prompt = %q", got)
	}
}
