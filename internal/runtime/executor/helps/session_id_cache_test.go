package helps

import "testing"

// Same (apiKey, proxyURL) must return a stable session ID within the TTL.
func TestCachedSessionID_StableForSameKeyAndProxy(t *testing.T) {
	a := CachedSessionID("user-key-1", "socks5://proxy-A:1080")
	b := CachedSessionID("user-key-1", "socks5://proxy-A:1080")
	if a == "" || a != b {
		t.Fatalf("expected stable session ID, got a=%q b=%q", a, b)
	}
}

// Same apiKey but different proxy URLs must yield different session IDs so
// Anthropic never sees the same Claude Code session ID arriving from two
// different egress IPs.
func TestCachedSessionID_RotatesOnProxyChange(t *testing.T) {
	a := CachedSessionID("user-key-2", "socks5://proxy-A:1080")
	b := CachedSessionID("user-key-2", "socks5://proxy-B:1080")
	if a == "" || b == "" || a == b {
		t.Fatalf("expected different session IDs for different proxies; got a=%q b=%q", a, b)
	}
}

// The empty-proxy variant must be stable and distinct from any proxied variant.
func TestCachedSessionID_EmptyProxyIsIsolated(t *testing.T) {
	none1 := CachedSessionID("user-key-3", "")
	none2 := CachedSessionID("user-key-3", "")
	proxied := CachedSessionID("user-key-3", "http://proxy:8080")
	if none1 == "" || none1 != none2 {
		t.Fatalf("empty-proxy variant should be stable, got %q vs %q", none1, none2)
	}
	if none1 == proxied {
		t.Fatalf("empty-proxy variant must differ from proxied variant")
	}
}

// Empty apiKey returns a fresh random ID every call (no caching).
func TestCachedSessionID_EmptyAPIKeyAlwaysFresh(t *testing.T) {
	a := CachedSessionID("", "http://proxy:8080")
	b := CachedSessionID("", "http://proxy:8080")
	if a == "" || b == "" || a == b {
		t.Fatalf("expected fresh IDs for empty apiKey, got a=%q b=%q", a, b)
	}
}
