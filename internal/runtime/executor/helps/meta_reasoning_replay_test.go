package helps

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/signature"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestMetaReasoningAccountKey(t *testing.T) {
	a := MetaReasoningAccountKey(&cliproxyauth.Auth{ID: "meta-a"})
	if a == "" || a != MetaReasoningAccountKey(&cliproxyauth.Auth{ID: "meta-a", Attributes: map[string]string{"api_key": "rotated"}}) {
		t.Fatalf("account key must depend on identity only and survive token rotation, got %q", a)
	}
	if a == MetaReasoningAccountKey(&cliproxyauth.Auth{ID: "meta-b"}) {
		t.Fatal("distinct accounts share a key")
	}
	if _, _, ok := signature.ParseMetaReasoningTag(signature.TagMetaReasoning(a, "x")); !ok {
		t.Fatalf("account key %q is not a valid tag key", a)
	}
	if got := MetaReasoningAccountKey(nil); got != "" {
		t.Fatalf("nil auth produced key %q", got)
	}
	if got := MetaReasoningAccountKey(&cliproxyauth.Auth{}); got != "" {
		t.Fatalf("anonymous auth produced key %q", got)
	}
	if got := MetaReasoningAccountKey(&cliproxyauth.Auth{Attributes: map[string]string{"api_key": "k"}}); got != "" {
		t.Fatalf("a key must never be derived from the API key, got %q", got)
	}
}
