package modelconfig

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/tidwall/gjson"
)

// TestResolveModelInfoUnknownModelKeepsClientThinkingConfig pins the behaviour
// that a configured model missing from the bundled catalog must not silently
// lose the caller's thinking configuration.
//
// Regression: a Gemini API key configured with a model newer than the embedded
// models.json (e.g. gemini-3.7-flash) resolved to an empty capability snapshot
// with Thinking == nil and UserDefined == false. applyThinking read that as
// "this model provably cannot think" and stripped generationConfig.thinkingConfig
// before dispatch, so the upstream received no includeThoughts and returned no
// thought parts while still billing thinking tokens.
func TestResolveModelInfoUnknownModelKeepsClientThinkingConfig(t *testing.T) {
	const unknownModel = "gemini-3.7-flash-not-in-catalog"

	info := ResolveModelInfo(unknownModel, "gemini", nil)
	if info == nil {
		t.Fatal("ResolveModelInfo() = nil")
	}
	if info.Thinking != nil {
		t.Fatalf("unknown model thinking = %+v, want nil (no capability was resolved)", info.Thinking)
	}
	if !info.UserDefined {
		t.Fatal("unknown configured model must be treated as unknown capability, not proven-absent")
	}

	body := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}],` +
		`"generationConfig":{"thinkingConfig":{"includeThoughts":true,"thinkingBudget":2048}}}`)
	out, err := thinking.ApplyThinkingWithModelInfoAndSummary(
		body, body, unknownModel, "gemini", "gemini", "gemini", info,
		thinking.SummaryConfig{Mode: thinking.SummaryEnabled, Detail: "auto"},
	)
	if err != nil {
		t.Fatalf("ApplyThinkingWithModelInfoAndSummary() error = %v", err)
	}
	if include := gjson.GetBytes(out, "generationConfig.thinkingConfig.includeThoughts"); !include.Exists() || !include.Bool() {
		t.Fatalf("includeThoughts = %v (exists=%v), want true; body=%s", include.Bool(), include.Exists(), out)
	}
	if budget := gjson.GetBytes(out, "generationConfig.thinkingConfig.thinkingBudget"); !budget.Exists() || budget.Int() != 2048 {
		t.Fatalf("thinkingBudget = %d (exists=%v), want 2048; body=%s", budget.Int(), budget.Exists(), out)
	}
}

// TestResolveModelInfoCatalogKnownWithoutThinkingStaysAuthoritative keeps the
// opposite guarantee: when the bundled catalog knows the model and records no
// thinking support, that absence is authoritative and the config must still be
// stripped rather than forwarded.
func TestResolveModelInfoCatalogKnownWithoutThinkingStaysAuthoritative(t *testing.T) {
	var known string
	for _, candidate := range []string{"claude-3-5-haiku-20241022", "imagen-4.0-generate-001", "kimi-k2"} {
		if static := registry.LookupStaticModelInfo(candidate); static != nil && static.Thinking == nil {
			known = candidate
			break
		}
	}
	if known == "" {
		t.Skip("no catalog model without thinking support available in this build")
	}

	info := ResolveModelInfo(known, "gemini", nil)
	if info == nil {
		t.Fatal("ResolveModelInfo() = nil")
	}
	if info.UserDefined {
		t.Fatalf("catalog-known model %q must keep its authoritative capability", known)
	}
}
