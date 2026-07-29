package config

import "testing"

func TestCodexBucketForAPIKey(t *testing.T) {
	cfg := &SDKConfig{CodexBuckets: map[string]CodexBucket{
		"team-a": {APIKeys: []string{"sk-a1", "sk-a2"}},
		"team-b": {APIKeys: []string{"sk-b1"}},
	}}
	if got := cfg.CodexBucketForAPIKey("sk-a2"); got != "team-a" {
		t.Fatalf("CodexBucketForAPIKey(sk-a2) = %q, want team-a", got)
	}
	if got := cfg.CodexBucketForAPIKey("sk-b1"); got != "team-b" {
		t.Fatalf("CodexBucketForAPIKey(sk-b1) = %q, want team-b", got)
	}
	if got := cfg.CodexBucketForAPIKey("sk-unmapped"); got != "" {
		t.Fatalf("CodexBucketForAPIKey(sk-unmapped) = %q, want empty", got)
	}
	if got := cfg.CodexBucketForAPIKey(""); got != "" {
		t.Fatalf("CodexBucketForAPIKey(empty) = %q, want empty", got)
	}
	var nilCfg *SDKConfig
	if got := nilCfg.CodexBucketForAPIKey("sk-a1"); got != "" {
		t.Fatalf("nil receiver = %q, want empty", got)
	}
}

func TestCodexBucketForAPIKeyTrimsConfiguredKey(t *testing.T) {
	cfg := &SDKConfig{CodexBuckets: map[string]CodexBucket{
		"team-a": {APIKeys: []string{" sk-a1 "}},
	}}
	if got := cfg.CodexBucketForAPIKey("sk-a1"); got != "team-a" {
		t.Fatalf("CodexBucketForAPIKey(sk-a1) = %q, want team-a (configured key should be trimmed)", got)
	}
	// The caller's key is compared as-is; a caller-supplied key with
	// surrounding whitespace should not match a trimmed configured key.
	if got := cfg.CodexBucketForAPIKey(" sk-a1 "); got != "" {
		t.Fatalf("CodexBucketForAPIKey(\" sk-a1 \") = %q, want empty (caller key is not trimmed)", got)
	}
}

func TestCodexBucketForContextValue(t *testing.T) {
	cfg := &SDKConfig{CodexBuckets: map[string]CodexBucket{
		"team-a": {APIKeys: []string{"sk-a1"}},
	}}
	if got := cfg.CodexBucketForContextValue("sk-a1"); got != "team-a" {
		t.Fatalf("CodexBucketForContextValue(sk-a1) = %q, want team-a", got)
	}
	if got := cfg.CodexBucketForContextValue(nil); got != "" {
		t.Fatalf("CodexBucketForContextValue(nil) = %q, want empty", got)
	}
	if got := cfg.CodexBucketForContextValue("sk-unmapped"); got != "" {
		t.Fatalf("CodexBucketForContextValue(sk-unmapped) = %q, want empty", got)
	}
	var nilCfg *SDKConfig
	if got := nilCfg.CodexBucketForContextValue("sk-a1"); got != "" {
		t.Fatalf("nil receiver = %q, want empty", got)
	}
}

func TestValidateCodexBucketsDuplicateKey(t *testing.T) {
	cfg := &SDKConfig{CodexBuckets: map[string]CodexBucket{
		"team-a": {APIKeys: []string{"sk-dup"}},
		"team-b": {APIKeys: []string{"sk-dup"}},
	}}
	if err := cfg.ValidateCodexBuckets(); err == nil {
		t.Fatal("expected error for api key mapped to two buckets")
	}
}

func TestValidateCodexBucketsOK(t *testing.T) {
	cfg := &SDKConfig{CodexBuckets: map[string]CodexBucket{
		// Same key twice inside one bucket is tolerated; empty entries ignored.
		"team-a": {APIKeys: []string{"sk-a1", "sk-a1", "  "}},
		"team-b": {APIKeys: []string{"sk-b1"}},
	}}
	if err := cfg.ValidateCodexBuckets(); err != nil {
		t.Fatalf("ValidateCodexBuckets: %v", err)
	}
	if err := (&SDKConfig{}).ValidateCodexBuckets(); err != nil {
		t.Fatalf("empty config: %v", err)
	}
}

func TestValidateCodexBucketsRejectsEmptyBucketName(t *testing.T) {
	cfg := &SDKConfig{CodexBuckets: map[string]CodexBucket{
		"":   {APIKeys: []string{"sk-a1"}},
		"ok": {APIKeys: []string{"sk-b1"}},
	}}
	if err := cfg.ValidateCodexBuckets(); err == nil {
		t.Fatal("expected error for empty bucket name")
	}
}

func TestValidateCodexBucketsRejectsWhitespaceBucketName(t *testing.T) {
	cfg := &SDKConfig{CodexBuckets: map[string]CodexBucket{
		"   ": {APIKeys: []string{"sk-a1"}},
	}}
	if err := cfg.ValidateCodexBuckets(); err == nil {
		t.Fatal("expected error for whitespace-only bucket name")
	}
}
