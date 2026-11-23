package config

import (
	"reflect"
	"testing"
)

func TestParseConfigBytesTrustedProxies(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte(`trusted-proxies:
  - 192.0.2.0/24
  - 2001:db8::1
`))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}

	want := []string{"192.0.2.0/24", "2001:db8::1"}
	if !reflect.DeepEqual(cfg.TrustedProxies, want) {
		t.Fatalf("TrustedProxies = %v, want %v", cfg.TrustedProxies, want)
	}
}

func TestParseConfigBytesRejectsInvalidTrustedProxy(t *testing.T) {
	if _, errParse := ParseConfigBytes([]byte("trusted-proxies: [not-an-ip]")); errParse == nil {
		t.Fatal("ParseConfigBytes() error = nil, want invalid trusted proxy error")
	}
}
