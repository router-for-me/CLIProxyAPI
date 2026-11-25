package signature

import (
	"strings"
	"testing"
)

func TestValidateGeminiThoughtSignatures_SnakeCaseFunctionCallAndResponse(t *testing.T) {
	t.Run("missing signature on first snake_case function_call is rejected", func(t *testing.T) {
		payload := []byte(`{
			"contents": [
				{
					"role": "model",
					"parts": [
						{"function_call": {"id": "call-1", "name": "get_weather", "args": {"city": "NYC"}}}
					]
				}
			]
		}`)

		err := ValidateGeminiThoughtSignatures(payload)
		if err == nil {
			t.Fatalf("expected error for unsigned snake_case function_call, got nil")
		}
		if !strings.Contains(err.Error(), "missing thoughtSignature on first functionCall") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("bypass sentinel on first snake_case function_call is accepted", func(t *testing.T) {
		payload := []byte(`{
			"contents": [
				{
					"role": "model",
					"parts": [
						{
							"function_call": {"id": "call-1", "name": "get_weather", "args": {"city": "NYC"}},
							"thoughtSignature": "` + GeminiSkipThoughtSignatureValidator + `"
						}
					]
				}
			]
		}`)

		if err := ValidateGeminiThoughtSignatures(payload, GeminiThoughtSignatureValidationOptions{AllowBypassSentinel: true}); err != nil {
			t.Fatalf("expected valid bypass sentinel on snake_case function_call, got: %v", err)
		}
	})

	t.Run("thoughtSignature on snake_case function_response is rejected", func(t *testing.T) {
		payload := []byte(`{
			"contents": [
				{
					"role": "user",
					"parts": [
						{
							"function_response": {"id": "call-1", "name": "get_weather", "response": {"temp": 72}},
							"thoughtSignature": "` + GeminiSkipThoughtSignatureValidator + `"
						}
					]
				}
			]
		}`)

		err := ValidateGeminiThoughtSignatures(payload)
		if err == nil {
			t.Fatalf("expected error for thoughtSignature on snake_case function_response, got nil")
		}
		if !strings.Contains(err.Error(), "functionResponse must not carry thoughtSignature") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestValidateGeminiFunctionCallPairing_SnakeCase(t *testing.T) {
	t.Run("valid snake_case pairing passes", func(t *testing.T) {
		payload := []byte(`{
			"contents": [
				{
					"role": "model",
					"parts": [
						{"function_call": {"id": "call-1", "name": "get_weather", "args": {"city": "NYC"}}}
					]
				},
				{
					"role": "user",
					"parts": [
						{"function_response": {"id": "call-1", "name": "get_weather", "response": {"temp": 72}}}
					]
				}
			]
		}`)

		if err := ValidateGeminiFunctionCallPairing(payload); err != nil {
			t.Fatalf("expected valid snake_case pairing, got: %v", err)
		}
	})

	t.Run("snake_case function_call missing name is rejected", func(t *testing.T) {
		payload := []byte(`{
			"contents": [
				{
					"role": "model",
					"parts": [
						{"function_call": {"id": "call-1", "args": {}}}
					]
				}
			]
		}`)

		err := ValidateGeminiFunctionCallPairing(payload)
		if err == nil || !strings.Contains(err.Error(), "missing functionCall.name") {
			t.Fatalf("expected missing functionCall.name error, got: %v", err)
		}
	})

	t.Run("orphan snake_case function_response is rejected", func(t *testing.T) {
		payload := []byte(`{
			"contents": [
				{
					"role": "user",
					"parts": [
						{"function_response": {"id": "call-1", "name": "get_weather", "response": {"temp": 72}}}
					]
				}
			]
		}`)

		err := ValidateGeminiFunctionCallPairing(payload)
		if err == nil || !strings.Contains(err.Error(), "functionResponse without preceding functionCall") {
			t.Fatalf("expected orphan functionResponse error, got: %v", err)
		}
	})

	t.Run("interleaved snake_case function_call and function_response is rejected", func(t *testing.T) {
		payload := []byte(`{
			"contents": [
				{
					"role": "model",
					"parts": [
						{"function_call": {"id": "call-1", "name": "get_weather", "args": {}}},
						{"function_response": {"id": "call-1", "name": "get_weather", "response": {}}}
					]
				}
			]
		}`)

		err := ValidateGeminiFunctionCallPairing(payload)
		if err == nil || !strings.Contains(err.Error(), "must not be interleaved") {
			t.Fatalf("expected interleaved error, got: %v", err)
		}
	})

	t.Run("mismatched id between snake_case function_call and function_response is rejected", func(t *testing.T) {
		payload := []byte(`{
			"contents": [
				{
					"role": "model",
					"parts": [
						{"function_call": {"id": "call-1", "name": "get_weather", "args": {}}}
					]
				},
				{
					"role": "user",
					"parts": [
						{"function_response": {"id": "call-2", "name": "get_weather", "response": {}}}
					]
				}
			]
		}`)

		err := ValidateGeminiFunctionCallPairing(payload)
		if err == nil || !strings.Contains(err.Error(), "does not match functionCall.id") {
			t.Fatalf("expected id mismatch error, got: %v", err)
		}
	})
}
