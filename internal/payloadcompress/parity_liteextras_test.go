package payloadcompress

import "testing"

// Lite must never rewrite content inside a single message: upstream only drops
// wholly duplicated messages, and image_url replacement is restricted to array
// parts of non-vision models.
func TestLitePreservesRepeatedLinesAndImageText(t *testing.T) {
	cases := []string{
		"Keep both rows:\nid=1\nid=1",
		"```\n<img src=\"data:image/png;base64,AAAA\">\n```",
	}
	for _, in := range cases {
		got, changed := liteEngine{}.compressKind(kindUser, in)
		if changed || got != in {
			t.Fatalf("compressKind(%q) = %q, changed=%v; want unchanged", in, got, changed)
		}
	}
}
