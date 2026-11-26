package signature

import (
	"strings"
	"testing"
)

func TestValidateGeminiThoughtSignatures_SnakeCaseFunctionCallAndResponse(t *testing.T) {
	t.Run("rejects_unsigned_first_function_call", func(t *testing.T) {
		input := []byte(`{
			"contents": [{
				"role": "model",
				"parts": [
					{"function_call": {"id": "call-1", "name": "read_file", "args": {}}}
				]
			}]
		}`)

		err := ValidateGeminiThoughtSignatures(input)
		if err == nil {
			t.Fatal("unsigned first snake_case function_call should fail")
		}
		if !strings.Contains(err.Error(), "missing thoughtSignature on first functionCall") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("accepts_bypass_sentinel_on_first_function_call_and_unsigned_sibling", func(t *testing.T) {
		input := []byte(`{
			"contents": [{
				"role": "model",
				"parts": [
					{
						"function_call": {"id": "call-1", "name": "read_file", "args": {}},
						"thoughtSignature": "skip_thought_signature_validator"
					},
					{"function_call": {"id": "call-2", "name": "read_file", "args": {}}}
				]
			}]
		}`)

		if err := ValidateGeminiThoughtSignatures(input, GeminiThoughtSignatureValidationOptions{AllowBypassSentinel: true}); err != nil {
			t.Fatalf("bypass sentinel on first snake_case function_call with unsigned sibling should be valid: %v", err)
		}
	})

	t.Run("rejects_bypass_sentinel_on_sibling_function_call", func(t *testing.T) {
		input := []byte(`{
			"contents": [{
				"role": "model",
				"parts": [
					{
						"function_call": {"id": "call-1", "name": "read_file", "args": {}},
						"thoughtSignature": "skip_thought_signature_validator"
					},
					{
						"function_call": {"id": "call-2", "name": "read_file", "args": {}},
						"thoughtSignature": "skip_thought_signature_validator"
					}
				]
			}]
		}`)

		err := ValidateGeminiThoughtSignatures(input, GeminiThoughtSignatureValidationOptions{AllowBypassSentinel: true})
		if err == nil || !strings.Contains(err.Error(), "contents[0].parts[1]") || !strings.Contains(err.Error(), "allowed only on the first model functionCall") {
			t.Fatalf("expected bypass sentinel rejection on sibling function_call at parts[1], got %v", err)
		}
	})

	t.Run("rejects_unsigned_function_call_after_text", func(t *testing.T) {
		input := []byte(`{
			"contents": [{
				"role": "model",
				"parts": [
					{
						"function_call": {"id": "call-1", "name": "read_file", "args": {}},
						"thoughtSignature": "skip_thought_signature_validator"
					},
					{"text": "more"},
					{"function_call": {"id": "call-2", "name": "read_file", "args": {}}}
				]
			}]
		}`)

		err := ValidateGeminiThoughtSignatures(input, GeminiThoughtSignatureValidationOptions{AllowBypassSentinel: true})
		if err == nil || !strings.Contains(err.Error(), "contents[0].parts[2]") || !strings.Contains(err.Error(), "missing thoughtSignature on first functionCall") {
			t.Fatalf("expected missing thoughtSignature error on function_call after text at parts[2], got %v", err)
		}
	})

	t.Run("rejects_signature_on_function_response", func(t *testing.T) {
		sig := testGemini3ThoughtSignature([]byte{0x01, 0x0c, 0x39, 0xd6, 0xc7, 0x34})
		input := []byte(`{
			"contents": [{
				"role": "user",
				"parts": [
					{
						"function_response": {"id": "call-1", "name": "read_file", "response": {"ok": true}},
						"thoughtSignature": "` + sig + `"
					}
				]
			}]
		}`)

		err := ValidateGeminiThoughtSignatures(input)
		if err == nil || !strings.Contains(err.Error(), "functionResponse must not carry thoughtSignature") {
			t.Fatalf("expected functionResponse signature rejection, got %v", err)
		}
	})
}

func TestValidateGeminiFunctionCallPairing_SnakeCase(t *testing.T) {
	t.Run("accepts_valid_parallel_pairing", func(t *testing.T) {
		input := []byte(`{
			"contents": [
				{
					"role": "model",
					"parts": [
						{"function_call": {"id": "call-1", "name": "weather", "args": {"city": "NYC"}}},
						{"function_call": {"id": "call-2", "name": "time", "args": {"tz": "UTC"}}}
					]
				},
				{
					"role": "user",
					"parts": [
						{"function_response": {"id": "call-1", "name": "weather", "response": {"temp": 72}}},
						{"function_response": {"id": "call-2", "name": "time", "response": {"now": "12:00"}}}
					]
				}
			]
		}`)

		if err := ValidateGeminiFunctionCallPairing(input); err != nil {
			t.Fatalf("valid snake_case parallel pairing should pass: %v", err)
		}
	})

	t.Run("accepts_mixed_camel_and_snake_case_pairing", func(t *testing.T) {
		input := []byte(`{
			"contents": [
				{
					"role": "model",
					"parts": [
						{"function_call": {"id": "call-1", "name": "weather", "args": {}}},
						{"functionCall": {"id": "call-2", "name": "time", "args": {}}}
					]
				},
				{
					"role": "user",
					"parts": [
						{"functionResponse": {"id": "call-1", "name": "weather", "response": {}}},
						{"function_response": {"id": "call-2", "name": "time", "response": {}}}
					]
				}
			]
		}`)

		if err := ValidateGeminiFunctionCallPairing(input); err != nil {
			t.Fatalf("mixed camelCase and snake_case pairing should pass: %v", err)
		}
	})

	t.Run("rejects_missing_function_call_name", func(t *testing.T) {
		input := []byte(`{
			"contents": [{
				"role": "model",
				"parts": [
					{"function_call": {"id": "call-1", "args": {}}}
				]
			}]
		}`)

		err := ValidateGeminiFunctionCallPairing(input)
		if err == nil || !strings.Contains(err.Error(), "missing functionCall.name") {
			t.Fatalf("expected missing functionCall.name error, got %v", err)
		}
	})

	t.Run("rejects_orphan_function_response", func(t *testing.T) {
		input := []byte(`{
			"contents": [{
				"role": "user",
				"parts": [
					{"function_response": {"id": "call-1", "name": "weather", "response": {}}}
				]
			}]
		}`)

		err := ValidateGeminiFunctionCallPairing(input)
		if err == nil || !strings.Contains(err.Error(), "functionResponse without preceding functionCall") {
			t.Fatalf("expected orphan functionResponse error, got %v", err)
		}
	})

	t.Run("rejects_same_content_interleaving", func(t *testing.T) {
		input := []byte(`{
			"contents": [{
				"role": "model",
				"parts": [
					{"function_call": {"id": "call-1", "name": "weather", "args": {}}},
					{"function_response": {"id": "call-1", "name": "weather", "response": {}}}
				]
			}]
		}`)

		err := ValidateGeminiFunctionCallPairing(input)
		if err == nil || !strings.Contains(err.Error(), "must not be interleaved") {
			t.Fatalf("expected interleaving error, got %v", err)
		}
	})

	t.Run("rejects_response_count_mismatch", func(t *testing.T) {
		input := []byte(`{
			"contents": [
				{
					"role": "model",
					"parts": [
						{"function_call": {"id": "call-1", "name": "weather", "args": {}}},
						{"function_call": {"id": "call-2", "name": "time", "args": {}}}
					]
				},
				{
					"role": "user",
					"parts": [
						{"function_response": {"id": "call-1", "name": "weather", "response": {}}}
					]
				}
			]
		}`)

		err := ValidateGeminiFunctionCallPairing(input)
		if err == nil || !strings.Contains(err.Error(), "does not match pending functionCall count") {
			t.Fatalf("expected count mismatch error, got %v", err)
		}
	})

	t.Run("rejects_response_id_mismatch", func(t *testing.T) {
		input := []byte(`{
			"contents": [
				{
					"role": "model",
					"parts": [
						{"function_call": {"id": "call-1", "name": "weather", "args": {}}}
					]
				},
				{
					"role": "user",
					"parts": [
						{"function_response": {"id": "call-other", "name": "weather", "response": {}}}
					]
				}
			]
		}`)

		err := ValidateGeminiFunctionCallPairing(input)
		if err == nil || !strings.Contains(err.Error(), "does not match functionCall.id") {
			t.Fatalf("expected id mismatch error, got %v", err)
		}
	})

	t.Run("rejects_missing_response_id", func(t *testing.T) {
		input := []byte(`{
			"contents": [
				{
					"role": "model",
					"parts": [
						{"function_call": {"id": "call-1", "name": "weather", "args": {}}}
					]
				},
				{
					"role": "user",
					"parts": [
						{"function_response": {"name": "weather", "response": {}}}
					]
				}
			]
		}`)

		err := ValidateGeminiFunctionCallPairing(input)
		if err == nil || !strings.Contains(err.Error(), "missing functionResponse.id") {
			t.Fatalf("expected missing functionResponse.id error, got %v", err)
		}
	})

	t.Run("rejects_missing_response_name", func(t *testing.T) {
		input := []byte(`{
			"contents": [
				{
					"role": "model",
					"parts": [
						{"function_call": {"id": "call-1", "name": "weather", "args": {}}}
					]
				},
				{
					"role": "user",
					"parts": [
						{"function_response": {"id": "call-1", "response": {}}}
					]
				}
			]
		}`)

		err := ValidateGeminiFunctionCallPairing(input)
		if err == nil || !strings.Contains(err.Error(), "missing functionResponse.name") {
			t.Fatalf("expected missing functionResponse.name error, got %v", err)
		}
	})

	t.Run("rejects_response_name_mismatch", func(t *testing.T) {
		input := []byte(`{
			"contents": [
				{
					"role": "model",
					"parts": [
						{"function_call": {"id": "call-1", "name": "weather", "args": {}}}
					]
				},
				{
					"role": "user",
					"parts": [
						{"function_response": {"id": "call-1", "name": "other_tool", "response": {}}}
					]
				}
			]
		}`)

		err := ValidateGeminiFunctionCallPairing(input)
		if err == nil || !strings.Contains(err.Error(), "does not match functionCall.name") {
			t.Fatalf("expected name mismatch error, got %v", err)
		}
	})
}
