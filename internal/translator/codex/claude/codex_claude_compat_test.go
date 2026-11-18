package claude

import (
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestConvertClaudeRequestToCodexWithCompatPreservesEmptyThinking(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"reason","signature":""}]}]}`)

	withoutCompat, _ := ConvertClaudeRequestToCodex("deepseek-v4", payload, false)
	if ojson.GetBytesNoCopy(withoutCompat, "input.#").Int() != 0 {
		t.Fatalf("default translation preserved empty-signature thinking: %s", withoutCompat)
	}

	withCompat, _ := ConvertClaudeRequestToCodexWithCompat("deepseek-v4", payload, false)
	if !ojson.GetBytesNoCopy(withCompat, "input.0.type").Exists() || ojson.GetBytesNoCopy(withCompat, "input.0.type").String() != "reasoning" {
		t.Fatalf("compat translation missing reasoning item: %s", withCompat)
	}
	if !ojson.GetBytesNoCopy(withCompat, "input.0.encrypted_content").Exists() {
		t.Fatalf("compat translation missing empty encrypted_content: %s", withCompat)
	}
}

func TestConvertClaudeRequestToCodexWithCompat_UnknownThinkingSignatures(t *testing.T) {
	const unknownSig = "opaque-encrypted-reasoning-token-xyz"
	unknownPayload := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"reason","signature":"` + unknownSig + `"}]}]}`)

	withCompatNonStream, _ := ConvertClaudeRequestToCodexWithCompat("deepseek-v4", unknownPayload, false)
	if ojson.GetBytesNoCopy(withCompatNonStream, "input.0.type").String() != "reasoning" || ojson.GetBytesNoCopy(withCompatNonStream, "input.0.encrypted_content").String() != unknownSig {
		t.Fatalf("compat non-stream translation failed to preserve unknown signature: %s", withCompatNonStream)
	}

	withCompatStream, _ := ConvertClaudeRequestToCodexWithCompat("deepseek-v4", unknownPayload, true)
	if ojson.GetBytesNoCopy(withCompatStream, "input.0.type").String() != "reasoning" || ojson.GetBytesNoCopy(withCompatStream, "input.0.encrypted_content").String() != unknownSig {
		t.Fatalf("compat stream translation failed to preserve unknown signature: %s", withCompatStream)
	}

	withoutCompatNonStream, _ := ConvertClaudeRequestToCodex("deepseek-v4", unknownPayload, false)
	if ojson.GetBytesNoCopy(withoutCompatNonStream, "input.#").Int() != 0 {
		t.Fatalf("non-compat translation unexpectedly preserved unknown signature: %s", withoutCompatNonStream)
	}

	withoutCompatStream, _ := ConvertClaudeRequestToCodex("deepseek-v4", unknownPayload, true)
	if ojson.GetBytesNoCopy(withoutCompatStream, "input.#").Int() != 0 {
		t.Fatalf("non-compat stream translation unexpectedly preserved unknown signature: %s", withoutCompatStream)
	}

	const claudeSig = "CAISqwIKiAEIEBgCKkBHRlRBsNiptQUWfPoOhuQKwi5LnncZVO9bB5jqOs76D7uBtgktML0zqJtNmLHXHHcgD6lk4MQu4QBXzFd1lbC3Mg5jbGF1ZGUtZmFibGUtNTgBQgh0aGlua2luZ1okZDk3NDM5NzUtNGJiMC00OTM2LTllMjgtZDViMGQyMWJkYzQ4EgxCGh+XVFFFeySAjtAaDL/A1LltGu6MMJ+eXSIwsN0oBpDrqLv22UBfkMnTotnIbkvkOyb9xZHgigG6OZVHaI3gThm+maLKmgO5PrFLKlDFYp+YZksy/wKwszJlnLTPzAK+NUlfzagOE1ymtZTXhAYK260XyFYmg/te/C231+Fr/hoX+EJoUBnrn0gD7hqMISOT+TaFEuOXYsN517GfaxgB"
	claudePayload := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"reason","signature":"` + claudeSig + `"}]}]}`)
	claudeCompat, _ := ConvertClaudeRequestToCodexWithCompat("deepseek-v4", claudePayload, false)
	if ojson.GetBytesNoCopy(claudeCompat, "input.#").Int() != 0 {
		t.Fatalf("compat translation unexpectedly preserved valid Claude signature: %s", claudeCompat)
	}

	gptRawSig := validCodexReasoningSignature()
	gptPayload := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"reason","signature":"gpt#` + gptRawSig + `"}]}]}`)
	gptCompat, _ := ConvertClaudeRequestToCodexWithCompat("deepseek-v4", gptPayload, false)
	if ojson.GetBytesNoCopy(gptCompat, "input.0.type").String() != "reasoning" || ojson.GetBytesNoCopy(gptCompat, "input.0.encrypted_content").String() != gptRawSig {
		t.Fatalf("compat translation failed to normalize gpt# signature: %s", gptCompat)
	}

	nonStringPayloadInt := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"reason","signature":12345}]}]}`)
	nonStringCompatInt, _ := ConvertClaudeRequestToCodexWithCompat("deepseek-v4", nonStringPayloadInt, false)
	if ojson.GetBytesNoCopy(nonStringCompatInt, "input.#").Int() != 0 {
		t.Fatalf("compat translation unexpectedly preserved non-string int signature: %s", nonStringCompatInt)
	}

	nonStringPayloadObj := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"reason","signature":{"opaque":"data"}}]}]}`)
	nonStringCompatObj, _ := ConvertClaudeRequestToCodexWithCompat("deepseek-v4", nonStringPayloadObj, false)
	if ojson.GetBytesNoCopy(nonStringCompatObj, "input.#").Int() != 0 {
		t.Fatalf("compat translation unexpectedly preserved non-string object signature: %s", nonStringCompatObj)
	}

	nonStringPayloadBool := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"reason","signature":true}]}]}`)
	nonStringCompatBool, _ := ConvertClaudeRequestToCodexWithCompat("deepseek-v4", nonStringPayloadBool, false)
	if ojson.GetBytesNoCopy(nonStringCompatBool, "input.#").Int() != 0 {
		t.Fatalf("compat translation unexpectedly preserved non-string bool signature: %s", nonStringCompatBool)
	}

	nonStringPayloadArr := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"reason","signature":["arr"]}]}]}`)
	nonStringCompatArr, _ := ConvertClaudeRequestToCodexWithCompat("deepseek-v4", nonStringPayloadArr, false)
	if ojson.GetBytesNoCopy(nonStringCompatArr, "input.#").Int() != 0 {
		t.Fatalf("compat translation unexpectedly preserved non-string array signature: %s", nonStringCompatArr)
	}
}

func TestConvertClaudeRequestToCodexWithCompat_PreservesMessageFlushingAndEscapedUnknownSignature(t *testing.T) {
	const escapedSig = "enc:\"token\"\\with\\unicode-\u4e00"
	escapedPayload := []byte(`{"messages":[{"role":"assistant","content":[{"type":"text","text":"before"},{"type":"thinking","thinking":"reason","signature":"enc:\"token\"\\with\\unicode-\u4e00"},{"type":"text","text":"after"}]}]}`)

	withCompat, _ := ConvertClaudeRequestToCodexWithCompat("deepseek-v4", escapedPayload, false)
	if ojson.GetBytesNoCopy(withCompat, "input.#").Int() != 3 {
		t.Fatalf("compat translation expected 3 items after message flush, got: %s", withCompat)
	}
	if ojson.GetBytesNoCopy(withCompat, "input.0.role").String() != "assistant" || ojson.GetBytesNoCopy(withCompat, "input.0.content.0.text").String() != "before" {
		t.Fatalf("first item unexpected: %s", withCompat)
	}
	if ojson.GetBytesNoCopy(withCompat, "input.1.type").String() != "reasoning" || ojson.GetBytesNoCopy(withCompat, "input.1.encrypted_content").String() != escapedSig {
		t.Fatalf("second item unexpected reasoning signature: %s", withCompat)
	}
	if ojson.GetBytesNoCopy(withCompat, "input.2.role").String() != "assistant" || ojson.GetBytesNoCopy(withCompat, "input.2.content.0.text").String() != "after" {
		t.Fatalf("third item unexpected: %s", withCompat)
	}
}

func TestConvertClaudeRequestToCodexWithCompat_WhitespaceAndNullSignatures(t *testing.T) {
	whitespacePayload := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"reason","signature":"   "}]}]}`)
	whitespaceCompat, _ := ConvertClaudeRequestToCodexWithCompat("deepseek-v4", whitespacePayload, false)
	if ojson.GetBytesNoCopy(whitespaceCompat, "input.0.type").String() != "reasoning" || ojson.GetBytesNoCopy(whitespaceCompat, "input.0.encrypted_content").String() != "   " {
		t.Fatalf("compat translation failed to preserve whitespace signature via empty fallback: %s", whitespaceCompat)
	}

	nullPayload := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"reason","signature":null}]}]}`)
	nullCompat, _ := ConvertClaudeRequestToCodexWithCompat("deepseek-v4", nullPayload, false)
	if ojson.GetBytesNoCopy(nullCompat, "input.0.type").String() != "reasoning" || ojson.GetBytesNoCopy(nullCompat, "input.0.encrypted_content").String() != "" {
		t.Fatalf("compat translation failed to preserve null signature via empty fallback: %s", nullCompat)
	}
}
