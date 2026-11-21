package helps

import (
	"context"
	"strconv"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestPrepareGeminiEmbeddingDocKeepsNativeSinglePayload(t *testing.T) {
	doc, err := PrepareGeminiEmbeddingDoc([]byte(`{"content":{"parts":[{"text":"hi"}]}}`), "gemini-embedding-001", GeminiEmbedContentAction)
	if err != nil {
		t.Fatalf("PrepareGeminiEmbeddingDoc() error = %v", err)
	}
	if got := doc.Get("model").String(); got != "models/gemini-embedding-001" {
		t.Errorf("model = %q, want models/gemini-embedding-001", got)
	}
	if got := doc.Get("content.parts.0.text").String(); got != "hi" {
		t.Errorf("content text = %q, want hi", got)
	}
	assertNoChatFields(t, doc.Bytes())
}

func TestPrepareGeminiEmbeddingDocNormalizesBareModelName(t *testing.T) {
	doc, err := PrepareGeminiEmbeddingDoc([]byte(`{"model":"gemini-embedding-2","content":{"parts":[{"text":"hi"}]}}`), "gemini-embedding-2", GeminiEmbedContentAction)
	if err != nil {
		t.Fatalf("PrepareGeminiEmbeddingDoc() error = %v", err)
	}
	if got := doc.Get("model").String(); got != "models/gemini-embedding-2" {
		t.Errorf("model = %q, want models/gemini-embedding-2", got)
	}
}

func TestPrepareGeminiEmbeddingDocKeepsBatchEntriesUntouched(t *testing.T) {
	raw := `{"requests":[{"model":"models/gemini-embedding-001","content":{"parts":[{"text":"first"}]}},{"model":"models/gemini-embedding-001","content":{"parts":[{"text":"second"}]}}]}`
	doc, err := PrepareGeminiEmbeddingDoc([]byte(raw), "gemini-embedding-001", GeminiBatchEmbedContentsAction)
	if err != nil {
		t.Fatalf("PrepareGeminiEmbeddingDoc() error = %v", err)
	}
	out := doc.Bytes()
	if doc.Get("model").Exists() {
		t.Errorf("batch payload must not gain a top-level model: %s", out)
	}
	if got := ojson.GetBytes(out, "requests.0.content.parts.0.text").String(); got != "first" {
		t.Errorf("requests[0] text = %q, want first", got)
	}
	if got := ojson.GetBytes(out, "requests.1.content.parts.0.text").String(); got != "second" {
		t.Errorf("requests[1] text = %q, want second", got)
	}
	assertNoChatFields(t, out)
}

func TestPrepareGeminiEmbeddingDocRejectsMalformedPayloads(t *testing.T) {
	for _, tt := range []struct {
		name    string
		payload string
		action  string
	}{
		{name: "empty body", payload: "", action: GeminiEmbedContentAction},
		{name: "blank body", payload: "   ", action: GeminiEmbedContentAction},
		{name: "array body", payload: `[]`, action: GeminiEmbedContentAction},
		{name: "null body", payload: `null`, action: GeminiEmbedContentAction},
		{name: "string body", payload: `"hi"`, action: GeminiEmbedContentAction},
		{name: "invalid json", payload: `{"content":`, action: GeminiEmbedContentAction},
		{name: "batch without requests", payload: `{"content":{}}`, action: GeminiBatchEmbedContentsAction},
		{name: "batch requests not array", payload: `{"requests":{}}`, action: GeminiBatchEmbedContentsAction},
		{name: "batch empty requests", payload: `{"requests":[]}`, action: GeminiBatchEmbedContentsAction},
		{name: "unsupported action", payload: `{"content":{}}`, action: "predict"},
		{name: "empty action", payload: `{"content":{}}`, action: ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if doc, err := PrepareGeminiEmbeddingDoc([]byte(tt.payload), "gemini-embedding-001", tt.action); err == nil {
				t.Fatalf("PrepareGeminiEmbeddingDoc() = %s, want error", doc.Bytes())
			}
		})
	}
}

func TestPrepareGeminiEmbeddingDocRequiresModel(t *testing.T) {
	if _, err := PrepareGeminiEmbeddingDoc([]byte(`{"content":{}}`), "  ", GeminiEmbedContentAction); err == nil {
		t.Fatal("expected error for empty model")
	}
}

func TestGeminiEmbeddingPayloadRulesApplyLastOnFinalBarrier(t *testing.T) {
	cfg := &config.Config{Payload: config.PayloadConfig{
		Override: []config.PayloadRule{{
			Models: []config.PayloadModelRule{{Name: "gemini-embedding-001"}},
			Params: map[string]any{"model": "models/from-rules", "taskType": "RETRIEVAL_QUERY"},
		}},
		Filter: []config.PayloadFilterRule{{
			Models: []config.PayloadModelRule{{Name: "gemini-embedding-001"}},
			Params: []string{"outputDimensionality"},
		}},
	}}
	raw := []byte(`{"model":"models/gemini-embedding-001","content":{"parts":[{"text":"hi"}]},"outputDimensionality":256}`)
	doc, err := PrepareGeminiEmbeddingDoc(raw, "gemini-embedding-001", GeminiEmbedContentAction)
	if err != nil {
		t.Fatalf("PrepareGeminiEmbeddingDoc() error = %v", err)
	}

	req := cliproxyexecutor.Request{Model: "gemini-embedding-001", Payload: raw}
	opts := cliproxyexecutor.Options{}
	ctx := WithPayloadFinalizer(context.Background(), NewPayloadFinalizer(cfg, "gemini", "gemini-embedding-001", "gemini", "", raw, req, opts))
	FinalizePayloadDoc(ctx, doc)
	out := doc.Bytes()

	if got := ojson.GetBytes(out, "model").String(); got != "models/from-rules" {
		t.Errorf("model = %q, want configured override to win", got)
	}
	if got := ojson.GetBytes(out, "taskType").String(); got != "RETRIEVAL_QUERY" {
		t.Errorf("taskType = %q, want RETRIEVAL_QUERY", got)
	}
	if ojson.GetBytes(out, "outputDimensionality").Exists() {
		t.Errorf("outputDimensionality must be filtered: %s", out)
	}
	assertNoChatFields(t, out)
}

func TestGeminiEmbeddingActionReadsRequestMetadata(t *testing.T) {
	for _, tt := range []struct {
		name string
		meta map[string]any
		want string
	}{
		{name: "absent", meta: nil},
		{name: "count tokens", meta: map[string]any{cliproxyexecutor.RequestActionMetadataKey: "countTokens"}},
		{name: "generate", meta: map[string]any{cliproxyexecutor.RequestActionMetadataKey: "generateContent"}},
		{name: "unknown embedding", meta: map[string]any{cliproxyexecutor.RequestActionMetadataKey: "predict"}},
		{name: "embed content", meta: map[string]any{cliproxyexecutor.RequestActionMetadataKey: GeminiEmbedContentAction}, want: GeminiEmbedContentAction},
		{name: "batch embed", meta: map[string]any{cliproxyexecutor.RequestActionMetadataKey: " " + GeminiBatchEmbedContentsAction + " "}, want: GeminiBatchEmbedContentsAction},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := GeminiEmbeddingAction(cliproxyexecutor.Request{Metadata: tt.meta}); got != tt.want {
				t.Fatalf("GeminiEmbeddingAction() = %q, want %q", got, tt.want)
			}
		})
	}
}

// assertNoChatFields guards the embedding contract: chat-only fields must never be injected.
func assertNoChatFields(t *testing.T, payload []byte) {
	t.Helper()
	for _, path := range []string{"contents", "safetySettings", "tools", "generationConfig", "thinkingConfig", "systemInstruction"} {
		if ojson.GetBytes(payload, path).Exists() {
			t.Fatalf("embedding payload must not contain %q: %s", path, payload)
		}
	}
}

func TestPrepareGeminiEmbeddingDocDefaultsMissingBatchEntryModels(t *testing.T) {
	raw := `{"requests":[{"content":{"parts":[{"text":"first"}]}},{"content":{"parts":[{"text":"second"}]}}]}`
	doc, err := PrepareGeminiEmbeddingDoc([]byte(raw), "gemini-embedding-2", GeminiBatchEmbedContentsAction)
	if err != nil {
		t.Fatalf("PrepareGeminiEmbeddingDoc() error = %v", err)
	}
	out := doc.Bytes()
	for i := 0; i < 2; i++ {
		path := "requests." + strconv.Itoa(i) + ".model"
		if got := ojson.GetBytes(out, path).String(); got != "models/gemini-embedding-2" {
			t.Errorf("%s = %q, want models/gemini-embedding-2", path, got)
		}
	}
	// Order and per-entry content must survive the fill.
	if got := ojson.GetBytes(out, "requests.0.content.parts.0.text").String(); got != "first" {
		t.Errorf("requests[0] text = %q, want first", got)
	}
	if got := ojson.GetBytes(out, "requests.1.content.parts.0.text").String(); got != "second" {
		t.Errorf("requests[1] text = %q, want second", got)
	}
}

func TestPrepareGeminiEmbeddingDocFillsOnlyEmptyBatchEntryModels(t *testing.T) {
	raw := `{"requests":[` +
		`{"content":{"parts":[{"text":"a"}]}},` +
		`{"model":"","content":{"parts":[{"text":"b"}]}},` +
		`{"model":null,"content":{"parts":[{"text":"c"}]}},` +
		`{"model":"models/gemini-embedding-001","content":{"parts":[{"text":"d"}]}},` +
		`{"model":"models/other-embedding","content":{"parts":[{"text":"e"}]}}` +
		`]}`
	doc, err := PrepareGeminiEmbeddingDoc([]byte(raw), "gemini-embedding-2", GeminiBatchEmbedContentsAction)
	if err != nil {
		t.Fatalf("PrepareGeminiEmbeddingDoc() error = %v", err)
	}
	out := doc.Bytes()
	want := []string{
		"models/gemini-embedding-2",
		"models/gemini-embedding-2",
		"models/gemini-embedding-2",
		"models/gemini-embedding-001",
		"models/other-embedding",
	}
	for i, expected := range want {
		path := "requests." + strconv.Itoa(i) + ".model"
		if got := ojson.GetBytes(out, path).String(); got != expected {
			t.Errorf("%s = %q, want %q", path, got, expected)
		}
	}
}

func TestPrepareGeminiEmbeddingDocLeavesExplicitBatchModelsAlone(t *testing.T) {
	raw := `{"requests":[{"model":"models/gemini-embedding-001","content":{"parts":[{"text":"only"}]}}]}`
	doc, err := PrepareGeminiEmbeddingDoc([]byte(raw), "gemini-embedding-2", GeminiBatchEmbedContentsAction)
	if err != nil {
		t.Fatalf("PrepareGeminiEmbeddingDoc() error = %v", err)
	}
	if got := ojson.GetBytes(doc.Bytes(), "requests.0.model").String(); got != "models/gemini-embedding-001" {
		t.Fatalf("requests[0].model = %q, want caller value untouched", got)
	}
}

func TestPrepareGeminiEmbeddingDocBatchFillIsFinalBarrierSafe(t *testing.T) {
	// A payload rule that filters entry model fields must win over the default fill,
	// because rules are evaluated after native preparation.
	cfg := &config.Config{Payload: config.PayloadConfig{
		Filter: []config.PayloadFilterRule{{
			Models: []config.PayloadModelRule{{Name: "gemini-embedding-2"}},
			Params: []string{"requests.0.model"},
		}},
	}}
	raw := []byte(`{"requests":[{"content":{"parts":[{"text":"a"}]}},{"content":{"parts":[{"text":"b"}]}}]}`)
	doc, err := PrepareGeminiEmbeddingDoc(raw, "gemini-embedding-2", GeminiBatchEmbedContentsAction)
	if err != nil {
		t.Fatalf("PrepareGeminiEmbeddingDoc() error = %v", err)
	}
	req := cliproxyexecutor.Request{Model: "gemini-embedding-2", Payload: raw}
	ctx := WithPayloadFinalizer(context.Background(), NewPayloadFinalizer(cfg, "gemini", "gemini-embedding-2", "gemini", "", raw, req, cliproxyexecutor.Options{}))
	FinalizePayloadDoc(ctx, doc)
	out := doc.Bytes()
	if ojson.GetBytes(out, "requests.0.model").Exists() {
		t.Errorf("filtered field was restored: %s", out)
	}
	if got := ojson.GetBytes(out, "requests.1.model").String(); got != "models/gemini-embedding-2" {
		t.Errorf("requests[1].model = %q, want default fill kept", got)
	}
}

// TestPrepareGeminiEmbeddingDocNormalizesBareBatchEntryModels covers the resource-name
// format upstream enforces per batch entry: a bare identifier (accepted by embedContent)
// is rejected with "unexpected model name format", so it is normalized here.
func TestPrepareGeminiEmbeddingDocNormalizesBareBatchEntryModels(t *testing.T) {
	raw := `{"requests":[{"model":"gemini-embedding-001","content":{"parts":[{"text":"a"}]}},{"model":"models/gemini-embedding-2","content":{"parts":[{"text":"b"}]}},{"model":"publishers/google/models/x","content":{"parts":[{"text":"c"}]}},{"content":{"parts":[{"text":"d"}]}}]}`
	doc, err := PrepareGeminiEmbeddingDoc([]byte(raw), "gemini-embedding-001", GeminiBatchEmbedContentsAction)
	if err != nil {
		t.Fatalf("PrepareGeminiEmbeddingDoc() error = %v", err)
	}
	for i, want := range []string{
		"models/gemini-embedding-001", // bare id normalized
		"models/gemini-embedding-2",   // already a resource name, untouched
		"publishers/google/models/x",  // other resource form, left for upstream
		"models/gemini-embedding-001", // missing entry inherits the path model
	} {
		got := ojson.GetBytes(doc.Bytes(), "requests."+strconv.Itoa(i)+".model").String()
		if got != want {
			t.Fatalf("requests[%d].model = %q, want %q", i, got, want)
		}
	}
}

// TestNormalizeGeminiModelResourceName covers the resource-name form that configured aliases
// and router targets may produce: keeping the prefix would build models/models/<id>.
func TestNormalizeGeminiModelResourceName(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{"gemini-embedding-001", "gemini-embedding-001"},
		{"models/gemini-embedding-001", "gemini-embedding-001"},
		{"  models/gemini-embedding-002  ", "gemini-embedding-002"},
		{"", ""},
	} {
		if got := NormalizeGeminiModelResourceName(tt.in); got != tt.want {
			t.Fatalf("NormalizeGeminiModelResourceName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestPrepareGeminiEmbeddingDocAcceptResourceFormModel guards the payload side of the same
// problem: a resource-form model must not produce models/models/<id>.
func TestPrepareGeminiEmbeddingDocAcceptResourceFormModel(t *testing.T) {
	doc, err := PrepareGeminiEmbeddingDoc([]byte(`{"content":{"parts":[{"text":"hi"}]}}`), "models/gemini-embedding-001", GeminiEmbedContentAction)
	if err != nil {
		t.Fatalf("PrepareGeminiEmbeddingDoc() error = %v", err)
	}
	if got := ojson.GetBytes(doc.Bytes(), "model").String(); got != "models/gemini-embedding-001" {
		t.Fatalf("model = %q, want models/gemini-embedding-001", got)
	}

	batch, err := PrepareGeminiEmbeddingDoc([]byte(`{"requests":[{"content":{"parts":[{"text":"hi"}]}}]}`), "models/gemini-embedding-001", GeminiBatchEmbedContentsAction)
	if err != nil {
		t.Fatalf("PrepareGeminiEmbeddingDoc() batch error = %v", err)
	}
	if got := ojson.GetBytes(batch.Bytes(), "requests.0.model").String(); got != "models/gemini-embedding-001" {
		t.Fatalf("requests[0].model = %q, want models/gemini-embedding-001", got)
	}
}
