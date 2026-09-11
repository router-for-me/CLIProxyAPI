package helps

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"runtime"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	"google.golang.org/protobuf/encoding/protowire"
)

const (
	// ConnectFlagData marks an uncompressed Connect-proto data frame.
	ConnectFlagData byte = 0x00
	// ConnectFlagCompressed marks a gzipped Connect-proto frame.
	ConnectFlagCompressed byte = 0x01
	// ConnectFlagEndStream marks the terminal Connect-proto trailer frame.
	ConnectFlagEndStream byte = 0x02

	// DevinDefaultBaseURL is the default upstream Codeium/Devin endpoint.
	DevinDefaultBaseURL = "https://server.codeium.com"
	// DevinChatPath is the Connect-RPC endpoint for chat completions.
	DevinChatPath = "/exa.api_server_pb.ApiServerService/GetChatMessage"

	// DevinDefaultClientName is the client identifier declared in metadata.
	DevinDefaultClientName = "chisel"
	// DevinDefaultClientVersion is the client version declared in metadata.
	DevinDefaultClientVersion = "3000.10.21"
	// DevinFingerprintHexLen is the mandatory 732-hex-character length for metadata #31.
	DevinFingerprintHexLen = 732

	// DevinDefaultMaxTokens is the fallback max completion tokens.
	DevinDefaultMaxTokens = 128000

	maxConnectFrameSize      = 16 * 1024 * 1024
	maxDecompressedFrameSize = 64 * 1024 * 1024
)

// DevinTool represents a tool definition in GetChatMessageRequest.
type DevinTool struct {
	Name        string
	Description string
	Parameters  []byte
}

// DevinToolCall represents a tool call in a ChatMessagePrompt.
type DevinToolCall struct {
	ID        string
	Name      string
	Arguments string
}

// DevinToolCallDelta represents a streaming tool call chunk from response Field 6.
type DevinToolCallDelta struct {
	ID        string
	Name      string
	Arguments string
	Index     int
}

// DevinImage represents an image attachment in a DevinPrompt (Prompt #10).
type DevinImage struct {
	Base64Data string
	MimeType   string
}

// DevinPrompt represents a single turn in the request history (repeated Field 3).
type DevinPrompt struct {
	MessageID     string
	Source        int // 1=user, 2=assistant, 4=tool
	Content       string
	Images        []DevinImage
	ToolCalls     []DevinToolCall
	ToolCallID    string // For source=4 (tool result)
	Thinking      string
	Signature     []byte
	SignatureType string
}

// DevinUsage captures token accounting from response Field 7.
type DevinUsage struct {
	PromptTokens     int64
	CompletionTokens int64
	CachedTokens     int64
	StatusCode       uint64
	RequestID        string
	ModelName        string
}

// DevinFrameResult represents decoded content from a single Connect-proto frame.
type DevinFrameResult struct {
	OutputID                string
	Timestamp               uint64
	ContentText             string
	DeltaTokens             uint64
	StopReason              uint64 // 2/4=stop, 10=tool_calls
	ToolCallDeltas          []DevinToolCallDelta
	ThinkingText            string
	DeltaSignature          []byte
	DeltaSignatureType      string
	Latency                 float64
	MessageID               string
	Usage                   *DevinUsage
	ResponseDimensionGroups []byte
}

// GenerateDevinDeviceFingerprint generates a stable 732-character hex device fingerprint.
func GenerateDevinDeviceFingerprint(seed string) string {
	if seed == "" {
		seed = uuid.New().String()
	}
	var sb strings.Builder
	counter := 0
	for sb.Len() < DevinFingerprintHexLen {
		h := sha256.Sum256([]byte(fmt.Sprintf("%s-%d", seed, counter)))
		sb.WriteString(hex.EncodeToString(h[:]))
		counter++
	}
	// Truncate to exact 732 chars (11 full 64-char sha256 hex blocks + 28 chars of the 12th block) to match Devin CLI.
	return sb.String()[:DevinFingerprintHexLen]
}

// WrapConnectEnvelope wraps raw payload bytes into a standard 5-byte Connect envelope:
// [1 byte flag: 0x00] + [4 byte big-endian length] + [payload].
func WrapConnectEnvelope(protoBytes []byte) []byte {
	return WrapConnectEnvelopeWithFlag(ConnectFlagData, protoBytes)
}

// WrapConnectEnvelopeWithFlag wraps payload bytes with an explicit Connect flag.
func WrapConnectEnvelopeWithFlag(flag byte, protoBytes []byte) []byte {
	header := make([]byte, 5, 5+len(protoBytes))
	header[0] = flag
	binary.BigEndian.PutUint32(header[1:5], uint32(len(protoBytes)))
	return append(header, protoBytes...)
}

// ReadConnectFrame reads a single framed message from a Connect-proto stream.
func ReadConnectFrame(r io.Reader) (flag byte, payload []byte, err error) {
	header := make([]byte, 5)
	if _, err := io.ReadFull(r, header); err != nil {
		return 0, nil, err
	}
	flag = header[0]
	length := binary.BigEndian.Uint32(header[1:5])
	if length > maxConnectFrameSize {
		return flag, nil, fmt.Errorf("connect frame length %d exceeds maximum limit (%d)", length, maxConnectFrameSize)
	}

	payload = make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}

	if flag&ConnectFlagCompressed != 0 {
		gz, errGz := gzip.NewReader(bytes.NewReader(payload))
		if errGz != nil {
			return flag, nil, fmt.Errorf("decompress gzip connect frame: %w", errGz)
		}
		defer func() { _ = gz.Close() }()

		limitedReader := io.LimitReader(gz, maxDecompressedFrameSize+1)
		decompressed, errRead := io.ReadAll(limitedReader)
		if errRead != nil {
			return flag, nil, fmt.Errorf("read decompressed connect frame: %w", errRead)
		}
		if len(decompressed) > maxDecompressedFrameSize {
			return flag, nil, fmt.Errorf("decompressed frame size exceeds maximum limit (%d)", maxDecompressedFrameSize)
		}
		payload = decompressed
	}

	return flag, payload, nil
}

// BuildDevinGetChatMessageRequest encodes an entire GetChatMessageRequest protobuf payload.
func BuildDevinGetChatMessageRequest(
	sessionToken string,
	deviceSeed string,
	chatModelUID string,
	systemPrompt string,
	prompts []DevinPrompt,
	tools []DevinTool,
	temperature *float64,
	maxTokens int,
	sessionID string,
	cascadeID string,
	matcher *SensitiveWordMatcher,
) []byte {
	if maxTokens <= 0 {
		maxTokens = DevinDefaultMaxTokens
	}
	if sessionID == "" {
		sessionID = uuid.New().String()
	}
	if cascadeID == "" {
		cascadeID = sessionID
	}
	deviceFingerprint := GenerateDevinDeviceFingerprint(deviceSeed)
	osName := runtime.GOOS

	var reqBytes []byte

	// 1. ClientMetadata (Field 1)
	var f1Bytes []byte
	f1Bytes = protowire.AppendTag(f1Bytes, 1, protowire.BytesType)
	f1Bytes = protowire.AppendString(f1Bytes, "devin-cli")

	f1Bytes = protowire.AppendTag(f1Bytes, 2, protowire.BytesType)
	f1Bytes = protowire.AppendString(f1Bytes, DevinDefaultClientVersion)

	f1Bytes = protowire.AppendTag(f1Bytes, 3, protowire.BytesType)
	f1Bytes = protowire.AppendString(f1Bytes, sessionToken)

	f1Bytes = protowire.AppendTag(f1Bytes, 4, protowire.BytesType)
	f1Bytes = protowire.AppendString(f1Bytes, "en")

	f1Bytes = protowire.AppendTag(f1Bytes, 5, protowire.BytesType)
	f1Bytes = protowire.AppendString(f1Bytes, osName)

	f1Bytes = protowire.AppendTag(f1Bytes, 7, protowire.BytesType)
	f1Bytes = protowire.AppendString(f1Bytes, DevinDefaultClientVersion)

	f1Bytes = protowire.AppendTag(f1Bytes, 12, protowire.BytesType)
	f1Bytes = protowire.AppendString(f1Bytes, DevinDefaultClientName)

	f1Bytes = protowire.AppendTag(f1Bytes, 28, protowire.BytesType)
	f1Bytes = protowire.AppendString(f1Bytes, DevinDefaultClientName)

	f1Bytes = protowire.AppendTag(f1Bytes, 31, protowire.BytesType)
	f1Bytes = protowire.AppendString(f1Bytes, deviceFingerprint)

	reqBytes = protowire.AppendTag(reqBytes, 1, protowire.BytesType)
	reqBytes = protowire.AppendBytes(reqBytes, f1Bytes)

	// 2. System prompt (Field 2)
	if systemPrompt != "" {
		sanitized := SanitizeDevinSystemPrompt(systemPrompt, matcher)
		if sanitized != "" {
			reqBytes = protowire.AppendTag(reqBytes, 2, protowire.BytesType)
			reqBytes = protowire.AppendString(reqBytes, sanitized)
		}
	}

	// 3. Repeated History Prompts (Field 3)
	for _, p := range prompts {
		var pBytes []byte

		msgID := p.MessageID
		if msgID == "" {
			msgID = uuid.New().String()
		}
		pBytes = protowire.AppendTag(pBytes, 1, protowire.BytesType)
		pBytes = protowire.AppendString(pBytes, msgID)

		source := p.Source
		if source <= 0 {
			source = 1 // default to user
		}
		pBytes = protowire.AppendTag(pBytes, 2, protowire.VarintType)
		pBytes = protowire.AppendVarint(pBytes, uint64(source))

		content := p.Content
		pBytes = protowire.AppendTag(pBytes, 3, protowire.BytesType)
		pBytes = protowire.AppendString(pBytes, content)

		for _, tc := range p.ToolCalls {
			var tcBytes []byte
			if tc.ID != "" {
				tcBytes = protowire.AppendTag(tcBytes, 1, protowire.BytesType)
				tcBytes = protowire.AppendString(tcBytes, tc.ID)
			}
			if tc.Name != "" {
				tcBytes = protowire.AppendTag(tcBytes, 2, protowire.BytesType)
				tcBytes = protowire.AppendString(tcBytes, tc.Name)
			}
			if tc.Arguments != "" {
				tcBytes = protowire.AppendTag(tcBytes, 3, protowire.BytesType)
				tcBytes = protowire.AppendString(tcBytes, tc.Arguments)
			}
			pBytes = protowire.AppendTag(pBytes, 6, protowire.BytesType)
			pBytes = protowire.AppendBytes(pBytes, tcBytes)
		}

		if p.ToolCallID != "" {
			pBytes = protowire.AppendTag(pBytes, 7, protowire.BytesType)
			pBytes = protowire.AppendString(pBytes, p.ToolCallID)
		}

		for _, img := range p.Images {
			data := strings.TrimSpace(img.Base64Data)
			if data == "" {
				continue
			}
			var imgBytes []byte
			imgBytes = protowire.AppendTag(imgBytes, 1, protowire.BytesType)
			imgBytes = protowire.AppendString(imgBytes, data)

			mime := strings.TrimSpace(img.MimeType)
			if mime == "" {
				mime = "image/png"
			}
			imgBytes = protowire.AppendTag(imgBytes, 2, protowire.BytesType)
			imgBytes = protowire.AppendString(imgBytes, mime)

			pBytes = protowire.AppendTag(pBytes, 10, protowire.BytesType)
			pBytes = protowire.AppendBytes(pBytes, imgBytes)
		}

		if p.Thinking != "" {
			pBytes = protowire.AppendTag(pBytes, 11, protowire.BytesType)
			pBytes = protowire.AppendString(pBytes, p.Thinking)
		}

		if len(p.Signature) > 0 {
			pBytes = protowire.AppendTag(pBytes, 12, protowire.BytesType)
			pBytes = protowire.AppendBytes(pBytes, p.Signature)
		}

		if p.SignatureType != "" {
			pBytes = protowire.AppendTag(pBytes, 18, protowire.BytesType)
			pBytes = protowire.AppendString(pBytes, p.SignatureType)
		}

		reqBytes = protowire.AppendTag(reqBytes, 3, protowire.BytesType)
		reqBytes = protowire.AppendBytes(reqBytes, pBytes)
	}

	// 4. Fixed flags (Field 7: Varint 5)
	reqBytes = protowire.AppendTag(reqBytes, 7, protowire.VarintType)
	reqBytes = protowire.AppendVarint(reqBytes, 5)

	// 5. Completion config (Field 8)
	var f8Bytes []byte
	f8Bytes = protowire.AppendTag(f8Bytes, 1, protowire.VarintType)
	f8Bytes = protowire.AppendVarint(f8Bytes, 1)

	f8Bytes = protowire.AppendTag(f8Bytes, 2, protowire.VarintType)
	f8Bytes = protowire.AppendVarint(f8Bytes, uint64(maxTokens))

	f8Bytes = protowire.AppendTag(f8Bytes, 3, protowire.VarintType)
	f8Bytes = protowire.AppendVarint(f8Bytes, 400)

	tempVal := 1.0
	if temperature != nil {
		tempVal = *temperature
	}
	f8Bytes = protowire.AppendTag(f8Bytes, 5, protowire.Fixed64Type)
	f8Bytes = protowire.AppendFixed64(f8Bytes, math.Float64bits(tempVal))

	f8Bytes = protowire.AppendTag(f8Bytes, 7, protowire.VarintType)
	f8Bytes = protowire.AppendVarint(f8Bytes, 40)

	f8Bytes = protowire.AppendTag(f8Bytes, 8, protowire.Fixed64Type)
	f8Bytes = protowire.AppendFixed64(f8Bytes, math.Float64bits(0.95))

	reqBytes = protowire.AppendTag(reqBytes, 8, protowire.BytesType)
	reqBytes = protowire.AppendBytes(reqBytes, f8Bytes)

	// 6. Repeated Tools (Field 10)
	for _, tool := range tools {
		var tBytes []byte
		if tool.Name != "" {
			tBytes = protowire.AppendTag(tBytes, 1, protowire.BytesType)
			tBytes = protowire.AppendString(tBytes, tool.Name)
		}
		desc := tool.Description
		if strings.Contains(desc, "Takes a task_id parameter identifying the task") {
			desc = strings.ReplaceAll(desc, "Takes a task_id parameter identifying the task", "Takes a taskId parameter identifying the task")
		}
		if desc != "" {
			tBytes = protowire.AppendTag(tBytes, 2, protowire.BytesType)
			tBytes = protowire.AppendString(tBytes, desc)
		}
		if len(tool.Parameters) > 0 {
			tBytes = protowire.AppendTag(tBytes, 3, protowire.BytesType)
			tBytes = protowire.AppendBytes(tBytes, tool.Parameters)
		}
		reqBytes = protowire.AppendTag(reqBytes, 10, protowire.BytesType)
		reqBytes = protowire.AppendBytes(reqBytes, tBytes)
	}

	// 7. Thread session metadata (Field 15)
	var f15Bytes []byte
	f15Bytes = protowire.AppendTag(f15Bytes, 1, protowire.BytesType)
	f15Bytes = protowire.AppendString(f15Bytes, sessionID)

	f15Bytes = protowire.AppendTag(f15Bytes, 2, protowire.VarintType)
	f15Bytes = protowire.AppendVarint(f15Bytes, 53)

	f15Bytes = protowire.AppendTag(f15Bytes, 3, protowire.VarintType)
	f15Bytes = protowire.AppendVarint(f15Bytes, 4)

	f15Bytes = protowire.AppendTag(f15Bytes, 4, protowire.VarintType)
	f15Bytes = protowire.AppendVarint(f15Bytes, 14)

	reqBytes = protowire.AppendTag(reqBytes, 15, protowire.BytesType)
	reqBytes = protowire.AppendBytes(reqBytes, f15Bytes)

	// 8. Cascade ID (Field 16: session-stable prompt cache key)
	reqBytes = protowire.AppendTag(reqBytes, 16, protowire.BytesType)
	reqBytes = protowire.AppendString(reqBytes, cascadeID)

	// 9. Fixed flag (Field 20: Varint 1)
	reqBytes = protowire.AppendTag(reqBytes, 20, protowire.VarintType)
	reqBytes = protowire.AppendVarint(reqBytes, 1)

	// 10. Model UID (Field 21)
	reqBytes = protowire.AppendTag(reqBytes, 21, protowire.BytesType)
	reqBytes = protowire.AppendString(reqBytes, chatModelUID)

	return reqBytes
}

// ParseDevinFrame extracts deltas, tool calls, thinking, signatures, and usage from a single response frame.
func ParseDevinFrame(payload []byte) (DevinFrameResult, error) {
	var res DevinFrameResult
	var textParts []string
	var thinkingParts []string

	pos := 0
	for pos < len(payload) {
		num, typ, n := protowire.ConsumeTag(payload[pos:])
		if n <= 0 {
			return res, fmt.Errorf("consume tag error at offset %d: %w", pos, protowire.ParseError(n))
		}
		pos += n

		switch typ {
		case protowire.VarintType:
			v, vn := protowire.ConsumeVarint(payload[pos:])
			if vn <= 0 {
				return res, fmt.Errorf("consume varint error at offset %d: %w", pos, protowire.ParseError(vn))
			}
			pos += vn
			switch num {
			case 2:
				res.Timestamp = v
			case 4:
				res.DeltaTokens = v
			case 5:
				res.StopReason = v
			}

		case protowire.Fixed64Type:
			v, fn := protowire.ConsumeFixed64(payload[pos:])
			if fn <= 0 {
				return res, fmt.Errorf("consume fixed64 error at offset %d: %w", pos, protowire.ParseError(fn))
			}
			pos += fn
			if num == 12 {
				res.Latency = math.Float64frombits(v)
			}

		case protowire.Fixed32Type:
			_, fn := protowire.ConsumeFixed32(payload[pos:])
			if fn <= 0 {
				return res, fmt.Errorf("consume fixed32 error at offset %d: %w", pos, protowire.ParseError(fn))
			}
			pos += fn

		case protowire.BytesType:
			val, bn := protowire.ConsumeBytes(payload[pos:])
			if bn <= 0 {
				return res, fmt.Errorf("consume bytes error at offset %d: %w", pos, protowire.ParseError(bn))
			}
			pos += bn

			switch num {
			case 1:
				res.OutputID = string(val)
			case 3:
				textParts = append(textParts, string(val))
			case 6:
				if tc, err := parseDevinToolCallDelta(val); err == nil {
					res.ToolCallDeltas = append(res.ToolCallDeltas, tc)
				}
			case 7:
				res.Usage = parseDevinUsageField(val)
			case 9:
				thinkingParts = append(thinkingParts, string(val))
			case 10:
				res.DeltaSignature = append(res.DeltaSignature, val...)
			case 17:
				res.MessageID = string(val)
			case 21:
				res.DeltaSignatureType = string(val)
			case 28:
				res.ResponseDimensionGroups = val
			}

		default:
			return res, fmt.Errorf("unsupported wire type %d at offset %d", typ, pos)
		}
	}

	if len(textParts) > 0 {
		res.ContentText = strings.Join(textParts, "")
	}
	if len(thinkingParts) > 0 {
		res.ThinkingText = strings.Join(thinkingParts, "")
	}

	return res, nil
}

// SanitizeDevinSystemPrompt strips Claude Code attribution and CLI identity headers
// (referencing Antigravity conventions), and applies zero-width obfuscation for configured sensitive words.
func SanitizeDevinSystemPrompt(prompt string, matcher *SensitiveWordMatcher) string {
	if prompt == "" {
		return ""
	}
	lines := strings.Split(prompt, "\n")
	var kept []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if util.IsClaudeCodeAttributionSystemText(trimmed) {
			continue
		}
		if strings.HasPrefix(trimmed, "You are Claude Code") {
			continue
		}
		if strings.Contains(trimmed, "authorized security testing") || strings.Contains(trimmed, "destructive techniques, DoS attacks") {
			continue
		}
		if strings.Contains(trimmed, "Claude Code is available as a CLI") {
			continue
		}
		if strings.Contains(trimmed, "Fast mode for Claude Code") {
			continue
		}
		if matcher != nil && matcher.Matches(trimmed) {
			continue
		}
		kept = append(kept, line)
	}
	res := strings.TrimSpace(strings.Join(kept, "\n"))
	if matcher != nil && res != "" {
		res = matcher.ObfuscateText(res)
	}
	return res
}

func parseDevinToolCallDelta(data []byte) (DevinToolCallDelta, error) {
	var tc DevinToolCallDelta
	pos := 0
	for pos < len(data) {
		num, typ, n := protowire.ConsumeTag(data[pos:])
		if n <= 0 {
			break
		}
		pos += n

		switch typ {
		case protowire.VarintType:
			v, vn := protowire.ConsumeVarint(data[pos:])
			if vn <= 0 {
				return tc, nil
			}
			pos += vn
			if num == 4 {
				tc.Index = int(v)
			}
		case protowire.BytesType:
			val, bn := protowire.ConsumeBytes(data[pos:])
			if bn <= 0 {
				return tc, nil
			}
			pos += bn
			switch num {
			case 1:
				tc.ID = string(val)
			case 2:
				tc.Name = string(val)
			case 3:
				tc.Arguments = string(val)
			}
		default:
			return tc, nil
		}
	}
	return tc, nil
}

func parseDevinUsageField(data []byte) *DevinUsage {
	u := &DevinUsage{}
	pos := 0
	for pos < len(data) {
		num, typ, n := protowire.ConsumeTag(data[pos:])
		if n <= 0 {
			break
		}
		pos += n

		switch typ {
		case protowire.VarintType:
			v, vn := protowire.ConsumeVarint(data[pos:])
			if vn <= 0 {
				return u
			}
			pos += vn
			switch num {
			case 2: // Prompt tokens (uncached input)
				u.PromptTokens = int64(v)
			case 3: // Output tokens
				u.CompletionTokens = int64(v)
			case 5: // Cache read tokens
				u.CachedTokens = int64(v)
			case 6: // Status code
				u.StatusCode = v
			}
		case protowire.BytesType:
			val, bn := protowire.ConsumeBytes(data[pos:])
			if bn <= 0 {
				return u
			}
			pos += bn
			switch num {
			case 8:
				u.RequestID = string(val)
			case 9:
				u.ModelName = string(val)
			}
		case protowire.Fixed64Type:
			_, fn := protowire.ConsumeFixed64(data[pos:])
			if fn <= 0 {
				return u
			}
			pos += fn
		case protowire.Fixed32Type:
			_, fn := protowire.ConsumeFixed32(data[pos:])
			if fn <= 0 {
				return u
			}
			pos += fn
		default:
			return u
		}
	}
	return u
}

// ParseDevinTrailerError inspects Connect-RPC EOS trailer frames and maps error status codes.
func ParseDevinTrailerError(payload []byte) (statusCode int, err error) {
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("{}")) {
		return 0, nil
	}
	var trailer struct {
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if errUnmarshal := json.Unmarshal(trimmed, &trailer); errUnmarshal != nil || trailer.Error == nil {
		return 0, nil
	}

	codeStr := strings.ToLower(trailer.Error.Code)
	msgLower := strings.ToLower(trailer.Error.Message)

	httpCode := http.StatusInternalServerError
	switch codeStr {
	case "invalid_argument":
		httpCode = http.StatusBadRequest
	case "unauthenticated":
		httpCode = http.StatusUnauthorized
	case "permission_denied":
		httpCode = http.StatusForbidden
	case "resource_exhausted":
		httpCode = http.StatusTooManyRequests
	case "unavailable":
		httpCode = http.StatusServiceUnavailable
	case "failed_precondition":
		if strings.Contains(msgLower, "quota") ||
			strings.Contains(msgLower, "credit") ||
			strings.Contains(msgLower, "acu") ||
			strings.Contains(msgLower, "exhausted") ||
			strings.Contains(msgLower, "limit") {
			httpCode = http.StatusTooManyRequests
		} else {
			httpCode = http.StatusBadRequest
		}
	}

	return httpCode, fmt.Errorf("devin upstream error (%s): %s", trailer.Error.Code, trailer.Error.Message)
}

// UTF8SplitBuffer buffers incomplete UTF-8 byte sequences across chunk boundaries.
type UTF8SplitBuffer struct {
	remainder []byte
}

// Feed consumes a byte chunk, prepending any pending remainder, and returns complete UTF-8 strings.
func (b *UTF8SplitBuffer) Feed(chunk []byte) string {
	combined := append(b.remainder, chunk...)
	b.remainder = nil

	if len(combined) == 0 {
		return ""
	}

	validUntil := 0
	for validUntil < len(combined) {
		r, size := utf8.DecodeRune(combined[validUntil:])
		if r == utf8.RuneError && size == 1 {
			trailingLen := len(combined) - validUntil
			if trailingLen < utf8.UTFMax && !utf8.FullRune(combined[validUntil:]) {
				break
			}
			validUntil++
			continue
		}
		validUntil += size
	}

	validBytes := combined[:validUntil]
	b.remainder = append(b.remainder, combined[validUntil:]...)

	return string(validBytes)
}
