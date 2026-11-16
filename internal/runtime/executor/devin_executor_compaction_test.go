package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"google.golang.org/protobuf/encoding/protowire"
)

const devinCompactionSummaryMarker = "DEVIN_COMPACTION_SUMMARY_42"

// devinCompactionTextFrames builds Devin Connect-RPC frames carrying one
// assistant text message and a usage frame, then the end-stream trailer.
func devinCompactionTextFrames(text string) []byte {
	var textFrame []byte
	textFrame = protowire.AppendTag(textFrame, 3, protowire.BytesType)
	textFrame = protowire.AppendString(textFrame, text)

	var usageInner []byte
	usageInner = protowire.AppendTag(usageInner, 2, protowire.VarintType)
	usageInner = protowire.AppendVarint(usageInner, 12)
	usageInner = protowire.AppendTag(usageInner, 3, protowire.VarintType)
	usageInner = protowire.AppendVarint(usageInner, 6)

	var usageFrame []byte
	usageFrame = protowire.AppendTag(usageFrame, 7, protowire.BytesType)
	usageFrame = protowire.AppendBytes(usageFrame, usageInner)

	var buf bytes.Buffer
	buf.Write(helps.WrapConnectEnvelope(textFrame))
	buf.Write(helps.WrapConnectEnvelope(usageFrame))
	buf.Write(helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`)))
	return buf.Bytes()
}

// devinCompactionContext returns a context whose Devin HTTP client is served
// by the given frames, recording every upstream request body.
func devinCompactionContext(frames []byte, seenBodies *[][]byte) context.Context {
	mockRT := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		body, errRead := io.ReadAll(req.Body)
		if errRead != nil {
			return nil, errRead
		}
		*seenBodies = append(*seenBodies, body)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/connect+proto"}},
			Body:       io.NopCloser(bytes.NewReader(frames)),
		}, nil
	})
	return context.WithValue(context.Background(), "cliproxy.roundtripper", mockRT)
}

// devinCompactionWireField returns the first length-delimited field value.
func devinCompactionWireField(data []byte, target protowire.Number) []byte {
	for len(data) > 0 {
		number, kind, n := protowire.ConsumeTag(data)
		if n < 0 {
			return nil
		}
		data = data[n:]
		if kind == protowire.BytesType {
			value, m := protowire.ConsumeBytes(data)
			if m < 0 {
				return nil
			}
			if number == target {
				return value
			}
			data = data[m:]
		} else {
			m := protowire.ConsumeFieldValue(number, kind, data)
			if m < 0 {
				return nil
			}
			data = data[m:]
		}
	}
	return nil
}

// devinCompactionWireVarint returns the first varint field value, or 0.
func devinCompactionWireVarint(data []byte, target protowire.Number) uint64 {
	for len(data) > 0 {
		number, kind, n := protowire.ConsumeTag(data)
		if n < 0 {
			return 0
		}
		data = data[n:]
		if kind == protowire.VarintType {
			value, m := protowire.ConsumeVarint(data)
			if m < 0 {
				return 0
			}
			if number == target {
				return value
			}
			data = data[m:]
		} else {
			m := protowire.ConsumeFieldValue(number, kind, data)
			if m < 0 {
				return 0
			}
			data = data[m:]
		}
	}
	return 0
}

// devinCompactionAssertSummaryRequest checks one captured Devin summary
// request: no trigger item, original history and tool call/result preserved,
// and user payload rules applied as the final semantic barrier.
func devinCompactionAssertSummaryRequest(t *testing.T, body []byte, label string) {
	t.Helper()
	if bytes.Contains(body, []byte("compaction_trigger")) {
		t.Fatalf("compaction_trigger reached Devin %s summary request", label)
	}
	if !bytes.Contains(body, []byte("summary of the preceding conversation")) {
		t.Fatalf("expected summary prompt in Devin %s summary request", label)
	}
	if !bytes.Contains(body, []byte("Remember the exact test marker")) {
		t.Fatalf("expected original user history in Devin %s summary request", label)
	}
	if !bytes.Contains(body, []byte("DEVIN_TOOL_CALL_42")) {
		t.Fatalf("expected tool call history in Devin %s summary request", label)
	}
	if !bytes.Contains(body, []byte("DEVIN_TOOL_RESULT_42")) {
		t.Fatalf("expected tool result history in Devin %s summary request", label)
	}
	if len(body) < 5 {
		t.Fatalf("missing Connect envelope in Devin %s summary request", label)
	}
	completionConfig := devinCompactionWireField(body[5:], 8)
	maxTokens := devinCompactionWireVarint(completionConfig, 2)
	if maxTokens != 4321 {
		t.Fatalf("%s summary request completion_config.max_tokens = %d, want payload rule 4321", label, maxTokens)
	}
}

func devinCompactionCompleted(t *testing.T, sse string) ojson.Result {
	t.Helper()
	for _, line := range strings.Split(sse, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
		if data == "[DONE]" {
			continue
		}
		ev := ojson.Parse(data)
		if ev.Get("type").String() == "response.completed" {
			return ev
		}
	}
	t.Fatalf("no response.completed event in stream output: %s", sse)
	return ojson.Result{}
}

func TestDevinExecutor_ResponsesCompactionTriggerAndReplay_Issue7291(t *testing.T) {
	var seenBodies [][]byte
	ctx := devinCompactionContext(devinCompactionTextFrames("Summary retaining "+devinCompactionSummaryMarker+"."), &seenBodies)

	executor := NewDevinExecutor(&config.Config{
		Payload: config.PayloadConfig{
			Override: []config.PayloadRule{{
				Models: []config.PayloadModelRule{{Name: "devin/swe-2"}},
				Params: map[string]any{
					"completion_config.max_tokens": 4321,
				},
			}},
		},
	})
	auth := &cliproxyauth.Auth{
		ID:       "devin-compaction",
		Provider: "devin",
		Attributes: map[string]string{
			"api_key": "test-devin-key",
		},
	}

	triggerPayload := []byte(`{
		"model": "devin/swe-2",
		"stream": true,
		"input": [
			{
				"type": "message",
				"role": "user",
				"content": [{"type": "input_text", "text": "Remember the exact test marker DEVIN_COMPACTION_SUMMARY_42."}]
			},
			{"type": "function_call", "call_id": "call_7291", "name": "exec_command", "arguments": "{\"cmd\":\"echo DEVIN_TOOL_CALL_42\"}"},
			{"type": "function_call_output", "call_id": "call_7291", "output": "DEVIN_TOOL_RESULT_42"},
			{"type": "compaction_trigger"}
		]
	}`)

	streamResult, errStream := executor.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "devin/swe-2",
		Payload: triggerPayload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		ResponseFormat:  sdktranslator.FormatOpenAIResponse,
		Stream:          true,
		OriginalRequest: triggerPayload,
	})
	if errStream != nil {
		t.Fatalf("ExecuteStream(compaction_trigger) error = %v", errStream)
	}

	var streamBuf bytes.Buffer
	for chunk := range streamResult.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
		streamBuf.Write(chunk.Payload)
	}

	completed := devinCompactionCompleted(t, streamBuf.String())
	outputItems := completed.Get("response.output").Array()
	if len(outputItems) != 1 || outputItems[0].Get("type").String() != "compaction" {
		t.Fatalf("expected single compaction item in stream response, got: %s", completed.Raw)
	}
	capsule := outputItems[0].Get("encrypted_content").String()
	if !strings.HasPrefix(capsule, "cpa-ag-compact-v1:") {
		t.Fatalf("expected CPA compaction capsule prefix, got %q", capsule)
	}
	itemID := outputItems[0].Get("id").String()

	// The streaming summary request must satisfy the summary invariants. It is
	// inspected now, before any later call appends more captured bodies.
	if len(seenBodies) != 1 {
		t.Fatalf("expected exactly one upstream request after streaming compaction, got %d", len(seenBodies))
	}
	devinCompactionAssertSummaryRequest(t, seenBodies[0], "streaming")

	nonStreamResp, errExec := executor.Execute(ctx, auth, cliproxyexecutor.Request{
		Model:   "devin/swe-2",
		Payload: triggerPayload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		ResponseFormat:  sdktranslator.FormatOpenAIResponse,
		OriginalRequest: triggerPayload,
	})
	if errExec != nil {
		t.Fatalf("Execute(compaction_trigger) error = %v", errExec)
	}
	nonStreamItems := ojson.GetBytes(nonStreamResp.Payload, "output").Array()
	if len(nonStreamItems) != 1 || nonStreamItems[0].Get("type").String() != "compaction" {
		t.Fatalf("expected single compaction item in non-stream response, got: %s", nonStreamResp.Payload)
	}

	// The non-streaming summary request must satisfy the same invariants. Each
	// executor call appends exactly one captured upstream body.
	if len(seenBodies) != 2 {
		t.Fatalf("expected exactly two upstream requests after non-streaming compaction, got %d", len(seenBodies))
	}
	devinCompactionAssertSummaryRequest(t, seenBodies[1], "non-streaming")

	// Replay: the CPA capsule must expand into summary context, and a foreign
	// compaction item must be dropped before reaching the Devin upstream.
	replayPayload := []byte(fmt.Sprintf(`{
		"model": "devin/swe-2",
		"stream": true,
		"input": [
			{"type": "compaction", "encrypted_content": "foreign-opaque-token"},
			{"type": "compaction", "id": %q, "encrypted_content": %q},
			{
				"type": "message",
				"role": "user",
				"content": [{"type": "input_text", "text": "What marker did I ask you to remember?"}]
			}
		]
	}`, itemID, capsule))

	replayStream, errReplay := executor.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "devin/swe-2",
		Payload: replayPayload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		ResponseFormat:  sdktranslator.FormatOpenAIResponse,
		Stream:          true,
		OriginalRequest: replayPayload,
	})
	if errReplay != nil {
		t.Fatalf("ExecuteStream(replay compaction) error = %v", errReplay)
	}
	for chunk := range replayStream.Chunks {
		if chunk.Err != nil {
			t.Fatalf("replay stream chunk error = %v", chunk.Err)
		}
	}

	replaySeen := seenBodies[len(seenBodies)-1]
	if !bytes.Contains(replaySeen, []byte(devinCompactionSummaryMarker)) {
		t.Fatalf("expected expanded compaction summary in Devin upstream request")
	}
	if bytes.Contains(replaySeen, []byte("foreign-opaque-token")) {
		t.Fatalf("expected foreign compaction item to be dropped")
	}
	if bytes.Contains(replaySeen, []byte("cpa-ag-compact-v1:")) {
		t.Fatalf("expected CPA compaction capsule to be expanded, not forwarded raw")
	}

	// Repeated compaction: a replayed capsule followed by another trigger must
	// expand the first capsule into the second summary request and again emit
	// exactly one compaction item.
	secondTriggerPayload := []byte(fmt.Sprintf(`{
		"model": "devin/swe-2",
		"input": [
			{"type": "compaction", "id": %q, "encrypted_content": %q},
			{
				"type": "message",
				"role": "user",
				"content": [{"type": "input_text", "text": "One more turn before compacting again."}]
			},
			{"type": "compaction_trigger"}
		]
	}`, itemID, capsule))

	secondResp, errSecond := executor.Execute(ctx, auth, cliproxyexecutor.Request{
		Model:   "devin/swe-2",
		Payload: secondTriggerPayload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		ResponseFormat:  sdktranslator.FormatOpenAIResponse,
		OriginalRequest: secondTriggerPayload,
	})
	if errSecond != nil {
		t.Fatalf("Execute(second compaction_trigger) error = %v", errSecond)
	}
	secondItems := ojson.GetBytes(secondResp.Payload, "output").Array()
	if len(secondItems) != 1 || secondItems[0].Get("type").String() != "compaction" {
		t.Fatalf("expected single compaction item in second compaction response, got: %s", secondResp.Payload)
	}
	secondCapsule := secondItems[0].Get("encrypted_content").String()
	if !strings.HasPrefix(secondCapsule, "cpa-ag-compact-v1:") {
		t.Fatalf("expected CPA compaction capsule prefix in second compaction, got %q", secondCapsule)
	}
	secondItemID := secondItems[0].Get("id").String()

	// Inspect the second summary request itself (the last upstream call so
	// far): it must carry the first capsule's expanded summary plus the newly
	// added turn and the summarization prompt, without the trigger or the raw
	// capsule.
	secondSeen := seenBodies[len(seenBodies)-1]
	if !bytes.Contains(secondSeen, []byte(devinCompactionSummaryMarker)) {
		t.Fatalf("expected first capsule summary in second compaction request")
	}
	if !bytes.Contains(secondSeen, []byte("One more turn before compacting again")) {
		t.Fatalf("expected newly added turn in second compaction request")
	}
	if !bytes.Contains(secondSeen, []byte("summary of the preceding conversation")) {
		t.Fatalf("expected summary prompt in second compaction request")
	}
	if bytes.Contains(secondSeen, []byte("compaction_trigger")) {
		t.Fatalf("second compaction_trigger reached Devin upstream request")
	}
	if bytes.Contains(secondSeen, []byte("cpa-ag-compact-v1:")) {
		t.Fatalf("expected first CPA compaction capsule to be expanded in second compaction request, not forwarded raw")
	}

	// The capsule minted by repeated compaction must itself replay into
	// summary context on a later ordinary request.
	secondReplayPayload := []byte(fmt.Sprintf(`{
		"model": "devin/swe-2",
		"input": [
			{"type": "compaction", "id": %q, "encrypted_content": %q},
			{
				"type": "message",
				"role": "user",
				"content": [{"type": "input_text", "text": "Continue after the second compaction."}]
			}
		]
	}`, secondItemID, secondCapsule))
	if _, errSecondReplay := executor.Execute(ctx, auth, cliproxyexecutor.Request{
		Model:   "devin/swe-2",
		Payload: secondReplayPayload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		ResponseFormat:  sdktranslator.FormatOpenAIResponse,
		OriginalRequest: secondReplayPayload,
	}); errSecondReplay != nil {
		t.Fatalf("Execute(second capsule replay) error = %v", errSecondReplay)
	}
	secondReplaySeen := seenBodies[len(seenBodies)-1]
	if !bytes.Contains(secondReplaySeen, []byte(devinCompactionSummaryMarker)) {
		t.Fatalf("expected second capsule summary expanded in Devin upstream request")
	}
	if bytes.Contains(secondReplaySeen, []byte("cpa-ag-compact-v1:")) {
		t.Fatalf("expected second CPA compaction capsule to be expanded, not forwarded raw")
	}

	// OriginalRequest-only trigger: the pipeline may deliver a translated
	// payload without the trigger while the original Responses request still
	// carries it; compaction must still be synthesized.
	origOnlyResp, errOrigOnly := executor.Execute(ctx, auth, cliproxyexecutor.Request{
		Model:   "devin/swe-2",
		Payload: []byte(`{"model":"devin/swe-2"}`),
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		ResponseFormat:  sdktranslator.FormatOpenAIResponse,
		OriginalRequest: triggerPayload,
	})
	if errOrigOnly != nil {
		t.Fatalf("Execute(OriginalRequest-only trigger) error = %v", errOrigOnly)
	}
	origOnlyItems := ojson.GetBytes(origOnlyResp.Payload, "output").Array()
	if len(origOnlyItems) != 1 || origOnlyItems[0].Get("type").String() != "compaction" {
		t.Fatalf("expected single compaction item for OriginalRequest-only trigger, got: %s", origOnlyResp.Payload)
	}
	origOnlySeen := seenBodies[len(seenBodies)-1]
	if !bytes.Contains(origOnlySeen, []byte("Remember the exact test marker")) {
		t.Fatalf("expected OriginalRequest history in Devin summary request")
	}
	if !bytes.Contains(origOnlySeen, []byte("DEVIN_TOOL_CALL_42")) || !bytes.Contains(origOnlySeen, []byte("DEVIN_TOOL_RESULT_42")) {
		t.Fatalf("expected OriginalRequest tool history in Devin summary request")
	}
}
