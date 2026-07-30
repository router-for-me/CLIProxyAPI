package auth

import "testing"

func TestAuthBucket(t *testing.T) {
	if got := authBucket(nil); got != "" {
		t.Fatalf("nil auth = %q, want empty", got)
	}
	if got := authBucket(&Auth{}); got != "" {
		t.Fatalf("no bucket = %q, want empty", got)
	}
	attr := &Auth{Attributes: map[string]string{AttributeBucket: " team-a "}}
	if got := authBucket(attr); got != "team-a" {
		t.Fatalf("attribute bucket = %q, want team-a", got)
	}
	meta := &Auth{Metadata: map[string]any{AttributeBucket: "team-b"}}
	if got := authBucket(meta); got != "team-b" {
		t.Fatalf("metadata bucket = %q, want team-b", got)
	}
	// Empty attribute falls through to metadata; non-string metadata is ignored.
	fallthroughAuth := &Auth{
		Attributes: map[string]string{AttributeBucket: "  "},
		Metadata:   map[string]any{AttributeBucket: "team-c"},
	}
	if got := authBucket(fallthroughAuth); got != "team-c" {
		t.Fatalf("fallthrough bucket = %q, want team-c", got)
	}
	nonString := &Auth{Metadata: map[string]any{AttributeBucket: 42}}
	if got := authBucket(nonString); got != "" {
		t.Fatalf("non-string metadata = %q, want empty", got)
	}
}

func TestEligibilityCodexBucket(t *testing.T) {
	codexIn := &Auth{Provider: "codex", Metadata: map[string]any{AttributeBucket: "team-a"}}
	codexOther := &Auth{Provider: "codex", Metadata: map[string]any{AttributeBucket: "team-b"}}
	codexDefault := &Auth{Provider: "codex"}
	gemini := &Auth{Provider: "gemini"}

	bucketed := authSelectionEligibility{codexBucket: "team-a"}
	if !bucketed.allows(codexIn) {
		t.Fatal("bucketed request must allow same-bucket codex auth")
	}
	if bucketed.allows(codexOther) {
		t.Fatal("bucketed request must reject other-bucket codex auth")
	}
	if bucketed.allows(codexDefault) {
		t.Fatal("bucketed request must reject unbucketed codex auth")
	}
	if !bucketed.allows(gemini) {
		t.Fatal("bucket filter must not affect non-codex providers")
	}

	unmapped := authSelectionEligibility{}
	if !unmapped.allows(codexDefault) {
		t.Fatal("unmapped request must allow unbucketed codex auth")
	}
	if unmapped.allows(codexIn) {
		t.Fatal("unmapped request must reject bucketed codex auth")
	}
	if !unmapped.allows(gemini) {
		t.Fatal("unmapped request must allow non-codex providers")
	}
}

func TestCodexBucketFromMetadata(t *testing.T) {
	if got := codexBucketFromMetadata(nil); got != "" {
		t.Fatalf("nil meta = %q, want empty", got)
	}
	meta := map[string]any{"codex_bucket": " team-a "}
	if got := codexBucketFromMetadata(meta); got != "team-a" {
		t.Fatalf("meta bucket = %q, want team-a", got)
	}
	if got := codexBucketFromMetadata(map[string]any{"codex_bucket": 7}); got != "" {
		t.Fatalf("non-string = %q, want empty", got)
	}
}
