package signature

import (
	"fmt"
	"strings"
	"testing"

	log "github.com/sirupsen/logrus"
	"github.com/sirupsen/logrus/hooks/test"
	"github.com/tidwall/gjson"
)

func newSignatureDebugHook(t *testing.T) *test.Hook {
	t.Helper()

	previousLevel := log.GetLevel()
	log.SetLevel(log.DebugLevel)
	hook := test.NewLocal(log.StandardLogger())
	t.Cleanup(func() {
		hook.Reset()
		log.SetLevel(previousLevel)
	})
	return hook
}

func assertSignatureDebugDoesNotLeak(t *testing.T, hook *test.Hook, forbidden string) {
	t.Helper()

	if forbidden == "" {
		return
	}
	for _, entry := range hook.AllEntries() {
		if strings.Contains(entry.Message, forbidden) {
			t.Fatalf("debug log leaked signature in message: %q", entry.Message)
		}
		for key, value := range entry.Data {
			if strings.Contains(fmt.Sprint(value), forbidden) {
				t.Fatalf("debug log leaked signature in field %q: %v", key, value)
			}
		}
	}
}

func TestSanitizeGeminiRequestThoughtSignaturesPreservesGeminiSignature(t *testing.T) {
	sig := testGemini3ThoughtSignature([]byte{0x01, 0x0c, 0x39})
	input := []byte(`{"contents":[{"role":"model","parts":[{"functionCall":{"name":"f","args":{}},"thoughtSignature":"` + sig + `"}]}]}`)

	out := SanitizeGeminiRequestThoughtSignatures(input, "contents")

	if got := gjson.GetBytes(out, "contents.0.parts.0.thoughtSignature").String(); got != sig {
		t.Fatalf("thoughtSignature = %q, want %q. Output: %s", got, sig, string(out))
	}
}

func TestSanitizeGeminiRequestThoughtSignaturesReplacesBase64UUIDFunctionCall(t *testing.T) {
	sig := testGeminiThoughtSignature([]byte("e24830a7-5cd6-42fe-998b-ee539e72b9c3"))
	input := []byte(`{"contents":[{"role":"model","parts":[{"functionCall":{"name":"f","args":{},"thoughtSignature":"` + sig + `"}}]}]}`)

	out := SanitizeGeminiRequestThoughtSignatures(input, "contents")

	if got := gjson.GetBytes(out, "contents.0.parts.0.thoughtSignature").String(); got != GeminiSkipThoughtSignatureValidator {
		t.Fatalf("thoughtSignature = %q, want bypass sentinel. Output: %s", got, string(out))
	}
	if gjson.GetBytes(out, "contents.0.parts.0.functionCall.thoughtSignature").Exists() {
		t.Fatalf("nested functionCall thoughtSignature should be removed. Output: %s", string(out))
	}
}

func TestSanitizeGeminiRequestThoughtSignaturesLogsBypassReplacement(t *testing.T) {
	hook := newSignatureDebugHook(t)
	sig := testGeminiThoughtSignature([]byte("e24830a7-5cd6-42fe-998b-ee539e72b9c3"))
	input := []byte(`{"contents":[{"role":"model","parts":[{"functionCall":{"name":"f","args":{},"thoughtSignature":"` + sig + `"}}]}]}`)

	out := SanitizeGeminiRequestThoughtSignatures(input, "contents")
	if got := gjson.GetBytes(out, "contents.0.parts.0.thoughtSignature").String(); got != GeminiSkipThoughtSignatureValidator {
		t.Fatalf("thoughtSignature = %q, want bypass sentinel. Output: %s", got, string(out))
	}

	found := false
	for _, entry := range hook.AllEntries() {
		if entry.Level != log.DebugLevel {
			continue
		}
		if entry.Data["component"] != "signature_sanitizer" ||
			entry.Data["target_provider"] != string(SignatureProviderGemini) ||
			entry.Data["action"] != "replace_with_gemini_bypass" {
			continue
		}
		if entry.Data["block_kind"] != string(SignatureBlockKindGeminiFunctionCall) {
			t.Fatalf("block_kind = %v, want %s", entry.Data["block_kind"], SignatureBlockKindGeminiFunctionCall)
		}
		found = true
	}
	if !found {
		t.Fatal("expected debug log for Gemini thoughtSignature bypass replacement")
	}
	assertSignatureDebugDoesNotLeak(t, hook, sig)
}

func TestSanitizeGeminiRequestThoughtSignaturesReplacesField2WrappedUUIDFunctionCall(t *testing.T) {
	sig := testGemini3ThoughtSignature([]byte("e24830a7-5cd6-42fe-998b-ee539e72b9c3"))
	input := []byte(`{"request":{"contents":[{"role":"model","parts":[{"functionCall":{"name":"f","args":{}},"thoughtSignature":"` + sig + `"}]}]}}`)

	out := SanitizeGeminiRequestThoughtSignatures(input, "request.contents")

	if got := gjson.GetBytes(out, "request.contents.0.parts.0.thoughtSignature").String(); got != GeminiSkipThoughtSignatureValidator {
		t.Fatalf("thoughtSignature = %q, want bypass sentinel. Output: %s", got, string(out))
	}
}

func TestSanitizeGeminiRequestThoughtSignaturesRemovesFunctionResponseSignature(t *testing.T) {
	input := []byte(`{"contents":[{"role":"user","parts":[{"functionResponse":{"name":"f","response":{"result":"ok"},"thoughtSignature":"bad"},"thoughtSignature":"bad"}]}]}`)

	out := SanitizeGeminiRequestThoughtSignatures(input, "contents")

	if gjson.GetBytes(out, "contents.0.parts.0.thoughtSignature").Exists() {
		t.Fatalf("functionResponse top-level thoughtSignature should be removed. Output: %s", string(out))
	}
	if gjson.GetBytes(out, "contents.0.parts.0.functionResponse.thoughtSignature").Exists() {
		t.Fatalf("functionResponse nested thoughtSignature should be removed. Output: %s", string(out))
	}
}

// TestSanitizeGeminiRequestThoughtSignaturesMultipleFunctionCallsOnlyKeepsFirst 测试当一次调用多个工具（包含多个 functionCall）时，
// 只有首个具有签名需要的 part 被保留或补齐签名，后续的 tool call 上的冗余签名是否会被彻底清除并且不再填入 skip 占位符。
func TestSanitizeGeminiRequestThoughtSignaturesMultipleFunctionCallsOnlyKeepsFirst(t *testing.T) {
	// 场景 1：构建两个 functionCall，第1个有合法签名，第2个没有。预期第1个签名被保留，第2个不被补置签名。
	sig := testGemini3ThoughtSignature([]byte{0x01, 0x0c, 0x39})
	input := []byte(`{"contents":[{"role":"model","parts":[
		{"functionCall":{"name":"f1","args":{}},"thoughtSignature":"` + sig + `"},
		{"functionCall":{"name":"f2","args":{}}}
	]}]}`)

	out := SanitizeGeminiRequestThoughtSignatures(input, "contents")

	if got := gjson.GetBytes(out, "contents.0.parts.0.thoughtSignature").String(); got != sig {
		t.Fatalf("第一个 functionCall 的 thoughtSignature 应该保留: 实际为 %q, 预期为 %q。输出: %s", got, sig, string(out))
	}
	if gjson.GetBytes(out, "contents.0.parts.1.thoughtSignature").Exists() {
		t.Fatalf("第二个 functionCall 不应产生或保留 thoughtSignature。输出: %s", string(out))
	}

	// 场景 2：构建两个 functionCall，第1个没有签名（会补置 skip 占位符），第2个原本也没有签名。预期第1个补齐占位符，第2个保持为空（不被补齐）。
	input2 := []byte(`{"contents":[{"role":"model","parts":[
		{"functionCall":{"name":"f1","args":{}}},
		{"functionCall":{"name":"f2","args":{}}}
	]}]}`)

	out2 := SanitizeGeminiRequestThoughtSignatures(input2, "contents")

	if got := gjson.GetBytes(out2, "contents.0.parts.0.thoughtSignature").String(); got != GeminiSkipThoughtSignatureValidator {
		t.Fatalf("第一个 functionCall 应该被补齐为 skip 占位符: 实际为 %q。输出: %s", got, string(out2))
	}
	if gjson.GetBytes(out2, "contents.0.parts.1.thoughtSignature").Exists() {
		t.Fatalf("第二个 functionCall 原本无签名，不应被主动补齐。输出: %s", string(out2))
	}
}
