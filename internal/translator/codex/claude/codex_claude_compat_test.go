package claude

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertClaudeRequestToCodexWithCompatPreservesEmptyThinking(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"reason","signature":""}]}]}`)

	withoutCompat := ConvertClaudeRequestToCodex("deepseek-v4", payload, false)
	if gjson.GetBytes(withoutCompat, "input.#").Int() != 0 {
		t.Fatalf("default translation preserved empty-signature thinking: %s", withoutCompat)
	}

	withCompat := ConvertClaudeRequestToCodexWithCompat("deepseek-v4", payload, false)
	if !gjson.GetBytes(withCompat, "input.0.type").Exists() || gjson.GetBytes(withCompat, "input.0.type").String() != "reasoning" {
		t.Fatalf("compat translation missing reasoning item: %s", withCompat)
	}
	if !gjson.GetBytes(withCompat, "input.0.encrypted_content").Exists() {
		t.Fatalf("compat translation missing empty encrypted_content: %s", withCompat)
	}
}

func TestConvertClaudeRequestToCodexWithCompat_UnknownThinkingSignatures(t *testing.T) {
	const unknownSig = "opaque-encrypted-reasoning-token-xyz"
	unknownPayload := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"reason","signature":"` + unknownSig + `"}]}]}`)

	withCompatNonStream := ConvertClaudeRequestToCodexWithCompat("deepseek-v4", unknownPayload, false)
	if gjson.GetBytes(withCompatNonStream, "input.0.type").String() != "reasoning" || gjson.GetBytes(withCompatNonStream, "input.0.encrypted_content").String() != unknownSig {
		t.Fatalf("compat non-stream translation failed to preserve unknown signature: %s", withCompatNonStream)
	}

	withCompatStream := ConvertClaudeRequestToCodexWithCompat("deepseek-v4", unknownPayload, true)
	if gjson.GetBytes(withCompatStream, "input.0.type").String() != "reasoning" || gjson.GetBytes(withCompatStream, "input.0.encrypted_content").String() != unknownSig {
		t.Fatalf("compat stream translation failed to preserve unknown signature: %s", withCompatStream)
	}

	withoutCompatNonStream := ConvertClaudeRequestToCodex("deepseek-v4", unknownPayload, false)
	if gjson.GetBytes(withoutCompatNonStream, "input.#").Int() != 0 {
		t.Fatalf("non-compat translation unexpectedly preserved unknown signature: %s", withoutCompatNonStream)
	}

	withoutCompatStream := ConvertClaudeRequestToCodex("deepseek-v4", unknownPayload, true)
	if gjson.GetBytes(withoutCompatStream, "input.#").Int() != 0 {
		t.Fatalf("non-compat stream translation unexpectedly preserved unknown signature: %s", withoutCompatStream)
	}

	const claudeSig = "CAISqwIKiAEIEBgCKkBHRlRBsNiptQUWfPoOhuQKwi5LnncZVO9bB5jqOs76D7uBtgktML0zqJtNmLHXHHcgD6lk4MQu4QBXzFd1lbC3Mg5jbGF1ZGUtZmFibGUtNTgBQgh0aGlua2luZ1okZDk3NDM5NzUtNGJiMC00OTM2LTllMjgtZDViMGQyMWJkYzQ4EgxCGh+XVFFFeySAjtAaDL/A1LltGu6MMJ+eXSIwsN0oBpDrqLv22UBfkMnTotnIbkvkOyb9xZHgigG6OZVHaI3gThm+maLKmgO5PrFLKlDFYp+YZksy/wKwszJlnLTPzAK+NUlfzagOE1ymtZTXhAYK260XyFYmg/te/C231+Fr/hoX+EJoUBnrn0gD7hqMISOT+TaFEuOXYsN517GfaxgB"
	claudePayload := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"reason","signature":"` + claudeSig + `"}]}]}`)
	claudeCompat := ConvertClaudeRequestToCodexWithCompat("deepseek-v4", claudePayload, false)
	if gjson.GetBytes(claudeCompat, "input.#").Int() != 0 {
		t.Fatalf("compat translation unexpectedly preserved valid Claude signature: %s", claudeCompat)
	}

	gptRawSig := validCodexReasoningSignature()
	gptPayload := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"reason","signature":"gpt#` + gptRawSig + `"}]}]}`)
	gptCompat := ConvertClaudeRequestToCodexWithCompat("deepseek-v4", gptPayload, false)
	if gjson.GetBytes(gptCompat, "input.0.type").String() != "reasoning" || gjson.GetBytes(gptCompat, "input.0.encrypted_content").String() != gptRawSig {
		t.Fatalf("compat translation failed to normalize gpt# signature: %s", gptCompat)
	}

	nonStringPayloadInt := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"reason","signature":12345}]}]}`)
	nonStringCompatInt := ConvertClaudeRequestToCodexWithCompat("deepseek-v4", nonStringPayloadInt, false)
	if gjson.GetBytes(nonStringCompatInt, "input.#").Int() != 0 {
		t.Fatalf("compat translation unexpectedly preserved non-string int signature: %s", nonStringCompatInt)
	}

	nonStringPayloadObj := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"reason","signature":{"opaque":"data"}}]}]}`)
	nonStringCompatObj := ConvertClaudeRequestToCodexWithCompat("deepseek-v4", nonStringPayloadObj, false)
	if gjson.GetBytes(nonStringCompatObj, "input.#").Int() != 0 {
		t.Fatalf("compat translation unexpectedly preserved non-string object signature: %s", nonStringCompatObj)
	}

	nonStringPayloadBool := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"reason","signature":true}]}]}`)
	nonStringCompatBool := ConvertClaudeRequestToCodexWithCompat("deepseek-v4", nonStringPayloadBool, false)
	if gjson.GetBytes(nonStringCompatBool, "input.#").Int() != 0 {
		t.Fatalf("compat translation unexpectedly preserved non-string bool signature: %s", nonStringCompatBool)
	}

	nonStringPayloadArr := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"reason","signature":["arr"]}]}]}`)
	nonStringCompatArr := ConvertClaudeRequestToCodexWithCompat("deepseek-v4", nonStringPayloadArr, false)
	if gjson.GetBytes(nonStringCompatArr, "input.#").Int() != 0 {
		t.Fatalf("compat translation unexpectedly preserved non-string array signature: %s", nonStringCompatArr)
	}
}

func TestConvertClaudeRequestToCodexWithCompat_PreservesMessageFlushingAndEscapedUnknownSignature(t *testing.T) {
	const escapedSig = "enc:\"token\"\\with\\unicode-\u4e00"
	escapedPayload := []byte(`{"messages":[{"role":"assistant","content":[{"type":"text","text":"before"},{"type":"thinking","thinking":"reason","signature":"enc:\"token\"\\with\\unicode-\u4e00"},{"type":"text","text":"after"}]}]}`)

	withCompat := ConvertClaudeRequestToCodexWithCompat("deepseek-v4", escapedPayload, false)
	if gjson.GetBytes(withCompat, "input.#").Int() != 3 {
		t.Fatalf("compat translation expected 3 items after message flush, got: %s", withCompat)
	}
	if gjson.GetBytes(withCompat, "input.0.role").String() != "assistant" || gjson.GetBytes(withCompat, "input.0.content.0.text").String() != "before" {
		t.Fatalf("first item unexpected: %s", withCompat)
	}
	if gjson.GetBytes(withCompat, "input.1.type").String() != "reasoning" || gjson.GetBytes(withCompat, "input.1.encrypted_content").String() != escapedSig {
		t.Fatalf("second item unexpected reasoning signature: %s", withCompat)
	}
	if gjson.GetBytes(withCompat, "input.2.role").String() != "assistant" || gjson.GetBytes(withCompat, "input.2.content.0.text").String() != "after" {
		t.Fatalf("third item unexpected: %s", withCompat)
	}
}

func TestConvertClaudeRequestToCodexWithCompat_WhitespaceAndNullSignatures(t *testing.T) {
	whitespacePayload := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"reason","signature":"   "}]}]}`)
	whitespaceCompat := ConvertClaudeRequestToCodexWithCompat("deepseek-v4", whitespacePayload, false)
	if gjson.GetBytes(whitespaceCompat, "input.0.type").String() != "reasoning" || gjson.GetBytes(whitespaceCompat, "input.0.encrypted_content").String() != "   " {
		t.Fatalf("compat translation failed to preserve whitespace signature via empty fallback: %s", whitespaceCompat)
	}

	nullPayload := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"reason","signature":null}]}]}`)
	nullCompat := ConvertClaudeRequestToCodexWithCompat("deepseek-v4", nullPayload, false)
	if gjson.GetBytes(nullCompat, "input.0.type").String() != "reasoning" || gjson.GetBytes(nullCompat, "input.0.encrypted_content").String() != "" {
		t.Fatalf("compat translation failed to preserve null signature via empty fallback: %s", nullCompat)
	}
}
