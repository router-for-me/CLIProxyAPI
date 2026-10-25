package helps

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"io"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func kiroTestFrame(event, messageType, payload string) []byte {
	var headers []byte
	for _, pair := range [][2]string{{":event-type", event}, {":message-type", messageType}, {":exception-type", event}} {
		headers = append(headers, byte(len(pair[0])))
		headers = append(headers, pair[0]...)
		headers = append(headers, 7, byte(len(pair[1])>>8), byte(len(pair[1])))
		headers = append(headers, pair[1]...)
	}
	frame := make([]byte, 12)
	binary.BigEndian.PutUint32(frame, uint32(16+len(headers)+len(payload)))
	binary.BigEndian.PutUint32(frame[4:], uint32(len(headers)))
	binary.BigEndian.PutUint32(frame[8:], crc32.ChecksumIEEE(frame[:8]))
	frame = append(frame, headers...)
	frame = append(frame, payload...)
	frame = binary.BigEndian.AppendUint32(frame, crc32.ChecksumIEEE(frame))
	return frame
}

func TestKiroEventFrames(t *testing.T) {
	frame := kiroTestFrame("assistantResponseEvent", "event", `{"content":"中文 😀"}`)
	event, data, err := ReadKiroEvent(bytes.NewReader(frame))
	if err != nil || event != "assistantResponseEvent" || gjson.GetBytes(data, "content").String() != "中文 😀" {
		t.Fatalf("decode: %q %s %v", event, data, err)
	}
	for _, broken := range [][]byte{frame[:len(frame)-1], append([]byte{1}, frame[1:]...)} {
		if _, _, err = ReadKiroEvent(bytes.NewReader(broken)); err == nil {
			t.Fatal("accepted truncated or corrupt frame")
		}
	}
	corrupt := bytes.Clone(frame)
	corrupt[12] ^= 1
	if _, _, err = ReadKiroEvent(bytes.NewReader(corrupt)); err == nil {
		t.Fatal("accepted bad CRC")
	}
	if _, _, err = ReadKiroEvent(bytes.NewReader(nil)); err != io.EOF {
		t.Fatalf("empty stream: %v", err)
	}
	_, _, err = ReadKiroEvent(bytes.NewReader(kiroTestFrame("throttlingException", "exception", `{}`)))
	if failure, ok := err.(*KiroStreamError); !ok || failure.StatusCode() != 429 {
		t.Fatalf("exception: %v", err)
	}
}

func TestKiroRequestPreservesHistoryToolsAndImages(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"Be precise"}],"max_tokens":1000,"temperature":0.2,"tools":[{"name":"weather","description":"Weather","input_schema":{"type":"object","properties":{}}}],"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":[{"type":"text","text":"Checking"},{"type":"tool_use","id":"call-1","name":"weather","input":{"city":"北京"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call-1","content":"sunny"}]},{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}},{"type":"text","text":"Explain"}]}]}`)
	out, err := BuildKiroRequest(body, "claude-sonnet-4.6", "arn:test")
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{
		"profileArn": "arn:test", "conversationState.history.0.userInputMessage.content": "Be precise\n\nhi",
		"conversationState.history.1.assistantResponseMessage.toolUses.0.input.city":                               "北京",
		"conversationState.currentMessage.userInputMessage.modelId":                                                "claude-sonnet-4.6",
		"conversationState.currentMessage.userInputMessage.userInputMessageContext.tools.0.toolSpecification.name": "weather",
		"conversationState.currentMessage.userInputMessage.userInputMessageContext.toolResults.0.toolUseId":        "call-1",
		"conversationState.currentMessage.userInputMessage.images.0.source.bytes":                                  "aGVsbG8=",
	} {
		if got := gjson.GetBytes(out, path).String(); got != want {
			t.Errorf("%s = %q; want %q", path, got, want)
		}
	}
}

func TestKiroResponseToolFragmentsAndNativeUsage(t *testing.T) {
	state := NewKiroResponse("auto")
	for _, event := range [][2]string{
		{"reasoningContentEvent", `{"text":"Think"}`}, {"assistantResponseEvent", `{"content":"hello"}`},
		{"toolUseEvent", `{"toolUseId":"t1","name":"weather","input":"{\"city\":"}`},
		{"toolUseEvent", `{"toolUseId":"t1","input":"\"北京\"}","stop":true}`},
		{"metadataEvent", `{"tokenUsage":{"uncachedInputTokens":10,"cacheReadInputTokens":20,"cacheWriteInputTokens":5,"outputTokens":7,"totalTokens":42}}`},
	} {
		if _, err := state.Consume(event[0], []byte(event[1])); err != nil {
			t.Fatal(err)
		}
	}
	out, err := state.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if gjson.GetBytes(out, "choices.0.finish_reason").String() != "tool_calls" || gjson.GetBytes(out, "choices.0.message.tool_calls.0.function.arguments").String() != `{"city":"北京"}` {
		t.Fatal(string(out))
	}
	if state.Usage.InputTokens != 35 || state.Usage.OutputTokens != 7 || state.Usage.TotalTokens != 42 || state.Usage.CachedTokens != 20 {
		t.Fatalf("usage: %+v", state.Usage)
	}
	chunks, err := state.FinishStream()
	if err != nil || !strings.Contains(string(chunks[0]), `"prompt_tokens":35`) {
		t.Fatalf("finish: %s %v", chunks, err)
	}
}

func TestKiroIncompleteAndEmptyResponsesFail(t *testing.T) {
	state := NewKiroResponse("auto")
	if _, err := state.JSON(); err == nil {
		t.Fatal("empty response accepted")
	}
	_, _ = state.Consume("toolUseEvent", []byte(`{"toolUseId":"a","name":"f","input":"{"}`))
	if _, err := state.FinishStream(); err == nil {
		t.Fatal("incomplete tool call accepted")
	}
}

func TestKiroToolResultPreservesImages(t *testing.T) {
	for _, historical := range []bool{false, true} {
		t.Run(map[bool]string{false: "current", true: "history"}[historical], func(t *testing.T) {
			messages := `[{"role":"user","content":"Read screen"},{"role":"assistant","content":[{"type":"tool_use","id":"c1","name":"screenshot","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"c1","content":[{"type":"text","text":"Screen"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}}]}]}`
			if historical {
				messages += `,{"role":"assistant","content":"Read it"},{"role":"user","content":"Continue"}`
			}
			messages += `]`
			body := []byte(`{"tools":[{"name":"screenshot","description":"Screen","input_schema":{"type":"object"}}],"messages":` + messages + `}`)
			out, err := BuildKiroRequest(body, "auto", "")
			if err != nil {
				t.Fatal(err)
			}
			path := "conversationState.currentMessage.userInputMessage"
			if historical {
				path = "conversationState.history.2.userInputMessage"
			}
			if gjson.GetBytes(out, path+".images.0.source.bytes").String() != "aGVsbG8=" || gjson.GetBytes(out, path+".userInputMessageContext.toolResults.0.content.0.text").String() != "Screen" {
				t.Fatalf("tool result lost its image or text: %s", out)
			}
		})
	}
}

func TestKiroToolResultRejectsUnsupportedContent(t *testing.T) {
	for _, content := range []string{
		`{"type":"image","source":{"type":"url","url":"https://example.com/screen.png"}}`,
		`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"invalid!"}}`,
		`{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"aGVsbG8="}}`,
		`{"type":"image","source":{"type":"base64","media_type":"image/png","data":""}}`,
	} {
		body := []byte(`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"c1","content":[` + content + `]}]}]}`)
		if _, err := BuildKiroRequest(body, "auto", ""); err == nil {
			t.Fatalf("unsupported tool result silently accepted: %s", content)
		}
	}
}

func TestKiroRejectsInvalidResultShapesAndAssistantImages(t *testing.T) {
	for _, content := range []string{`7`, `true`, `{"unexpected":"value"}`} {
		body := []byte(`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"c1","content":` + content + `}]}]}`)
		if _, err := BuildKiroRequest(body, "auto", ""); err == nil {
			t.Fatalf("invalid result content silently accepted: %s", content)
		}
	}
	body := []byte(`{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}}]},{"role":"user","content":"continue"}]}`)
	if _, err := BuildKiroRequest(body, "auto", ""); err == nil {
		t.Fatal("assistant image was silently dropped")
	}
	for _, content := range []string{`""`, `[]`, `null`} {
		body := []byte(`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"c1","content":` + content + `}]}]}`)
		if _, err := BuildKiroRequest(body, "auto", ""); err != nil {
			t.Fatalf("empty tool result rejected: %s %v", content, err)
		}
	}
}
