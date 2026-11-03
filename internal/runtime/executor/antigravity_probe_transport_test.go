package executor

import (
	"net/http"
	"testing"
)

func TestNewAntigravityProbeTransport_ForcesHTTP11(t *testing.T) {
	roundTripper, err := NewAntigravityProbeTransport("")
	if err != nil {
		t.Fatalf("NewAntigravityProbeTransport() error = %v", err)
	}
	transport, ok := roundTripper.(*http.Transport)
	if !ok || transport == nil {
		t.Fatalf("NewAntigravityProbeTransport() = %T, want *http.Transport", roundTripper)
	}
	if transport.ForceAttemptHTTP2 {
		t.Fatal("ForceAttemptHTTP2 = true, want false")
	}
	if transport.TLSNextProto == nil || len(transport.TLSNextProto) != 0 {
		t.Fatalf("TLSNextProto = %v, want empty non-nil map", transport.TLSNextProto)
	}
	if transport.TLSClientConfig == nil || len(transport.TLSClientConfig.NextProtos) != 0 {
		t.Fatalf("TLSClientConfig = %+v, want empty NextProtos", transport.TLSClientConfig)
	}
	if transport.Proxy == nil {
		t.Fatal("Proxy = nil, want environment proxy function for inherit mode")
	}

	directRoundTripper, err := NewAntigravityProbeTransport("direct")
	if err != nil {
		t.Fatalf("NewAntigravityProbeTransport(direct) error = %v", err)
	}
	direct, ok := directRoundTripper.(*http.Transport)
	if !ok || direct == nil {
		t.Fatalf("NewAntigravityProbeTransport(direct) = %T, want *http.Transport", directRoundTripper)
	}
	if direct.Proxy != nil {
		t.Fatal("Proxy != nil for direct mode")
	}
	if direct.ForceAttemptHTTP2 || len(direct.TLSNextProto) != 0 {
		t.Fatal("direct transport still allows HTTP/2")
	}

	if _, err = NewAntigravityProbeTransport("::not a url"); err == nil {
		t.Fatal("NewAntigravityProbeTransport(invalid) error = nil, want error")
	}
}

func TestNewAntigravityProbeTransport_KeepsCustomDefaultRoundTripper(t *testing.T) {
	previous := http.DefaultTransport
	custom := roundTripperFunc(func(req *http.Request) (*http.Response, error) { return nil, nil })
	http.DefaultTransport = custom
	t.Cleanup(func() { http.DefaultTransport = previous })

	roundTripper, err := NewAntigravityProbeTransport("")
	if err != nil {
		t.Fatalf("NewAntigravityProbeTransport() error = %v", err)
	}
	if _, isTransport := roundTripper.(*http.Transport); isTransport {
		t.Fatal("custom default round tripper was replaced")
	}
}
