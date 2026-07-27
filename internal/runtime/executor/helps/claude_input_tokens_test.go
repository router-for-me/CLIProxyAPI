package helps

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tiktoken-go/tokenizer"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

type failingClaudeInputCodec struct{}

func (failingClaudeInputCodec) GetName() string {
	return "failing"
}

func (failingClaudeInputCodec) Count(string) (int, error) {
	return 0, errors.New("count failed")
}

func (failingClaudeInputCodec) Encode(string) ([]uint, []string, error) {
	return nil, nil, errors.New("encode failed")
}

func (failingClaudeInputCodec) Decode([]uint) (string, error) {
	return "", errors.New("decode failed")
}

func TestCollectClaudeInputTokenSegments(t *testing.T) {
	payload := []byte(`{
        "model":"claude-test",
        "system":[
            {"type":"text","text":"Follow repository rules.","cache_control":{"type":"ephemeral"}},
            {"type":"image","source":{"type":"base64","media_type":"image/png","data":"ignored-system-image"}}
        ],
        "messages":[
            {"role":"user","content":[
                {"type":"text","text":"Review the implementation."},
                {"type":"document","source":{"type":"text","data":"Reference document text."}},
                {"type":"image","source":{"type":"base64","media_type":"image/png","data":"ignored-image"}}
            ]},
            {"role":"assistant","content":[
                {"type":"thinking","thinking":"Inspect the relevant files.","signature":"ignored-signature"},
                {"type":"tool_use","id":"toolu_1","name":"read_file","input":{"path":"main.go"}}
            ]},
            {"role":"user","content":[
                {"type":"tool_result","tool_use_id":"toolu_1","content":[
                    {"type":"text","text":"package main"},
                    {"type":"image","source":{"type":"base64","data":"ignored-tool-image"}}
                ]}
            ]}
        ],
        "tools":[{
            "name":"read_file",
            "description":"Reads a repository file.",
            "input_schema":{"type":"object","properties":{"path":{"type":"string"}}},
            "cache_control":{"type":"ephemeral"}
        }],
        "tool_choice":{"type":"tool","name":"read_file"},
        "metadata":{"user_id":"ignored-metadata"},
        "max_tokens":4096,
        "stream":true
    }`)

	got, err := collectClaudeInputTokenSegments(payload)
	if err != nil {
		t.Fatalf("collectClaudeInputTokenSegments() error = %v", err)
	}
	want := []string{
		"Follow repository rules.",
		"user",
		"Review the implementation.",
		"Reference document text.",
		"assistant",
		"Inspect the relevant files.",
		"toolu_1",
		"read_file",
		`{"path":"main.go"}`,
		"user",
		"toolu_1",
		"package main",
		"read_file",
		"Reads a repository file.",
		`{"type":"object","properties":{"path":{"type":"string"}}}`,
		"tool",
		"read_file",
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("segments = %#v, want %#v", got, want)
	}
}

func TestCollectClaudeInputTokenSegmentsIncludesKnownToolResults(t *testing.T) {
	payload := []byte(`{
        "messages":[{"role":"user","content":[
            {"type":"web_search_tool_result","tool_use_id":"ws_tool_1","content":[
                {"type":"web_search_result","source":"Search source","title":"Search result title","url":"https://search.example/result","page_age":"1 day","encrypted_content":"ignored-secret"}
            ]},
            {"type":"web_fetch_tool_result","tool_use_id":"fetch_tool_1","content":{
                "type":"web_fetch_result","url":"https://docs.example/page","retrieved_at":"2026-07-22T00:00:00Z","content":{
                    "type":"document","title":"Fetched document","source":{"type":"text","data":"Fetched body"}
                }
            }},
            {"type":"bash_code_execution_tool_result","tool_use_id":"bash_tool_1","content":{
                "type":"bash_code_execution_result","stdout":"command output","stderr":"command error","return_code":1,
                "content":[{"type":"text","text":"additional output"}]
            }},
            {"type":"tool_result","tool_use_id":"toolu_1","content":[
                {"type":"tool_reference","tool_name":"proxy_mcp__nia__manage_resource"}
            ]}
        ]}]
    }`)

	segments, err := collectClaudeInputTokenSegments(payload)
	if err != nil {
		t.Fatalf("collectClaudeInputTokenSegments() error = %v", err)
	}
	joined := "\n" + strings.Join(segments, "\n") + "\n"
	for _, want := range []string{
		"ws_tool_1",
		"Search source",
		"Search result title",
		"https://search.example/result",
		"1 day",
		"fetch_tool_1",
		"https://docs.example/page",
		"2026-07-22T00:00:00Z",
		"Fetched document",
		"Fetched body",
		"bash_tool_1",
		"command output",
		"command error",
		"1",
		"additional output",
		"toolu_1",
		"proxy_mcp__nia__manage_resource",
	} {
		if !strings.Contains(joined, "\n"+want+"\n") {
			t.Errorf("segments do not contain %q: %#v", want, segments)
		}
	}
	if strings.Contains(joined, "ignored-secret") {
		t.Fatalf("segments contain encrypted content: %#v", segments)
	}
}

func TestCountClaudeInputTokensExcludesMultimediaAndControlFields(t *testing.T) {
	enc, err := tokenizer.Get(tokenizer.O200kBase)
	if err != nil {
		t.Fatalf("tokenizer.Get() error = %v", err)
	}

	base := []byte(`{
        "system":"System text.",
        "messages":[{"role":"user","content":[{"type":"text","text":"User text."}]}],
        "tools":[{"name":"lookup","description":"Looks up data.","input_schema":{"type":"object"}}]
    }`)
	withExcludedFields := []byte(`{
        "model":"claude-test",
        "system":"System text.",
        "messages":[{"role":"user","content":[
            {"type":"text","text":"User text."},
            {"type":"image","source":{"type":"base64","media_type":"image/png","data":"very-large-image-data"}},
            {"type":"input_audio","source":{"type":"base64","data":"very-large-audio-data"}},
            {"type":"video","source":{"type":"url","url":"https://example.com/video.mp4"}},
            {"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"very-large-pdf-data"}}
        ]}],
        "tools":[{"name":"lookup","description":"Looks up data.","input_schema":{"type":"object"},"cache_control":{"type":"ephemeral"}}],
        "metadata":{"large_wrapper":"ignored"},
        "max_tokens":8192,
        "temperature":0.8,
        "top_p":0.9,
        "thinking":{"type":"enabled","budget_tokens":4096},
        "stream":true
    }`)

	baseCount, errBase := countClaudeInputTokens(enc, base)
	if errBase != nil {
		t.Fatalf("countClaudeInputTokens(base) error = %v", errBase)
	}
	excludedCount, errExcluded := countClaudeInputTokens(enc, withExcludedFields)
	if errExcluded != nil {
		t.Fatalf("countClaudeInputTokens(withExcludedFields) error = %v", errExcluded)
	}
	if excludedCount != baseCount {
		t.Fatalf("count with excluded fields = %d, want %d", excludedCount, baseCount)
	}
}

func TestTranslateStreamWithClaudeInputTokensPatchesMessageStartOnce(t *testing.T) {
	upstreamFormat := sdktranslator.Format("claude-input-token-test-upstream")
	sdktranslator.Register(sdktranslator.FormatClaude, upstreamFormat, nil, sdktranslator.ResponseTransform{
		Stream: func(_ context.Context, _ string, _, _, rawJSON []byte, _ *any) [][]byte {
			return [][]byte{rawJSON}
		},
	})

	originalRequest := []byte(`{"system":"System text.","messages":[{"role":"user","content":"Hello."}]}`)
	state := NewClaudeInputTokenState(sdktranslator.FormatClaude, upstreamFormat, sdktranslator.FormatClaude, originalRequest)
	var param any
	combined := []byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":0,\"output_tokens\":0}}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0}\n\n")

	got := TranslateStreamWithClaudeInputTokens(
		context.Background(),
		upstreamFormat,
		sdktranslator.FormatClaude,
		"claude-test",
		originalRequest,
		nil,
		combined,
		&param,
		state,
	)
	if tokens := messageStartInputTokens(got); tokens <= 0 {
		t.Fatalf("message_start input_tokens = %d, want positive estimate; output = %q", tokens, joinClaudeInputChunks(got))
	}
	if !state.handled {
		t.Fatal("state.handled = false, want true after message_start")
	}
	if !strings.Contains(joinClaudeInputChunks(got), `"type":"content_block_start"`) {
		t.Fatalf("combined non-target event was not preserved: %q", joinClaudeInputChunks(got))
	}

	secondStart := []byte(`event: message_start
data: {"type":"message_start","message":{"usage":{"input_tokens":0}}}

`)
	gotSecond := TranslateStreamWithClaudeInputTokens(
		context.Background(),
		upstreamFormat,
		sdktranslator.FormatClaude,
		"claude-test",
		originalRequest,
		nil,
		secondStart,
		&param,
		state,
	)
	if tokens := messageStartInputTokens(gotSecond); tokens != 0 {
		t.Fatalf("second message_start input_tokens = %d, want 0 after state handled", tokens)
	}
}

func TestClaudeInputTokenStatePreservesCRLFAndNonTargetEvents(t *testing.T) {
	originalRequest := []byte(`{"messages":[{"role":"user","content":"Hello."}]}`)
	state := NewClaudeInputTokenState(sdktranslator.FormatClaude, sdktranslator.FormatOpenAI, sdktranslator.FormatClaude, originalRequest)
	chunk := []byte("event: message_start\r\ndata:  {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":0,\"output_tokens\":0}}}  \r\n\r\n" +
		"event: ping\r\ndata: {\"type\":\"ping\",\"value\":\"keep\"}\r\n\r\n")

	got := state.apply(context.Background(), [][]byte{chunk})
	tokens := messageStartInputTokens(got)
	if tokens <= 0 {
		t.Fatalf("input_tokens = %d, want positive estimate", tokens)
	}
	want := fmt.Sprintf("event: message_start\r\ndata:  {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":%d,\"output_tokens\":0}}}  \r\n\r\n"+
		"event: ping\r\ndata: {\"type\":\"ping\",\"value\":\"keep\"}\r\n\r\n", tokens)
	if joined := joinClaudeInputChunks(got); joined != want {
		t.Fatalf("output bytes changed unexpectedly:\n got: %q\nwant: %q", joined, want)
	}
}

func TestClaudeInputTokenStatePatchesMissingAndPreservesNonZero(t *testing.T) {
	originalRequest := []byte(`{"messages":[{"role":"user","content":"Hello."}]}`)

	t.Run("missing", func(t *testing.T) {
		state := NewClaudeInputTokenState(sdktranslator.FormatClaude, sdktranslator.FormatOpenAI, sdktranslator.FormatClaude, originalRequest)
		chunks := [][]byte{[]byte("data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"output_tokens\":0}}}\n\n")}
		got := state.apply(context.Background(), chunks)
		if tokens := messageStartInputTokens(got); tokens <= 0 {
			t.Fatalf("input_tokens = %d, want positive estimate", tokens)
		}
	})

	t.Run("non-zero", func(t *testing.T) {
		state := NewClaudeInputTokenState(sdktranslator.FormatClaude, sdktranslator.FormatOpenAI, sdktranslator.FormatClaude, []byte(`not valid json`))
		chunks := [][]byte{[]byte("data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":73}}}\n\n")}
		got := state.apply(context.Background(), chunks)
		if tokens := messageStartInputTokens(got); tokens != 73 {
			t.Fatalf("input_tokens = %d, want preserved value 73", tokens)
		}
		if !state.handled {
			t.Fatal("state.handled = false, want true")
		}
	})
}

func TestClaudeInputTokenStateSkipsUnsupportedFlows(t *testing.T) {
	originalRequest := []byte(`{"messages":[{"role":"user","content":"Hello."}]}`)
	testCases := []struct {
		name           string
		sourceFormat   sdktranslator.Format
		upstreamFormat sdktranslator.Format
		responseFormat sdktranslator.Format
	}{
		{name: "non-Claude source", sourceFormat: sdktranslator.FormatOpenAI, upstreamFormat: sdktranslator.FormatGemini, responseFormat: sdktranslator.FormatClaude},
		{name: "Claude passthrough", sourceFormat: sdktranslator.FormatClaude, upstreamFormat: sdktranslator.FormatClaude, responseFormat: sdktranslator.FormatClaude},
		{name: "non-Claude response", sourceFormat: sdktranslator.FormatClaude, upstreamFormat: sdktranslator.FormatOpenAI, responseFormat: sdktranslator.FormatOpenAI},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			state := NewClaudeInputTokenState(tc.sourceFormat, tc.upstreamFormat, tc.responseFormat, originalRequest)
			chunks := [][]byte{[]byte("data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":0}}}\n\n")}
			got := state.apply(context.Background(), chunks)
			if tokens := messageStartInputTokens(got); tokens != 0 {
				t.Fatalf("input_tokens = %d, want unchanged 0", tokens)
			}
			if !state.handled {
				t.Fatal("state.handled = false, want disabled flow handled at initialization")
			}
		})
	}
}

func TestClaudeInputTokenStateCountErrorKeepsZero(t *testing.T) {
	originalLogOutput := log.StandardLogger().Out
	log.SetOutput(io.Discard)
	defer log.SetOutput(originalLogOutput)

	state := NewClaudeInputTokenState(
		sdktranslator.FormatClaude,
		sdktranslator.FormatOpenAI,
		sdktranslator.FormatClaude,
		[]byte(`{"messages":[{"role":"user","content":"Hello."}]}`),
	)
	state.codec = failingClaudeInputCodec{}
	chunks := [][]byte{[]byte("data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":0}}}\n\n")}

	got := state.apply(context.Background(), chunks)
	if tokens := messageStartInputTokens(got); tokens != 0 {
		t.Fatalf("input_tokens = %d, want fallback 0", tokens)
	}
	if !state.handled {
		t.Fatal("state.handled = false, want true after failed estimate")
	}
}

func TestClaudeInputTokenStateInvalidJSONKeepsZeroWithoutLoggingRequest(t *testing.T) {
	originalLogOutput := log.StandardLogger().Out
	var logOutput bytes.Buffer
	log.SetOutput(&logOutput)
	defer log.SetOutput(originalLogOutput)

	const sensitiveRequest = `{"messages":["sensitive-original-request"`
	state := NewClaudeInputTokenState(
		sdktranslator.FormatClaude,
		sdktranslator.FormatOpenAI,
		sdktranslator.FormatClaude,
		[]byte(sensitiveRequest),
	)
	chunks := [][]byte{[]byte("data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":0}}}\n\n")}

	got := state.apply(context.Background(), chunks)
	if tokens := messageStartInputTokens(got); tokens != 0 {
		t.Fatalf("input_tokens = %d, want fallback 0", tokens)
	}
	if !state.handled {
		t.Fatal("state.handled = false, want true after invalid JSON")
	}
	if !strings.Contains(logOutput.String(), "failed to estimate Claude input tokens") {
		t.Fatalf("warning not logged: %q", logOutput.String())
	}
	if strings.Contains(logOutput.String(), "sensitive-original-request") {
		t.Fatalf("warning leaked original request: %q", logOutput.String())
	}
}

func TestClaudeInputTokenizerConcurrentCount(t *testing.T) {
	first, errFirst := claudeInputTokenizer()
	if errFirst != nil {
		t.Fatalf("claudeInputTokenizer() error = %v", errFirst)
	}
	second, errSecond := claudeInputTokenizer()
	if errSecond != nil {
		t.Fatalf("claudeInputTokenizer() second error = %v", errSecond)
	}
	if first != second {
		t.Fatal("claudeInputTokenizer() returned different codec instances")
	}

	const workers = 32
	const iterations = 50
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for worker := 0; worker < workers; worker++ {
		worker := worker
		wg.Add(1)
		go func() {
			defer wg.Done()
			for iteration := 0; iteration < iterations; iteration++ {
				payload := []byte(fmt.Sprintf(`{"messages":[{"role":"user","content":"worker %d iteration %d 你好"}]}`, worker, iteration))
				count, errCount := countClaudeInputTokens(first, payload)
				if errCount != nil {
					errs <- errCount
					return
				}
				if count <= 0 {
					errs <- fmt.Errorf("non-positive count: %d", count)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func messageStartInputTokens(chunks [][]byte) int64 {
	for _, chunk := range chunks {
		for _, line := range strings.Split(string(chunk), "\n") {
			trimmed := strings.TrimSpace(line)
			if !strings.HasPrefix(trimmed, "data:") {
				continue
			}
			payload := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
			if gjson.Get(payload, "type").String() == "message_start" {
				return gjson.Get(payload, "message.usage.input_tokens").Int()
			}
		}
	}
	return 0
}

func joinClaudeInputChunks(chunks [][]byte) string {
	var builder strings.Builder
	for _, chunk := range chunks {
		builder.Write(chunk)
	}
	return builder.String()
}
