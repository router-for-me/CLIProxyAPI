package executor

import (
	"context"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

func sendChunk(ctx context.Context, out chan<- cliproxyexecutor.StreamChunk, chunk cliproxyexecutor.StreamChunk) bool {
	if out == nil {
		return false
	}
	if ctx == nil {
		out <- chunk
		return true
	}
	select {
	case <-ctx.Done():
		return false
	case out <- chunk:
		return true
	}
}
