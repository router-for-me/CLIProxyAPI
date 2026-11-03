package thinking

import (
	"bytes"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

func TestCollapseConfigurationUpdatesBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "empty input", body: `{"input":[]}`},
		{name: "invalid JSON", body: `{"input":[`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(tc.body)
			if got := CollapseConfigurationUpdates(body); !bytes.Equal(got, body) {
				t.Fatalf("body changed: got %s, want %s", got, body)
			}
		})
	}

	t.Run("only configuration updates", func(t *testing.T) {
		body := []byte(`{"input":[{"type":"configuration_update","reasoning":{"effort":"low"}},{"type":"configuration_update","reasoning":{"effort":"high"}}]}`)
		got := CollapseConfigurationUpdates(body)
		if input := ojson.GetBytes(got, "input"); !input.IsArray() || len(input.Array()) != 0 {
			t.Fatalf("input = %s, want []; body=%s", input.Raw, got)
		}
		if effort := ojson.GetBytes(got, "reasoning.effort").String(); effort != "high" {
			t.Fatalf("reasoning.effort = %q, want high; body=%s", effort, got)
		}
	})
}

func TestStripConfigurationUpdatesExported(t *testing.T) {
	t.Run("items removed without changing top level effort", func(t *testing.T) {
		body := []byte(`{"reasoning":{"effort":"medium"},"input":[{"type":"configuration_update","reasoning":{"effort":"high"}},{"role":"user","content":"hi"}]}`)
		got := StripConfigurationUpdates(body)
		if input := ojson.GetBytes(got, "input").Raw; input != `[{"role":"user","content":"hi"}]` {
			t.Fatalf("input = %s, want user message; body=%s", input, got)
		}
		if effort := ojson.GetBytes(got, "reasoning.effort").String(); effort != "medium" {
			t.Fatalf("reasoning.effort = %q, want medium; body=%s", effort, got)
		}
	})

	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "empty input", body: `{"input":[]}`},
		{name: "invalid JSON", body: `{"input":[`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(tc.body)
			if got := StripConfigurationUpdates(body); !bytes.Equal(got, body) {
				t.Fatalf("body changed: got %s, want %s", got, body)
			}
		})
	}
}

func TestHasThinkingSuffix(t *testing.T) {
	for _, tc := range []struct {
		model string
		want  bool
	}{
		{model: "model", want: false},
		{model: "model()", want: false},
		{model: "model(invalid)", want: false},
		{model: "model(high)", want: true},
		{model: "model(xhigh)", want: true},
		{model: "model(none)", want: true},
		{model: "model(auto)", want: true},
		{model: "model(16384)", want: true},
	} {
		t.Run(tc.model, func(t *testing.T) {
			if got := HasThinkingSuffix(tc.model); got != tc.want {
				t.Fatalf("HasThinkingSuffix(%q) = %t, want %t", tc.model, got, tc.want)
			}
		})
	}
}

type inPlaceLegacyApplier struct{}

func (inPlaceLegacyApplier) Apply(body []byte, _ ThinkingConfig, _ *registry.ModelInfo) ([]byte, error) {
	idx := bytes.Index(body, []byte(`"low"`))
	if idx >= 0 {
		copy(body[idx:], []byte(`"max"`))
	}
	return body, nil
}

func TestInvokeProviderApplierOnDocPreservesInPlaceMutation(t *testing.T) {
	doc := ojson.NewDocString(`{"effort":"low"}`)
	docValid := true
	modified, err := invokeProviderApplierOnDoc(inPlaceLegacyApplier{}, doc, &docValid, ThinkingConfig{Mode: ModeLevel, Level: LevelMax}, nil)
	if err != nil {
		t.Fatalf("invokeProviderApplierOnDoc error: %v", err)
	}
	if !modified {
		t.Fatalf("expected modified=true")
	}
	if got := doc.Get("effort").String(); got != "max" {
		t.Fatalf("doc.Get(effort) = %q, want max", got)
	}
}
