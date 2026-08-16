package pluginhost

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestFinalizeOutboundHeadersChainsAndProtectsHostHeaders(t *testing.T) {
	calls := make([]string, 0, 2)
	host := newHostWithRecords(
		capabilityRecord{
			id:       "high",
			priority: 20,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{OutboundHeaderInterceptor: outboundHeaderInterceptorFunc(func(_ context.Context, req pluginapi.OutboundHeaderInterceptRequest) (pluginapi.OutboundHeaderInterceptResponse, error) {
				calls = append(calls, "high")
				if req.Provider != "codex" || req.Transport != pluginapi.OutboundTransportHTTP {
					t.Fatalf("request identity = %#v", req)
				}
				if req.Headers.Get("Authorization") != "" || req.Headers.Get("X-Api-Key") != "" || req.Headers.Get("Content-Length") != "" {
					t.Fatalf("protected headers reached plugin: %#v", req.Headers)
				}
				return pluginapi.OutboundHeaderInterceptResponse{Headers: http.Header{"User-Agent": {"high/1.0"}}}, nil
			})}},
		},
		capabilityRecord{
			id:       "low",
			priority: 10,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{OutboundHeaderInterceptor: outboundHeaderInterceptorFunc(func(_ context.Context, req pluginapi.OutboundHeaderInterceptRequest) (pluginapi.OutboundHeaderInterceptResponse, error) {
				calls = append(calls, "low")
				if req.Headers.Get("User-Agent") != "high/1.0" {
					t.Fatalf("chained user agent = %q", req.Headers.Get("User-Agent"))
				}
				return pluginapi.OutboundHeaderInterceptResponse{
					Headers:      http.Header{"User-Agent": {"low/1.0"}},
					ClearHeaders: []string{"X-Remove"},
				}, nil
			})}},
		},
	)
	original := http.Header{
		"Authorization":  {"Bearer secret"},
		"X-Api-Key":      {"secret"},
		"Content-Length": {"10"},
		"User-Agent":     {"original/1.0"},
		"X-Remove":       {"yes"},
	}
	got, errFinalize := host.FinalizeOutboundHeaders(context.Background(), pluginapi.OutboundHeaderInterceptRequest{
		Provider:  " CODEX ",
		Transport: pluginapi.OutboundTransportHTTP,
		Headers:   original,
	})
	if errFinalize != nil {
		t.Fatalf("FinalizeOutboundHeaders() error = %v", errFinalize)
	}
	if len(calls) != 2 || calls[0] != "high" || calls[1] != "low" {
		t.Fatalf("calls = %v", calls)
	}
	if got.Get("User-Agent") != "low/1.0" || got.Get("X-Remove") != "" {
		t.Fatalf("final headers = %#v", got)
	}
	if got.Get("Authorization") != "Bearer secret" || got.Get("X-Api-Key") != "secret" || got.Get("Content-Length") != "10" {
		t.Fatalf("protected headers changed: %#v", got)
	}
	if original.Get("User-Agent") != "original/1.0" || original.Get("X-Remove") != "yes" {
		t.Fatalf("input headers mutated: %#v", original)
	}
}

func TestFinalizeOutboundHeadersFailsClosed(t *testing.T) {
	tests := []struct {
		name        string
		interceptor pluginapi.OutboundHeaderInterceptor
	}{
		{
			name: "plugin error",
			interceptor: outboundHeaderInterceptorFunc(func(context.Context, pluginapi.OutboundHeaderInterceptRequest) (pluginapi.OutboundHeaderInterceptResponse, error) {
				return pluginapi.OutboundHeaderInterceptResponse{}, errors.New("broken")
			}),
		},
		{
			name: "protected update",
			interceptor: outboundHeaderInterceptorFunc(func(context.Context, pluginapi.OutboundHeaderInterceptRequest) (pluginapi.OutboundHeaderInterceptResponse, error) {
				return pluginapi.OutboundHeaderInterceptResponse{Headers: http.Header{"Authorization": {"changed"}}}, nil
			}),
		},
		{
			name: "protected clear",
			interceptor: outboundHeaderInterceptorFunc(func(context.Context, pluginapi.OutboundHeaderInterceptRequest) (pluginapi.OutboundHeaderInterceptResponse, error) {
				return pluginapi.OutboundHeaderInterceptResponse{ClearHeaders: []string{"Sec-WebSocket-Key"}}, nil
			}),
		},
		{
			name: "invalid value",
			interceptor: outboundHeaderInterceptorFunc(func(context.Context, pluginapi.OutboundHeaderInterceptRequest) (pluginapi.OutboundHeaderInterceptResponse, error) {
				return pluginapi.OutboundHeaderInterceptResponse{Headers: http.Header{"User-Agent": {"bad\r\nvalue"}}}, nil
			}),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			host := newHostWithRecords(capabilityRecord{id: "headers", plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{OutboundHeaderInterceptor: test.interceptor}}})
			if _, errFinalize := host.FinalizeOutboundHeaders(context.Background(), pluginapi.OutboundHeaderInterceptRequest{
				Provider: "claude", Transport: pluginapi.OutboundTransportHTTP,
			}); errFinalize == nil {
				t.Fatal("FinalizeOutboundHeaders() error = nil")
			}
		})
	}
}

func TestFinalizeOutboundHeadersPanicFusesAndFailsClosed(t *testing.T) {
	host := newHostWithRecords(capabilityRecord{id: "panic", plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{OutboundHeaderInterceptor: outboundHeaderInterceptorFunc(func(context.Context, pluginapi.OutboundHeaderInterceptRequest) (pluginapi.OutboundHeaderInterceptResponse, error) {
		panic("boom")
	})}}})
	req := pluginapi.OutboundHeaderInterceptRequest{Provider: "xai", Transport: pluginapi.OutboundTransportWebSocket}
	if _, errFinalize := host.FinalizeOutboundHeaders(context.Background(), req); errFinalize == nil {
		t.Fatal("first FinalizeOutboundHeaders() error = nil")
	}
	if !host.isPluginFused("panic") {
		t.Fatal("plugin was not fused after panic")
	}
	if _, errFinalize := host.FinalizeOutboundHeaders(context.Background(), req); errFinalize == nil {
		t.Fatal("second FinalizeOutboundHeaders() error = nil")
	}
}

func TestFinalizeOutboundHeadersSkipsCallingPluginForOwnHostRequest(t *testing.T) {
	called := false
	host := newHostWithRecords(capabilityRecord{id: "self", plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{OutboundHeaderInterceptor: outboundHeaderInterceptorFunc(func(context.Context, pluginapi.OutboundHeaderInterceptRequest) (pluginapi.OutboundHeaderInterceptResponse, error) {
		called = true
		return pluginapi.OutboundHeaderInterceptResponse{}, nil
	})}}})
	ctx := context.WithValue(context.Background(), hostCallbackPluginIDKey{}, "self")
	got, errFinalize := host.FinalizeOutboundHeaders(ctx, pluginapi.OutboundHeaderInterceptRequest{
		Provider: "plugin-provider", Transport: pluginapi.OutboundTransportHTTP, Headers: http.Header{"User-Agent": {"original"}},
	})
	if errFinalize != nil {
		t.Fatalf("FinalizeOutboundHeaders() error = %v", errFinalize)
	}
	if called || got.Get("User-Agent") != "original" {
		t.Fatalf("self interception called=%v headers=%#v", called, got)
	}
}
