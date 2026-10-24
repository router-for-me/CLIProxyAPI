package common

import (
	"encoding/base64"
	"strings"
	"testing"
)

// testCodexReplayMetaSignature builds a shape-valid synthetic Meta envelope:
// "Q-PaDgE" pins the observed five-byte header, and 1426 unpadded base64url
// chars decode to 45 + 1024 bytes. No captured signature is embedded.
func testCodexReplayMetaSignature() string {
	return "Q-PaDgE" + strings.Repeat("A", 1419)
}

func testCodexReplayGPTSignature() string {
	decoded := make([]byte, 89)
	decoded[0] = 0x80
	for i := 9; i < len(decoded); i++ {
		decoded[i] = byte(i)
	}
	return base64.RawURLEncoding.EncodeToString(decoded)
}

func TestCodexReplaySignatureForTarget(t *testing.T) {
	metaSig := testCodexReplayMetaSignature()
	routedSig := metaSig + "." + strings.Repeat("Ab3", 23)[:67]
	gptSig := testCodexReplayGPTSignature()

	cases := []struct {
		name  string
		model string
		sig   string
		want  string
		ok    bool
	}{
		{"native meta on muse target", "muse-spark-1.3", metaSig, metaSig, true},
		{"routed meta on muse target", "meta/muse-wonder", routedSig, routedSig, true},
		{"gpt on gpt target", "gpt-5.4", gptSig, gptSig, true},
		{"routed gpt on openrouter gpt target", "openrouter/openai/gpt-5.6-luna-pro", gptSig + "." + strings.Repeat("Ab3", 27)[:79], gptSig + "." + strings.Repeat("Ab3", 27)[:79], true},
		{"gpt cache prefix normalized", "gpt-5.4", "gpt#" + gptSig, gptSig, true},
		{"meta sig on gpt target rejected", "gpt-5.4", metaSig, "", false},
		{"routed meta on gpt target rejected", "gpt-5.4", routedSig, "", false},
		{"gpt sig on meta target rejected", "muse-spark-1.3", gptSig, "", false},
		// An unmapped target defaults to the Codex GPT family.
		{"gpt on unmapped target", "deepseek-v4", gptSig, gptSig, true},
		{"meta on unmapped target rejected", "deepseek-v4", metaSig, "", false},
		{"empty signature", "gpt-5.4", "", "", false},
		{"unknown format", "gpt-5.4", "opaque-blob-xyz", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := CodexReplaySignatureForTarget(tc.model, tc.sig)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("CodexReplaySignatureForTarget(%q, sig) = (%v, %v), want (%v, %v)", tc.model, truncateForTest(got), ok, truncateForTest(tc.want), tc.ok)
			}
		})
	}
}

func truncateForTest(s string) string {
	if len(s) > 24 {
		return s[:24] + "..."
	}
	return s
}
