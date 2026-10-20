package interactions

import (
	"bytes"
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertInteractionsRequestToCodexWithToolMessagesDirect(t *testing.T) {
	out, _ := ConvertInteractionsRequestToCodex("codex-test", []byte(`{"model":"codex-test","system_instruction":"be brief","input":[{"type":"user_input","content":[{"type":"text","text":"hi"}]},{"type":"thought","content":[{"type":"text","text":"thinking"}]},{"type":"function_call","name":"lookup","call_id":"call_1","arguments":{"q":"x"}},{"type":"function_result","name":"lookup","call_id":"call_1","result":{"ok":true}}],"tools":[{"type":"function","name":"lookup","parameters":{"type":"object","properties":{"q":{"type":"string"}}}}]}`), false)
	if got := gjson.GetBytes(out, "instructions").String(); got != "be brief" {
		t.Fatalf("instructions = %q, want be brief. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "input.0.content.0.text").String(); got != "hi" {
		t.Fatalf("input.0.content.0.text = %q, want hi. Output: %s", got, string(out))
	}
	// The signatureless thought step is dropped entirely: a reasoning item
	// without replayable encrypted_content is rejected by both upstreams.
	if got := gjson.GetBytes(out, "input.1.type").String(); got != "function_call" {
		t.Fatalf("input.1.type = %q, want function_call. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "input.1.call_id").String(); got != "call_1" {
		t.Fatalf("function_call call_id = %q, want call_1. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "input.2.type").String(); got != "function_call_output" {
		t.Fatalf("input.2.type = %q, want function_call_output. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.name").String(); got != "lookup" {
		t.Fatalf("tools.0.name = %q, want lookup. Output: %s", got, string(out))
	}
	if gjson.GetBytes(out, "contents").Exists() || gjson.GetBytes(out, "systemInstruction").Exists() {
		t.Fatalf("Codex request must not use foreign request shape. Output: %s", string(out))
	}
}

func TestConvertInteractionsRequestToCodexPreservesNonImageMediaContent(t *testing.T) {
	out, _ := ConvertInteractionsRequestToCodex("codex-test", []byte(`{"model":"codex-test","input":[{"type":"model_output","content":[{"type":"audio","mime_type":"audio/wav","data":"UklGRg=="},{"type":"video","mime_type":"video/mp4","data":"AAAAIGZ0eXA="},{"type":"document","mime_type":"application/pdf","data":"JVBERi0="}]}]}`), false)

	if got := gjson.GetBytes(out, "input.0.role").String(); got != "assistant" {
		t.Fatalf("input.0.role = %q, want assistant. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "input.0.content.0.type").String(); got != "input_audio" {
		t.Fatalf("audio content type = %q, want input_audio. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "input.1.content.0.type").String(); got != "input_file" {
		t.Fatalf("video content type = %q, want input_file. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "input.2.content.0.type").String(); got != "input_file" {
		t.Fatalf("document content type = %q, want input_file. Output: %s", got, string(out))
	}
}

func TestConvertInteractionsRequestToCodexPreservesTopLevelThinkingLevel(t *testing.T) {
	out, _ := ConvertInteractionsRequestToCodex("codex-test", []byte(`{"model":"codex-test","generation_config":{"thinking_level":"high"},"input":"hi"}`), true)
	if got := gjson.GetBytes(out, "reasoning.effort").String(); got != "high" {
		t.Fatalf("reasoning.effort = %q, want high. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "stream").Bool(); !got {
		t.Fatalf("stream = %v, want true. Output: %s", got, string(out))
	}
}

func TestConvertInteractionsRequestToCodexUsesBodyStream(t *testing.T) {
	out, _ := ConvertInteractionsRequestToCodex("codex-test", []byte(`{"model":"codex-test","stream":true,"input":"hi"}`), false)
	if got := gjson.GetBytes(out, "stream").Bool(); !got {
		t.Fatalf("stream = %v, want true. Output: %s", got, string(out))
	}
}

func TestConvertInteractionsRequestToCodexFunctionDeclarations(t *testing.T) {
	out, _ := ConvertInteractionsRequestToCodex("codex-test", []byte(`{"model":"codex-test","input":"hi","tools":[{"function_declarations":[{"name":"lookup","description":"Lookup data","parameters":{"type":"object","$schema":"http://json-schema.org/draft-07/schema#","properties":{"q":{"type":"string"}}}}]}]}`), false)
	if got := gjson.GetBytes(out, "tools.0.type").String(); got != "function" {
		t.Fatalf("tools.0.type = %q, want function. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.name").String(); got != "lookup" {
		t.Fatalf("tools.0.name = %q, want lookup. Output: %s", got, string(out))
	}
	if gjson.GetBytes(out, "tools.0.parameters.$schema").Exists() {
		t.Fatalf("tool parameters should not keep $schema. Output: %s", string(out))
	}
}

func TestConvertCodexResponseToInteractionsIncompleteTerminal(t *testing.T) {
	raw := []byte(`{"type":"response.incomplete","response":{"id":"resp_1","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}`)
	nonStreamOut := ConvertCodexResponseToInteractionsNonStream(context.Background(), "codex-test", nil, nil, raw, nil)
	if got := gjson.GetBytes(nonStreamOut, "status").String(); got != "incomplete" {
		t.Fatalf("non-stream status = %q, want incomplete. Output: %s", got, nonStreamOut)
	}

	var param any
	streamOut := ConvertCodexResponseToInteractions(context.Background(), "codex-test", nil, nil, append([]byte("data: "), raw...), &param)
	payload := findCodexInteractionsEventPayload(streamOut, "interaction.completed")
	if len(payload) == 0 {
		t.Fatalf("stream incomplete event did not terminate interaction: %q", streamOut)
	}
	if got := gjson.GetBytes(payload, "interaction.status").String(); got != "incomplete" {
		t.Fatalf("stream status = %q, want incomplete. Payload: %s", got, payload)
	}
}

func TestConvertCodexResponseToInteractionsNonStream(t *testing.T) {
	raw := []byte(`{"type":"response.completed","response":{"id":"resp_1","created_at":1700000000,"usage":{"input_tokens":3,"output_tokens":2},"output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]},{"type":"reasoning","content":"thinking"},{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"q\":\"x\"}"}]}}`)
	out := ConvertCodexResponseToInteractionsNonStream(context.Background(), "codex-test", nil, nil, raw, nil)
	if got := gjson.GetBytes(out, "steps.0.content.0.text").String(); got != "ok" {
		t.Fatalf("steps.0.content.0.text = %q, want ok. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "steps.1.type").String(); got != "thought" {
		t.Fatalf("steps.1.type = %q, want thought. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "steps.2.type").String(); got != "function_call" {
		t.Fatalf("steps.2.type = %q, want function_call. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "usage.total_tokens").Int(); got != 5 {
		t.Fatalf("usage.total_tokens = %d, want 5. Output: %s", got, string(out))
	}
}

func TestConvertCodexResponseToInteractionsStream(t *testing.T) {
	var param any
	events := ConvertCodexResponseToInteractions(context.Background(), "codex-test", nil, nil, []byte(`data: {"type":"response.output_text.delta","delta":"ok"}`), &param)
	payload := findCodexInteractionsEventPayload(events, "step.delta")
	if len(payload) == 0 {
		t.Fatalf("step.delta event not found: %q", events)
	}
	if got := gjson.GetBytes(payload, "delta.text").String(); got != "ok" {
		t.Fatalf("delta.text = %q, want ok. Payload: %s", got, string(payload))
	}
}

func TestConvertCodexResponseToInteractionsStreamFunctionCallStartHasCallID(t *testing.T) {
	var param any
	events := ConvertCodexResponseToInteractions(context.Background(), "codex-test", nil, nil, []byte(`data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"q\":\"x\"}"}}`), &param)
	payload := findCodexInteractionsEventPayload(events, "step.start")
	if got := gjson.GetBytes(payload, "step.call_id").String(); got != "call_1" {
		t.Fatalf("step.call_id = %q, want call_1. Payload: %s", got, string(payload))
	}
}

func TestConvertCodexResponseToInteractionsStreamCompletesAfterSteps(t *testing.T) {
	var param any
	var events [][]byte
	for _, chunk := range [][]byte{
		[]byte(`data: {"type":"response.created","response":{"id":"resp_1","model":"codex-test"}}`),
		[]byte(`data: {"type":"response.output_text.delta","delta":"我将调用工具。"}`),
		[]byte(`data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"q\":\"weather\"}"},"output_index":1}`),
		[]byte(`data: {"type":"response.completed","response":{"id":"resp_1","output":[],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}`),
	} {
		events = append(events, ConvertCodexResponseToInteractions(context.Background(), "codex-test", nil, nil, chunk, &param)...)
	}

	got := strings.Join(codexInteractionsEventNames(events), ",")
	want := "interaction.created,interaction.status_update,step.start,step.delta,step.stop,step.start,step.delta,step.stop,interaction.completed,done"
	if got != want {
		t.Fatalf("events = %s, want %s", got, want)
	}
	completed := findCodexInteractionsEventPayload(events, "interaction.completed")
	if gotTokens := gjson.GetBytes(completed, "interaction.usage.total_tokens").Int(); gotTokens != 3 {
		t.Fatalf("total_tokens = %d, want 3. Payload: %s", gotTokens, string(completed))
	}
}

// testInteractionsMetaSignature builds a shape-valid synthetic Meta envelope;
// no captured signature is embedded.
func testInteractionsMetaSignature() string {
	return "Q-PaDgE" + strings.Repeat("A", 1419)
}

func testInteractionsGPTSignature() string {
	decoded := make([]byte, 89)
	decoded[0] = 0x80
	for i := 9; i < len(decoded); i++ {
		decoded[i] = byte(i)
	}
	return base64.RawURLEncoding.EncodeToString(decoded)
}

func findCodexInteractionsEventPayloads(events [][]byte, eventType string) []gjson.Result {
	var out []gjson.Result
	for _, event := range events {
		for _, line := range bytes.Split(event, []byte("\n")) {
			line = bytes.TrimSpace(line)
			if !bytes.HasPrefix(line, []byte("data:")) {
				continue
			}
			payload := bytes.TrimSpace(line[len("data:"):])
			if codexInteractionsEventName(codexInteractionsFrameEventName(event), payload) == eventType {
				out = append(out, gjson.ParseBytes(payload))
			}
		}
	}
	return out
}

func TestConvertCodexResponseToInteractionsNonStream_ReasoningSignature(t *testing.T) {
	metaSig := testInteractionsMetaSignature()
	routedSig := metaSig + "." + strings.Repeat("Ab3", 23)[:67]
	raw := []byte(`{"type":"response.completed","response":{"id":"resp_1","output":[` +
		`{"type":"reasoning","summary":[{"type":"summary_text","text":"thinking hard"}],"encrypted_content":"` + metaSig + `"},` +
		`{"type":"reasoning","summary":[],"encrypted_content":"` + routedSig + `"},` +
		`{"type":"reasoning","summary":[]}` +
		`]}}`)

	out := ConvertCodexResponseToInteractionsNonStream(context.Background(), "codex-test", nil, nil, raw, nil)
	steps := gjson.GetBytes(out, "steps").Array()
	if len(steps) != 2 {
		t.Fatalf("steps len = %d, want 2 (empty reasoning emits no step). Output: %s", len(steps), string(out))
	}
	if got := steps[0].Get("type").String(); got != "thought" {
		t.Fatalf("steps.0.type = %q, want thought. Output: %s", got, string(out))
	}
	if got := steps[0].Get("signature").String(); got != metaSig {
		t.Fatalf("steps.0.signature = %q, want Meta signature verbatim. Output: %s", got, string(out))
	}
	if got := steps[0].Get("content.0.text").String(); got != "thinking hard" {
		t.Fatalf("steps.0.content.0.text = %q, want thinking hard. Output: %s", got, string(out))
	}
	if got := steps[1].Get("signature").String(); got != routedSig {
		t.Fatalf("steps.1.signature = %q, want routed Meta signature verbatim. Output: %s", got, string(out))
	}
	if steps[1].Get("content").Exists() {
		t.Fatalf("steps.1 should carry no content when there is no text. Output: %s", string(out))
	}
}

func TestConvertCodexResponseToInteractionsStream_ReasoningSignature(t *testing.T) {
	metaSig := testInteractionsMetaSignature()
	var param any
	var events [][]byte
	for _, chunk := range [][]byte{
		[]byte(`data: {"type":"response.created","response":{"id":"resp_1","model":"codex-test"}}`),
		[]byte(`data: {"type":"response.reasoning_summary_text.delta","delta":"streamed thinking"}`),
		[]byte(`data: {"type":"response.output_item.done","item":{"id":"rs_1","type":"reasoning","summary":[{"type":"summary_text","text":"streamed thinking"}],"encrypted_content":"` + metaSig + `"}}`),
		// A duplicated done event for the same item must not re-emit the signature or summary.
		[]byte(`data: {"type":"response.output_item.done","item":{"id":"rs_1","type":"reasoning","summary":[{"type":"summary_text","text":"streamed thinking"}],"encrypted_content":"` + metaSig + `"}}`),
		[]byte(`data: {"type":"response.output_item.done","item":{"type":"message","content":[{"type":"output_text","text":"answer"}]}}`),
	} {
		events = append(events, ConvertCodexResponseToInteractions(context.Background(), "codex-test", nil, nil, chunk, &param)...)
	}

	starts := findCodexInteractionsEventPayloads(events, "step.start")
	if len(starts) != 2 {
		t.Fatalf("step.start count = %d, want thought + model_output. Events: %q", len(starts), events)
	}
	if got := starts[0].Get("step.type").String(); got != "thought" {
		t.Fatalf("step.start[0] type = %q, want thought", got)
	}
	if got := starts[1].Get("step.type").String(); got != "model_output" {
		t.Fatalf("step.start[1] type = %q, want model_output", got)
	}

	signatureDeltas := 0
	for _, delta := range findCodexInteractionsEventPayloads(events, "step.delta") {
		if delta.Get("delta.type").String() != "thought_signature" {
			continue
		}
		signatureDeltas++
		if got := delta.Get("delta.signature").String(); got != metaSig {
			t.Fatalf("thought_signature delta signature = %q, want Meta signature verbatim", got)
		}
	}
	if signatureDeltas != 1 {
		t.Fatalf("thought_signature delta count = %d, want 1 (duplicate suppressed)", signatureDeltas)
	}
	summaryDeltas := 0
	for _, delta := range findCodexInteractionsEventPayloads(events, "step.delta") {
		if delta.Get("delta.type").String() == "thought_summary" {
			summaryDeltas++
		}
	}
	if summaryDeltas != 1 {
		t.Fatalf("thought_summary delta count = %d, want 1 (output_item.done must not duplicate streamed summary)", summaryDeltas)
	}

	names := strings.Join(codexInteractionsEventNames(events), ",")
	want := "interaction.created,interaction.status_update,step.start,step.delta,step.delta,step.stop,step.start,step.delta,step.stop"
	if names != want {
		t.Fatalf("events = %s, want %s", names, want)
	}

	// A second done-only reasoning item after a streamed-delta reasoning item must still emit its summary.
	var param2 any
	var events2 [][]byte
	for _, chunk := range [][]byte{
		[]byte(`data: {"type":"response.created","response":{"id":"resp_2","model":"codex-test"}}`),
		[]byte(`data: {"type":"response.reasoning_summary_text.delta","delta":"first summary"}`),
		[]byte(`data: {"type":"response.output_item.done","item":{"id":"rs_a","type":"reasoning","summary":[{"type":"summary_text","text":"first summary"}],"encrypted_content":"` + metaSig + `"}}`),
		[]byte(`data: {"type":"response.output_item.done","item":{"id":"rs_b","type":"reasoning","summary":[{"type":"summary_text","text":"second summary only in done"}]}}`),
	} {
		events2 = append(events2, ConvertCodexResponseToInteractions(context.Background(), "codex-test", nil, nil, chunk, &param2)...)
	}
	var summaries2 []string
	for _, delta := range findCodexInteractionsEventPayloads(events2, "step.delta") {
		if delta.Get("delta.type").String() == "thought_summary" {
			summaries2 = append(summaries2, delta.Get("delta.content.text").String())
		}
	}
	if len(summaries2) != 2 || summaries2[0] != "first summary" || summaries2[1] != "second summary only in done" {
		t.Fatalf("consecutive reasoning summaries = %v, want [first summary, second summary only in done]", summaries2)
	}
}

func TestConvertInteractionsRequestToCodex_RestoresThoughtSignature(t *testing.T) {
	metaSig := testInteractionsMetaSignature()
	routedSig := metaSig + "." + strings.Repeat("Ab3", 23)[:67]
	for _, tc := range []struct {
		name  string
		model string
		sig   string
	}{
		{"native meta", "muse-spark-1.3", metaSig},
		{"namespaced router model", "meta/muse-wonder", metaSig},
		{"routed meta verbatim", "meta/muse-wonder", routedSig},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := []byte(`{"model":"` + tc.model + `","input":[{"type":"thought","signature":"` + tc.sig + `","content":[{"type":"text","text":"thinking"}]}]}`)
			out, _ := ConvertInteractionsRequestToCodex(tc.model, payload, false)
			item := gjson.GetBytes(out, "input.0")
			if got := item.Get("type").String(); got != "reasoning" {
				t.Fatalf("input.0.type = %q, want reasoning. Output: %s", got, string(out))
			}
			if got := item.Get("encrypted_content").String(); got != tc.sig {
				t.Fatalf("encrypted_content = %q, want signature verbatim. Output: %s", got, string(out))
			}
			if got := item.Get("summary.0.text").String(); got != "thinking" {
				t.Fatalf("summary.0.text = %q, want thinking. Output: %s", got, string(out))
			}
			if item.Get("content").Exists() {
				t.Fatalf("replay item must not carry content. Output: %s", string(out))
			}
		})
	}
}

func TestConvertInteractionsRequestToCodex_ThoughtSignatureTargetMismatch(t *testing.T) {
	metaSig := testInteractionsMetaSignature()
	payload := []byte(`{"model":"gpt-5.4","input":[{"type":"thought","signature":"` + metaSig + `","content":[{"type":"text","text":"thinking"}],"id":"step_1"}]}`)

	// A foreign signature is never replayed and the thought step is dropped
	// entirely rather than sent as an unsigned reasoning item.
	out, _ := ConvertInteractionsRequestToCodex("gpt-5.4", payload, false)
	if got := len(gjson.GetBytes(out, "input").Array()); got != 0 {
		t.Fatalf("input len = %d, want 0 (mismatched thought dropped). Output: %s", got, string(out))
	}
}

func TestConvertInteractionsRequestToCodex_DropsEmptyThoughtSteps(t *testing.T) {
	metaSig := testInteractionsMetaSignature()

	// A rejected signature with no text emits no item at all: a bare
	// reasoning item carries nothing replayable and is rejected upstream.
	out, _ := ConvertInteractionsRequestToCodex("gpt-5.4", []byte(`{"input":[{"type":"user_input","content":[{"type":"text","text":"hi"}]},{"type":"thought","signature":"`+metaSig+`"}]}`), false)
	if got := len(gjson.GetBytes(out, "input").Array()); got != 1 {
		t.Fatalf("input len = %d, want 1 (thought step dropped). Output: %s", got, string(out))
	}

	// A signatureless, textless thought step emits no item.
	out, _ = ConvertInteractionsRequestToCodex("muse-spark-1.3", []byte(`{"input":[{"type":"thought"}]}`), false)
	if got := len(gjson.GetBytes(out, "input").Array()); got != 0 {
		t.Fatalf("input len = %d, want 0. Output: %s", got, string(out))
	}

	// A duplicate accepted signature with no text emits nothing the second
	// time either.
	out, _ = ConvertInteractionsRequestToCodex("muse-spark-1.3", []byte(`{"input":[{"type":"thought","signature":"`+metaSig+`"},{"type":"thought","signature":"`+metaSig+`"}]}`), false)
	if got := len(gjson.GetBytes(out, "input").Array()); got != 1 {
		t.Fatalf("input len = %d, want 1 (duplicate without text dropped). Output: %s", got, string(out))
	}

	// A signatureless thought with text is dropped too, and the message/tool
	// items around it keep their order.
	out, _ = ConvertInteractionsRequestToCodex("muse-spark-1.3", []byte(`{"input":[{"type":"user_input","content":[{"type":"text","text":"hi"}]},{"type":"thought","content":[{"type":"text","text":"thinking"}]},{"type":"function_call","name":"lookup","call_id":"call_1","arguments":{"q":"x"}}]}`), false)
	items := gjson.GetBytes(out, "input").Array()
	if len(items) != 2 || items[0].Get("type").String() != "message" || items[1].Get("type").String() != "function_call" {
		t.Fatalf("input = %s, want [message, function_call] with the thought dropped", items)
	}
}

func TestConvertInteractionsRequestToCodex_ThoughtSignatureVariants(t *testing.T) {
	metaSig := testInteractionsMetaSignature()
	gptSig := testInteractionsGPTSignature()

	// A cache-prefixed signature normalizes to its native payload.
	out, _ := ConvertInteractionsRequestToCodex("gpt-5.4", []byte(`{"input":[{"type":"thought","signature":"gpt#`+gptSig+`"}]}`), false)
	if got := gjson.GetBytes(out, "input.0.encrypted_content").String(); got != gptSig {
		t.Fatalf("prefixed GPT signature should normalize to native payload. Output: %s", string(out))
	}

	// The snake_case thought_signature spelling is accepted too.
	out, _ = ConvertInteractionsRequestToCodex("muse-spark-1.3", []byte(`{"input":[{"type":"thought","thought_signature":"`+metaSig+`"}]}`), false)
	if got := gjson.GetBytes(out, "input.0.encrypted_content").String(); got != metaSig {
		t.Fatalf("thought_signature field should be accepted. Output: %s", string(out))
	}

	// An identical signature replays once; the duplicate thought is dropped.
	out, _ = ConvertInteractionsRequestToCodex("muse-spark-1.3", []byte(`{"input":[{"type":"thought","signature":"`+metaSig+`","content":[{"type":"text","text":"one"}]},{"type":"thought","signature":"`+metaSig+`","content":[{"type":"text","text":"two"}]}]}`), false)
	if got := len(gjson.GetBytes(out, "input").Array()); got != 1 {
		t.Fatalf("input len = %d, want 1 (duplicate dropped). Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "input.0.encrypted_content").String(); got != metaSig {
		t.Fatalf("first thought should carry the signature. Output: %s", string(out))
	}
}

func TestConvertInteractionsRequestToCodex_IncludeEncryptedContent(t *testing.T) {
	out, _ := ConvertInteractionsRequestToCodex("codex-test", []byte(`{"input":"hi"}`), false)
	if got := gjson.GetBytes(out, "include.0").String(); got != "reasoning.encrypted_content" {
		t.Fatalf("include.0 = %q, want reasoning.encrypted_content. Output: %s", got, string(out))
	}

	out, _ = ConvertInteractionsRequestToCodex("codex-test", []byte(`{"input":"hi","include":["web_search_call.action.sources"]}`), false)
	include := gjson.GetBytes(out, "include").Array()
	if len(include) != 2 || include[0].String() != "web_search_call.action.sources" || include[1].String() != "reasoning.encrypted_content" {
		t.Fatalf("include = %s, want sources + reasoning.encrypted_content. Output: %s", include, string(out))
	}

	out, _ = ConvertInteractionsRequestToCodex("codex-test", []byte(`{"input":"hi","include":["reasoning.encrypted_content"]}`), false)
	if include := gjson.GetBytes(out, "include").Array(); len(include) != 1 {
		t.Fatalf("include = %s, want unchanged single entry. Output: %s", include, string(out))
	}
}

func findCodexInteractionsEventPayload(events [][]byte, eventType string) []byte {
	prefix := []byte("data:")
	for _, event := range events {
		eventName := codexInteractionsFrameEventName(event)
		for _, line := range bytes.Split(event, []byte("\n")) {
			line = bytes.TrimSpace(line)
			if !bytes.HasPrefix(line, prefix) {
				continue
			}
			payload := bytes.TrimSpace(line[len(prefix):])
			if codexInteractionsEventName(eventName, payload) == eventType {
				return payload
			}
		}
	}
	return nil
}

func codexInteractionsEventNames(events [][]byte) []string {
	names := make([]string, 0, len(events))
	for _, event := range events {
		eventName := codexInteractionsFrameEventName(event)
		for _, line := range bytes.Split(event, []byte("\n")) {
			line = bytes.TrimSpace(line)
			if !bytes.HasPrefix(line, []byte("data:")) {
				continue
			}
			payload := bytes.TrimSpace(line[len("data:"):])
			if name := codexInteractionsEventName(eventName, payload); name != "" {
				names = append(names, name)
			}
		}
	}
	return names
}

func codexInteractionsEventName(eventName string, payload []byte) string {
	if eventType := gjson.GetBytes(payload, "event_type").String(); eventType != "" {
		return eventType
	}
	if eventType := gjson.GetBytes(payload, "type").String(); eventType != "" {
		return eventType
	}
	return eventName
}

func codexInteractionsFrameEventName(event []byte) string {
	for _, line := range bytes.Split(event, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if bytes.HasPrefix(line, []byte("event:")) {
			return strings.TrimSpace(string(line[len("event:"):]))
		}
	}
	return ""
}
