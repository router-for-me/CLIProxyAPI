package executor

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
	"google.golang.org/protobuf/encoding/protowire"
)

// devinPatchFramesWithParseError mirrors task6DevinFrames and also sets Devin's
// invalid-JSON parse error (tool call field 5).
func devinPatchFramesWithParseError(args, parseErr, trailer string) []byte {
	var tool []byte
	tool = protowire.AppendTag(tool, 1, protowire.BytesType)
	tool = protowire.AppendString(tool, "c")
	tool = protowire.AppendTag(tool, 2, protowire.BytesType)
	tool = protowire.AppendString(tool, "apply_patch")
	tool = protowire.AppendTag(tool, 4, protowire.BytesType)
	tool = protowire.AppendString(tool, args)
	tool = protowire.AppendTag(tool, 5, protowire.BytesType)
	tool = protowire.AppendString(tool, parseErr)
	var frame []byte
	frame = protowire.AppendTag(frame, 6, protowire.BytesType)
	frame = protowire.AppendBytes(frame, tool)
	frames := helps.WrapConnectEnvelope(frame)
	if trailer != "" {
		frames = append(frames, helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(trailer))...)
	}
	return frames
}

func TestDevinApplyPatchRejectionCarriesLogDiagnostic(t *testing.T) {
	const truncatedRawPatch = "*** Begin Patch\n*** Add File: RAW_SECRET_PATH\n+RAW_SECRET_BODY"
	var thinkingThenTruncatedPatchFrames []byte
	var thoughtFrame []byte
	thoughtFrame = protowire.AppendTag(thoughtFrame, 9, protowire.BytesType)
	thoughtFrame = protowire.AppendString(thoughtFrame, "thinking before tool call")
	thinkingThenTruncatedPatchFrames = append(thinkingThenTruncatedPatchFrames, helps.WrapConnectEnvelope(thoughtFrame)...)
	thinkingThenTruncatedPatchFrames = append(thinkingThenTruncatedPatchFrames, devinPatchFramesWithParseError(truncatedRawPatch, "invalid character '*' looking for beginning of value", "")...)

	for _, tc := range []struct {
		name, body, wantReason, wantShape string
	}{
		{
			name:       "truncated raw patch in invalid_json_str",
			body:       string(devinPatchFramesWithParseError(truncatedRawPatch, "invalid character '*' looking for beginning of value", `{}`)),
			wantReason: "legacy freeform arguments are invalid for apply_patch",
			wantShape:  "shape=raw_patch",
		},
		{
			name:       "thinking queued tool call before premature EOF",
			body:       string(thinkingThenTruncatedPatchFrames),
			wantReason: "devin_stream_ended_before_eos",
			wantShape:  "shape=raw_patch",
		},
		{
			name:       "json wrapper with extra field",
			body:       string(task6DevinFrames(`{"input":"RAW_SECRET_BODY","explanation":"RAW_SECRET"}`, false, `{}`)),
			wantReason: "apply_patch arguments must contain only one input field",
			wantShape:  "shape=json_object",
		},
		{
			name:       "json wrapper with raw newlines in input",
			body:       string(task6DevinFrames("{\"input\":\"*** Begin Patch\n*** Add File: RAW_SECRET_PATH\n+RAW_SECRET_BODY\n*** End Patch\"}", false, `{}`)),
			wantReason: "invalid control character in apply_patch input",
			wantShape:  "shape=json_object",
		},
		{
			name:       "stream ended before EOS",
			body:       string(task6DevinFrames(`{"input":"RAW_SECRET_BODY"}`, false, "")),
			wantReason: "devin_stream_ended_before_eos",
			wantShape:  "shape=json_object",
		},
		{
			name:       "stream read error after patch call",
			body:       string(append(task6DevinFrames(`{"input":"RAW_SECRET_BODY"}`, false, ""), 0, 0)),
			wantReason: "devin_stream_read_error",
			wantShape:  "shape=json_object",
		},
	} {
		for _, stream := range []bool{true, false} {
			name := tc.name + "/nonstream"
			if stream {
				name = tc.name + "/stream"
			}
			t.Run(name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = w.Write([]byte(tc.body))
				}))
				defer server.Close()
				exec := NewDevinExecutor(&config.Config{})
				auth := &cliproxyauth.Auth{ID: "devin-patch-diagnostic", Provider: "devin", Attributes: map[string]string{"api_key": "test", "base_url": server.URL}}
				req := cliproxyexecutor.Request{Model: "devin/swe-2", Payload: []byte(task6PatchRequest)}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, OriginalRequest: req.Payload}

				var errPatch error
				if stream {
					result, errExecuteStream := exec.ExecuteStream(t.Context(), auth, req, opts)
					if errExecuteStream != nil {
						t.Fatal(errExecuteStream)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							errPatch = chunk.Err
						}
					}
				} else {
					_, errPatch = exec.Execute(t.Context(), auth, req, opts)
				}

				// The client-facing contract is unchanged.
				assertTask6PatchError(t, errPatch)
				var diagnosticErr interface{ LogDiagnostic() string }
				if !errors.As(errPatch, &diagnosticErr) {
					t.Fatalf("rejection has no log diagnostic: %T", errPatch)
				}
				diagnostic := diagnosticErr.LogDiagnostic()
				want := []string{tc.wantReason, tc.wantShape}
				if !stream && tc.wantReason == "legacy freeform arguments are invalid for apply_patch" {
					want[0] = "legacy_invalid_json_str"
				}
				if tc.wantShape == "shape=raw_patch" {
					want = append(want, `devin_parse_error="invalid_character"`)
				}
				for _, part := range want {
					if !strings.Contains(diagnostic, part) {
						t.Fatalf("diagnostic missing %q: %s", part, diagnostic)
					}
				}
				if strings.Contains(diagnostic, "RAW_SECRET") {
					t.Fatalf("diagnostic leaked patch content: %s", diagnostic)
				}
			})
		}
	}
}

func TestDevinApplyPatchRecoversCompleteBarePatchFromInvalidJSONStr(t *testing.T) {
	const validPatch = "*** Begin Patch\n*** Add File: a.txt\n+hi\n*** End Patch"
	const validUpdateNoHeaderPatch = "*** Begin Patch\n*** Update File: a.txt\n-old\n+new\n*** End Patch"

	buildToolFrame := func(id, name, args, invalidJSON, invalidErr string) []byte {
		var tool []byte
		if id != "" {
			tool = protowire.AppendTag(tool, 1, protowire.BytesType)
			tool = protowire.AppendString(tool, id)
		}
		if name != "" {
			tool = protowire.AppendTag(tool, 2, protowire.BytesType)
			tool = protowire.AppendString(tool, name)
		}
		if args != "" {
			tool = protowire.AppendTag(tool, 3, protowire.BytesType)
			tool = protowire.AppendString(tool, args)
		}
		if invalidJSON != "" {
			tool = protowire.AppendTag(tool, 4, protowire.BytesType)
			tool = protowire.AppendString(tool, invalidJSON)
		}
		if invalidErr != "" {
			tool = protowire.AppendTag(tool, 5, protowire.BytesType)
			tool = protowire.AppendString(tool, invalidErr)
		}
		var frame []byte
		frame = protowire.AppendTag(frame, 6, protowire.BytesType)
		frame = protowire.AppendBytes(frame, tool)
		return helps.WrapConnectEnvelope(frame)
	}

	var splitFrames []byte
	splitFrames = append(splitFrames, buildToolFrame("call_patch", "", "", "*** Begin", "invalid character '*' looking for beginning of value")...)
	splitFrames = append(splitFrames, buildToolFrame("", "apply_patch", "", " Patch\n*** Add File: a.txt\n", "")...)
	splitFrames = append(splitFrames, buildToolFrame("", "", "", "+hi\n*** End Patch", "")...)
	splitFrames = append(splitFrames, helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`))...)

	var mixedToolFrames []byte
	mixedToolFrames = append(mixedToolFrames, buildToolFrame("call_shell", "shell", `{"cmd":"pwd"}`, "", "")...)
	mixedToolFrames = append(mixedToolFrames, buildToolFrame("call_patch", "apply_patch", "", validPatch, "invalid character '*' looking for beginning of value")...)
	mixedToolFrames = append(mixedToolFrames, helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`))...)

	for _, tc := range []struct {
		name        string
		requestJSON string
		frames      []byte
		wantPatch   string
		wantShell   bool
	}{
		{
			name:        "single frame bare patch",
			requestJSON: task6PatchRequest,
			frames:      devinPatchFramesWithParseError(validPatch, "invalid character '*' looking for beginning of value", `{}`),
			wantPatch:   validPatch,
		},
		{
			name:        "update bare patch without initial @@ header",
			requestJSON: task6PatchRequest,
			frames:      devinPatchFramesWithParseError(validUpdateNoHeaderPatch, "invalid character '*' looking for beginning of value", `{}`),
			wantPatch:   validUpdateNoHeaderPatch,
		},
		{
			name:        "split frames with late tool name",
			requestJSON: task6PatchRequest,
			frames:      splitFrames,
			wantPatch:   validPatch,
		},
		{
			name:        "mixed shell function and bare patch custom tool",
			requestJSON: `{"input":"patch","tools":[{"type":"custom","name":"apply_patch"},{"type":"function","name":"shell","parameters":{"type":"object"}}]}`,
			frames:      mixedToolFrames,
			wantPatch:   validPatch,
			wantShell:   true,
		},
	} {
		for _, stream := range []bool{false, true} {
			mode := "nonstream"
			if stream {
				mode = "stream"
			}
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = w.Write(tc.frames)
				}))
				defer server.Close()
				exec := NewDevinExecutor(&config.Config{})
				auth := &cliproxyauth.Auth{ID: "devin-bare-patch-ok", Provider: "devin", Attributes: map[string]string{"api_key": "test", "base_url": server.URL}}
				req := cliproxyexecutor.Request{Model: "devin/swe-2", Payload: []byte(tc.requestJSON)}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, OriginalRequest: req.Payload}

				if !stream {
					resp, errExecute := exec.Execute(t.Context(), auth, req, opts)
					if errExecute != nil {
						t.Fatalf("Execute failed: %v", errExecute)
					}
					outputs := gjson.GetBytes(resp.Payload, "output").Array()
					if tc.wantShell {
						if len(outputs) != 2 || outputs[0].Get("type").String() != "function_call" || outputs[0].Get("name").String() != "shell" {
							t.Fatalf("unexpected mixed output items: %s", resp.Payload)
						}
						if outputs[1].Get("type").String() != "custom_tool_call" || outputs[1].Get("input").String() != tc.wantPatch {
							t.Fatalf("unexpected recovered patch item: %s", resp.Payload)
						}
					} else {
						if len(outputs) != 1 || outputs[0].Get("type").String() != "custom_tool_call" || outputs[0].Get("input").String() != tc.wantPatch {
							t.Fatalf("unexpected recovered patch item: %s", resp.Payload)
						}
					}
					return
				}

				result, errExecuteStream := exec.ExecuteStream(t.Context(), auth, req, opts)
				if errExecuteStream != nil {
					t.Fatal(errExecuteStream)
				}
				var output []byte
				var doneInputs []string
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatalf("stream failed: %v", chunk.Err)
					}
					output = append(output, chunk.Payload...)
					for _, line := range bytes.Split(chunk.Payload, []byte("\n")) {
						if !bytes.HasPrefix(line, []byte("data:")) {
							continue
						}
						payload := bytes.TrimSpace(line[5:])
						if gjson.GetBytes(payload, "type").String() == "response.custom_tool_call_input.done" {
							doneInputs = append(doneInputs, gjson.GetBytes(payload, "input").String())
						}
					}
				}
				if len(doneInputs) != 1 || doneInputs[0] != tc.wantPatch {
					t.Fatalf("custom_tool_call_input.done = %v, want [%q]; full stream: %s", doneInputs, tc.wantPatch, output)
				}
				if !bytes.Contains(output, []byte(`"type":"response.completed"`)) || bytes.Contains(output, []byte(`"type":"response.failed"`)) {
					t.Fatalf("unexpected terminal stream events: %s", output)
				}
				if tc.wantShell && !bytes.Contains(output, []byte(`"name":"shell"`)) {
					t.Fatalf("missing shell tool call in stream: %s", output)
				}
			})
		}
	}
}

func TestDevinApplyPatchBarePatchRecoveryFailsClosedOnMalformedOrTruncated(t *testing.T) {
	const secretPatch = "*** Begin Patch\n*** Add File: RAW_SECRET_PATH\n+RAW_SECRET_BODY\n*** End Patch"

	var mixedJSONThenInvalidFrames []byte
	var tool1 []byte
	tool1 = protowire.AppendTag(tool1, 1, protowire.BytesType)
	tool1 = protowire.AppendString(tool1, "c")
	tool1 = protowire.AppendTag(tool1, 2, protowire.BytesType)
	tool1 = protowire.AppendString(tool1, "apply_patch")
	tool1 = protowire.AppendTag(tool1, 3, protowire.BytesType)
	tool1 = protowire.AppendString(tool1, `{"input":"ok"}`)
	var f1 []byte
	f1 = protowire.AppendTag(f1, 6, protowire.BytesType)
	f1 = protowire.AppendBytes(f1, tool1)
	mixedJSONThenInvalidFrames = append(mixedJSONThenInvalidFrames, helps.WrapConnectEnvelope(f1)...)
	mixedJSONThenInvalidFrames = append(mixedJSONThenInvalidFrames, devinPatchFramesWithParseError(secretPatch, "invalid character '*' looking for beginning of value", `{}`)...)

	var mixedInvalidThenJSONFrames []byte
	var tool2 []byte
	tool2 = protowire.AppendTag(tool2, 1, protowire.BytesType)
	tool2 = protowire.AppendString(tool2, "c")
	tool2 = protowire.AppendTag(tool2, 2, protowire.BytesType)
	tool2 = protowire.AppendString(tool2, "apply_patch")
	tool2 = protowire.AppendTag(tool2, 3, protowire.BytesType)
	tool2 = protowire.AppendString(tool2, "\n*** Add File: RAW_SECRET_PATH\n+RAW_SECRET_BODY\n*** End Patch")
	var f2 []byte
	f2 = protowire.AppendTag(f2, 6, protowire.BytesType)
	f2 = protowire.AppendBytes(f2, tool2)
	mixedInvalidThenJSONFrames = append(mixedInvalidThenJSONFrames, devinPatchFramesWithParseError("*** Begin Patch", "invalid character '*' looking for beginning of value", "")...)
	mixedInvalidThenJSONFrames = append(mixedInvalidThenJSONFrames, helps.WrapConnectEnvelope(f2)...)
	mixedInvalidThenJSONFrames = append(mixedInvalidThenJSONFrames, helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`))...)

	for _, tc := range []struct {
		name   string
		frames []byte
	}{
		{
			name:   "complete bare patch but missing EOS trailer",
			frames: devinPatchFramesWithParseError(secretPatch, "invalid character '*' looking for beginning of value", ""),
		},
		{
			name:   "complete bare patch but trailer error",
			frames: devinPatchFramesWithParseError(secretPatch, "invalid character '*' looking for beginning of value", `{"error":{"code":"internal","message":"RAW_SECRET"}}`),
		},
		{
			name:   "empty bare patch without file ops",
			frames: devinPatchFramesWithParseError("*** Begin Patch\n*** End Patch", "invalid character '*' looking for beginning of value", `{}`),
		},
		{
			name:   "update bare patch with only hunk header and no diff lines",
			frames: devinPatchFramesWithParseError("*** Begin Patch\n*** Update File: RAW_SECRET_PATH\n@@\n*** End Patch", "invalid character '*' looking for beginning of value", `{}`),
		},
		{
			name:   "update bare patch with move only and no diff lines",
			frames: devinPatchFramesWithParseError("*** Begin Patch\n*** Update File: RAW_SECRET_PATH\n*** Move to: RAW_SECRET_DEST\n*** End Patch", "invalid character '*' looking for beginning of value", `{}`),
		},
		{
			name:   "bare patch with duplicate environment id",
			frames: devinPatchFramesWithParseError("*** Begin Patch\n*** Environment ID: env-1\n*** Environment ID: env-2\n*** Add File: RAW_SECRET_PATH\n+RAW_SECRET_BODY\n*** End Patch", "invalid character '*' looking for beginning of value", `{}`),
		},
		{
			name:   "bare patch with missing space after add file colon",
			frames: devinPatchFramesWithParseError("*** Begin Patch\n*** Add File:RAW_SECRET_PATH\n+RAW_SECRET_BODY\n*** End Patch", "invalid character '*' looking for beginning of value", `{}`),
		},
		{
			name:   "mixed JSON arguments then invalid_json_str on same call",
			frames: mixedJSONThenInvalidFrames,
		},
		{
			name:   "mixed invalid_json_str then JSON arguments on same call",
			frames: mixedInvalidThenJSONFrames,
		},
	} {
		for _, stream := range []bool{false, true} {
			mode := "nonstream"
			if stream {
				mode = "stream"
			}
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = w.Write(tc.frames)
				}))
				defer server.Close()
				exec := NewDevinExecutor(&config.Config{})
				auth := &cliproxyauth.Auth{ID: "devin-bare-fail-closed", Provider: "devin", Attributes: map[string]string{"api_key": "test", "base_url": server.URL}}
				req := cliproxyexecutor.Request{Model: "devin/swe-2", Payload: []byte(task6PatchRequest)}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, OriginalRequest: req.Payload}

				if stream {
					result, errExecuteStream := exec.ExecuteStream(t.Context(), auth, req, opts)
					if errExecuteStream != nil {
						t.Fatal(errExecuteStream)
					}
					assertTask6FailedStream(t, result.Chunks)
				} else {
					resp, errExecute := exec.Execute(t.Context(), auth, req, opts)
					if len(resp.Payload) > 0 {
						t.Fatalf("expected empty payload on failure, got %s", resp.Payload)
					}
					assertTask6PatchError(t, errExecute)
				}
			})
		}
	}
}
