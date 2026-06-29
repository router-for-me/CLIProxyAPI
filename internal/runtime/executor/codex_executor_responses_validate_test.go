package executor

import (
	"errors"
	"net/http"
	"testing"
)

func TestValidateCodexExecutorUpstreamBody_RejectsMixedUserMessage(t *testing.T) {
	body := []byte("{\"model\":\"gpt-5.5\",\"stream\":true,\"store\":false,\"input\":[{\"type\":\"message\",\"role\":\"user\",\"content\":[{\"type\":\"output_text\",\"text\":\"a\"},{\"type\":\"input_text\",\"text\":\"b\"}]}]}")
	err := validateCodexExecutorUpstreamBody(body)
	if err == nil {
		t.Fatal("expected validation error")
	}
	var status statusErr
	if !errors.As(err, &status) || status.code != http.StatusBadRequest {
		t.Fatalf("got %#v", err)
	}
}
