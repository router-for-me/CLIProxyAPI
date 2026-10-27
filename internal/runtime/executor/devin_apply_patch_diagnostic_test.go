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
	"google.golang.org/protobuf/encoding/protowire"
)

// devinPatchFramesWithParseError mirrors task6DevinFrames and also sets Devin's
// invalid-JSON parse error (tool call field 5).
func devinPatchFramesWithParseError(args, parseErr string) []byte {
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
	return append(frames, helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`))...)
}

func TestDevinApplyPatchRejectionCarriesLogDiagnostic(t *testing.T) {
	const rawPatch = "*** Begin Patch\n*** Add File: RAW_SECRET_PATH\n+RAW_SECRET_BODY\n*** End Patch"
	for _, tc := range []struct {
		name, body, wantReason, wantShape string
	}{
		{
			name:       "raw patch in invalid_json_str",
			body:       string(devinPatchFramesWithParseError(rawPatch, "invalid character '*' looking for beginning of value")),
			wantReason: "legacy freeform arguments are invalid for apply_patch",
			wantShape:  "shape=raw_patch",
		},
		{
			name:       "json wrapper with extra field",
			body:       string(task6DevinFrames(`{"input":"RAW_SECRET_BODY","explanation":"RAW_SECRET"}`, false, `{}`)),
			wantReason: "apply_patch arguments must contain only one input field",
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
				want := []string{tc.wantReason}
				if stream || tc.wantShape == "shape=raw_patch" {
					// Non-stream translator failures report only the reason.
					want = append(want, tc.wantShape)
				}
				if !stream && tc.wantShape == "shape=raw_patch" {
					want[0] = "legacy_invalid_json_str"
				}
				for _, part := range want {
					if !strings.Contains(diagnostic, part) {
						t.Fatalf("diagnostic missing %q: %s", part, diagnostic)
					}
				}
				if strings.Contains(diagnostic, "RAW_SECRET") {
					t.Fatalf("diagnostic leaked patch content: %s", diagnostic)
				}
				t.Logf("diagnostic: %s", diagnostic)
			})
		}
	}
}

func TestDevinApplyPatchValidStreamCompletes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(task6DevinFrames(`{"input":"*** Begin Patch\n*** Add File: a.txt\n+ok\n*** End Patch"}`, false, `{}`))
	}))
	defer server.Close()
	exec := NewDevinExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{ID: "devin-patch-valid-stream", Provider: "devin", Attributes: map[string]string{"api_key": "test", "base_url": server.URL}}
	req := cliproxyexecutor.Request{Model: "devin/swe-2", Payload: []byte(task6PatchRequest)}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, OriginalRequest: req.Payload}
	result, errExecuteStream := exec.ExecuteStream(t.Context(), auth, req, opts)
	if errExecuteStream != nil {
		t.Fatal(errExecuteStream)
	}
	var output []byte
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("valid stream failed: %v", chunk.Err)
		}
		output = append(output, chunk.Payload...)
	}
	for _, expected := range []string{"response.custom_tool_call_input.done", "response.completed"} {
		if !bytes.Contains(output, []byte(expected)) {
			t.Fatalf("valid stream missing %s: %s", expected, output)
		}
	}
}
