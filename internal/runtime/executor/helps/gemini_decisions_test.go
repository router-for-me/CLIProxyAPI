package helps

import (
	"context"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

const decisionsSamplePayload = `{"input":"The blue mug I ordered on Monday arrived cracked on Wednesday.","questions":{"is_damaged":{"type":"noul","instructions":"Did the item arrive damaged?"}}}`

func TestPrepareGeminiDecisionsDocKeepsNativePayload(t *testing.T) {
	doc, err := PrepareGeminiDecisionsDoc([]byte(decisionsSamplePayload), "gemini-decision-1")
	if err != nil {
		t.Fatalf("PrepareGeminiDecisionsDoc() error = %v", err)
	}
	if got := doc.Get("model").String(); got != "gemini-decision-1" {
		t.Errorf("model = %q, want bare gemini-decision-1", got)
	}
	if got := doc.Get("input").String(); got == "" {
		t.Error("input was dropped")
	}
	if got := doc.Get("questions.is_damaged.type").String(); got != "noul" {
		t.Errorf("questions.is_damaged.type = %q, want noul", got)
	}
	assertNoDecisionsChatFields(t, doc.Bytes())
}

func TestPrepareGeminiDecisionsDocStripsModelsPrefixFromBodyModel(t *testing.T) {
	raw := `{"model":"models/gemini-decision-1","input":"hi","questions":{"q":{"type":"noul","instructions":"ok?"}}}`
	doc, err := PrepareGeminiDecisionsDoc([]byte(raw), "gemini-decision-1")
	if err != nil {
		t.Fatalf("PrepareGeminiDecisionsDoc() error = %v", err)
	}
	if got := doc.Get("model").String(); got != "gemini-decision-1" {
		t.Errorf("model = %q, want bare model ID without models/ prefix", got)
	}
}

func TestPrepareGeminiDecisionsDocAcceptsResourceNameBaseModel(t *testing.T) {
	for _, baseModel := range []string{"models/gemini-decision-1", " gemini-decision-1 "} {
		doc, err := PrepareGeminiDecisionsDoc([]byte(`{"input":"hi","questions":{"q":{"type":"noul"}}}`), baseModel)
		if err != nil {
			t.Fatalf("baseModel %q: PrepareGeminiDecisionsDoc() error = %v", baseModel, err)
		}
		if got := doc.Get("model").String(); got != "gemini-decision-1" {
			t.Errorf("baseModel %q: model = %q, want bare gemini-decision-1", baseModel, got)
		}
	}
}

func TestPrepareGeminiDecisionsDocKeepsMultimodalInputUntouched(t *testing.T) {
	raw := `{"input":[{"type":"text","text":"compare"},{"type":"image","mime_type":"image/png","data":"AAA"}],"questions":{"q":{"type":"choice","instructions":"which?","criteria":["a","b"]}}}`
	doc, err := PrepareGeminiDecisionsDoc([]byte(raw), "gemini-decision-1")
	if err != nil {
		t.Fatalf("PrepareGeminiDecisionsDoc() error = %v", err)
	}
	out := doc.Bytes()
	if got := ojson.GetBytes(out, "input.0.text").String(); got != "compare" {
		t.Errorf("input[0].text = %q, want compare", got)
	}
	if got := ojson.GetBytes(out, "input.1.mime_type").String(); got != "image/png" {
		t.Errorf("input[1].mime_type = %q, want image/png", got)
	}
	if got := ojson.GetBytes(out, "questions.q.criteria.1").String(); got != "b" {
		t.Errorf("criteria[1] = %q, want b", got)
	}
	assertNoDecisionsChatFields(t, out)
}

func TestPrepareGeminiDecisionsDocRejectsMalformedEnvelopes(t *testing.T) {
	for _, tt := range []struct {
		name    string
		payload string
	}{
		{name: "empty body", payload: ""},
		{name: "blank body", payload: "  "},
		{name: "array body", payload: `[]`},
		{name: "null body", payload: `null`},
		{name: "invalid json", payload: `{"questions":`},
		{name: "missing questions", payload: `{"input":"hi"}`},
		{name: "questions not object", payload: `{"input":"hi","questions":[]}`},
		{name: "empty questions", payload: `{"input":"hi","questions":{}}`},
		{name: "missing input", payload: `{"questions":{"q":{"type":"noul"}}}`},
		{name: "null input", payload: `{"input":null,"questions":{"q":{"type":"noul"}}}`},
		{name: "stream true", payload: `{"input":"hi","questions":{"q":{"type":"noul"}},"stream":true}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if doc, err := PrepareGeminiDecisionsDoc([]byte(tt.payload), "gemini-decision-1"); err == nil {
				t.Fatalf("PrepareGeminiDecisionsDoc() = %s, want error", doc.Bytes())
			}
		})
	}
}

func TestPrepareGeminiDecisionsDocAllowsExplicitStreamFalse(t *testing.T) {
	raw := `{"input":"hi","questions":{"q":{"type":"noul"}},"stream":false}`
	if _, err := PrepareGeminiDecisionsDoc([]byte(raw), "gemini-decision-1"); err != nil {
		t.Fatalf("PrepareGeminiDecisionsDoc() error = %v", err)
	}
}

func TestPrepareGeminiDecisionsDocRequiresModel(t *testing.T) {
	if _, err := PrepareGeminiDecisionsDoc([]byte(decisionsSamplePayload), "  "); err == nil {
		t.Fatal("expected error for empty model")
	}
}

func TestPrepareGeminiDecisionsDocAppliesPayloadRulesLast(t *testing.T) {
	cfg := &config.Config{Payload: config.PayloadConfig{
		Override: []config.PayloadRule{{
			Models: []config.PayloadModelRule{{Name: "gemini-decision-1"}},
			Params: map[string]any{"questions.is_damaged.instructions": "Did it break?"},
		}},
		Filter: []config.PayloadFilterRule{{
			Models: []config.PayloadModelRule{{Name: "gemini-decision-1"}},
			Params: []string{"questions.is_damaged.criteria"},
		}},
	}}
	raw := []byte(`{"model":"gemini-decision-1","input":"hi","questions":{"is_damaged":{"type":"noul","instructions":"damaged?","criteria":["yes","no"]}}}`)
	doc, err := PrepareGeminiDecisionsDoc(raw, "gemini-decision-1")
	if err != nil {
		t.Fatalf("PrepareGeminiDecisionsDoc() error = %v", err)
	}

	req := cliproxyexecutor.Request{Model: "gemini-decision-1", Payload: raw}
	ctx := WithPayloadFinalizer(context.Background(), NewPayloadFinalizer(cfg, "gemini", "gemini-decision-1", "gemini", "", raw, req, cliproxyexecutor.Options{}))
	FinalizePayloadDoc(ctx, doc)
	out := doc.Bytes()

	if got := ojson.GetBytes(out, "questions.is_damaged.instructions").String(); got != "Did it break?" {
		t.Errorf("instructions = %q, want configured override", got)
	}
	if ojson.GetBytes(out, "questions.is_damaged.criteria").Exists() {
		t.Errorf("criteria must be filtered: %s", out)
	}
	if got := ojson.GetBytes(out, "model").String(); got != "gemini-decision-1" {
		t.Errorf("model = %q, want bare ID preserved", got)
	}
	assertNoDecisionsChatFields(t, out)
}

func TestParseGeminiDecisionsUsage(t *testing.T) {
	// Fixture shape from the official Decisions API documentation.
	body := []byte(`{"answers":{"is_damaged":{"type":"noul","noul":0.99,"confidence":0.99}},"usage":{"total_input_tokens":100,"input_tokens_by_modality":[{"modality":"text","tokens":100}],"total_output_tokens":0,"total_tokens":100}}`)
	detail := ParseGeminiDecisionsUsage(body)
	if detail.InputTokens != 100 {
		t.Errorf("InputTokens = %d, want 100", detail.InputTokens)
	}
	if detail.OutputTokens != 0 {
		t.Errorf("OutputTokens = %d, want 0", detail.OutputTokens)
	}
	if detail.TotalTokens != 100 {
		t.Errorf("TotalTokens = %d, want 100", detail.TotalTokens)
	}
	if detail.TextInputTokens == nil || *detail.TextInputTokens != 100 {
		t.Errorf("TextInputTokens = %v, want 100", detail.TextInputTokens)
	}
}

func TestParseGeminiDecisionsUsageMixedModalities(t *testing.T) {
	body := []byte(`{"answers":{},"usage":{"total_input_tokens":324,"input_tokens_by_modality":[{"modality":"text","tokens":200},{"modality":"image","tokens":100},{"modality":"audio","tokens":24},{"modality":"unknown","tokens":7}],"total_output_tokens":0,"total_tokens":324}}`)
	detail := ParseGeminiDecisionsUsage(body)
	if detail.InputTokens != 324 || detail.TotalTokens != 324 {
		t.Fatalf("tokens = %d/%d, want 324/324", detail.InputTokens, detail.TotalTokens)
	}
	assertTokenPointer(t, "text", detail.TextInputTokens, 200)
	assertTokenPointer(t, "image", detail.ImageInputTokens, 100)
	assertTokenPointer(t, "audio", detail.AudioInputTokens, 24)
	if detail.VideoInputTokens != nil || detail.FileInputTokens != nil {
		t.Error("untracked modalities must not be invented")
	}
}

func TestParseGeminiDecisionsUsageDerivesTotalWhenMissing(t *testing.T) {
	detail := ParseGeminiDecisionsUsage([]byte(`{"usage":{"total_input_tokens":42,"total_output_tokens":0}}`))
	if detail.TotalTokens != 42 {
		t.Errorf("TotalTokens = %d, want derived 42", detail.TotalTokens)
	}
}

func TestParseGeminiDecisionsUsageIgnoresForeignShapes(t *testing.T) {
	for _, tt := range []struct {
		name string
		body string
	}{
		{name: "empty", body: ""},
		{name: "invalid json", body: `{"usage":`},

		{name: "usage not object", body: `{"usage":[]}`},
		{name: "no usage", body: `{"answers":{}}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			detail := ParseGeminiDecisionsUsage([]byte(tt.body))
			if detail.InputTokens != 0 || detail.OutputTokens != 0 || detail.TotalTokens != 0 {
				t.Fatalf("detail = %+v, want zero usage", detail)
			}
		})
	}
}

func TestGeminiDecisionActionReadsRequestMetadata(t *testing.T) {
	for _, tt := range []struct {
		name string
		meta map[string]any
		want string
	}{
		{name: "absent", meta: nil},
		{name: "embed content", meta: map[string]any{cliproxyexecutor.RequestActionMetadataKey: GeminiEmbedContentAction}},
		{name: "count tokens", meta: map[string]any{cliproxyexecutor.RequestActionMetadataKey: "countTokens"}},
		{name: "decisions", meta: map[string]any{cliproxyexecutor.RequestActionMetadataKey: GeminiDecisionsAction}, want: GeminiDecisionsAction},
		{name: "decisions padded", meta: map[string]any{cliproxyexecutor.RequestActionMetadataKey: " decisions "}, want: GeminiDecisionsAction},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := GeminiDecisionAction(cliproxyexecutor.Request{Metadata: tt.meta}); got != tt.want {
				t.Fatalf("GeminiDecisionAction() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDecisionsIsNotAnEmbeddingAction(t *testing.T) {
	if IsGeminiEmbeddingAction(GeminiDecisionsAction) {
		t.Fatal("decisions must not be classified as an embedding action")
	}
	if IsGeminiDecisionsAction(GeminiEmbedContentAction) {
		t.Fatal("embedContent must not be classified as a decisions action")
	}
}

func assertTokenPointer(t *testing.T, label string, got *int64, want int64) {
	t.Helper()
	if got == nil {
		t.Errorf("%s = nil, want %d", label, want)
		return
	}
	if *got != want {
		t.Errorf("%s = %d, want %d", label, *got, want)
	}
}

// assertNoDecisionsChatFields guards the decisions contract: chat-only fields must never be injected.
func assertNoDecisionsChatFields(t *testing.T, payload []byte) {
	t.Helper()
	for _, path := range []string{"contents", "safetySettings", "tools", "generationConfig", "thinkingConfig", "systemInstruction"} {
		if ojson.GetBytes(payload, path).Exists() {
			t.Fatalf("decisions payload must not contain %q: %s", path, payload)
		}
	}
}
