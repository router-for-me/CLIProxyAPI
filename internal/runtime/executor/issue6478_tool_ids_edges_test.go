package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestIssue6478_DevinReservedPrefixStatelessFragmentedRoundTrip(t *testing.T) {
	nativeIDs := []string{"Write:1#abc", "cpa_tid_v1_V3JpdGU6MSNhYmM", "cpa_tid_v1_Y2FsbF8x", "call_1"}
	var frames []byte
	for i, id := range nativeIDs {
		for j, args := range []string{`{"literal_id":`, `"Write:1#abc"}`} {
			tool := appendVarintField(nil, 4, uint64(i))
			if j == 0 {
				tool = appendDevinFieldBytes(tool, 1, []byte(id))
				tool = appendDevinFieldBytes(tool, 2, []byte(issue6478Calls[i].name))
			}
			tool = appendDevinFieldBytes(tool, 3, []byte(args))
			frames = append(frames, helps.WrapConnectEnvelope(appendDevinFieldBytes(nil, 6, tool))...)
		}
	}
	frames = append(frames, helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`))...)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/connect+proto")
		if _, errWrite := w.Write(frames); errWrite != nil {
			t.Errorf("write frames: %v", errWrite)
		}
	}))
	defer server.Close()
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "test", "base_url": server.URL}}
	var idsByMode [][]string
	for _, stream := range []bool{false, true} {
		name := "Execute"
		if stream {
			name = "ExecuteStream"
		}
		t.Run(name, func(t *testing.T) {
			executor := NewDevinExecutor(&config.Config{})
			payload := []byte(`{"model":"devin/swe-2","messages":[{"role":"user","content":"tools"}]}`)
			req := cliproxyexecutor.Request{Model: "devin/swe-2", Payload: payload}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, OriginalRequest: payload, Stream: stream}
			var ids, arguments []string
			if stream {
				result, errStream := executor.ExecuteStream(t.Context(), auth, req, opts)
				if errStream != nil {
					t.Fatal(errStream)
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatal(chunk.Err)
					}
					for _, line := range strings.Split(string(chunk.Payload), "\n") {
						event := gjson.Parse(strings.TrimPrefix(line, "data: "))
						if event.Get("content_block.type").String() == "tool_use" {
							ids = append(ids, event.Get("content_block.id").String())
							arguments = append(arguments, "")
						}
						if event.Get("delta.type").String() == "input_json_delta" && len(arguments) > 0 {
							arguments[len(arguments)-1] += event.Get("delta.partial_json").String()
						}
					}
				}
			} else {
				result, errExecute := executor.Execute(t.Context(), auth, req, opts)
				if errExecute != nil {
					t.Fatal(errExecute)
				}
				for _, block := range gjson.GetBytes(result.Payload, "content").Array() {
					if block.Get("type").String() == "tool_use" {
						ids = append(ids, block.Get("id").String())
						arguments = append(arguments, block.Get("input").Raw)
					}
				}
			}
			if len(ids) != 4 {
				t.Fatalf("IDs = %q, want four calls", ids)
			}
			seen := make(map[string]bool)
			for i, id := range ids {
				if !regexp.MustCompile(`^[a-zA-Z0-9_-]+$`).MatchString(id) || seen[id] {
					t.Errorf("non-portable or colliding ID[%d] = %q", i, id)
				}
				seen[id] = true
				if i < 3 && id == nativeIDs[i] {
					t.Errorf("illegal or reserved native ID[%d] was not escaped: %q", i, id)
				}
				if gjson.Get(arguments[i], "literal_id").String() != "Write:1#abc" {
					t.Errorf("fragmented arguments corrupted: %s", arguments[i])
				}
			}
			if ids[3] != "call_1" {
				t.Errorf("ordinary valid ID changed to %q", ids[3])
			}
			idsByMode = append(idsByMode, ids)

			// A fresh executor has no state from the response that minted these IDs.
			fresh := NewDevinExecutor(&config.Config{})
			req.Payload = issue6478History(ids)
			opts.OriginalRequest = req.Payload
			httpReq, _, _, errPrepare := fresh.prepareDevinHTTPRequest(t.Context(), auth, req, opts)
			if errPrepare != nil {
				t.Fatal(errPrepare)
			}
			defer func() {
				if errClose := httpReq.Body.Close(); errClose != nil {
					t.Error(errClose)
				}
			}()
			_, wire, errRead := helps.ReadConnectFrame(httpReq.Body)
			if errRead != nil {
				t.Fatal(errRead)
			}
			_, body, errDecode := helps.FinalizeDevinPayload(wire, func(body []byte) []byte { return body })
			if errDecode != nil {
				t.Fatal(errDecode)
			}
			for i, id := range nativeIDs {
				if got := gjson.GetBytes(body, "prompts.1.tool_calls").Array()[i].Get("id").String(); got != id {
					t.Errorf("stateless call[%d] = %q, want original %q", i, got, id)
				}
				if got := gjson.GetBytes(body, "prompts").Array()[i+2].Get("tool_call_id").String(); got != id {
					t.Errorf("stateless result[%d] = %q, want original %q", i, got, id)
				}
			}
		})
	}
	if len(idsByMode) == 2 && !slices.Equal(idsByMode[0], idsByMode[1]) {
		t.Errorf("fragmented stream IDs differ: %q vs %q", idsByMode[0], idsByMode[1])
	}
}

func TestIssue6478_ClaudeLegacyEncodingCollision(t *testing.T) {
	ids := []string{issue6478Calls[0].id, issue6478Calls[1].id, "cpa_tid_v1_V3JpdGU6MSNlMWYwZGE4MTE0MzY0OWYzYTM3OGQyNGFmNmZjM2VjOQ", "call_8jAZYBK5veMdr1DD2DqObARK"}
	for _, stream := range []bool{false, true} {
		body := executeClaudeContextManagementRequest(t, &config.Config{}, issue6478History(ids), stream)
		uses := gjson.GetBytes(body, "messages.1.content").Array()
		results := gjson.GetBytes(body, "messages.2.content").Array()
		if len(uses) != 4 || len(results) != 4 {
			t.Fatalf("history lost calls/results: %s", body)
		}
		seen := make(map[string]bool)
		for i, use := range uses {
			id := use.Get("id").String()
			if !regexp.MustCompile(`^[a-zA-Z0-9_-]+$`).MatchString(id) || seen[id] {
				t.Errorf("legacy normalization produced invalid or colliding ID %q (stream=%v)", id, stream)
			}
			seen[id] = true
			if results[i].Get("tool_use_id").String() != id {
				t.Errorf("legacy collision mapping broke pair[%d]", i)
			}
			if i >= 2 && id != ids[i] {
				t.Errorf("legal/encoded history ID was re-encoded: %q, want %q", id, ids[i])
			}
		}
		// Repairing a repaired transcript must be byte-stable at schema ID fields.
		again := executeClaudeContextManagementRequest(t, &config.Config{}, body, stream)
		if gjson.GetBytes(again, "messages.1.content").Raw != gjson.GetBytes(body, "messages.1.content").Raw {
			t.Errorf("history ID repair is not idempotent")
		}
	}
}

func TestIssue6478_ClaudeUpstreamCountTokensRepairBeforePayload(t *testing.T) {
	ids := []string{issue6478Calls[0].id, issue6478Calls[1].id, issue6478Calls[2].id, issue6478Calls[3].id}
	for _, configured := range []bool{false, true} {
		cfg := &config.Config{}
		if configured {
			cfg.Payload = config.PayloadConfig{Override: []config.PayloadRule{{
				Models: []config.PayloadModelRule{{Name: "*", NotMatch: []map[string]any{{"messages.1.content.0.id": ids[0]}}}},
				Params: map[string]any{"tools.0.description": "observed repaired ID", "messages.1.content.0.id": "configured:id#override", "messages.2.content.0.tool_use_id": "configured:id#override"},
			}}}
		}
		var body []byte
		transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Host != "api.anthropic.com" || req.URL.Path != "/v1/messages/count_tokens" {
				t.Errorf("wrong token-count boundary: %s", req.URL)
			}
			var errRead error
			body, errRead = io.ReadAll(req.Body)
			if errRead != nil {
				return nil, errRead
			}
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"input_tokens":12}`)), Request: req}, nil
		})
		ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", http.RoundTripper(transport))
		auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "test"}}
		_, errCount := NewClaudeExecutor(cfg).CountTokens(ctx, auth, cliproxyexecutor.Request{Model: "claude-opus-5", Payload: issue6478History(ids)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
		if errCount != nil {
			t.Fatal(errCount)
		}
		if configured {
			if gjson.GetBytes(body, "tools.0.description").String() != "observed repaired ID" || gjson.GetBytes(body, "messages.1.content.0.id").String() != "configured:id#override" || gjson.GetBytes(body, "messages.2.content.0.tool_use_id").String() != "configured:id#override" {
				t.Errorf("count_tokens repair must precede final user rules: %s", body)
			}
		} else {
			useIDs := gjson.GetBytes(body, "messages.1.content.#.id").Array()
			resultIDs := gjson.GetBytes(body, "messages.2.content.#.tool_use_id").Array()
			if len(useIDs) != 4 || len(resultIDs) != 4 {
				t.Fatalf("count_tokens lost history calls/results: %s", body)
			}
			for i, id := range useIDs {
				if !regexp.MustCompile(`^[a-zA-Z0-9_-]+$`).MatchString(id.String()) {
					t.Errorf("count_tokens sent illegal ID[%d] = %q", i, id.String())
				}
				if resultIDs[i].String() != id.String() {
					t.Errorf("count_tokens call/result pair[%d] differs: %q vs %q", i, id.String(), resultIDs[i].String())
				}
				if i >= 2 && id.String() != ids[i] {
					t.Errorf("count_tokens changed legal ID[%d]: %q", i, id.String())
				}
			}
		}
	}
}

func TestIssue6478_DevinDoesNotDecodeOrdinaryNativeIDs(t *testing.T) {
	ids := []string{"call_1", "cpa_tid_v1_Y2FsbF8x", "cpa_tid_v1_not_base64!", "cpa_tid_v1_"}
	httpReq, _, body, errPrepare := NewDevinExecutor(&config.Config{}).prepareDevinHTTPRequest(t.Context(), &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "test"}}, cliproxyexecutor.Request{Model: "devin/swe-2", Payload: issue6478History(ids)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	if errPrepare != nil {
		t.Fatal(errPrepare)
	}
	defer func() {
		if errClose := httpReq.Body.Close(); errClose != nil {
			t.Error(errClose)
		}
	}()
	for i, id := range ids {
		got := gjson.GetBytes(body, "prompts.1.tool_calls").Array()[i].Get("id").String()
		if got != id {
			t.Errorf("ordinary native ID was misdecoded: %q, want %q", got, id)
		}
	}
}
