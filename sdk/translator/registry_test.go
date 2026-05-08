package translator

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestTranslateRequest_FallbackNormalizesModel(t *testing.T) {
	r := NewRegistry()

	tests := []struct {
		name          string
		model         string
		payload       string
		wantModel     string
		wantUnchanged bool
	}{
		{
			name:      "prefixed model is rewritten",
			model:     "gpt-5-mini",
			payload:   `{"model":"copilot/gpt-5-mini","input":"ping"}`,
			wantModel: "gpt-5-mini",
		},
		{
			name:          "matching model is left unchanged",
			model:         "gpt-5-mini",
			payload:       `{"model":"gpt-5-mini","input":"ping"}`,
			wantModel:     "gpt-5-mini",
			wantUnchanged: true,
		},
		{
			name:          "empty model leaves payload unchanged",
			model:         "",
			payload:       `{"model":"copilot/gpt-5-mini","input":"ping"}`,
			wantModel:     "copilot/gpt-5-mini",
			wantUnchanged: true,
		},
		{
			name:      "deeply prefixed model is rewritten",
			model:     "gpt-5.3-codex",
			payload:   `{"model":"team/gpt-5.3-codex","stream":true}`,
			wantModel: "gpt-5.3-codex",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := []byte(tt.payload)
			got := r.TranslateRequest(Format("a"), Format("b"), tt.model, input, false)

			gotModel := gjson.GetBytes(got, "model").String()
			if gotModel != tt.wantModel {
				t.Errorf("model = %q, want %q", gotModel, tt.wantModel)
			}

			if tt.wantUnchanged && string(got) != tt.payload {
				t.Errorf("payload was modified when it should not have been:\ngot:  %s\nwant: %s", got, tt.payload)
			}

			// Verify other fields are preserved.
			for _, key := range []string{"input", "stream"} {
				orig := gjson.Get(tt.payload, key)
				if !orig.Exists() {
					continue
				}
				after := gjson.GetBytes(got, key)
				if orig.Raw != after.Raw {
					t.Errorf("field %q changed: got %s, want %s", key, after.Raw, orig.Raw)
				}
			}
		})
	}
}

func TestTranslateRequest_RegisteredTransformTakesPrecedence(t *testing.T) {
	r := NewRegistry()
	from := Format("openai-response")
	to := Format("openai-response")

	r.Register(from, to, func(model string, rawJSON []byte, stream bool) []byte {
		return []byte(`{"model":"from-transform"}`)
	}, ResponseTransform{})

	input := []byte(`{"model":"copilot/gpt-5-mini","input":"ping"}`)
	got := r.TranslateRequest(from, to, "gpt-5-mini", input, false)

	gotModel := gjson.GetBytes(got, "model").String()
	if gotModel != "from-transform" {
		t.Errorf("expected registered transform to take precedence, got model = %q", gotModel)
	}
}
