package executor

import (
	"bytes"
	"context"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

// executeNativeResponses shares the streaming path so SSE-only upstreams retain opaque items.
func (e *OpenAICompatExecutor) executeNativeResponses(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	ctx = helps.EnsureSessionContext(ctx, opts, req.Payload)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	opts.Stream = true
	result, err := e.ExecuteStream(ctx, auth, req, opts)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	var payload []byte
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			return cliproxyexecutor.Response{}, chunk.Err
		}
		for _, line := range bytes.Split(chunk.Payload, []byte("\n")) {
			if !bytes.HasPrefix(line, []byte("data:")) {
				continue
			}
			event := bytes.TrimSpace(line[5:])
			if gjson.GetBytes(event, "type").String() == "response.completed" {
				payload = []byte(gjson.GetBytes(event, "response").Raw)
			}
		}
	}
	if len(payload) == 0 {
		return cliproxyexecutor.Response{}, statusErr{code: http.StatusBadGateway, msg: "upstream Responses stream ended without a completed response"}
	}
	return cliproxyexecutor.Response{Payload: helps.EnsureResponsesUsageDetails(payload), Headers: result.Headers}, nil
}
