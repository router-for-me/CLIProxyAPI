package claude

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestSortClaudeModelsByDisplayName(t *testing.T) {
	models := []map[string]any{
		{"id": "claude-fable-5-dd-b", "display_name": "Zebra"},
		{"id": "claude-a", "display_name": "Alpha"},
		{"id": "claude-c", "display_name": "Alpha"},
		{"id": "claude-fable-5-dd-d", "display_name": "Beta"},
	}
	sortClaudeModelsByDisplayName(models)

	wantIDs := []string{"claude-a", "claude-c", "claude-fable-5-dd-d", "claude-fable-5-dd-b"}
	for i, want := range wantIDs {
		got, _ := models[i]["id"].(string)
		if got != want {
			t.Fatalf("models[%d].id = %q, want %q", i, got, want)
		}
	}
}

func TestRewriteClaudeDDModelInBody(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantModel string
	}{
		{
			name:      "encoded model is decoded",
			body:      `{"model":"claude-fable-5-dd-o4-tpg","messages":[]}`,
			wantModel: "gpt-4o",
		},
		{
			name:      "plain claude model unchanged",
			body:      `{"model":"claude-sonnet-4-6","messages":[]}`,
			wantModel: "claude-sonnet-4-6",
		},
		{
			name:      "encoded model with thinking suffix",
			body:      `{"model":"claude-fable-5-dd-o4-tpg(high)","stream":true}`,
			wantModel: "gpt-4o(high)",
		},
		{
			name:      "missing model field unchanged",
			body:      `{"messages":[]}`,
			wantModel: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rewriteClaudeDDModelInBody([]byte(tt.body))
			if model := gjson.GetBytes(got, "model").String(); model != tt.wantModel {
				t.Fatalf("model = %q, want %q; body=%s", model, tt.wantModel, string(got))
			}
		})
	}
}
