package pluginhost

import (
	"context"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func markRequestInterceptorPanic(call func(context.Context, pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error), panicked *bool) func(context.Context, pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
	return func(ctx context.Context, req pluginapi.RequestInterceptRequest) (resp pluginapi.RequestInterceptResponse, err error) {
		defer func() {
			if recovered := recover(); recovered != nil {
				if panicked != nil {
					*panicked = true
				}
				panic(recovered)
			}
		}()
		return call(ctx, req)
	}
}
