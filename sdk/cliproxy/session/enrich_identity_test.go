package session

import (
	"net/http"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestEnrichStoresIdentityInRequestAndOptions(t *testing.T) {
	req := cliproxyexecutor.Request{Payload: []byte(`{"prompt_cache_key":"cache-1","conversation":{"id":"conv_1"}}`)}
	opts := cliproxyexecutor.Options{}

	req, opts = Enrich(req, opts)

	optionIdentity, ok := FromMetadata(opts.Metadata)
	if !ok {
		t.Fatal("options metadata is missing the session identity")
	}
	requestIdentity, ok := FromMetadata(req.Metadata)
	if !ok {
		t.Fatal("request metadata is missing the session identity")
	}
	if optionIdentity.ID != "pck:cache-1" {
		t.Fatalf("ID = %q, want pck:cache-1", optionIdentity.ID)
	}
	if optionIdentity.FallbackID() != "conv:conv_1" {
		t.Fatalf("FallbackID() = %q, want conv:conv_1", optionIdentity.FallbackID())
	}
	if requestIdentity.ID != optionIdentity.ID {
		t.Fatalf("request identity %q differs from option identity %q", requestIdentity.ID, optionIdentity.ID)
	}
}

func TestEnrichSuppressesDerivedIdentityForNewExplicitHeaders(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	tests := map[string]struct {
		headers http.Header
		wantID  string
	}{
		"gateway session": {
			headers: http.Header{"X-Gateway-Session-Id": {"gw-1"}},
			wantID:  "gateway:gw-1",
		},
		"opencode session": {
			headers: http.Header{"X-Opencode-Session": {"ses_1"}},
			wantID:  "opencode:ses_1",
		},
		"thread only": {
			headers: http.Header{"Thread-Id": {"thread-1"}},
			wantID:  "thread:thread-1",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			req := cliproxyexecutor.Request{Payload: payload}
			opts := cliproxyexecutor.Options{Headers: tt.headers}

			req, opts = Enrich(req, opts)

			if _, exists := opts.Metadata[cliproxyexecutor.DerivedSessionIDMetadataKey]; exists {
				t.Fatal("derived identity was computed despite an explicit client session signal")
			}
			identity, ok := FromMetadata(opts.Metadata)
			if !ok {
				t.Fatal("options metadata is missing the session identity")
			}
			if identity.ID != tt.wantID {
				t.Fatalf("ID = %q, want %q", identity.ID, tt.wantID)
			}
		})
	}
}

func TestEnrichDerivesIdentityWhenClientSendsNoSessionSignal(t *testing.T) {
	req := cliproxyexecutor.Request{Payload: []byte(`{"messages":[{"role":"user","content":"hello"}]}`)}
	opts := cliproxyexecutor.Options{}

	_, opts = Enrich(req, opts)

	derived, _ := opts.Metadata[cliproxyexecutor.DerivedSessionIDMetadataKey].(string)
	if derived == "" {
		t.Fatal("derived identity was not computed for a request without session signals")
	}
	identity, ok := FromMetadata(opts.Metadata)
	if !ok {
		t.Fatal("options metadata is missing the session identity")
	}
	if identity.ID != "derived:"+derived {
		t.Fatalf("ID = %q, want derived:%s", identity.ID, derived)
	}
	if identity.Source != SourceDerived {
		t.Fatalf("Source = %q, want %q", identity.Source, SourceDerived)
	}
}

func TestEnrichLeavesNoIdentityWhenNothingIsKnown(t *testing.T) {
	_, opts := Enrich(cliproxyexecutor.Request{}, cliproxyexecutor.Options{})

	if _, ok := FromMetadata(opts.Metadata); ok {
		t.Fatal("an identity was stored for a request with no session signals at all")
	}
}
