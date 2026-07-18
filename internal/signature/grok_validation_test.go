package signature

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
)

func TestInspectGrokEncryptedContent_NativeSamples(t *testing.T) {
	path, ok := grokEncryptedContentSamplesPath()
	if !ok {
		t.Skip("grok encrypted_content corpus missing; run docs/native-prompt-capture/scripts/harvest-grok-encrypted-content.sh")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read samples: %v", err)
	}
	var samples []string
	if err := json.Unmarshal(raw, &samples); err != nil {
		t.Fatalf("unmarshal samples: %v", err)
	}
	if len(samples) == 0 {
		t.Fatal("expected native Grok encrypted_content samples")
	}
	for i, sample := range samples {
		if _, err := InspectGrokEncryptedContent(sample); err != nil {
			t.Fatalf("sample[%d] should be valid, got %v", i, err)
		}
	}
}

func TestInspectGrokEncryptedContent_RejectsAgyGeminiThoughtSignatures(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(file), "testdata", "agy_gemini_thought_signature_entries.json")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Skip("agy gemini corpus missing; run harvest_agy_gemini_signatures.py")
	} else if err != nil {
		t.Fatalf("stat samples: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read samples: %v", err)
	}
	var entries []struct {
		ThoughtSignature string `json:"thoughtSignature"`
	}
	if err := json.Unmarshal(raw, &entries); err != nil {
		t.Fatalf("unmarshal samples: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("expected agy Gemini thought signatures")
	}
	checkedUnpaddedGemini := false
	for i, entry := range entries {
		_, err := InspectGrokEncryptedContent(entry.ThoughtSignature)
		if err == nil {
			t.Fatalf("entry[%d] should not pass as Grok encrypted_content", i)
		}
		if !strings.Contains(entry.ThoughtSignature, "=") {
			checkedUnpaddedGemini = true
			if !strings.Contains(err.Error(), "Gemini") {
				t.Fatalf("entry[%d] error = %q, want Gemini fast-reject detail", i, err.Error())
			}
		}
	}
	if !checkedUnpaddedGemini {
		t.Fatal("expected at least one unpadded Gemini thought signature sample")
	}
}

func TestInspectGrokEncryptedContent_RejectsGeminiThoughtSignatureEnvelope(t *testing.T) {
	sample := testGeminiThoughtSignatureEnvelope()

	_, err := InspectGrokEncryptedContent(sample)
	if err == nil {
		t.Fatal("expected Gemini thoughtSignature envelope to be rejected")
	}
	if !strings.Contains(err.Error(), "Gemini") {
		t.Fatalf("error = %q, want Gemini fast-reject detail", err.Error())
	}
}

func TestInspectGrokEncryptedContent_RejectsGemini25Field1Envelope(t *testing.T) {
	sample := testGemini25Field1ThoughtSignatureEnvelope()
	if !IsValidGeminiThoughtSignature(sample, GeminiThoughtSignatureValidationOptions{RequireKnownEnvelope: true}) {
		t.Fatal("fixture should be a known Gemini field-1 thoughtSignature")
	}

	_, err := InspectGrokEncryptedContent(sample)
	if err == nil {
		t.Fatal("expected Gemini field-1 thoughtSignature envelope to be rejected")
	}
	if !strings.Contains(err.Error(), "Gemini") {
		t.Fatalf("error = %q, want Gemini fast-reject detail", err.Error())
	}
}

func TestInspectGrokEncryptedContent_RejectsClaudeThinkingSignature(t *testing.T) {
	sample := testUnpaddedClaudeThinkingSignature()
	if !IsValidClaudeThinkingSignature(sample, ClaudeSignatureValidationOptions{Strict: true}) {
		t.Fatal("fixture should be a strict Claude thinking signature")
	}

	_, err := InspectGrokEncryptedContent(sample)
	if err == nil {
		t.Fatal("expected Claude thinking signature to be rejected")
	}
	if !strings.Contains(err.Error(), "Claude") {
		t.Fatalf("error = %q, want Claude fast-reject detail", err.Error())
	}
}

func TestInspectGrokEncryptedContent_RejectsAntigravityClaudeThinkingSignature(t *testing.T) {
	sample := testUnpaddedAntigravityClaudeThinkingSignature()
	if !strings.HasPrefix(sample, "R") || strings.Contains(sample, "=") {
		t.Fatalf("fixture should be an unpadded R-form Claude signature, got prefix=%q has_padding=%t", sample[:1], strings.Contains(sample, "="))
	}
	if !IsValidClaudeThinkingSignature(sample, ClaudeSignatureValidationOptions{Strict: true}) {
		t.Fatal("fixture should be a strict Antigravity Claude thinking signature")
	}

	_, err := InspectGrokEncryptedContent(sample)
	if err == nil {
		t.Fatal("expected Antigravity Claude thinking signature to be rejected")
	}
	if !strings.Contains(err.Error(), "Claude") {
		t.Fatalf("error = %q, want Claude fast-reject detail", err.Error())
	}
}

func TestInspectGrokEncryptedContent_RejectsForeignShapes(t *testing.T) {
	cases := []string{
		"",
		"bad",
		" opaque",
		"gAAAAABinvalid-gpt-shape",
		"abcd_efg",
		base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0xa5}, MinGrokEncryptedContentDecodedLen)),
	}
	for _, sample := range cases {
		if _, err := InspectGrokEncryptedContent(sample); err == nil {
			t.Fatalf("expected invalid Grok encrypted_content, got pass for %q", sample)
		}
	}
}

func TestInspectGrokEncryptedContent_RejectsLowEntropyPayload(t *testing.T) {
	sample := base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{0xa5}, MinGrokEncryptedContentDecodedLen))

	_, err := InspectGrokEncryptedContent(sample)
	if err == nil {
		t.Fatal("expected low-entropy payload to be rejected")
	}
	if !strings.Contains(err.Error(), "entropy ratio") {
		t.Fatalf("error = %q, want entropy ratio detail", err.Error())
	}
}

func TestInspectGrokEncryptedContent_RejectsInvalidBase64Length(t *testing.T) {
	_, err := InspectGrokEncryptedContent("AAAAA")
	if err == nil {
		t.Fatal("expected invalid base64 length to be rejected")
	}
	if !strings.Contains(err.Error(), "base64 decode failed") {
		t.Fatalf("error = %q, want base64 decode detail", err.Error())
	}
}

func TestByteEntropyRatio_SingleByteReturnsZero(t *testing.T) {
	if got := byteEntropyRatio([]byte{0xa5}); got != 0 {
		t.Fatalf("byteEntropyRatio(single byte) = %v, want 0", got)
	}
}

func testGeminiThoughtSignatureEnvelope() string {
	payload := []byte{0x01, 0x0c}
	for i := 0; i < 97; i++ {
		payload = append(payload, byte(i))
	}
	inner := []byte{0x0a, byte(len(payload))}
	inner = append(inner, payload...)
	outer := []byte{0x12, byte(len(inner))}
	outer = append(outer, inner...)
	return base64.RawStdEncoding.EncodeToString(outer)
}

func testGemini25Field1ThoughtSignatureEnvelope() string {
	payload := []byte{0x01}
	for i := 0; len(payload) < 128; i++ {
		payload = append(payload, byte((i*37+11)%251))
	}

	var decoded []byte
	decoded = protowire.AppendTag(decoded, 1, protowire.BytesType)
	decoded = protowire.AppendBytes(decoded, payload)
	return base64.RawStdEncoding.EncodeToString(decoded)
}

func testUnpaddedClaudeThinkingSignature() string {
	return testClaudeThinkingSignatureWithOpaqueLen(35)
}

func testUnpaddedAntigravityClaudeThinkingSignature() string {
	return base64.StdEncoding.EncodeToString([]byte(testClaudeThinkingSignatureWithOpaqueLen(41)))
}

func testClaudeThinkingSignatureWithOpaqueLen(opaqueLen int) string {
	var channelBlock []byte
	channelBlock = protowire.AppendTag(channelBlock, 1, protowire.VarintType)
	channelBlock = protowire.AppendVarint(channelBlock, 12)
	channelBlock = protowire.AppendTag(channelBlock, 2, protowire.VarintType)
	channelBlock = protowire.AppendVarint(channelBlock, 2)
	channelBlock = protowire.AppendTag(channelBlock, 6, protowire.BytesType)
	channelBlock = protowire.AppendString(channelBlock, "claude-sonnet-4-6")

	var container []byte
	container = protowire.AppendTag(container, 1, protowire.BytesType)
	container = protowire.AppendBytes(container, channelBlock)

	var payload []byte
	payload = protowire.AppendTag(payload, 2, protowire.BytesType)
	payload = protowire.AppendBytes(payload, container)
	payload = protowire.AppendTag(payload, 3, protowire.VarintType)
	payload = protowire.AppendVarint(payload, 1)
	payload = protowire.AppendTag(payload, 4, protowire.BytesType)
	opaque := make([]byte, 0, opaqueLen)
	for i := 0; len(opaque) < opaqueLen; i++ {
		opaque = append(opaque, byte((i*41+17)%251))
	}
	payload = protowire.AppendBytes(payload, opaque)
	return base64.StdEncoding.EncodeToString(payload)
}

func grokEncryptedContentSamplesPath() (string, bool) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", false
	}
	repo := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	path := filepath.Join(repo, "docs", "native-prompt-capture", "corpus", "grok-encrypted-content", "samples.json")
	if _, err := os.Stat(path); err != nil {
		return path, false
	}
	return path, true
}
