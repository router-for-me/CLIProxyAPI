package executor

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/tidwall/gjson"
)

func TestCapBedrockRequestMaxTokens_ClampsWhenExceedsLimit(t *testing.T) {
	input := []byte(`{"model":"deepseek-r1","max_tokens":64000,"messages":[{"role":"user","content":"hi"}]}`)

	out := capBedrockRequestMaxTokens(input, 32768)

	if got := gjson.GetBytes(out, "max_tokens").Int(); got != 32767 {
		t.Fatalf("max_tokens = %d, want %d", got, 32767)
	}
}

func TestCapBedrockRequestMaxTokens_PreservesWhenWithinLimit(t *testing.T) {
	input := []byte(`{"model":"deepseek-r1","max_tokens":3000,"messages":[{"role":"user","content":"hi"}]}`)

	out := capBedrockRequestMaxTokens(input, 32768)

	if got := gjson.GetBytes(out, "max_tokens").Int(); got != 3000 {
		t.Fatalf("max_tokens = %d, want %d", got, 3000)
	}
}

func TestCapBedrockRequestMaxTokens_ClampsLimitBoundary(t *testing.T) {
	input := []byte(`{"model":"deepseek-r1","max_tokens":32768,"messages":[{"role":"user","content":"hi"}]}`)

	out := capBedrockRequestMaxTokens(input, 32768)

	if got := gjson.GetBytes(out, "max_tokens").Int(); got != 32767 {
		t.Fatalf("max_tokens = %d, want %d", got, 32767)
	}
}

func TestResolveBedrockMaxCompletionTokens_UsesGeoPrefixedFallbackLookup(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	clientID := "test-bedrock-max-token-lookup"
	reg.RegisterClient(clientID, "aws-bedrock", []*registry.ModelInfo{
		{
			ID:                  "deepseek.r1-v1:0",
			Object:              "model",
			OwnedBy:             "deepseek",
			Type:                "aws-bedrock",
			MaxCompletionTokens: 32768,
			UserDefined:         true,
		},
	})
	defer reg.UnregisterClient(clientID)

	got := resolveBedrockMaxCompletionTokens("us.deepseek.r1-v1:0", nil)
	if got != 32768 {
		t.Fatalf("resolveBedrockMaxCompletionTokens() = %d, want %d", got, 32768)
	}
}

func TestResolveBedrockMaxCompletionTokens_UsesStaticFallbackForGeoPrefixedModel(t *testing.T) {
	got := resolveBedrockMaxCompletionTokens("us.deepseek.r1-v1:0", nil)
	if got != 32768 {
		t.Fatalf("resolveBedrockMaxCompletionTokens() = %d, want %d", got, 32768)
	}
}

func TestResolveBedrockMaxCompletionTokens_UserDefinedFallsBackToStaticByName(t *testing.T) {
	userDefined := &registry.ModelInfo{
		ID:   "deepseek-r1",
		Name: "us.deepseek.r1-v1:0",
		Type: "aws-bedrock",
	}
	got := resolveBedrockMaxCompletionTokens("deepseek-r1", userDefined)
	if got != 32768 {
		t.Fatalf("resolveBedrockMaxCompletionTokens() = %d, want %d", got, 32768)
	}
}

func TestStripBedrockUnsupportedToolConfig_StripsToolConfigWhenModelDoesNotSupport(t *testing.T) {
	input := []byte(`{"inferenceConfig":{"maxTokens":2048},"messages":[{"role":"user","content":[{"text":"hi"}]}],"toolConfig":{"tools":[{"toolSpec":{"name":"Read","inputSchema":{"json":{"type":"object"}}}}]}}`)
	modelInfo := &registry.ModelInfo{
		ID:                  "deepseek.r1-v1:0",
		Type:                "deepseek",
		SupportedParameters: []string{"max_tokens", "temperature"},
	}

	out := stripBedrockUnsupportedToolConfig(input, modelInfo)
	if gjson.GetBytes(out, "toolConfig").Exists() {
		t.Fatalf("toolConfig should be stripped for models without tools support: %s", string(out))
	}
}

func TestStripBedrockUnsupportedToolConfig_PreservesToolConfigWhenModelSupports(t *testing.T) {
	input := []byte(`{"inferenceConfig":{"maxTokens":2048},"messages":[{"role":"user","content":[{"text":"hi"}]}],"toolConfig":{"tools":[{"toolSpec":{"name":"Read","inputSchema":{"json":{"type":"object"}}}}]}}`)
	modelInfo := &registry.ModelInfo{
		ID:                  "anthropic.claude-sonnet-4-20250514-v1:0",
		Type:                "claude",
		SupportedParameters: []string{"tools", "max_tokens"},
	}

	out := stripBedrockUnsupportedToolConfig(input, modelInfo)
	if !gjson.GetBytes(out, "toolConfig").Exists() {
		t.Fatalf("toolConfig should be preserved for models with tools support: %s", string(out))
	}
}

func TestReadBedrockEvent_MalformedHeaderReturnsError(t *testing.T) {
	// Header declares key length 5 but only carries 1 byte of key payload.
	headers := []byte{5, 'x'}
	payload := []byte(`{"x":1}`)
	event := buildBedrockEventForTest(t, headers, payload)

	_, err := readBedrockEvent(bytes.NewReader(event))
	if err == nil {
		t.Fatal("expected malformed header error, got nil")
	}
	if !strings.Contains(err.Error(), "malformed header") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReadBedrockEvent_WrapsEventTypeWithoutDroppingPayloadFields(t *testing.T) {
	headers := encodeEventTypeHeaderForTest("contentBlockDelta")
	payload := []byte(`{"delta":{"text":"hello"}}`)
	event := buildBedrockEventForTest(t, headers, payload)

	got, err := readBedrockEvent(bytes.NewReader(event))
	if err != nil {
		t.Fatalf("readBedrockEvent() error = %v", err)
	}
	if gjson.GetBytes(got, "type").String() != "contentBlockDelta" {
		t.Fatalf("type = %q, want %q, body=%s", gjson.GetBytes(got, "type").String(), "contentBlockDelta", string(got))
	}
	if gjson.GetBytes(got, "contentBlockDelta.delta.text").String() != "hello" {
		t.Fatalf("missing wrapped payload: %s", string(got))
	}
	if gjson.GetBytes(got, "delta.text").String() != "hello" {
		t.Fatalf("missing top-level payload fields: %s", string(got))
	}
}

func buildBedrockEventForTest(t *testing.T, headers []byte, payload []byte) []byte {
	t.Helper()
	totalLen := uint32(12 + len(headers) + len(payload) + 4)
	prelude := make([]byte, 12)
	binary.BigEndian.PutUint32(prelude[0:4], totalLen)
	binary.BigEndian.PutUint32(prelude[4:8], uint32(len(headers)))
	binary.BigEndian.PutUint32(prelude[8:12], crc32.ChecksumIEEE(prelude[:8]))

	message := make([]byte, 0, len(headers)+len(payload)+4)
	message = append(message, headers...)
	message = append(message, payload...)
	message = append(message, 0, 0, 0, 0) // message CRC placeholder

	full := make([]byte, 0, len(prelude)+len(message))
	full = append(full, prelude...)
	full = append(full, message...)
	msgCRC := crc32.ChecksumIEEE(full[:len(full)-4])
	binary.BigEndian.PutUint32(full[len(full)-4:], msgCRC)
	return full
}

func encodeEventTypeHeaderForTest(eventType string) []byte {
	key := []byte(":event-type")
	val := []byte(eventType)
	out := make([]byte, 0, 1+len(key)+1+2+len(val))
	out = append(out, byte(len(key)))
	out = append(out, key...)
	out = append(out, byte(7)) // string header type
	lenBuf := make([]byte, 2)
	binary.BigEndian.PutUint16(lenBuf, uint16(len(val)))
	out = append(out, lenBuf...)
	out = append(out, val...)
	return out
}
