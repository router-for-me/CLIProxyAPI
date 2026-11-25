package common

import (
	"crypto/rand"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

const tooluLetters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// GenerateClaudeToolCallID generates a random tool use ID prefixed with toolu_
// using rejection sampling to guarantee a uniform distribution across the 62 alphanumeric characters.
func GenerateClaudeToolCallID() string {
	const maxValidByte = 256 - (256 % len(tooluLetters)) // 248: exact multiple of 62
	var b strings.Builder
	b.Grow(len("toolu_") + 24)
	b.WriteString("toolu_")

	var buf [32]byte
	n := 0
	for n < 24 {
		_, _ = rand.Read(buf[:])
		for _, bVal := range buf {
			if int(bVal) < maxValidByte {
				b.WriteByte(tooluLetters[int(bVal)%len(tooluLetters)])
				n++
				if n == 24 {
					break
				}
			}
		}
	}
	return b.String()
}

// RequestModelName returns the model name from the original request, falling
// back to the translated request when the original request is unavailable.
func RequestModelName(originalRequestRawJSON, requestRawJSON []byte) string {
	for _, rawJSON := range [][]byte{originalRequestRawJSON, requestRawJSON} {
		if modelName := requestModelName(rawJSON); modelName != "" {
			return modelName
		}
	}
	return ""
}

func requestModelName(rawJSON []byte) string {
	if len(rawJSON) == 0 || !ojson.ValidBytes(rawJSON) {
		return ""
	}

	root := util.ParseGJSONBytesNoCopy(rawJSON)
	for _, path := range []string{"model", "request.model"} {
		model := root.Get(path)
		if model.Type == ojson.String && strings.TrimSpace(model.String()) != "" {
			return strings.Clone(model.String())
		}
	}
	return ""
}

// ClaudeMaxTokensRaw returns the raw JSON number token for a Claude max_tokens
// field so translators preserve native values (including 0) without float
// reformatting, silent truncation, or arbitrary clamping.
func ClaudeMaxTokensRaw(v ojson.Result) (string, bool) {
	if !v.Exists() || v.Type != ojson.Number || v.Raw == "" {
		return "", false
	}
	return v.Raw, true
}

// ClaudeStopSequences extracts non-empty string entries from a Claude stop_sequences array.
func ClaudeStopSequences(v ojson.Result) []string {
	if !v.Exists() || !v.IsArray() {
		return nil
	}
	var sequences []string
	v.ForEach(func(_, item ojson.Result) bool {
		if item.Type == ojson.String && item.String() != "" {
			sequences = append(sequences, item.String())
		}
		return true
	})
	if len(sequences) == 0 {
		return nil
	}
	return sequences
}

// ClaudeOutputJSONSchema returns the schema object when output_config.format
// specifies a valid json_schema format.
func ClaudeOutputJSONSchema(format ojson.Result) (ojson.Result, bool) {
	if !format.Exists() || !format.IsObject() {
		return ojson.Result{}, false
	}
	if !strings.EqualFold(strings.TrimSpace(format.Get("type").String()), "json_schema") {
		return ojson.Result{}, false
	}
	schema := format.Get("schema")
	if !schema.Exists() || !schema.IsObject() {
		return ojson.Result{}, false
	}
	return schema, true
}
