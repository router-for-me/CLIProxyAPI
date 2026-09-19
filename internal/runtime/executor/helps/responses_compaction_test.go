package helps

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestIsCPACompactionCapsule(t *testing.T) {
	tests := []struct {
		name             string
		encryptedContent string
		want             bool
	}{
		{name: "global capsule", encryptedContent: antigravityCompactionCapsulePrefix + "payload", want: true},
		{name: "legacy per-lane capsule", encryptedContent: legacyCompatCompactionCapsulePrefix + "payload", want: true},
		{name: "legacy derived-key capsule", encryptedContent: legacyCompatCompactionCapsulePrefixV3 + "payload", want: true},
		{name: "upstream opaque", encryptedContent: "upstream-native-opaque"},
		{name: "empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsCPACompactionCapsule(tt.encryptedContent); got != tt.want {
				t.Fatalf("IsCPACompactionCapsule(%q) = %v, want %v", tt.encryptedContent, got, tt.want)
			}
		})
	}
}

// CPA seals every lane with the same global capsule format, so a capsule minted for one provider
// opens for any other. The format itself is covered by antigravity_compaction_test.go.
func TestNormalizeCPACompactionItemsInlinesReadableCapsule(t *testing.T) {
	capsule, errSeal := SealAntigravityCompaction("summary text", "model")
	if errSeal != nil {
		t.Fatalf("seal: %v", errSeal)
	}
	payload := []byte(`{"model":"m","input":[{"type":"compaction","encrypted_content":"` + capsule + `"},{"type":"message","role":"user","content":"next"}]}`)

	got := NormalizeCPACompactionItems(context.Background(), payload)

	if strings.Contains(string(got), capsule) {
		t.Fatalf("CPA ciphertext survived: %s", got)
	}
	if strings.Contains(string(got), `"type":"compaction"`) {
		t.Fatalf("compaction item survived: %s", got)
	}
	if text := gjson.GetBytes(got, "input.0.content.0.text").String(); !strings.Contains(text, "summary text") {
		t.Fatalf("summary was not inlined: %s", got)
	}
	if !strings.Contains(string(got), `"content":"next"`) {
		t.Fatalf("unrelated item was lost: %s", got)
	}
}

// Capsules minted by an older build with a per-lane or derived key can no longer be opened, but
// they are still CPA ciphertext: they must be deleted, never forwarded to an upstream.
func TestNormalizeCPACompactionItemsDropsLegacyCapsule(t *testing.T) {
	legacyCapsules := map[string]string{
		"per-lane v2": legacyCompatCompactionCapsulePrefix + "AAAAAAAAAAAAAAAA:AAAA",
		"derived v3":  legacyCompatCompactionCapsulePrefixV3 + "AAAA",
	}
	for name, legacyCapsule := range legacyCapsules {
		t.Run(name, func(t *testing.T) {
			payload := []byte(`{"model":"m","input":[{"type":"compaction","encrypted_content":"` + legacyCapsule + `"},{"type":"message","role":"user","content":"next"}]}`)

			got := NormalizeCPACompactionItems(context.Background(), payload)

			if strings.Contains(string(got), legacyCapsule) {
				t.Fatalf("legacy CPA ciphertext survived: %s", got)
			}
			if strings.Contains(string(got), `"type":"compaction"`) {
				t.Fatalf("compaction item survived: %s", got)
			}
			if !strings.Contains(string(got), `"content":"next"`) {
				t.Fatalf("unrelated item was lost: %s", got)
			}
		})
	}
}

func TestNormalizeCPACompactionItemsDropsCorruptedCapsule(t *testing.T) {
	capsule, errSeal := SealAntigravityCompaction("summary text", "model")
	if errSeal != nil {
		t.Fatalf("seal: %v", errSeal)
	}
	raw, errDecode := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(capsule, antigravityCompactionCapsulePrefix))
	if errDecode != nil {
		t.Fatalf("decode payload: %v", errDecode)
	}
	raw[len(raw)/2] ^= 1
	corrupted := antigravityCompactionCapsulePrefix + base64.RawURLEncoding.EncodeToString(raw)
	payload := []byte(`{"model":"m","input":[{"type":"compaction","encrypted_content":"` + corrupted + `"}]}`)

	got := NormalizeCPACompactionItems(context.Background(), payload)

	if strings.Contains(string(got), corrupted) || strings.Contains(string(got), `"type":"compaction"`) {
		t.Fatalf("corrupted CPA ciphertext survived: %s", got)
	}
}

func TestNormalizeCPACompactionItemsPreservesNonCPAItem(t *testing.T) {
	payload := []byte(`{"model":"m","input":[{"type":"compaction","encrypted_content":"upstream-native-opaque"}]}`)

	got := NormalizeCPACompactionItems(context.Background(), payload)

	if string(got) != string(payload) {
		t.Fatalf("a non-CPA item must be preserved untouched, got: %s", got)
	}
}

func TestNormalizeCPACompactionItemsLeavesPayloadWithoutItemsUntouched(t *testing.T) {
	payload := []byte(`{"model":"m","input":[{"type":"message","role":"user","content":"hi"}],"stream":true}`)

	got := NormalizeCPACompactionItems(context.Background(), payload)

	if string(got) != string(payload) {
		t.Fatalf("payload changed: %s", got)
	}
}

func TestResponsesCompactionCapsuleResponseContainsOneItem(t *testing.T) {
	capsule, errSeal := SealAntigravityCompaction("summary", "model")
	if errSeal != nil {
		t.Fatalf("seal: %v", errSeal)
	}
	response := BuildResponsesCompactionResponse("model", capsule, "compat_compact", 1, 2, 3)
	if !json.Valid(response) {
		t.Fatalf("response is not valid JSON: %s", response)
	}
	output := gjson.GetBytes(response, "output")
	if !output.IsArray() || len(output.Array()) != 1 {
		t.Fatalf("expected exactly one output item, got %s", output.Raw)
	}
	item := output.Get("0")
	if item.Get("type").String() != "compaction" {
		t.Fatalf("expected compaction item, got %s", item.Get("type").String())
	}
	if item.Get("encrypted_content").String() == "" {
		t.Fatal("expected populated encrypted_content")
	}
}

func TestResponsesCompactionTriggerResponseUsesNormalResponseObject(t *testing.T) {
	capsule, errSeal := SealAntigravityCompaction("summary", "model")
	if errSeal != nil {
		t.Fatalf("seal: %v", errSeal)
	}
	response := BuildResponsesCompactionTriggerResponse("model", capsule, "compat_compact", 1, 2, 3)
	if got := gjson.GetBytes(response, "object").String(); got != "response" {
		t.Fatalf("object = %q, want response", got)
	}
	if got := gjson.GetBytes(response, "status").String(); got != "completed" {
		t.Fatalf("status = %q, want completed", got)
	}
	if !gjson.GetBytes(response, "completed_at").Exists() {
		t.Fatal("completed_at is missing")
	}
	output := gjson.GetBytes(response, "output")
	if !output.IsArray() || len(output.Array()) != 1 || output.Get("0.type").String() != "compaction" {
		t.Fatalf("unexpected trigger response output: %s", output.Raw)
	}
}
