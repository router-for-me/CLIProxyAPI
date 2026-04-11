package executor

import "testing"

func statusCodeFromErr(err error) int {
	if err == nil {
		return 0
	}
	if se, ok := err.(interface{ StatusCode() int }); ok {
		return se.StatusCode()
	}
	return 0
}

func TestValidateOpenAINonStreamSuccessBody(t *testing.T) {
	t.Run("valid content response", func(t *testing.T) {
		body := []byte(`{"id":"x","choices":[{"message":{"role":"assistant","content":"pong"}}],"usage":{"completion_tokens":1}}`)
		if err := validateOpenAINonStreamSuccessBody(body); err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
	})

	t.Run("business 434 wrapped by 200", func(t *testing.T) {
		body := []byte(`{"status":"434","msg":"Invalid apiKey","body":null}`)
		err := validateOpenAINonStreamSuccessBody(body)
		if err == nil {
			t.Fatalf("expected error")
		}
		if code := statusCodeFromErr(err); code != 401 {
			t.Fatalf("expected status 401, got %d (err=%v)", code, err)
		}
	})

	t.Run("choices missing", func(t *testing.T) {
		body := []byte(`{"id":"x","object":"chat.completion"}`)
		err := validateOpenAINonStreamSuccessBody(body)
		if err == nil {
			t.Fatalf("expected error")
		}
		if code := statusCodeFromErr(err); code != 502 {
			t.Fatalf("expected status 502, got %d", code)
		}
	})

	t.Run("empty completion with completion_tokens=0", func(t *testing.T) {
		body := []byte(`{"id":"x","choices":[{"message":{"role":"assistant","content":""}}],"usage":{"completion_tokens":0}}`)
		err := validateOpenAINonStreamSuccessBody(body)
		if err == nil {
			t.Fatalf("expected error")
		}
		if code := statusCodeFromErr(err); code != 502 {
			t.Fatalf("expected status 502, got %d", code)
		}
	})

	t.Run("tool calls only is valid", func(t *testing.T) {
		body := []byte(`{"id":"x","choices":[{"message":{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]}}],"usage":{"completion_tokens":0}}`)
		if err := validateOpenAINonStreamSuccessBody(body); err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
	})
}
