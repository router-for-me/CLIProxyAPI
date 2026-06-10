package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyJSBeforeRequestUsesReturnedCtxBody(t *testing.T) {
	scriptPath := filepath.Join(t.TempDir(), "before.js")
	script := `
function on_before_request(ctx) {
    var req = JSON.parse(ctx.body);
    req.messages[0].content = req.messages[0].content.replace("sensitive_word", "safe_word");
    ctx.body = JSON.stringify(req);
    ctx.headers["X-Plugin"] = "updated";
    return ctx;
}
`
	if errWrite := os.WriteFile(scriptPath, []byte(script), 0600); errWrite != nil {
		t.Fatalf("os.WriteFile() error = %v", errWrite)
	}

	plugin := &jsHandlerPlugin{cfg: defaultJSHandlerConfig()}
	headers := http.Header{"X-Plugin": []string{"original"}}
	processed, _, errApply := plugin.applyJSRequestHook(
		scriptPath,
		"on_before_request",
		[]byte(`{"messages":[{"role":"user","content":"contains sensitive_word"}]}`),
		"gpt-test",
		"openai",
		"",
		headers,
		"",
	)
	if errApply != nil {
		t.Fatalf("applyJSRequestHook() error = %v", errApply)
	}
	if body := string(processed); !strings.Contains(body, "safe_word") || strings.Contains(body, "sensitive_word") {
		t.Fatalf("processed body = %q, want sensitive word rewritten", body)
	}
	if got := headers.Get("X-Plugin"); got != "updated" {
		t.Fatalf("header X-Plugin = %q, want updated", got)
	}
}

func TestApplyJSAfterAuthRequestReceivesFormats(t *testing.T) {
	scriptPath := filepath.Join(t.TempDir(), "after_auth.js")
	script := `
function on_after_auth_request(ctx) {
    if (ctx.source_format !== "openai" || ctx.to_format !== "codex") {
        throw new Error("unexpected formats: " + ctx.source_format + " -> " + ctx.to_format);
    }
    if (ctx.sourceFormat !== "openai" || ctx.toFormat !== "codex") {
        throw new Error("unexpected camel formats: " + ctx.sourceFormat + " -> " + ctx.toFormat);
    }
    var req = JSON.parse(ctx.body);
    req.after_auth = ctx.source_format + "_to_" + ctx.to_format;
    ctx.headers["X-Protocol"] = req.after_auth;
    ctx.body = JSON.stringify(req);
    return ctx;
}
`
	if errWrite := os.WriteFile(scriptPath, []byte(script), 0600); errWrite != nil {
		t.Fatalf("os.WriteFile() error = %v", errWrite)
	}

	plugin := &jsHandlerPlugin{cfg: defaultJSHandlerConfig()}
	headers := http.Header{}
	processed, _, errApply := plugin.applyJSRequestHook(
		scriptPath,
		"on_after_auth_request",
		[]byte(`{"model":"gpt-test"}`),
		"gpt-test",
		"openai",
		"codex",
		headers,
		"",
	)
	if errApply != nil {
		t.Fatalf("applyJSRequestHook() error = %v", errApply)
	}
	if body := string(processed); !strings.Contains(body, `"after_auth":"openai_to_codex"`) {
		t.Fatalf("processed body = %q, want after_auth marker", body)
	}
	if got := headers.Get("X-Protocol"); got != "openai_to_codex" {
		t.Fatalf("header X-Protocol = %q, want openai_to_codex", got)
	}
}

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
		"",
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
		"",
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
