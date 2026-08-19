package executor

import (
	"encoding/binary"
	"fmt"
	"math"
	"strings"

	"google.golang.org/protobuf/encoding/protowire"
)

// Devin Connect-RPC constants.
const (
	devinAPIBaseURL   = "https://server.codeium.com"
	devinChatPath     = "/exa.api_server_pb.ApiServerService/GetChatMessage"
	devinConnectProto = "application/connect+proto"

	// Connect frame flags.
	connectFlagUncompressed = 0x00
	connectFlagCompressed   = 0x01
	connectFlagEndStream    = 0x02

	// ChatMessageRequestType.CASCADE = 5
	devinRequestTypeCascade = 5

	// ConversationalPlannerMode.DEFAULT = 1
	devinPlannerModeDefault = 1

	// ChatMessageSource enum values.
	devinMsgSourceUser         = 1
	devinMsgSourceSystem       = 2
	devinMsgSourceTool         = 4
	devinMsgSourceSystemPrompt = 5

	// StopReason enum values.
	devinStopReasonUnspecified = 0
	devinStopReasonIncomplete  = 1
	devinStopReasonStopPattern = 2
	devinStopReasonMaxTokens   = 3
	devinStopReasonMinLogProb  = 4
	devinStopReasonMaxNewlines = 5
)

// devinMetadata holds the Metadata message fields used by the Devin CLI.
type devinMetadata struct {
	IdeName          string
	ExtensionVersion string
	ApiKey           string
	Locale           string
	OS               string
	IdeVersion       string
	ExtensionName    string
	IdeType          string
	F                string // attestation blob (field 31)
}

// devinCompletionConfig holds CompletionConfiguration fields.
type devinCompletionConfig struct {
	NumCompletions uint64
	MaxTokens      uint64
	MaxNewlines    uint64
	Temperature    float64
	TopK           uint64
	TopP           float64
	StopPatterns   []string
}

// devinChatMessagePrompt holds a ChatMessagePrompt (conversation message).
type devinChatMessagePrompt struct {
	MessageID     string
	Source        int // ChatMessageSource enum
	Prompt        string
	Thinking      string
	Signature     string
	ToolCallID    string
	ToolCalls     []devinToolCall
	OutputID      string
	SignatureType string
}

// devinToolCall holds a ChatToolCall.
type devinToolCall struct {
	ID            string
	Name          string
	ArgumentsJSON string
}

// devinToolDefinition holds a ChatToolDefinition.
type devinToolDefinition struct {
	Name             string
	Description      string
	JSONSchemaString string
}

// devinRequest holds the top-level GetChatMessageRequest fields we send.
type devinRequest struct {
	Metadata           devinMetadata
	Prompt             string
	ChatMessagePrompts []devinChatMessagePrompt
	RequestType        int
	Configuration      devinCompletionConfig
	Tools              []devinToolDefinition
	CascadeID          string
	PlannerMode        int
	ChatModelUID       string
}

// encodeMetadata builds the Metadata protobuf message (field 1 of the request).
func encodeMetadata(m devinMetadata) []byte {
	var b []byte
	b = protowire.AppendTag(b, 1, protowire.BytesType)
	b = protowire.AppendString(b, m.IdeName)
	b = protowire.AppendTag(b, 2, protowire.BytesType)
	b = protowire.AppendString(b, m.ExtensionVersion)
	b = protowire.AppendTag(b, 3, protowire.BytesType)
	b = protowire.AppendString(b, m.ApiKey)
	b = protowire.AppendTag(b, 4, protowire.BytesType)
	b = protowire.AppendString(b, m.Locale)
	b = protowire.AppendTag(b, 5, protowire.BytesType)
	b = protowire.AppendString(b, m.OS)
	b = protowire.AppendTag(b, 7, protowire.BytesType)
	b = protowire.AppendString(b, m.IdeVersion)
	b = protowire.AppendTag(b, 12, protowire.BytesType)
	b = protowire.AppendString(b, m.ExtensionName)
	b = protowire.AppendTag(b, 28, protowire.BytesType)
	b = protowire.AppendString(b, m.IdeType)
	if m.F != "" {
		b = protowire.AppendTag(b, 31, protowire.BytesType)
		b = protowire.AppendString(b, m.F)
	}
	return b
}

// encodeCompletionConfig builds the CompletionConfiguration protobuf message.
func encodeCompletionConfig(c devinCompletionConfig) []byte {
	var b []byte
	b = protowire.AppendTag(b, 1, protowire.VarintType)
	b = protowire.AppendVarint(b, c.NumCompletions)
	b = protowire.AppendTag(b, 2, protowire.VarintType)
	b = protowire.AppendVarint(b, c.MaxTokens)
	b = protowire.AppendTag(b, 3, protowire.VarintType)
	b = protowire.AppendVarint(b, c.MaxNewlines)
	b = protowire.AppendTag(b, 5, protowire.Fixed64Type)
	b = protowire.AppendFixed64(b, math.Float64bits(c.Temperature))
	b = protowire.AppendTag(b, 7, protowire.VarintType)
	b = protowire.AppendVarint(b, c.TopK)
	b = protowire.AppendTag(b, 8, protowire.Fixed64Type)
	b = protowire.AppendFixed64(b, math.Float64bits(c.TopP))
	for _, sp := range c.StopPatterns {
		b = protowire.AppendTag(b, 9, protowire.BytesType)
		b = protowire.AppendString(b, sp)
	}
	return b
}

// encodeToolCall builds a ChatToolCall protobuf message.
func encodeToolCall(tc devinToolCall) []byte {
	var b []byte
	b = protowire.AppendTag(b, 1, protowire.BytesType)
	b = protowire.AppendString(b, tc.ID)
	b = protowire.AppendTag(b, 2, protowire.BytesType)
	b = protowire.AppendString(b, tc.Name)
	b = protowire.AppendTag(b, 3, protowire.BytesType)
	b = protowire.AppendString(b, tc.ArgumentsJSON)
	return b
}

// encodeChatMessagePrompt builds a ChatMessagePrompt protobuf message.
func encodeChatMessagePrompt(p devinChatMessagePrompt) []byte {
	var b []byte
	b = protowire.AppendTag(b, 1, protowire.BytesType)
	b = protowire.AppendString(b, p.MessageID)
	b = protowire.AppendTag(b, 2, protowire.VarintType)
	b = protowire.AppendVarint(b, uint64(p.Source))
	b = protowire.AppendTag(b, 3, protowire.BytesType)
	b = protowire.AppendString(b, p.Prompt)
	if p.Thinking != "" {
		b = protowire.AppendTag(b, 11, protowire.BytesType)
		b = protowire.AppendString(b, p.Thinking)
	}
	if p.Signature != "" {
		b = protowire.AppendTag(b, 12, protowire.BytesType)
		b = protowire.AppendString(b, p.Signature)
	}
	if p.ToolCallID != "" {
		b = protowire.AppendTag(b, 7, protowire.BytesType)
		b = protowire.AppendString(b, p.ToolCallID)
	}
	for _, tc := range p.ToolCalls {
		tcBytes := encodeToolCall(tc)
		b = protowire.AppendTag(b, 6, protowire.BytesType)
		b = protowire.AppendBytes(b, tcBytes)
	}
	if p.OutputID != "" {
		b = protowire.AppendTag(b, 15, protowire.BytesType)
		b = protowire.AppendString(b, p.OutputID)
	}
	if p.SignatureType != "" {
		b = protowire.AppendTag(b, 18, protowire.BytesType)
		b = protowire.AppendString(b, p.SignatureType)
	}
	return b
}

// encodeToolDefinition builds a ChatToolDefinition protobuf message.
func encodeToolDefinition(t devinToolDefinition) []byte {
	var b []byte
	b = protowire.AppendTag(b, 1, protowire.BytesType)
	b = protowire.AppendString(b, t.Name)
	b = protowire.AppendTag(b, 2, protowire.BytesType)
	b = protowire.AppendString(b, t.Description)
	b = protowire.AppendTag(b, 3, protowire.BytesType)
	b = protowire.AppendString(b, t.JSONSchemaString)
	return b
}

// encodeGetChatMessageRequest builds the full GetChatMessageRequest protobuf message.
func encodeGetChatMessageRequest(r devinRequest) []byte {
	var b []byte
	// Field 1: metadata
	metaBytes := encodeMetadata(r.Metadata)
	b = protowire.AppendTag(b, 1, protowire.BytesType)
	b = protowire.AppendBytes(b, metaBytes)

	// Field 2: prompt (system prompt)
	b = protowire.AppendTag(b, 2, protowire.BytesType)
	b = protowire.AppendString(b, r.Prompt)

	// Field 3: chat_message_prompts (repeated)
	for _, p := range r.ChatMessagePrompts {
		pBytes := encodeChatMessagePrompt(p)
		b = protowire.AppendTag(b, 3, protowire.BytesType)
		b = protowire.AppendBytes(b, pBytes)
	}

	// Field 7: request_type
	b = protowire.AppendTag(b, 7, protowire.VarintType)
	b = protowire.AppendVarint(b, uint64(r.RequestType))

	// Field 8: configuration
	cfgBytes := encodeCompletionConfig(r.Configuration)
	b = protowire.AppendTag(b, 8, protowire.BytesType)
	b = protowire.AppendBytes(b, cfgBytes)

	// Field 10: tools (repeated)
	for _, t := range r.Tools {
		tBytes := encodeToolDefinition(t)
		b = protowire.AppendTag(b, 10, protowire.BytesType)
		b = protowire.AppendBytes(b, tBytes)
	}

	// Field 16: cascade_id
	b = protowire.AppendTag(b, 16, protowire.BytesType)
	b = protowire.AppendString(b, r.CascadeID)

	// Field 20: planner_mode
	b = protowire.AppendTag(b, 20, protowire.VarintType)
	b = protowire.AppendVarint(b, uint64(r.PlannerMode))

	// Field 21: chat_model_uid
	b = protowire.AppendTag(b, 21, protowire.BytesType)
	b = protowire.AppendString(b, r.ChatModelUID)

	return b
}

// frameConnectMessage wraps a protobuf payload in a Connect-RPC frame (flag + length + payload).
func frameConnectMessage(payload []byte) []byte {
	frame := make([]byte, 5+len(payload))
	frame[0] = connectFlagUncompressed
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(payload)))
	copy(frame[5:], payload)
	return frame
}

// devinResponseFrame represents a parsed Connect-RPC streaming response frame.
type devinResponseFrame struct {
	Flag    byte
	Payload []byte
}

// readConnectFrame reads a single Connect-RPC frame from a byte buffer.
// Returns the frame and the remaining bytes, or an error if the buffer is too short.
func readConnectFrame(buf []byte) (devinResponseFrame, []byte, error) {
	if len(buf) < 5 {
		return devinResponseFrame{}, nil, fmt.Errorf("connect frame: buffer too short (%d bytes)", len(buf))
	}
	flag := buf[0]
	length := binary.BigEndian.Uint32(buf[1:5])
	if len(buf) < 5+int(length) {
		return devinResponseFrame{}, nil, fmt.Errorf("connect frame: incomplete frame (need %d, have %d)", 5+length, len(buf))
	}
	payload := buf[5 : 5+length]
	remaining := buf[5+length:]
	return devinResponseFrame{Flag: flag, Payload: payload}, remaining, nil
}

// devinStreamChunk represents a parsed GetChatMessageResponse with the fields we care about.
type devinStreamChunk struct {
	MessageID      string
	DeltaText      string
	DeltaThinking  string
	DeltaSignature string
	StopReason     int
	DeltaToolCalls []devinToolCall
	OutputID       string
	RequestID      string
	Usage          *devinUsageStats
	IsTrailer      bool
	TrailerJSON    string
}

// devinUsageStats holds ModelUsageStats fields.
type devinUsageStats struct {
	InputTokens      uint64
	OutputTokens     uint64
	CacheReadTokens  uint64
	CacheWriteTokens uint64
	ModelUID         string
}

// parseGetChatMessageResponse parses a GetChatMessageResponse protobuf message.
func parseGetChatMessageResponse(data []byte) (*devinStreamChunk, error) {
	chunk := &devinStreamChunk{}
	for len(data) > 0 {
		num, wireType, length := protowire.ConsumeTag(data)
		if length < 0 {
			return nil, protowire.ParseError(length)
		}
		data = data[length:]
		switch wireType {
		case protowire.BytesType:
			val, valLen := protowire.ConsumeBytes(data)
			if valLen < 0 {
				return nil, protowire.ParseError(valLen)
			}
			data = data[valLen:]
			switch num {
			case 1: // message_id
				chunk.MessageID = string(val)
			case 3: // delta_text
				chunk.DeltaText = string(val)
			case 6: // delta_tool_calls (repeated)
				tc, err := parseToolCall(val)
				if err == nil {
					chunk.DeltaToolCalls = append(chunk.DeltaToolCalls, tc)
				}
			case 7: // usage
				usage, err := parseUsageStats(val)
				if err == nil {
					chunk.Usage = usage
				}
			case 9: // delta_thinking
				chunk.DeltaThinking = string(val)
			case 10: // delta_signature
				chunk.DeltaSignature = string(val)
			case 15: // output_id
				chunk.OutputID = string(val)
			case 17: // request_id
				chunk.RequestID = string(val)
			}
		case protowire.VarintType:
			val, valLen := protowire.ConsumeVarint(data)
			if valLen < 0 {
				return nil, protowire.ParseError(valLen)
			}
			data = data[valLen:]
			switch num {
			case 5: // stop_reason
				chunk.StopReason = int(val)
			}
		case protowire.Fixed64Type:
			val, valLen := protowire.ConsumeFixed64(data)
			if valLen < 0 {
				return nil, protowire.ParseError(valLen)
			}
			data = data[valLen:]
			_ = val // latency (field 12), not needed
		default:
			// Skip unknown fields
			skipLen := protowire.ConsumeFieldValue(num, wireType, data)
			if skipLen < 0 {
				return nil, protowire.ParseError(skipLen)
			}
			data = data[skipLen:]
		}
	}
	return chunk, nil
}

// parseToolCall parses a ChatToolCall protobuf message.
func parseToolCall(data []byte) (devinToolCall, error) {
	var tc devinToolCall
	for len(data) > 0 {
		num, wireType, length := protowire.ConsumeTag(data)
		if length < 0 {
			return tc, protowire.ParseError(length)
		}
		data = data[length:]
		if wireType != protowire.BytesType {
			skipLen := protowire.ConsumeFieldValue(num, wireType, data)
			if skipLen < 0 {
				return tc, protowire.ParseError(skipLen)
			}
			data = data[skipLen:]
			continue
		}
		val, valLen := protowire.ConsumeBytes(data)
		if valLen < 0 {
			return tc, protowire.ParseError(valLen)
		}
		data = data[valLen:]
		switch num {
		case 1:
			tc.ID = string(val)
		case 2:
			tc.Name = string(val)
		case 3:
			tc.ArgumentsJSON = string(val)
		}
	}
	return tc, nil
}

// parseUsageStats parses a ModelUsageStats protobuf message.
func parseUsageStats(data []byte) (*devinUsageStats, error) {
	usage := &devinUsageStats{}
	for len(data) > 0 {
		num, wireType, length := protowire.ConsumeTag(data)
		if length < 0 {
			return nil, protowire.ParseError(length)
		}
		data = data[length:]
		switch wireType {
		case protowire.VarintType:
			val, valLen := protowire.ConsumeVarint(data)
			if valLen < 0 {
				return nil, protowire.ParseError(valLen)
			}
			data = data[valLen:]
			switch num {
			case 2:
				usage.InputTokens = val
			case 3:
				usage.OutputTokens = val
			case 4:
				usage.CacheWriteTokens = val
			case 5:
				usage.CacheReadTokens = val
			}
		case protowire.BytesType:
			val, valLen := protowire.ConsumeBytes(data)
			if valLen < 0 {
				return nil, protowire.ParseError(valLen)
			}
			data = data[valLen:]
			if num == 9 { // model_uid
				usage.ModelUID = string(val)
			}
		default:
			skipLen := protowire.ConsumeFieldValue(num, wireType, data)
			if skipLen < 0 {
				return nil, protowire.ParseError(skipLen)
			}
			data = data[skipLen:]
		}
	}
	return usage, nil
}

// devinStopReasonToString maps Devin stop reasons to OpenAI-compatible strings.
func devinStopReasonToString(reason int) string {
	switch reason {
	case devinStopReasonStopPattern:
		return "stop"
	case devinStopReasonMaxTokens:
		return "length"
	case devinStopReasonMaxNewlines:
		return "length"
	case devinStopReasonIncomplete:
		return "length"
	default:
		return "stop"
	}
}

// generateAttestationF generates a deterministic 366-byte hex string (732 chars)
// from the given install ID. This mimics the Devin CLI's attestation field (f).
func generateAttestationF(installID string) string {
	// Simple deterministic hash-based generation.
	// The real CLI uses a machine-specific algorithm we can't reverse-engineer,
	// but the server accepts any 732-char hex string.
	if installID == "" {
		installID = "cliproxyapi-default-install"
	}
	// Use a simple FNV-based expansion to fill 732 hex chars.
	hash := fnvHash("devin-fingerprint" + installID)
	hex := fmt.Sprintf("%016x", hash)
	for len(hex) < 732 {
		hash = fnvHash(hex)
		hex += fmt.Sprintf("%016x", hash)
	}
	return hex[:732]
}

// fnvHash is a simple 64-bit FNV-1a hash.
func fnvHash(s string) uint64 {
	h := uint64(14695981039346656037)
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	return h
}

// normalizeDevinSessionToken ensures the token has the devin-session-token$ prefix.
func normalizeDevinSessionToken(token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return ""
	}
	if !strings.HasPrefix(token, "devin-session-token$") {
		token = "devin-session-token$" + token
	}
	return token
}
