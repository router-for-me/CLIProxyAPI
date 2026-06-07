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
	if MethodHostHTTPDo != "host.http.do" {
		t.Fatalf("MethodHostHTTPDo = %q", MethodHostHTTPDo)
	}
	if MethodHostHTTPStreamRead != "host.http.stream_read" {
		t.Fatalf("MethodHostHTTPStreamRead = %q", MethodHostHTTPStreamRead)
	}
	if MethodExecutorExecuteStream != "executor.execute_stream" {
		t.Fatalf("MethodExecutorExecuteStream = %q", MethodExecutorExecuteStream)
	}
}
