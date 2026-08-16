// Package outbound carries provider identity and final outbound-header hooks to network send boundaries.
package outbound

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// HeaderFinalizer applies active host plugins to a provider request immediately before network I/O.
type HeaderFinalizer interface {
	FinalizeOutboundHeaders(context.Context, pluginapi.OutboundHeaderInterceptRequest) (http.Header, error)
}

type finalizerContextKey struct{}
type providerContextKey struct{}

type finalizingRoundTripper struct {
	ctx       context.Context
	provider  string
	transport http.RoundTripper
}

// WithHeaderFinalizer attaches a final outbound-header host to ctx.
func WithHeaderFinalizer(ctx context.Context, finalizer HeaderFinalizer) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if finalizer == nil {
		return ctx
	}
	return context.WithValue(ctx, finalizerContextKey{}, finalizer)
}

// WithProvider attaches the canonical provider identity used by nested send helpers.
func WithProvider(ctx context.Context, provider string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	provider = normalizeProvider(provider)
	if provider == "" {
		return ctx
	}
	return context.WithValue(ctx, providerContextKey{}, provider)
}

// Provider returns the canonical context provider or a normalized fallback.
func Provider(ctx context.Context, fallback string) string {
	if ctx != nil {
		if provider, ok := ctx.Value(providerContextKey{}).(string); ok {
			if provider = normalizeProvider(provider); provider != "" {
				return provider
			}
		}
	}
	return normalizeProvider(fallback)
}

// FinalizeHeaders applies the host finalizer to a cloned header map.
func FinalizeHeaders(ctx context.Context, provider string, transport pluginapi.OutboundTransport, headers http.Header) (http.Header, error) {
	provider = Provider(ctx, provider)
	if provider == "" {
		return cloneHeader(headers), nil
	}
	finalizer := headerFinalizer(ctx)
	if finalizer == nil {
		return cloneHeader(headers), nil
	}
	return finalizer.FinalizeOutboundHeaders(ctx, pluginapi.OutboundHeaderInterceptRequest{
		Provider:  provider,
		Transport: transport,
		Headers:   cloneHeader(headers),
	})
}

// FinalizeRequest applies final HTTP headers to req without changing its body or context.
func FinalizeRequest(ctx context.Context, provider string, req *http.Request) error {
	if req == nil {
		return fmt.Errorf("outbound request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	headers, errFinalize := FinalizeHeaders(ctx, provider, pluginapi.OutboundTransportHTTP, req.Header)
	if errFinalize != nil {
		return errFinalize
	}
	req.Header = headers
	return nil
}

// Do finalizes req and sends it through client.
func Do(ctx context.Context, provider string, client *http.Client, req *http.Request) (*http.Response, error) {
	if client == nil {
		client = http.DefaultClient
	}
	if errFinalize := FinalizeRequest(ctx, provider, req); errFinalize != nil {
		return nil, errFinalize
	}
	return client.Do(req)
}

// WrapHTTPClient returns a shallow client copy that finalizes requests created by
// libraries which call Client.Do internally.
func WrapHTTPClient(ctx context.Context, provider string, client *http.Client) *http.Client {
	if client == nil {
		client = http.DefaultClient
	}
	wrapped := *client
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	wrapped.Transport = &finalizingRoundTripper{
		ctx:       ctx,
		provider:  normalizeProvider(provider),
		transport: transport,
	}
	return &wrapped
}

func (t *finalizingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("outbound request is nil")
	}
	if t == nil || t.transport == nil {
		return nil, fmt.Errorf("outbound transport is nil")
	}
	finalizeCtx := req.Context()
	if headerFinalizer(finalizeCtx) == nil {
		finalizeCtx = WithHeaderFinalizer(finalizeCtx, headerFinalizer(t.ctx))
	}
	provider := Provider(finalizeCtx, Provider(t.ctx, t.provider))
	finalizeCtx = WithProvider(finalizeCtx, provider)
	wireReq := req.Clone(finalizeCtx)
	if errFinalize := FinalizeRequest(finalizeCtx, provider, wireReq); errFinalize != nil {
		return nil, errFinalize
	}
	return t.transport.RoundTrip(wireReq)
}

func headerFinalizer(ctx context.Context) HeaderFinalizer {
	if ctx == nil {
		return nil
	}
	finalizer, _ := ctx.Value(finalizerContextKey{}).(HeaderFinalizer)
	return finalizer
}

func normalizeProvider(provider string) string {
	return strings.ToLower(strings.TrimSpace(provider))
}

func cloneHeader(headers http.Header) http.Header {
	if len(headers) == 0 {
		return nil
	}
	return headers.Clone()
}
