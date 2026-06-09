package main

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestApplyJSAfterResponseUsesFrozenNativeHistoryChunks(t *testing.T) {
	scriptPath := filepath.Join(t.TempDir(), "stream.js")
	script := `
function on_after_stream_response(ctx) {
    if (!Object.isFrozen(ctx.history_chunks)) {
        throw new Error("history_chunks is not frozen");
    }
    var original = ctx.history_chunks[0];
    try {
        ctx.history_chunks[0] = "changed";
    } catch (e) {
    }
    if (ctx.history_chunks[0] !== original) {
        throw new Error("history_chunks item was changed");
    }
    try {
        ctx.history_chunks = ["changed"];
    } catch (e) {
    }
    if (ctx.history_chunks[0] !== original) {
        throw new Error("history_chunks property was replaced");
    }
    return { chunk: ctx.chunk + "|ok" };
}
`
	if errWrite := os.WriteFile(scriptPath, []byte(script), 0600); errWrite != nil {
		t.Fatalf("os.WriteFile() error = %v", errWrite)
	}

	plugin := &jsHandlerPlugin{cfg: defaultJSHandlerConfig()}
	chunk := `data: {"choices":[{"delta":{},"finish_reason":null}]}`
	processedBody, _, changed, errApply := plugin.applyJSAfterResponse(
		scriptPath,
		"gpt-test",
		"openai",
		nil,
		nil,
		"",
		&chunk,
		http.Header{},
		true,
		[]string{`data: {"choices":[{"delta":{"tool_calls":[{"index":0}]}}]}`},
	)
	if errApply != nil {
		t.Fatalf("applyJSAfterResponse() error = %v", errApply)
	}
	if !changed {
		t.Fatal("applyJSAfterResponse() changed = false, want true")
	}
	if processedBody != chunk+"|ok" {
		t.Fatalf("applyJSAfterResponse() body = %q, want %q", processedBody, chunk+"|ok")
	}
}

func TestApplyJSAfterResponseDispatchesNonStreamHook(t *testing.T) {
	scriptPath := filepath.Join(t.TempDir(), "nonstream.js")
	script := `
function on_after_stream_response(ctx) {
    throw new Error("stream hook should not run");
}
function on_after_nonstream_response(ctx) {
    return { body: ctx.body + "|nonstream" };
}
`
	if errWrite := os.WriteFile(scriptPath, []byte(script), 0600); errWrite != nil {
		t.Fatalf("os.WriteFile() error = %v", errWrite)
	}

	plugin := &jsHandlerPlugin{cfg: defaultJSHandlerConfig()}
	processedBody, _, changed, errApply := plugin.applyJSAfterResponse(
		scriptPath,
		"gpt-test",
		"openai",
		nil,
		nil,
		`{"ok":true}`,
		nil,
		http.Header{},
		false,
		nil,
	)
	if errApply != nil {
		t.Fatalf("applyJSAfterResponse() error = %v", errApply)
	}
	if !changed {
		t.Fatal("applyJSAfterResponse() changed = false, want true")
	}
	if processedBody != `{"ok":true}|nonstream` {
		t.Fatalf("applyJSAfterResponse() body = %q", processedBody)
	}
}
