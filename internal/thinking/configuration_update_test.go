package thinking

import (
	"bytes"
	"testing"

	"github.com/tidwall/gjson"
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
		if input := gjson.GetBytes(got, "input"); !input.IsArray() || len(input.Array()) != 0 {
			t.Fatalf("input = %s, want []; body=%s", input.Raw, got)
		}
		if effort := gjson.GetBytes(got, "reasoning.effort").String(); effort != "high" {
			t.Fatalf("reasoning.effort = %q, want high; body=%s", effort, got)
		}
	})
}

func TestStripConfigurationUpdatesExported(t *testing.T) {
	t.Run("items removed without changing top level effort", func(t *testing.T) {
		body := []byte(`{"reasoning":{"effort":"medium"},"input":[{"type":"configuration_update","reasoning":{"effort":"high"}},{"role":"user","content":"hi"}]}`)
		got := StripConfigurationUpdates(body)
		if input := gjson.GetBytes(got, "input").Raw; input != `[{"role":"user","content":"hi"}]` {
			t.Fatalf("input = %s, want user message; body=%s", input, got)
		}
		if effort := gjson.GetBytes(got, "reasoning.effort").String(); effort != "medium" {
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
