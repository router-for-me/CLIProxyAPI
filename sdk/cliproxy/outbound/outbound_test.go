package outbound

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type headerFinalizerFunc func(context.Context, pluginapi.OutboundHeaderInterceptRequest) (http.Header, error)

func (f headerFinalizerFunc) FinalizeOutboundHeaders(ctx context.Context, req pluginapi.OutboundHeaderInterceptRequest) (http.Header, error) {
	return f(ctx, req)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestDoFinalizesHeadersImmediatelyBeforeRoundTrip(t *testing.T) {
	var finalized bool
	ctx := WithHeaderFinalizer(context.Background(), headerFinalizerFunc(func(_ context.Context, req pluginapi.OutboundHeaderInterceptRequest) (http.Header, error) {
		if req.Provider != "codex" || req.Transport != pluginapi.OutboundTransportHTTP {
			t.Fatalf("finalizer request identity = %#v", req)
		}
		if req.Headers.Get("User-Agent") != "host/1.0" {
			t.Fatalf("finalizer User-Agent = %q", req.Headers.Get("User-Agent"))
		}
		finalized = true
		headers := req.Headers.Clone()
		headers.Set("User-Agent", "plugin/1.0")
		return headers, nil
	}))
	req, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.test", nil)
	if errRequest != nil {
		t.Fatalf("NewRequestWithContext() error = %v", errRequest)
	}
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("User-Agent", "host/1.0")
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if !finalized {
			t.Fatal("RoundTrip called before finalizer")
		}
		if req.Header.Get("User-Agent") != "plugin/1.0" || req.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("wire headers = %#v", req.Header)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok")), Request: req}, nil
	})}

	resp, errDo := Do(ctx, "codex", client, req)
	if errDo != nil {
		t.Fatalf("Do() error = %v", errDo)
	}
	_ = resp.Body.Close()
}

func TestDoFailsClosedBeforeRoundTrip(t *testing.T) {
	errRejected := errors.New("header plugin rejected request")
	ctx := WithProvider(context.Background(), " XAI ")
	ctx = WithHeaderFinalizer(ctx, headerFinalizerFunc(func(_ context.Context, req pluginapi.OutboundHeaderInterceptRequest) (http.Header, error) {
		if req.Provider != "xai" {
			t.Fatalf("provider = %q, want context provider xai", req.Provider)
		}
		return nil, errRejected
	}))
	req, errRequest := http.NewRequestWithContext(ctx, http.MethodPost, "https://example.test", nil)
	if errRequest != nil {
		t.Fatalf("NewRequestWithContext() error = %v", errRequest)
	}
	var roundTrips int
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		roundTrips++
		return nil, errors.New("unexpected network call")
	})}

	if _, errDo := Do(ctx, "codex", client, req); !errors.Is(errDo, errRejected) {
		t.Fatalf("Do() error = %v, want %v", errDo, errRejected)
	}
	if roundTrips != 0 {
		t.Fatalf("round trips = %d, want 0", roundTrips)
	}
}

func TestWrapHTTPClientFinalizesLibraryOwnedRequests(t *testing.T) {
	errRejected := errors.New("library request rejected")
	var roundTrips int
	base := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		roundTrips++
		return nil, errors.New("unexpected network call")
	})}
	ctx := WithHeaderFinalizer(context.Background(), headerFinalizerFunc(func(_ context.Context, req pluginapi.OutboundHeaderInterceptRequest) (http.Header, error) {
		if req.Provider != "vertex" || req.Transport != pluginapi.OutboundTransportHTTP {
			t.Fatalf("finalizer request identity = %#v", req)
		}
		return nil, errRejected
	}))
	client := WrapHTTPClient(ctx, "vertex", base)
	req, errRequest := http.NewRequest(http.MethodPost, "https://oauth2.example.test/token", nil)
	if errRequest != nil {
		t.Fatalf("NewRequest() error = %v", errRequest)
	}

	if _, errDo := client.Do(req); !errors.Is(errDo, errRejected) {
		t.Fatalf("wrapped client error = %v, want %v", errDo, errRejected)
	}
	if roundTrips != 0 {
		t.Fatalf("round trips = %d, want 0", roundTrips)
	}
}
