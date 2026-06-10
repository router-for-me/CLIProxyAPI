package pluginabi

import (
	"encoding/json"
	"testing"
)

func TestEnvelopeRoundTrip(t *testing.T) {
	payload := json.RawMessage(`{"name":"example"}`)
	env := Envelope{
		OK:     true,
		Result: payload,
	}

	raw, errMarshal := json.Marshal(env)
	if errMarshal != nil {
		t.Fatalf("marshal envelope: %v", errMarshal)
	}

	var decoded Envelope
	if errUnmarshal := json.Unmarshal(raw, &decoded); errUnmarshal != nil {
		t.Fatalf("unmarshal envelope: %v", errUnmarshal)
	}
	if !decoded.OK || string(decoded.Result) != string(payload) {
		t.Fatalf("decoded envelope = %#v, want ok payload", decoded)
	}
}

func TestMethodNamesAreStable(t *testing.T) {
	if MethodPluginRegister != "plugin.register" {
		t.Fatalf("MethodPluginRegister = %q", MethodPluginRegister)
	}
	if MethodRequestInterceptBefore != "request.intercept_before" {
		t.Fatalf("MethodRequestInterceptBefore = %q", MethodRequestInterceptBefore)
	}
	if MethodRequestInterceptAfter != "request.intercept_after" {
		t.Fatalf("MethodRequestInterceptAfter = %q", MethodRequestInterceptAfter)
	}
	if MethodResponseInterceptAfter != "response.intercept_after" {
		t.Fatalf("MethodResponseInterceptAfter = %q", MethodResponseInterceptAfter)
	}
	if MethodResponseInterceptStreamChunk != "response.intercept_stream_chunk" {
		t.Fatalf("MethodResponseInterceptStreamChunk = %q", MethodResponseInterceptStreamChunk)
	}
	if MethodHostHTTPDo != "host.http.do" {
		t.Fatalf("MethodHostHTTPDo = %q", MethodHostHTTPDo)
	}
	if MethodHostHTTPStreamRead != "host.http.stream_read" {
		t.Fatalf("MethodHostHTTPStreamRead = %q", MethodHostHTTPStreamRead)
	}
	if MethodExecutorExecuteStream != "executor.execute_stream" {
		t.Fatalf("MethodExecutorExecuteStream = %q", MethodExecutorExecuteStream)
	}
	if MethodServerToolHandle != "server_tool.handle" {
		t.Fatalf("MethodServerToolHandle = %q", MethodServerToolHandle)
	}
	if MethodServerToolHandleStream != "server_tool.handle_stream" {
		t.Fatalf("MethodServerToolHandleStream = %q", MethodServerToolHandleStream)
	}
}

func TestSchedulerPickMethodName(t *testing.T) {
	if MethodSchedulerPick != "scheduler.pick" {
		t.Fatalf("MethodSchedulerPick = %q", MethodSchedulerPick)
	}
}
