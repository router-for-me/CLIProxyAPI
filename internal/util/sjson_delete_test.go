package util

import (
	"bytes"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestDeleteJSONBytesIfExistsMatchesSJSON(t *testing.T) {
	doc := []byte(`{"request":{"contents":[{"text":"hi"}],"generationConfig":{"thinkingConfig":{"thinkingBudget":8,"include_thoughts":true}}},"a.b":1}`)
	for _, path := range []string{
		"request.generationConfig.thinkingConfig.thinkingBudget",
		"request.generationConfig.thinkingConfig.include_thoughts",
		"request.generationConfig.thinkingConfig",
		"request.generationConfig.thinkingConfig.thinking_level",
		"request.generationConfig.missing.leaf",
		"missing",
		`a\.b`,
	} {
		want, errWant := sjson.DeleteBytes(bytes.Clone(doc), path)
		got, errGot := DeleteJSONBytesIfExists(bytes.Clone(doc), path)
		if (errWant != nil) != (errGot != nil) || !bytes.Equal(got, want) {
			t.Fatalf("path %q: got %s (%v), want %s (%v)", path, got, errGot, want, errWant)
		}
	}
}

func TestDeleteJSONBytesIfExistsMissingPathReturnsInput(t *testing.T) {
	// A large prefix before the parent object is what sjson copies needlessly.
	doc := []byte(`{"request":{"contents":"` + strings.Repeat("x", 1<<20) + `","generationConfig":{"thinkingConfig":{}}}}`)
	const path = "request.generationConfig.thinkingConfig.include_thoughts"
	got, errDelete := DeleteJSONBytesIfExists(doc, path)
	if errDelete != nil || &got[0] != &doc[0] || len(got) != len(doc) {
		t.Fatalf("missing path must return the input slice unchanged")
	}
	allocated := testing.Benchmark(func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_, _ = DeleteJSONBytesIfExists(doc, path)
		}
	}).AllocedBytesPerOp()
	if allocated > 1024 {
		t.Fatalf("missing-path delete allocated %d bytes per call, want no document copy", allocated)
	}
}

func TestDeleteJSONBytesIfExistsPresentLargeValueAllocation(t *testing.T) {
	doc := []byte(`{"request":{"contents":"hi","generationConfig":{"thinkingConfig":{"includeThoughts":"` + strings.Repeat("x", 1<<20) + `"}}}}`)
	const path = "request.generationConfig.thinkingConfig.includeThoughts"
	if !gjson.ValidBytes(doc) {
		t.Fatal("fixture is not valid JSON")
	}
	want, errWant := sjson.DeleteBytes(bytes.Clone(doc), path)
	got, errGot := DeleteJSONBytesIfExists(bytes.Clone(doc), path)
	if errWant != nil || errGot != nil || !bytes.Equal(got, want) {
		t.Fatalf("delete result differs from sjson: got %q (%v), want %q (%v)", got, errGot, want, errWant)
	}

	sjsonAllocs := testing.Benchmark(func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_, _ = sjson.DeleteBytes(doc, path)
		}
	}).AllocedBytesPerOp()

	helperAllocs := testing.Benchmark(func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_, _ = DeleteJSONBytesIfExists(doc, path)
		}
	}).AllocedBytesPerOp()

	// GetGJSONBytesNoCopy ensures that checking existence of a 1 MiB value
	// does not copy its Raw string (which gjson.GetBytes would).
	if helperAllocs > sjsonAllocs+1024 {
		t.Fatalf("present large value: helper allocated %d B/op, sjson allocated %d B/op; want difference <= 1024", helperAllocs, sjsonAllocs)
	}
}
