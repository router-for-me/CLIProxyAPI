package util

import (
	"bytes"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
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
		want, errWant := ojson.DeleteBytes(bytes.Clone(doc), path)
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
	if !ojson.ValidBytes(doc) {
		t.Fatal("fixture is not valid JSON")
	}
	want, errWant := ojson.DeleteBytes(bytes.Clone(doc), path)
	got, errGot := DeleteJSONBytesIfExists(bytes.Clone(doc), path)
	if errWant != nil || errGot != nil || !bytes.Equal(got, want) {
		t.Fatalf("delete result differs from sjson: got %q (%v), want %q (%v)", got, errGot, want, errWant)
	}

	sjsonAllocs := testing.Benchmark(func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_, _ = ojson.DeleteBytes(doc, path)
		}
	}).AllocedBytesPerOp()

	helperAllocs := testing.Benchmark(func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_, _ = DeleteJSONBytesIfExists(doc, path)
		}
	}).AllocedBytesPerOp()

	// GetGJSONBytesNoCopy ensures that checking existence of a 1 MiB value
	// does not copy its Raw string (which ojson.GetBytes would).
	if helperAllocs > sjsonAllocs+1024 {
		t.Fatalf("present large value: helper allocated %d B/op, sjson allocated %d B/op; want difference <= 1024", helperAllocs, sjsonAllocs)
	}
}

func TestDeleteJSONDocIfExists(t *testing.T) {
	if errNil := DeleteJSONDocIfExists(nil, "extra"); errNil != nil {
		t.Fatalf("DeleteJSONDocIfExists(nil) error: %v", errNil)
	}
	emptyDoc := ojson.NewDoc(nil)
	if errSet := emptyDoc.Set("a", 1); errSet != nil {
		t.Fatalf("emptyDoc.Set error: %v", errSet)
	}
	if errMissing := DeleteJSONDocIfExists(emptyDoc, "missing"); errMissing != nil {
		t.Fatalf("DeleteJSONDocIfExists(missing) error: %v", errMissing)
	}
	doc := ojson.NewDoc([]byte(`{"keep":"yes","extra":"remove_me"}`))
	if errDel := DeleteJSONDocIfExists(doc, "extra"); errDel != nil {
		t.Fatalf("DeleteJSONDocIfExists(extra) error: %v", errDel)
	}
	out := doc.Bytes()
	if ojson.GetBytesNoCopy(out, "extra").Exists() {
		t.Fatalf("expected extra to be deleted in %s", string(out))
	}
	if got := ojson.GetBytesNoCopy(out, "keep").String(); got != "yes" {
		t.Fatalf("keep = %q, want yes", got)
	}
}
