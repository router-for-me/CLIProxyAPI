package executor

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// TestAntigravityPayloadBuilder_AllQuadrants pins the four
// (sanitized, plain) x (claude, gemini) paths the Phase D builder
// extraction must keep byte-equivalent to the prior inline branches.
// Codex Phase D round-1 IMPORTANT #1.
//
// Each row asserts:
//   - which body source (strings.Reader vs bytes.Reader) is produced
//   - the final on-the-wire bytes contain the expected model-specific
//     transform (Claude: VALIDATED tool-config; non-Claude:
//     maxOutputTokens deleted)
//   - in the sanitized branch, schema cleanup ran (parametersJsonSchema
//     renamed to parameters)
//   - the plain branch did NOT touch the schema (no tools field, no
//     parametersJsonSchema rewrite)
func TestAntigravityPayloadBuilder_AllQuadrants(t *testing.T) {
	const (
		sanitizedPayload = `{
			"request": {
				"tools": [
					{"function_declarations": [{"name": "f", "parametersJsonSchema": {"type": "object"}}]}
				],
				"generationConfig": {"maxOutputTokens": 256}
			}
		}`
		plainPayload = `{
			"request": {
				"contents": [{"role": "user", "parts": [{"text": "hi"}]}],
				"generationConfig": {"maxOutputTokens": 256}
			}
		}`
	)

	cases := []struct {
		name              string
		modelName         string
		payload           string
		wantSanitized     bool
		wantClaudeMode    bool // VALIDATED tool-config mode set
		wantMaxOutputGone bool // generationConfig.maxOutputTokens deleted
	}{
		{
			name:              "sanitized_claude",
			modelName:         "claude-sonnet-4-5",
			payload:           sanitizedPayload,
			wantSanitized:     true,
			wantClaudeMode:    true,
			wantMaxOutputGone: false, // claude path SETS toolConfig but does not delete maxOutputTokens
		},
		{
			name:              "sanitized_gemini",
			modelName:         "gemini-2.5-pro",
			payload:           sanitizedPayload,
			wantSanitized:     true,
			wantClaudeMode:    false,
			wantMaxOutputGone: true,
		},
		{
			name:              "plain_claude",
			modelName:         "claude-sonnet-4-5",
			payload:           plainPayload,
			wantSanitized:     false,
			wantClaudeMode:    true,
			wantMaxOutputGone: false,
		},
		{
			name:              "plain_gemini",
			modelName:         "gemini-2.5-pro",
			payload:           plainPayload,
			wantSanitized:     false,
			wantClaudeMode:    false,
			wantMaxOutputGone: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			builder := &antigravityPayloadBuilder{
				payload:    []byte(tc.payload),
				modelName:  tc.modelName,
				requestLog: true,
			}
			body, logBytes := builder.build()

			// Body type per branch: sanitized goes through string form,
			// plain stays in bytes form.
			switch body.(type) {
			case *strings.Reader:
				if !tc.wantSanitized {
					t.Fatalf("expected *bytes.Reader for plain path, got *strings.Reader")
				}
			case *bytes.Reader:
				if tc.wantSanitized {
					t.Fatalf("expected *strings.Reader for sanitized path, got *bytes.Reader")
				}
			default:
				t.Fatalf("unexpected body type %T", body)
			}

			raw, err := io.ReadAll(body)
			if err != nil {
				t.Fatalf("read body: %v", err)
			}

			// requestLog copy must match the wire bytes byte-for-byte.
			if string(logBytes) != string(raw) {
				t.Fatalf("requestLog bytes mismatch\nwire:  %q\nlog:   %q", raw, logBytes)
			}

			// Schema sanitization: parametersJsonSchema must be renamed
			// to parameters if and only if the sanitized path ran.
			hasJSONSchema := gjson.GetBytes(raw, "request.tools.0.function_declarations.0.parametersJsonSchema").Exists()
			hasParameters := gjson.GetBytes(raw, "request.tools.0.function_declarations.0.parameters").Exists()
			if tc.wantSanitized {
				if hasJSONSchema {
					t.Fatalf("parametersJsonSchema should be renamed in sanitized path")
				}
				if !hasParameters {
					t.Fatalf("parameters should exist after rename in sanitized path")
				}
			}

			// Claude transform: VALIDATED tool-config mode set.
			mode := gjson.GetBytes(raw, "request.toolConfig.functionCallingConfig.mode")
			if tc.wantClaudeMode {
				if mode.String() != "VALIDATED" {
					t.Fatalf("expected toolConfig.functionCallingConfig.mode=VALIDATED, got %q", mode.String())
				}
			}

			// Non-Claude transform: maxOutputTokens deleted.
			maxOut := gjson.GetBytes(raw, "request.generationConfig.maxOutputTokens")
			if tc.wantMaxOutputGone {
				if maxOut.Exists() {
					t.Fatalf("expected generationConfig.maxOutputTokens deleted, got %v", maxOut.Raw)
				}
			} else if tc.wantClaudeMode {
				// Claude path keeps maxOutputTokens (only sets VALIDATED).
				if !maxOut.Exists() {
					t.Fatalf("Claude path should preserve maxOutputTokens, got missing")
				}
			}
		})
	}
}

// TestAntigravityPayloadBuilder_RequestLogOff_NoLogBytes asserts that
// when cfg.RequestLog is false (requestLog: false in the builder), no
// payload copy is allocated for logging. Phase D round-1 IMPORTANT #1
// follow-on: gates the requestLog branch in both buildSanitized and
// buildPlain.
func TestAntigravityPayloadBuilder_RequestLogOff_NoLogBytes(t *testing.T) {
	cases := []struct {
		name      string
		modelName string
		payload   string
	}{
		{"sanitized_off", "claude-sonnet-4-5", `{"request":{"tools":[{"x": 1}]}}`},
		{"plain_off", "gemini-2.5-pro", `{"request":{"contents":[]}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			builder := &antigravityPayloadBuilder{
				payload:    []byte(tc.payload),
				modelName:  tc.modelName,
				requestLog: false,
			}
			_, logBytes := builder.build()
			if logBytes != nil {
				t.Fatalf("expected nil logBytes when requestLog=false, got %d bytes", len(logBytes))
			}
		})
	}
}

// TestAntigravityPayloadBuilder_UseAntigravitySchema covers the dialect
// dispatch between CleanJSONSchemaForAntigravity (Claude + gemini-3-pro
// family) and CleanJSONSchemaForGemini (everything else).
func TestAntigravityPayloadBuilder_UseAntigravitySchema(t *testing.T) {
	cases := []struct {
		modelName string
		want      bool
	}{
		{"claude-sonnet-4-5", true},
		{"claude-opus-4-6", true},
		{"gemini-3-pro", true},
		{"gemini-3-pro-preview", true},
		{"gemini-3.1-pro", true},
		{"gemini-2.5-pro", false},
		{"gemini-3.1-flash-image", false},
		{"gpt-5.5", false},
	}
	for _, tc := range cases {
		t.Run(tc.modelName, func(t *testing.T) {
			b := &antigravityPayloadBuilder{modelName: tc.modelName}
			if got := b.useAntigravitySchema(); got != tc.want {
				t.Fatalf("useAntigravitySchema(%q) = %v, want %v", tc.modelName, got, tc.want)
			}
		})
	}
}
