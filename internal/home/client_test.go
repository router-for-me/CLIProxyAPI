package home

import (
	"crypto/tls"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestAuthDispatchRequestIncludesCount(t *testing.T) {
	req := newAuthDispatchRequest("gpt-5.4", "session-1", http.Header{"Authorization": {"Bearer test"}}, 2)

	raw, err := json.Marshal(&req)
	if err != nil {
		t.Fatalf("marshal auth dispatch request: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("unmarshal auth dispatch request: %v", err)
	}
	if got := int(payload["count"].(float64)); got != 2 {
		t.Fatalf("count = %d, want 2", got)
	}
}

func TestAuthDispatchRequestDefaultsCountToOne(t *testing.T) {
	req := newAuthDispatchRequest("gpt-5.4", "", nil, 0)

	if req.Count != 1 {
		t.Fatalf("count = %d, want 1", req.Count)
	}
}

func TestRedisOptionsHomeTLSDisabled(t *testing.T) {
	client := New(config.HomeConfig{
		Enabled:  true,
		Host:     "127.0.0.1",
		Port:     6379,
		Password: "secret",
	})

	client.mu.Lock()
	options, err := client.redisOptionsLocked("127.0.0.1:6379")
	client.mu.Unlock()
	if err != nil {
		t.Fatalf("redisOptionsLocked() error = %v", err)
	}

	if options.TLSConfig != nil {
		t.Fatalf("TLSConfig = %#v, want nil", options.TLSConfig)
	}
	if options.Password != "secret" {
		t.Fatalf("Password = %q, want secret", options.Password)
	}
}

func TestRedisOptionsHomeTLSEnabledUsesSeedHostAsServerName(t *testing.T) {
	client := New(config.HomeConfig{
		Enabled: true,
		Host:    "home.example.com",
		Port:    444,
		TLS: config.HomeTLSConfig{
			Enable: true,
		},
	})
	client.homeCfg.Host = "127.0.0.1"

	client.mu.Lock()
	options, err := client.redisOptionsLocked("127.0.0.1:444")
	client.mu.Unlock()
	if err != nil {
		t.Fatalf("redisOptionsLocked() error = %v", err)
	}

	if options.TLSConfig == nil {
		t.Fatal("TLSConfig is nil")
	}
	if options.TLSConfig.ServerName != "home.example.com" {
		t.Fatalf("ServerName = %q, want home.example.com", options.TLSConfig.ServerName)
	}
	if options.TLSConfig.MinVersion != tls.VersionTLS12 {
		t.Fatalf("MinVersion = %d, want TLS 1.2", options.TLSConfig.MinVersion)
	}
}

func TestRedisOptionsHomeTLSEnabledUsesExplicitServerName(t *testing.T) {
	client := New(config.HomeConfig{
		Enabled: true,
		Host:    "127.0.0.1",
		Port:    444,
		TLS: config.HomeTLSConfig{
			Enable:             true,
			ServerName:         "home.example.com",
			InsecureSkipVerify: true,
		},
	})

	client.mu.Lock()
	options, err := client.redisOptionsLocked("127.0.0.1:444")
	client.mu.Unlock()
	if err != nil {
		t.Fatalf("redisOptionsLocked() error = %v", err)
	}

	if options.TLSConfig == nil {
		t.Fatal("TLSConfig is nil")
	}
	if options.TLSConfig.ServerName != "home.example.com" {
		t.Fatalf("ServerName = %q, want home.example.com", options.TLSConfig.ServerName)
	}
	if !options.TLSConfig.InsecureSkipVerify {
		t.Fatal("InsecureSkipVerify = false, want true")
	}
}
