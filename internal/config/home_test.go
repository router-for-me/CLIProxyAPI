package config

import "testing"

func TestParseConfigBytesHomeTLS(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte(`
home:
  enabled: true
  host: home.example.com
  port: 444
  password: secret
  tls:
    enable: true
    server-name: home.example.com
    ca-cert: C:/certs/ca.pem
    insecure-skip-verify: true
`))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}

	if !cfg.Home.Enabled {
		t.Fatal("Home.Enabled = false, want true")
	}
	if cfg.Home.Host != "home.example.com" {
		t.Fatalf("Home.Host = %q, want home.example.com", cfg.Home.Host)
	}
	if cfg.Home.Port != 444 {
		t.Fatalf("Home.Port = %d, want 444", cfg.Home.Port)
	}
	if cfg.Home.Password != "secret" {
		t.Fatalf("Home.Password = %q, want secret", cfg.Home.Password)
	}
	if !cfg.Home.TLS.Enable {
		t.Fatal("Home.TLS.Enable = false, want true")
	}
	if cfg.Home.TLS.ServerName != "home.example.com" {
		t.Fatalf("Home.TLS.ServerName = %q, want home.example.com", cfg.Home.TLS.ServerName)
	}
	if cfg.Home.TLS.CACert != "C:/certs/ca.pem" {
		t.Fatalf("Home.TLS.CACert = %q, want C:/certs/ca.pem", cfg.Home.TLS.CACert)
	}
	if !cfg.Home.TLS.InsecureSkipVerify {
		t.Fatal("Home.TLS.InsecureSkipVerify = false, want true")
	}
}
