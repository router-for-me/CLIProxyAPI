package config

import "testing"

func TestParseConfigBytesProxyURLs(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte(`
proxy-url: "http://fallback-proxy.example.com:8080"
proxy_urls:
  - " socks5://proxy-a.example.com:1080 "
  - ""
  - "socks5://proxy-b.example.com:1080"
`))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}

	want := []string{
		"socks5://proxy-a.example.com:1080",
		"socks5://proxy-b.example.com:1080",
	}
	if len(cfg.ProxyURLs) != len(want) {
		t.Fatalf("ProxyURLs length = %d, want %d: %#v", len(cfg.ProxyURLs), len(want), cfg.ProxyURLs)
	}
	for i := range want {
		if cfg.ProxyURLs[i] != want[i] {
			t.Fatalf("ProxyURLs[%d] = %q, want %q", i, cfg.ProxyURLs[i], want[i])
		}
	}
	if cfg.ProxyURL != "http://fallback-proxy.example.com:8080" {
		t.Fatalf("ProxyURL = %q, want fallback proxy", cfg.ProxyURL)
	}
}
