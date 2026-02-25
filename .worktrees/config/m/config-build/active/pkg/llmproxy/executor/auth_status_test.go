package executor

import (
	"context"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/wsrelay"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

func TestAIStudioHttpRequestMissingAuthStatus(t *testing.T) {
	exec := &AIStudioExecutor{relay: &wsrelay.Manager{}}
	req, errReq := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://example.com", nil)
	if errReq != nil {
		t.Fatalf("new request: %v", errReq)
	}

	_, err := exec.HttpRequest(context.Background(), nil, req)
	if err == nil {
		t.Fatal("expected missing auth error")
	}
	se, ok := err.(interface{ StatusCode() int })
	if !ok {
		t.Fatalf("expected status error type, got %T (%v)", err, err)
	}
	if got := se.StatusCode(); got != http.StatusUnauthorized {
		t.Fatalf("status code = %d, want %d", got, http.StatusUnauthorized)
	}
}

func TestKiloRefreshMissingAuthStatus(t *testing.T) {
	exec := &KiloExecutor{}
	_, err := exec.Refresh(context.Background(), nil)
	if err == nil {
		t.Fatal("expected missing auth error")
	}
	se, ok := err.(interface{ StatusCode() int })
	if !ok {
		t.Fatalf("expected status error type, got %T (%v)", err, err)
	}
	if got := se.StatusCode(); got != http.StatusUnauthorized {
		t.Fatalf("status code = %d, want %d", got, http.StatusUnauthorized)
	}
}

func TestCodexRefreshMissingAuthStatus(t *testing.T) {
	exec := &CodexExecutor{}
	_, err := exec.Refresh(context.Background(), nil)
	if err == nil {
		t.Fatal("expected missing auth error")
	}
	se, ok := err.(interface{ StatusCode() int })
	if !ok {
		t.Fatalf("expected status error type, got %T (%v)", err, err)
	}
	if got := se.StatusCode(); got != http.StatusUnauthorized {
		t.Fatalf("status code = %d, want %d", got, http.StatusUnauthorized)
	}
}

func TestIFlowExecuteMissingAuthStatus(t *testing.T) {
	exec := &IFlowExecutor{}
	_, err := exec.Execute(context.Background(), nil, cliproxyexecutor.Request{Model: "iflow/gpt-4.1"}, cliproxyexecutor.Options{})
	if err == nil {
		t.Fatal("expected missing auth error")
	}
	se, ok := err.(interface{ StatusCode() int })
	if !ok {
		t.Fatalf("expected status error type, got %T (%v)", err, err)
	}
	if got := se.StatusCode(); got != http.StatusUnauthorized {
		t.Fatalf("status code = %d, want %d", got, http.StatusUnauthorized)
	}
}

func TestIFlowRefreshMissingAuthStatus(t *testing.T) {
	exec := &IFlowExecutor{}
	_, err := exec.Refresh(context.Background(), nil)
	if err == nil {
		t.Fatal("expected missing auth error")
	}
	se, ok := err.(interface{ StatusCode() int })
	if !ok {
		t.Fatalf("expected status error type, got %T (%v)", err, err)
	}
	if got := se.StatusCode(); got != http.StatusUnauthorized {
		t.Fatalf("status code = %d, want %d", got, http.StatusUnauthorized)
	}
}
