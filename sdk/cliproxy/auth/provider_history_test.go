package auth

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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

func TestNormalizeProviderBoundResponseHistoryRejectsForeignCompactionWithNeutralPrefix(t *testing.T) {
	body := []byte(`{
		"input":[
			{"type":"message","role":"user","content":"history before compaction"},
			{"type":"compaction","id":"cmp_foreign","encrypted_content":"opaque"},
			{"type":"message","role":"user","content":"continue"}
		]
	}`)

	_, err := normalizeProviderBoundResponseHistory(body)
	var historyErr *providerHistoryError
	if !errors.As(err, &historyErr) || historyErr.reason != "foreign_compaction_requires_rehydration" {
		t.Fatalf("error = %v, want providerHistoryError(foreign_compaction_requires_rehydration)", err)
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

func TestNormalizeProviderBoundResponseHistoryDropsKnownOrphanHostOutput(t *testing.T) {
	items := []any{map[string]any{"type": "message", "role": "user", "content": "continue"}}
	for index := 0; index < 40; index++ {
		callID := fmt.Sprintf("call_%02d", index)
		items = append(items,
			map[string]any{"type": "custom_tool_call", "id": "foreign_call_" + callID, "call_id": callID, "name": "shell", "input": "pwd"},
			map[string]any{"type": "custom_tool_call_output", "id": "foreign_output_" + callID, "call_id": callID, "output": "ok"},
		)
	}
	items = append(items, map[string]any{
		"type":   "function_call_output",
		"id":     "host_injected_output",
		"name":   "automation_update",
		"output": `{"status":"applied"}`,
	})
	body, errMarshal := json.Marshal(map[string]any{"input": items})
	if errMarshal != nil {
		t.Fatalf("marshal fixture: %v", errMarshal)
	}

	got, err := normalizeProviderBoundResponseHistory(body)
	if err != nil {
		t.Fatalf("normalizeProviderBoundResponseHistory() error = %v", err)
	}
	if !got.Changed || got.DroppedItems != 1 || got.StrippedFields != 80 {
		t.Fatalf("normalization metadata = %#v", got)
	}
	if count := int(gjson.GetBytes(got.Body, "input.#").Int()); count != 81 {
		t.Fatalf("normalized input count = %d, want 81", count)
	}
	if bytes.Contains(got.Body, []byte("automation_update")) {
		t.Fatalf("recoverable orphan host output survived: %s", got.Body)
	}
}

func TestNormalizeProviderBoundResponseHistoryRejectsUnknownOrPairedMissingCallID(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "unknown host output",
			body: `{"input":[{"type":"message","role":"user","content":"continue"},{"type":"function_call_output","name":"unknown_host_tool","output":"ok"}]}`,
		},
		{
			name: "matching call exists",
			body: `{"input":[{"type":"message","role":"user","content":"continue"},{"type":"function_call","call_id":"call_1","name":"automation_update","arguments":"{}"},{"type":"function_call_output","name":"automation_update","output":"ok"}]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := normalizeProviderBoundResponseHistory([]byte(tt.body))
			var historyErr *providerHistoryError
			if !errors.As(err, &historyErr) || historyErr.reason != "missing_call_id" {
				t.Fatalf("error = %v, want providerHistoryError(missing_call_id)", err)
			}
		})
	}
}

func TestNormalizeProviderBoundResponseHistoryPreservesAdditionalTools(t *testing.T) {
	body := []byte(`{
		"input":[
			{"type":"reasoning","id":"foreign_reasoning","encrypted_content":"opaque","summary":[{"type":"summary_text","text":"use the available tools"}]},
			{"type":"message","id":"foreign_message","role":"user","content":[{"type":"input_text","text":"continue"}]},
			{"type":"additional_tools","id":"foreign_tools","tools":[
				{"type":"namespace","name":"functions","description":"Local functions","tools":[{"name":"view_image","description":"View an image","input_schema":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}]},
				{"type":"namespace","name":"collaboration","description":"Team coordination","tools":[{"name":"send_message","description":"Send a message","input_schema":{"type":"object","properties":{"target":{"type":"string"},"message":{"type":"string"}},"required":["target","message"]}}]}
			]}
		]
	}`)

	got, err := normalizeProviderBoundResponseHistory(body)
	if err != nil {
		t.Fatalf("normalizeProviderBoundResponseHistory() error = %v", err)
	}
	if !got.Changed || got.StrippedFields != 4 || got.DroppedItems != 0 {
		t.Fatalf("normalization metadata = %#v", got)
	}
	if value := gjson.GetBytes(got.Body, "input.2.tools.0.name").String(); value != "functions" {
		t.Fatalf("first namespace = %q, want functions", value)
	}
	if value := gjson.GetBytes(got.Body, "input.2.tools.0.tools.0.input_schema.properties.path.type").String(); value != "string" {
		t.Fatalf("tool schema path type = %q, want string", value)
	}
	if value := gjson.GetBytes(got.Body, "input.2.tools.1.name").String(); value != "collaboration" {
		t.Fatalf("second namespace = %q, want collaboration", value)
	}
	for _, path := range []string{"input.0.id", "input.0.encrypted_content", "input.1.id", "input.2.id"} {
		if gjson.GetBytes(got.Body, path).Exists() {
			t.Fatalf("provider-bound field %s survived: %s", path, got.Body)
		}
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
		{
			name:   "credential-bound input file",
			body:   `{"input":[{"type":"message","role":"user","content":[{"type":"input_file","file_id":"file_a"}]}]}`,
			reason: "foreign_file_reference_requires_rehydration",
		},
		{
			name:   "credential-bound input image",
			body:   `{"input":[{"type":"message","role":"user","content":[{"type":"input_image","file_id":"file_image_a"}]}]}`,
			reason: "foreign_file_reference_requires_rehydration",
		},
		{
			name:   "credential-bound hosted tool",
			body:   `{"tools":[{"type":"file_search","vector_store_ids":["vs_a"]}],"input":[{"type":"message","role":"user","content":"continue"}]}`,
			reason: "foreign_tool_resource_requires_rehydration",
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

func TestManagerTracksStablePromptCacheAliasAcrossRequestIDs(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	t.Cleanup(manager.StopAutoRefresh)

	body := []byte(`{"prompt_cache_key":"stable-thread","input":[{"type":"message","id":"foreign_message","role":"user","content":"continue"}]}`)
	manager.rememberProviderHistoryScope(Result{
		AuthID:   "codex-a",
		Provider: "codex",
		Success:  true,
		Options: cliproxyexecutor.Options{
			Headers:         http.Header{"X-Client-Request-Id": []string{"request-1"}},
			SourceFormat:    sdktranslator.FormatOpenAIResponse,
			OriginalRequest: bytes.Clone(body),
		},
	})

	opts := cliproxyexecutor.Options{
		Headers:         http.Header{"X-Client-Request-Id": []string{"request-2"}},
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		OriginalRequest: bytes.Clone(body),
		Metadata:        map[string]any{cliproxyexecutor.SelectedAuthMetadataKey: "codex-b"},
	}
	got, _, err := manager.applyRequestAfterAuthInterceptor(nil, nil, &Auth{ID: "codex-b", Provider: "codex"}, "codex", cliproxyexecutor.Request{Payload: bytes.Clone(body)}, opts, "gpt-5.6")
	if err != nil {
		t.Fatalf("cross-request-id handoff error = %v", err)
	}
	if gjson.GetBytes(got.Payload, "input.0.id").Exists() {
		t.Fatalf("stable prompt cache alias did not trigger projection: %s", got.Payload)
	}
}

func TestManagerContinuesNormalizingTaintedHistoryAfterCredentialHandoff(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	t.Cleanup(manager.StopAutoRefresh)

	firstBody := []byte(`{
		"prompt_cache_key":"thread-mixed",
		"input":[
			{"type":"reasoning","id":"reasoning_from_a","encrypted_content":"opaque-a","summary":[{"type":"summary_text","text":"plan-a"}]},
			{"type":"message","role":"user","content":"continue"}
		]
	}`)
	manager.rememberProviderHistoryScope(Result{
		AuthID:   "codex-a",
		Provider: "codex",
		Success:  true,
		Options: cliproxyexecutor.Options{
			SourceFormat:    sdktranslator.FormatOpenAIResponse,
			OriginalRequest: bytes.Clone(firstBody),
		},
	})

	firstOptions := cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		OriginalRequest: bytes.Clone(firstBody),
		Metadata:        map[string]any{cliproxyexecutor.SelectedAuthMetadataKey: "codex-b"},
	}
	firstReq, firstResultOptions, errFirst := manager.applyRequestAfterAuthInterceptor(nil, nil, &Auth{ID: "codex-b", Provider: "codex"}, "codex", cliproxyexecutor.Request{Payload: bytes.Clone(firstBody)}, firstOptions, "gpt-5.6")
	if errFirst != nil {
		t.Fatalf("first handoff error = %v", errFirst)
	}
	if gjson.GetBytes(firstReq.Payload, "input.0.id").Exists() {
		t.Fatalf("first handoff identity survived: %s", firstReq.Payload)
	}
	manager.rememberProviderHistoryScope(Result{
		AuthID:   "codex-b",
		Provider: "codex",
		Success:  true,
		Options:  firstResultOptions,
	})

	secondBody := []byte(`{
		"prompt_cache_key":"thread-mixed",
		"input":[
			{"type":"reasoning","id":"reasoning_from_a","encrypted_content":"opaque-a","summary":[{"type":"summary_text","text":"plan-a"}]},
			{"type":"message","role":"user","content":"continue"},
			{"type":"reasoning","id":"reasoning_from_b","encrypted_content":"opaque-b","summary":[{"type":"summary_text","text":"plan-b"}]},
			{"type":"message","role":"user","content":"continue again"}
		]
	}`)
	secondOptions := cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		OriginalRequest: bytes.Clone(secondBody),
		Metadata:        map[string]any{cliproxyexecutor.SelectedAuthMetadataKey: "codex-b"},
	}
	secondReq, _, errSecond := manager.applyRequestAfterAuthInterceptor(nil, nil, &Auth{ID: "codex-b", Provider: "codex"}, "codex", cliproxyexecutor.Request{Payload: bytes.Clone(secondBody)}, secondOptions, "gpt-5.6")
	if errSecond != nil {
		t.Fatalf("second same-scope request error = %v", errSecond)
	}
	if gjson.GetBytes(secondReq.Payload, "input.0.id").Exists() || gjson.GetBytes(secondReq.Payload, "input.2.id").Exists() {
		t.Fatalf("mixed provider identity survived: %s", secondReq.Payload)
	}
}

func TestManagerLeavesTaintedSameScopeIncrementalRequestUnchanged(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	t.Cleanup(manager.StopAutoRefresh)

	body := []byte(`{"prompt_cache_key":"thread-incremental","previous_response_id":"resp-b","input":[{"type":"message","id":"msg-new","role":"user","content":"continue"}]}`)
	manager.providerHistoryScopes.SetAliases(providerHistoryTaintedPrefix+providerHistoryScope("codex", "codex-b"), "pck:thread-incremental")
	opts := cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		OriginalRequest: bytes.Clone(body),
		Metadata:        map[string]any{cliproxyexecutor.SelectedAuthMetadataKey: "codex-b"},
	}

	got, _, err := manager.applyRequestAfterAuthInterceptor(nil, nil, &Auth{ID: "codex-b", Provider: "codex"}, "codex", cliproxyexecutor.Request{Payload: bytes.Clone(body)}, opts, "gpt-5.6")
	if err != nil {
		t.Fatalf("incremental same-scope request error = %v", err)
	}
	if !bytes.Equal(got.Payload, body) {
		t.Fatalf("incremental same-scope request changed:\n%s", got.Payload)
	}
}

func TestManagerNormalizesTaintedSameScopePreviousResponseWithMixedHistory(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	t.Cleanup(manager.StopAutoRefresh)

	body := []byte(`{"prompt_cache_key":"thread-mixed-incremental","previous_response_id":"resp-b","input":[{"type":"reasoning","id":"reasoning-a","encrypted_content":"opaque","summary":[{"type":"summary_text","text":"portable"}]},{"type":"message","role":"user","content":"continue"}]}`)
	manager.providerHistoryScopes.SetAliases(providerHistoryTaintedPrefix+providerHistoryScope("codex", "codex-b"), "pck:thread-mixed-incremental")
	opts := cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		OriginalRequest: bytes.Clone(body),
		Metadata:        map[string]any{cliproxyexecutor.SelectedAuthMetadataKey: "codex-b"},
	}

	got, _, err := manager.applyRequestAfterAuthInterceptor(nil, nil, &Auth{ID: "codex-b", Provider: "codex"}, "codex", cliproxyexecutor.Request{Payload: bytes.Clone(body)}, opts, "gpt-5.6")
	if err != nil {
		t.Fatalf("mixed same-scope incremental request error = %v", err)
	}
	if gjson.GetBytes(got.Payload, "input.0.id").Exists() || gjson.GetBytes(got.Payload, "input.0.encrypted_content").Exists() {
		t.Fatalf("mixed same-scope previous-response history bypassed projection: %s", got.Payload)
	}
}

func TestManagerRejectsForeignPreviousResponseContinuation(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	t.Cleanup(manager.StopAutoRefresh)

	body := []byte(`{"prompt_cache_key":"thread-foreign-incremental","previous_response_id":"resp-a","input":[{"type":"message","role":"user","content":"continue"}]}`)
	manager.providerHistoryScopes.SetAliases(providerHistoryScope("codex", "codex-a"), "pck:thread-foreign-incremental")
	opts := cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		OriginalRequest: bytes.Clone(body),
		Metadata:        map[string]any{cliproxyexecutor.SelectedAuthMetadataKey: "codex-b"},
	}

	_, _, err := manager.applyRequestAfterAuthInterceptor(nil, nil, &Auth{ID: "codex-b", Provider: "codex"}, "codex", cliproxyexecutor.Request{Payload: bytes.Clone(body)}, opts, "gpt-5.6")
	var historyErr *providerHistoryError
	if !errors.As(err, &historyErr) || historyErr.reason != "foreign_previous_response_requires_rehydration" {
		t.Fatalf("error = %v, want providerHistoryError(foreign_previous_response_requires_rehydration)", err)
	}
}

func TestManagerRejectsForeignConversationContinuation(t *testing.T) {
	tests := []struct {
		name         string
		conversation string
	}{
		{name: "string", conversation: `"conversation-a"`},
		{name: "object", conversation: `{"id":"conversation-a"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager := NewManager(nil, nil, nil)
			t.Cleanup(manager.StopAutoRefresh)

			body := []byte(`{"prompt_cache_key":"thread-foreign-conversation","conversation":` + tt.conversation + `,"input":[{"type":"message","role":"user","content":"continue"}]}`)
			manager.providerHistoryScopes.SetAliases(providerHistoryScope("codex", "codex-a"), "pck:thread-foreign-conversation")
			opts := cliproxyexecutor.Options{
				SourceFormat:    sdktranslator.FormatOpenAIResponse,
				OriginalRequest: bytes.Clone(body),
				Metadata:        map[string]any{cliproxyexecutor.SelectedAuthMetadataKey: "codex-b"},
			}

			_, _, err := manager.applyRequestAfterAuthInterceptor(nil, nil, &Auth{ID: "codex-b", Provider: "codex"}, "codex", cliproxyexecutor.Request{Payload: bytes.Clone(body)}, opts, "gpt-5.6")
			var historyErr *providerHistoryError
			if !errors.As(err, &historyErr) || historyErr.reason != "foreign_conversation_requires_rehydration" {
				t.Fatalf("error = %v, want providerHistoryError(foreign_conversation_requires_rehydration)", err)
			}
		})
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
