package auth

import (
	"bytes"
	"errors"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestNormalizeProviderBoundResponseHistoryPreservesSemanticReasoning(t *testing.T) {
	body := []byte(`{
		"model":"gpt-5.6",
		"input":[
			{"type":"reasoning","id":"foreign_reasoning","encrypted_content":"opaque","summary":[{"type":"summary_text","text":"portable summary"}]},
			{"type":"message","id":"foreign_message","role":"user","content":[{"type":"input_text","text":"continue"}]}
		]
	}`)

	got, err := normalizeProviderBoundResponseHistory(body)
	if err != nil {
		t.Fatalf("normalizeProviderBoundResponseHistory() error = %v", err)
	}
	if !got.Changed || got.StrippedFields != 3 || got.DroppedItems != 0 {
		t.Fatalf("normalization metadata = %#v", got)
	}
	if value := gjson.GetBytes(got.Body, "input.0.summary.0.text").String(); value != "portable summary" {
		t.Fatalf("reasoning summary = %q, want portable summary", value)
	}
	if gjson.GetBytes(got.Body, "input.0.id").Exists() || gjson.GetBytes(got.Body, "input.0.encrypted_content").Exists() {
		t.Fatalf("provider-bound reasoning identity survived: %s", got.Body)
	}
	if gjson.GetBytes(got.Body, "input.1.id").Exists() {
		t.Fatalf("provider-bound message identity survived: %s", got.Body)
	}
}

func TestNormalizeProviderBoundResponseHistoryDropsForeignCompaction(t *testing.T) {
	body := []byte(`{
		"input":[
			{"type":"message","role":"user","content":"history before compaction"},
			{"type":"compaction","id":"cmp_foreign","encrypted_content":"opaque"},
			{"type":"message","role":"user","content":"continue"}
		]
	}`)

	got, err := normalizeProviderBoundResponseHistory(body)
	if err != nil {
		t.Fatalf("normalizeProviderBoundResponseHistory() error = %v", err)
	}
	if !got.RequiresTargetCompaction || got.TargetCompactionProtocol != "remote_compaction_v2" {
		t.Fatalf("target compaction metadata = %#v", got)
	}
	if value := gjson.GetBytes(got.Body, "input.#").Int(); value != 2 {
		t.Fatalf("input length = %d, want 2; body=%s", value, got.Body)
	}
	if value := gjson.GetBytes(got.Body, "input.0.type").String(); value != "message" {
		t.Fatalf("remaining item type = %q, want message", value)
	}
}

func TestNormalizeProviderBoundResponseHistoryPreservesCustomToolPairs(t *testing.T) {
	body := []byte(`{
		"input":[
			{"type":"message","role":"user","content":"continue"},
			{"type":"custom_tool_call","id":"foreign_call","call_id":"call_1","name":"shell","input":"pwd"},
			{"type":"custom_tool_call_output","id":"foreign_output","call_id":"call_1","output":"/tmp"}
		]
	}`)

	got, err := normalizeProviderBoundResponseHistory(body)
	if err != nil {
		t.Fatalf("normalizeProviderBoundResponseHistory() error = %v", err)
	}
	if !got.Changed || got.StrippedFields != 2 {
		t.Fatalf("normalization metadata = %#v", got)
	}
	if gjson.GetBytes(got.Body, "input.1.id").Exists() || gjson.GetBytes(got.Body, "input.2.id").Exists() {
		t.Fatalf("provider-bound custom tool identity survived: %s", got.Body)
	}
	if value := gjson.GetBytes(got.Body, "input.1.call_id").String(); value != "call_1" {
		t.Fatalf("custom tool call_id = %q, want call_1", value)
	}
}

func TestNormalizeProviderBoundResponseHistoryPreservesLargeJSONNumbers(t *testing.T) {
	body := []byte(`{
		"metadata":{"exact_integer":9007199254740993},
		"input":[{"type":"message","id":"foreign_message","role":"user","content":"continue"}]
	}`)

	got, err := normalizeProviderBoundResponseHistory(body)
	if err != nil {
		t.Fatalf("normalizeProviderBoundResponseHistory() error = %v", err)
	}
	if !bytes.Contains(got.Body, []byte(`9007199254740993`)) {
		t.Fatalf("large integer lost precision: %s", got.Body)
	}
}

func TestNormalizeProviderBoundResponseHistoryRejectsUnsafeReplay(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		reason string
	}{
		{
			name:   "foreign compaction without rehydratable prefix",
			body:   `{"input":[{"type":"compaction","id":"cmp_foreign","encrypted_content":"opaque"},{"type":"message","role":"user","content":"continue"}]}`,
			reason: "foreign_compaction_requires_rehydration",
		},
		{
			name:   "opaque reasoning only",
			body:   `{"input":[{"type":"reasoning","id":"foreign","encrypted_content":"opaque","summary":[]}]}`,
			reason: "no_provider_neutral_history",
		},
		{
			name:   "unpaired tool output",
			body:   `{"input":[{"type":"message","role":"user","content":"continue"},{"type":"function_call_output","call_id":"call_missing","output":"result"}]}`,
			reason: "unpaired_tool_history",
		},
		{
			name:   "unsupported item",
			body:   `{"input":[{"type":"computer_call","id":"foreign"}]}`,
			reason: "unsupported_history_item",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := normalizeProviderBoundResponseHistory([]byte(tt.body))
			var historyErr *providerHistoryError
			if !errors.As(err, &historyErr) || historyErr.reason != tt.reason {
				t.Fatalf("error = %v, want providerHistoryError(%s)", err, tt.reason)
			}
			if !historyErr.IsRequestScoped() || historyErr.StatusCode() != 409 {
				t.Fatalf("history error contract = %#v", historyErr)
			}
		})
	}
}

func TestManagerNormalizesHistoryOnlyWhenSessionChangesUpstreamScope(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	t.Cleanup(manager.StopAutoRefresh)

	body := []byte(`{
		"prompt_cache_key":"thread-42",
		"input":[
			{"type":"reasoning","id":"reasoning_from_a","encrypted_content":"opaque","summary":[{"type":"summary_text","text":"plan"}]},
			{"type":"message","role":"user","content":"continue"}
		]
	}`)
	sourceOptions := cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		OriginalRequest: bytes.Clone(body),
	}
	manager.rememberProviderHistoryScope(Result{
		AuthID:   "codex-a",
		Provider: "codex",
		Success:  true,
		Options:  sourceOptions,
	})

	sameScopeOptions := sourceOptions
	sameScopeOptions.Metadata = map[string]any{cliproxyexecutor.SelectedAuthMetadataKey: "codex-a"}
	sameReq, _, errSame := manager.applyRequestAfterAuthInterceptor(nil, nil, &Auth{ID: "codex-a", Provider: "codex"}, "codex", cliproxyexecutor.Request{Payload: bytes.Clone(body)}, sameScopeOptions, "gpt-5.6")
	if errSame != nil {
		t.Fatalf("same-scope request error = %v", errSame)
	}
	if !bytes.Equal(sameReq.Payload, body) {
		t.Fatalf("same-scope request changed:\n%s", sameReq.Payload)
	}

	crossScopeOptions := sourceOptions
	crossScopeOptions.Metadata = map[string]any{cliproxyexecutor.SelectedAuthMetadataKey: "codex-b"}
	crossReq, crossOpts, errCross := manager.applyRequestAfterAuthInterceptor(nil, nil, &Auth{ID: "codex-b", Provider: "codex"}, "codex", cliproxyexecutor.Request{Payload: bytes.Clone(body)}, crossScopeOptions, "gpt-5.6")
	if errCross != nil {
		t.Fatalf("cross-scope request error = %v", errCross)
	}
	if gjson.GetBytes(crossReq.Payload, "input.0.id").Exists() || gjson.GetBytes(crossReq.Payload, "input.0.encrypted_content").Exists() {
		t.Fatalf("cross-scope identity survived: %s", crossReq.Payload)
	}
	if !bytes.Equal(crossReq.Payload, crossOpts.OriginalRequest) {
		t.Fatalf("translated payload and OriginalRequest diverged")
	}
}

func TestManagerRemembersScopeAfterCommittedStreamFailure(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	t.Cleanup(manager.StopAutoRefresh)
	body := []byte(`{"prompt_cache_key":"thread-committed","input":[{"type":"message","role":"user","content":"continue"}]}`)
	manager.rememberProviderHistoryScope(Result{
		AuthID:   "ark",
		Provider: "codex",
		Success:  false,
		Options: cliproxyexecutor.Options{
			SourceFormat:    sdktranslator.FormatOpenAIResponse,
			OriginalRequest: body,
			Metadata: map[string]any{
				cliproxyexecutor.ProviderOutputCommittedMetadataKey: true,
			},
		},
	})
	if scope, ok := manager.providerHistoryScopes.Get("pck:thread-committed"); !ok || scope != "codex::ark" {
		t.Fatalf("committed failure scope = (%q, %t), want (codex::ark, true)", scope, ok)
	}
}
