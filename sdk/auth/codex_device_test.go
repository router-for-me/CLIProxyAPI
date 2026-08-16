package auth

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/outbound"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type codexDeviceRoundTripFunc func(*http.Request) (*http.Response, error)

func (f codexDeviceRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type codexDeviceHeaderFinalizerFunc func(context.Context, pluginapi.OutboundHeaderInterceptRequest) (http.Header, error)

func (f codexDeviceHeaderFinalizerFunc) FinalizeOutboundHeaders(ctx context.Context, req pluginapi.OutboundHeaderInterceptRequest) (http.Header, error) {
	return f(ctx, req)
}

func TestRequestCodexDeviceUserCodeFinalizesHeaders(t *testing.T) {
	ctx := outbound.WithHeaderFinalizer(context.Background(), codexDeviceHeaderFinalizerFunc(func(_ context.Context, req pluginapi.OutboundHeaderInterceptRequest) (http.Header, error) {
		if req.Provider != "codex" || req.Transport != pluginapi.OutboundTransportHTTP {
			t.Fatalf("finalizer request identity = %#v", req)
		}
		headers := req.Headers.Clone()
		headers.Set("User-Agent", "device-plugin/1.0")
		return headers, nil
	}))
	client := &http.Client{Transport: codexDeviceRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("User-Agent") != "device-plugin/1.0" {
			t.Fatalf("User-Agent = %q", req.Header.Get("User-Agent"))
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"device_auth_id":"device","user_code":"code","interval":1}`)),
			Request:    req,
		}, nil
	})}

	resp, errRequest := requestCodexDeviceUserCode(ctx, client)
	if errRequest != nil {
		t.Fatalf("requestCodexDeviceUserCode() error = %v", errRequest)
	}
	if resp.DeviceAuthID != "device" || resp.UserCode != "code" {
		t.Fatalf("response = %#v", resp)
	}
}
