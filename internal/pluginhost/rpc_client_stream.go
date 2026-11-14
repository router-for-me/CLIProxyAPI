package pluginhost

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

var errPluginInstanceClosed = errors.New("plugin instance closed during execution")

func (a *rpcPluginAdapter) ExecuteStream(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
	if a == nil || a.host == nil || a.host.streams == nil {
		return pluginapi.ExecutorStreamResponse{}, fmt.Errorf("plugin stream bridge is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// Unload invalidates callbacks before plugin shutdown can close its streams.
	// Bind this stream to the exact instance so a reload cannot inherit its lifetime.
	ctx, cancel := context.WithCancelCause(ctx)
	stopInstanceCancel := func() bool { return false }
	if a.instance != nil {
		stopInstanceCancel = context.AfterFunc(a.instance.lifetimeContext(), func() { cancel(errPluginInstanceClosed) })
		if a.instance.closed.Load() {
			cancel(errPluginInstanceClosed)
		}
	}
	streamID, chunks, cleanupStream := a.host.streams.open(ctx)
	callbackID, closeCallback := a.openHostCallbackContext(ctx)
	cleanup := combinedCleanup(cleanupStream, closeCallback, func() {
		stopInstanceCancel()
		cancel(nil)
	})
	rpcReq := rpcExecutorRequest{
		ExecutorRequest: req,
		StreamID:        streamID,
		HostCallbackID:  callbackID,
	}
	resp, errCall := callPlugin[rpcExecutorStreamResponse](ctx, a.client, pluginabi.MethodExecutorExecuteStream, rpcReq)
	if errCall != nil {
		if ctx.Err() != nil {
			errCall = context.Cause(ctx)
		}
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
	if ctx == nil {
		ctx = context.Background()
	}
	out := make(chan pluginapi.ExecutorStreamChunk, 1)
	go func() {
		defer func() {
			if err := context.Cause(ctx); err != nil {
				// Reserve the slot for the terminal error, even without a consumer.
				// Pending payloads are discarded when this stream is aborted.
				select {
				case <-out:
				default:
				}
				out <- pluginapi.ExecutorStreamChunk{Err: err}
			}
			if cleanup != nil {
				cleanup()
			}
			close(out)
		}()
		done := ctx.Done()
		for {
			var chunk pluginapi.ExecutorStreamChunk
			var ok bool
			select {
			case chunk, ok = <-chunks:
				if !ok {
					return
				}
			case <-done:
				return
			}
			select {
			case out <- chunk:
			case <-done:
				return
			}
		}
	}()
	return out
}
