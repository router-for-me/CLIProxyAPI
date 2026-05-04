package management

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

// TestAmpMappingKey_DistinguishesSystemPrefix ensures that two mappings
// differing only by When.SystemPrefix produce distinct identity keys, so
// PATCH updates routed through the management API do not collide.
func TestAmpMappingKey_DistinguishesSystemPrefix(t *testing.T) {
	a := config.AmpModelMapping{
		From: "claude-opus-4-6",
		To:   "target-a",
		When: &config.AmpMappingCondition{SystemPrefix: "you are agg man"},
	}
	b := config.AmpModelMapping{
		From: "claude-opus-4-6",
		To:   "target-b",
		When: &config.AmpMappingCondition{SystemPrefix: "you are amp"},
	}
	if ampMappingKey(a) == ampMappingKey(b) {
		t.Fatalf("keys collide for distinct SystemPrefix:\n a=%q\n b=%q",
			ampMappingKey(a), ampMappingKey(b))
	}
}

// TestAmpMappingKey_CaseInsensitiveAlignsWithConditionMatches verifies
// that the identity key normalizes case the same way ConditionMatches
// does, so a PATCH that differs only by case (e.g. "HANDOFF" vs
// "handoff") updates the existing rule instead of appending a stale
// duplicate that is semantically equivalent.
func TestAmpMappingKey_CaseInsensitiveAlignsWithConditionMatches(t *testing.T) {
	a := config.AmpModelMapping{
		From: "Gemini-3-Flash-Preview",
		To:   "x",
		When: &config.AmpMappingCondition{Feature: "HANDOFF", ToolChoice: "Create_Handoff_Context"},
	}
	b := config.AmpModelMapping{
		From: "gemini-3-flash-preview",
		To:   "x",
		When: &config.AmpMappingCondition{Feature: "handoff", ToolChoice: "create_handoff_context"},
	}
	if ampMappingKey(a) != ampMappingKey(b) {
		t.Fatalf("keys differ for case-equivalent mappings:\n a=%q\n b=%q",
			ampMappingKey(a), ampMappingKey(b))
	}
}

// TestAmpMappingKey_NilAndEmptyWhenAreEquivalent verifies that
// `When: nil` and `When: &AmpMappingCondition{}` collapse to the same
// PATCH identity. Both forms match unconditionally at runtime
// (selectTarget routes them through the fallback bucket), so PATCH
// must treat them as the same rule and update in place rather than
// appending a duplicate that would never win at runtime.
func TestAmpMappingKey_NilAndEmptyWhenAreEquivalent(t *testing.T) {
	a := config.AmpModelMapping{From: "gemini-3-flash-preview", To: "x"}
	b := config.AmpModelMapping{
		From: "gemini-3-flash-preview",
		To:   "x",
		When: &config.AmpMappingCondition{},
	}
	if ampMappingKey(a) != ampMappingKey(b) {
		t.Fatalf("nil When and empty When must share a key:\n a=%q\n b=%q",
			ampMappingKey(a), ampMappingKey(b))
	}
}

// TestAmpMappingKey_DistinguishesAllWhenFields verifies each When field
// participates in the identity key.
func TestAmpMappingKey_DistinguishesAllWhenFields(t *testing.T) {
	base := config.AmpModelMapping{From: "m", To: "t"}
	cases := []struct {
		name string
		mod  func(m *config.AmpModelMapping)
	}{
		{"feature", func(m *config.AmpModelMapping) {
			m.When = &config.AmpMappingCondition{Feature: "handoff"}
		}},
		{"tool_choice", func(m *config.AmpModelMapping) {
			m.When = &config.AmpMappingCondition{ToolChoice: "create_handoff_context"}
		}},
		{"user_suffix", func(m *config.AmpModelMapping) {
			m.When = &config.AmpMappingCondition{UserSuffix: "x"}
		}},
		{"system_prefix", func(m *config.AmpModelMapping) {
			m.When = &config.AmpMappingCondition{SystemPrefix: "y"}
		}},
	}
	seen := map[string]string{}
	seen[ampMappingKey(base)] = "base"
	for _, c := range cases {
		m := base
		c.mod(&m)
		k := ampMappingKey(m)
		if prev, ok := seen[k]; ok {
			t.Errorf("%s collides with %s: key=%q", c.name, prev, k)
		}
		seen[k] = c.name
	}
}
