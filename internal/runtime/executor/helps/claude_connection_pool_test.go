package helps

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func claudeOAuthAuth(id, refreshToken string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{ID: id, Provider: "claude", Metadata: map[string]any{"refresh_token": refreshToken}}
}

func claudePoolConfig(pool *config.ProxyConnectionPoolConfig) *config.Config {
	return &config.Config{Claude: config.ClaudeConfig{ConnectionPool: pool}}
}

// TestClaudeProviderConnectionPool pins claude.connection-pool: OAuth credentials
// get a credential-scoped pool with the 90s default, while an absent block,
// enabled: false, or a zero bound keeps the shared short-lived transport.
func TestClaudeProviderConnectionPool(t *testing.T) {
	enabled, disabled, zero, four := true, false, 0, 4
	tests := []struct {
		name     string
		pool     *config.ProxyConnectionPoolConfig
		wantPool bool
		wantIdle time.Duration
		wantMax  int
	}{
		{name: "no block", pool: nil},
		{name: "empty block opts in with defaults", pool: &config.ProxyConnectionPoolConfig{}, wantPool: true, wantIdle: 90 * time.Second, wantMax: 2},
		{name: "explicit settings", pool: &config.ProxyConnectionPoolConfig{Enabled: &enabled, IdleConnTimeout: "45s", MaxIdleConnsPerHost: &four}, wantPool: true, wantIdle: 45 * time.Second, wantMax: 4},
		{name: "invalid idle falls back to 90s", pool: &config.ProxyConnectionPoolConfig{IdleConnTimeout: "soon"}, wantPool: true, wantIdle: 90 * time.Second, wantMax: 2},
		{name: "enabled false", pool: &config.ProxyConnectionPoolConfig{Enabled: &disabled}},
		{name: "zero idle", pool: &config.ProxyConnectionPoolConfig{IdleConnTimeout: "0s"}},
		{name: "zero max idle", pool: &config.ProxyConnectionPoolConfig{MaxIdleConnsPerHost: &zero}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ResetProxyTransports()
			t.Cleanup(ResetProxyTransports)
			cfg := claudePoolConfig(tt.pool)
			auth := claudeOAuthAuth("claude-oauth", "refresh-a")

			if got := ClaudeUpstreamConnectionReuse(cfg, auth); got != tt.wantPool {
				t.Fatalf("ClaudeUpstreamConnectionReuse() = %t, want %t", got, tt.wantPool)
			}
			tr := pooledClaudeCodeTransport("direct", cfg, auth)
			if !tt.wantPool {
				if tr != nil {
					t.Fatalf("pooledClaudeCodeTransport() = %p, want nil (shared short-lived transport)", tr)
				}
				return
			}
			if tr == nil {
				t.Fatal("pooledClaudeCodeTransport() = nil, want a pooled transport")
			}
			if tr.DisableKeepAlives {
				t.Fatal("pooled transport disabled keep-alives")
			}
			if tr.IdleConnTimeout != tt.wantIdle || tr.MaxIdleConnsPerHost != tt.wantMax {
				t.Fatalf("idle=%s max=%d, want idle=%s max=%d", tr.IdleConnTimeout, tr.MaxIdleConnsPerHost, tt.wantIdle, tt.wantMax)
			}
		})
	}
}

// TestClaudeProviderConnectionPoolIsPerCredential ensures the provider switch never
// lets two OAuth accounts share a connection, and that a rotated refresh token
// switches pools.
func TestClaudeProviderConnectionPoolIsPerCredential(t *testing.T) {
	ResetProxyTransports()
	t.Cleanup(ResetProxyTransports)
	cfg := claudePoolConfig(&config.ProxyConnectionPoolConfig{})

	first := pooledClaudeCodeTransport("direct", cfg, claudeOAuthAuth("account-a", "refresh-a"))
	again := pooledClaudeCodeTransport("direct", cfg, claudeOAuthAuth("account-a", "refresh-a"))
	other := pooledClaudeCodeTransport("direct", cfg, claudeOAuthAuth("account-b", "refresh-b"))
	rotated := pooledClaudeCodeTransport("direct", cfg, claudeOAuthAuth("account-a", "refresh-a2"))
	if first == nil || first != again {
		t.Fatalf("same credential got different transports: %p vs %p", first, again)
	}
	if other == first || rotated == first {
		t.Fatal("distinct or rotated credentials share a pooled transport")
	}
}

// TestClaudeProviderConnectionPoolScope keeps the switch to Claude credentials and
// lets a credential's own connection-pool block win over it.
func TestClaudeProviderConnectionPoolScope(t *testing.T) {
	ResetProxyTransports()
	t.Cleanup(ResetProxyTransports)
	cfg := claudePoolConfig(&config.ProxyConnectionPoolConfig{IdleConnTimeout: "45s"})

	codex := &cliproxyauth.Auth{ID: "codex-oauth", Provider: "codex"}
	if ClaudeUpstreamConnectionReuse(cfg, codex) || pooledClaudeCodeTransport("direct", cfg, codex) != nil {
		t.Fatal("claude.connection-pool pooled a non-Claude credential")
	}

	// A key's own block keeps the generic 300s default and its current behavior:
	// streams are not drained for reuse.
	key := &cliproxyauth.Auth{ID: "claude-key", Provider: "claude", ProxyConnectionPool: poolBlock()}
	tr := pooledClaudeCodeTransport("direct", cfg, key)
	if tr == nil || tr.IdleConnTimeout != proxyPoolDefaultIdleConnTimeout {
		t.Fatalf("own block transport = %+v, want idle %s", tr, proxyPoolDefaultIdleConnTimeout)
	}
	if ClaudeUpstreamConnectionReuse(cfg, key) {
		t.Fatal("claude.connection-pool enabled stream draining for a key with its own block")
	}

	// A key that opted out of pooling stays out, even with the provider switch on.
	disabled := false
	optOut := &cliproxyauth.Auth{ID: "claude-key-off", Provider: "claude", ProxyConnectionPool: &cliproxyauth.ProxyConnectionPoolOverride{Enabled: &disabled}}
	if ClaudeUpstreamConnectionReuse(cfg, optOut) || pooledClaudeCodeTransport("direct", cfg, optOut) != nil {
		t.Fatal("claude.connection-pool overrode a key's enabled: false")
	}

	// The standard transport kind ignores the Claude switch.
	if tr := pooledProxyTransport("direct", cfg, claudeOAuthAuth("claude-oauth", "refresh-a")); tr == nil || tr.MaxIdleConnsPerHost != 0 {
		t.Fatalf("standard transport for OAuth credential was pooled: %+v", tr)
	}
}
