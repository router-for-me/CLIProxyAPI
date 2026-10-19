package config

import "testing"

func TestUsageClientTagHeaderLoadsFromRequestsSection(t *testing.T) {
	data := []byte("requests:\n  usage-client-tag-header: \"X-Client-Tag\"\n")
	if err := ValidateV8Config(data); err != nil {
		t.Fatalf("v8 validation rejected requests.usage-client-tag-header: %v", err)
	}
	cfg, err := ParseConfigBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.UsageClientTagHeader != "X-Client-Tag" {
		t.Fatalf("UsageClientTagHeader = %q, want %q", cfg.UsageClientTagHeader, "X-Client-Tag")
	}
}

func TestUsageClientTagHeaderDefaultsEmpty(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte("requests:\n  passthrough-headers: false\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.UsageClientTagHeader != "" {
		t.Fatalf("UsageClientTagHeader = %q, want empty", cfg.UsageClientTagHeader)
	}
}
