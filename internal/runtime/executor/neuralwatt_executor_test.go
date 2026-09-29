package executor

import (
	"context"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestNeuralwattExecutorIdentifier(t *testing.T) {
	e := NewNeuralwattExecutor(nil)
	if got := e.Identifier(); got != "neuralwatt" {
		t.Fatalf("Identifier() = %q, want %q", got, "neuralwatt")
	}
}

func TestNeuralwattExecutorNilCompatIsSafe(t *testing.T) {
	var e *NeuralwattExecutor
	if _, err := e.Execute(context.Background(), nil, cliproxyexecutor.Request{}, cliproxyexecutor.Options{}); err == nil {
		t.Fatal("Execute on nil executor: want error, got nil")
	}
}
