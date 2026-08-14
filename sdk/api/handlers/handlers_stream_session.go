package handlers

import (
	stdcontext "context"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"golang.org/x/net/context"
)

func openStreamChunkInterceptorSession(host PluginInterceptorHost, skipPluginID string) (pluginapi.StreamChunkInterceptorSession, bool) {
	if host == nil {
		return nil, false
	}
	opener, ok := host.(pluginapi.StreamChunkInterceptorSessionHost)
	if !ok {
		return nil, false
	}
	return opener.OpenStreamChunkInterceptorSession(skipPluginID), true
}

func streamFinalizerContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return stdcontext.WithoutCancel(ctx)
}

func interceptStreamChunkWithSession(ctx context.Context, session pluginapi.StreamChunkInterceptorSession, host PluginInterceptorHost, req pluginapi.StreamChunkInterceptRequest, skipPluginID string) pluginapi.StreamChunkInterceptResponse {
	if session != nil {
		return session.InterceptStreamChunk(ctx, req)
	}
	return interceptStreamChunk(ctx, host, req, skipPluginID)
}
