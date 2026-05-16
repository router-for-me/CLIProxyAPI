package main

import "testing"

func TestParseHomeFlagConfigHostPort(t *testing.T) {
	cfg, err := parseHomeFlagConfig("home.example.com:8327", "secret")
	if err != nil {
		t.Fatalf("parseHomeFlagConfig() error = %v", err)
	}

	if !cfg.Enabled {
		t.Fatal("Enabled = false, want true")
	}
	if cfg.Host != "home.example.com" {
		t.Fatalf("Host = %q, want home.example.com", cfg.Host)
	}
	if cfg.Port != 8327 {
		t.Fatalf("Port = %d, want 8327", cfg.Port)
	}
	if cfg.Password != "secret" {
		t.Fatalf("Password = %q, want secret", cfg.Password)
	}
	if cfg.TLS.Enable {
		t.Fatal("TLS.Enable = true, want false")
	}
}

func TestParseHomeFlagConfigRediss(t *testing.T) {
	cfg, err := parseHomeFlagConfig("rediss://:url-secret@home.example.com:444?server-name=home.example.com&skip_verify=true&ca-cert=C%3A%2Fcerts%2Fca.pem", "")
	if err != nil {
		t.Fatalf("parseHomeFlagConfig() error = %v", err)
	}

	if cfg.Host != "home.example.com" {
		t.Fatalf("Host = %q, want home.example.com", cfg.Host)
	}
	if cfg.Port != 444 {
		t.Fatalf("Port = %d, want 444", cfg.Port)
	}
	if cfg.Password != "url-secret" {
		t.Fatalf("Password = %q, want url-secret", cfg.Password)
	}
	if !cfg.TLS.Enable {
		t.Fatal("TLS.Enable = false, want true")
	}
	if cfg.TLS.ServerName != "home.example.com" {
		t.Fatalf("TLS.ServerName = %q, want home.example.com", cfg.TLS.ServerName)
	}
	if !cfg.TLS.InsecureSkipVerify {
		t.Fatal("TLS.InsecureSkipVerify = false, want true")
	}
	if cfg.TLS.CACert != "C:/certs/ca.pem" {
		t.Fatalf("TLS.CACert = %q, want C:/certs/ca.pem", cfg.TLS.CACert)
	}
}

func TestParseHomeFlagConfigPasswordFlagOverridesURLPassword(t *testing.T) {
	cfg, err := parseHomeFlagConfig("rediss://:url-secret@home.example.com:444", "flag-secret")
	if err != nil {
		t.Fatalf("parseHomeFlagConfig() error = %v", err)
	}

	if cfg.Password != "flag-secret" {
		t.Fatalf("Password = %q, want flag-secret", cfg.Password)
	}
}

func TestParseHomeFlagConfigDisableClusterDiscovery(t *testing.T) {
	cfg, err := parseHomeFlagConfig("redis://home.example.com:8327?disable-cluster-discovery=true", "")
	if err != nil {
		t.Fatalf("parseHomeFlagConfig() error = %v", err)
	}

	if !cfg.DisableClusterDiscovery {
		t.Fatal("DisableClusterDiscovery = false, want true")
	}
}
