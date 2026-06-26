package pluginhost

import (
	"context"
	"fmt"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func (a *rpcPluginAdapter) ExecuteStream(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
	if a == nil || a.host == nil || a.host.streams == nil {
		return pluginapi.ExecutorStreamResponse{}, fmt.Errorf("plugin stream bridge is unavailable")
	}
	streamID, chunks, cleanupStream := a.host.streams.open(ctx)
	callbackID, closeCallback := a.openHostCallbackContext(ctx)
	cleanup := combinedCleanup(cleanupStream, closeCallback)
	rpcReq := rpcExecutorRequest{
		ExecutorRequest: req,
		StreamID:        streamID,
		HostCallbackID:  callbackID,
	}
	resp, errCall := callPlugin[rpcExecutorStreamResponse](ctx, a.client, pluginabi.MethodExecutorExecuteStream, rpcReq)
	if errCall != nil {
		cleanup()
		return pluginapi.ExecutorStreamResponse{}, errCall
	}
	if len(resp.Chunks) > 0 {
		cleanup()
		out := make(chan pluginapi.ExecutorStreamChunk, len(resp.Chunks))
		for _, chunk := range resp.Chunks {
			out <- chunk
		}
		close(out)
		return pluginapi.ExecutorStreamResponse{Headers: resp.Headers, Chunks: out}, nil
	}
	// Async streaming plugins can return before they finish emitting chunks, so keep callbacks alive until the stream ends.
	return pluginapi.ExecutorStreamResponse{
		Headers: resp.Headers,
		Chunks:  cleanupWhenStreamDone(ctx, chunks, cleanup),
	}, nil
}

func combinedCleanup(cleanups ...func()) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			for _, cleanup := range cleanups {
				if cleanup != nil {
					cleanup()
				}
			}
		})
	}
}

func cleanupWhenStreamDone(ctx context.Context, chunks <-chan pluginapi.ExecutorStreamChunk, cleanup func()) <-chan pluginapi.ExecutorStreamChunk {
	out := make(chan pluginapi.ExecutorStreamChunk)
	go func() {
		defer func() {
			if cleanup != nil {
				cleanup()
			}
			close(out)
		}()
		var done <-chan struct{}
		if ctx != nil {
			done = ctx.Done()
		}
		for chunk := range chunks {
			select {
			case out <- chunk:
			case <-done:
				return
			}
		}
	}()
	return out
}
