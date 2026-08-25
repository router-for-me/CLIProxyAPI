package cursor

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Connect envelope flag bits, as used by api2.cursor.sh.
const (
	// flagCompressed marks a payload compressed with connect-content-encoding.
	flagCompressed = 0x01
	// flagEndStream marks the trailing frame, whose payload is JSON and carries
	// the error (if any) that terminated the stream.
	flagEndStream = 0x02
)

// gzipMessageThreshold is the conversation length from which the Cursor client
// gzips the protobuf payload. Matching it keeps request fingerprints plausible.
const gzipMessageThreshold = 3

// Message is one conversation turn handed to GenerateChatBody.
type Message struct {
	// Role is an OpenAI role: "system", "user", "assistant" or "tool".
	Role string
	// Content is the flattened plain-text content of the message.
	Content string
}

// GenerateChatBody builds the Connect-RPC framed body for
// aiserver.v1.ChatService/StreamUnifiedChatWithTools from OpenAI-style messages.
// System messages are merged into the instruction block; everything else becomes
// a conversation turn.
func GenerateChatBody(messages []Message, modelName string) ([]byte, error) {
	var instruction strings.Builder
	formatted := make([]ChatMessage, 0, len(messages))
	for _, m := range messages {
		text := m.Content
		if m.Role == "system" || m.Role == "developer" {
			if instruction.Len() > 0 {
				instruction.WriteString("\n")
			}
			instruction.WriteString(text)
			continue
		}
		cm := ChatMessage{
			Content:   text,
			MessageID: uuid.NewString(),
		}
		// Cursor only distinguishes user (1) from assistant (2); tool results are
		// replayed as user turns since the chat schema has no tool role.
		if m.Role == "assistant" {
			cm.Role = 2
		} else {
			cm.Role = 1
			cm.ChatModeEnum = 1
		}
		formatted = append(formatted, cm)
	}

	refs := make([]MessageIDRef, 0, len(formatted))
	for i := range formatted {
		refs = append(refs, MessageIDRef{
			MessageID: formatted[i].MessageID,
			SummaryID: formatted[i].SummaryID,
			Role:      formatted[i].Role,
		})
	}

	req := &ChatRequest{
		Messages:       formatted,
		Unknown2:       1,
		Instruction:    instruction.String(),
		Unknown4:       1,
		ModelName:      modelName,
		Unknown13:      1,
		Unknown19:      1,
		ConversationID: uuid.NewString(),
		Metadata: &RequestMetadata{
			OS:        "win32",
			Arch:      "x64",
			Version:   "10.0.22631",
			Path:      `C:\Program Files\PowerShell\7\pwsh.exe`,
			Timestamp: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		},
		MessageIDs:   refs,
		ChatModeEnum: 1,
		Unknown53:    1,
		ChatMode:     "Ask",
	}

	payload, err := EncodeStreamUnifiedChatRequest(req)
	if err != nil {
		return nil, err
	}

	flag := byte(0x00)
	if len(formatted) >= gzipMessageThreshold {
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		if _, errWrite := zw.Write(payload); errWrite != nil {
			return nil, fmt.Errorf("cursor: gzip request payload: %w", errWrite)
		}
		if errClose := zw.Close(); errClose != nil {
			return nil, fmt.Errorf("cursor: close gzip writer: %w", errClose)
		}
		payload = buf.Bytes()
		flag = flagCompressed
	}

	out := make([]byte, 0, len(payload)+5)
	out = append(out, flag)
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(payload)))
	out = append(out, lenBuf[:]...)
	out = append(out, payload...)
	return out, nil
}

// decodeFramePayload gunzips a frame payload when the compressed flag is set.
func decodeFramePayload(flag byte, data []byte) ([]byte, error) {
	if flag&flagCompressed == 0 || len(data) == 0 {
		return data, nil
	}
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer func() { _ = zr.Close() }()
	return io.ReadAll(zr)
}

// StreamError is an error Cursor reported in the Connect end-of-stream frame.
// The API answers with HTTP 200 and describes the failure only there, so this is
// the sole place a rejected request becomes visible.
type StreamError struct {
	// Code is the Connect status, e.g. "resource_exhausted".
	Code string
	// Reason is the Cursor-specific code, e.g. "ERROR_RATE_LIMITED_CHANGEABLE".
	Reason string
	// Title and Detail are the user-facing strings Cursor would render.
	Title  string
	Detail string
}

func (e *StreamError) Error() string {
	parts := make([]string, 0, 3)
	if e.Reason != "" {
		parts = append(parts, e.Reason)
	} else if e.Code != "" {
		parts = append(parts, e.Code)
	}
	switch {
	case e.Detail != "":
		parts = append(parts, e.Detail)
	case e.Title != "":
		parts = append(parts, e.Title)
	}
	if len(parts) == 0 {
		return "cursor: stream failed"
	}
	if hint := reasonHint[e.Reason]; hint != "" {
		parts = append(parts, hint)
	}
	return "cursor: " + strings.Join(parts, ": ")
}

// HTTPStatus maps a Connect status onto the HTTP status the proxy should report.
func (e *StreamError) HTTPStatus() int {
	switch e.Code {
	case "unauthenticated":
		return 401
	case "permission_denied":
		return 403
	case "resource_exhausted":
		return 429
	case "invalid_argument":
		return 400
	case "unavailable":
		return 503
	default:
		return 502
	}
}

// reasonHint annotates Cursor codes whose own message points the wrong way.
var reasonHint = map[string]string{
	// Cursor reuses this bucket for Auto and the composer-* models, and its text
	// tells you to update the client. Bumping x-cursor-client-version changes
	// nothing: those models moved to a request schema this package does not
	// encode. Pick a named model instead.
	"ERROR_GPT_4_VISION_PREVIEW_RATE_LIMIT": "(this provider cannot drive Auto/composer-* models; use a named model)",
}

// endStreamFrame is the JSON payload of a flagEndStream frame.
type endStreamFrame struct {
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Details []struct {
			Debug struct {
				Error   string `json:"error"`
				Details struct {
					Title  string `json:"title"`
					Detail string `json:"detail"`
				} `json:"details"`
			} `json:"debug"`
		} `json:"details"`
	} `json:"error"`
}

// parseEndStream returns the error carried by an end-of-stream frame, or nil
// when the stream finished normally.
func parseEndStream(payload []byte) error {
	if len(bytes.TrimSpace(payload)) == 0 {
		return nil
	}
	var frame endStreamFrame
	if err := json.Unmarshal(payload, &frame); err != nil {
		// Not JSON we recognise; surface it verbatim rather than dropping it.
		return &StreamError{Detail: string(payload)}
	}
	if frame.Error == nil {
		return nil
	}
	se := &StreamError{Code: frame.Error.Code, Detail: frame.Error.Message}
	if len(frame.Error.Details) > 0 {
		d := frame.Error.Details[0].Debug
		se.Reason = d.Error
		se.Title = d.Details.Title
		if d.Details.Detail != "" {
			se.Detail = d.Details.Detail
		}
	}
	return se
}

// ReadFrames reads the Connect-RPC framed stream incrementally and calls fn for
// each decoded chunk's thinking/text payloads. It returns a *StreamError when
// Cursor terminated the stream with an error.
func ReadFrames(body io.Reader, fn func(thinking, text string)) error {
	reader := bufio.NewReader(body)
	for {
		var header [5]byte
		if _, err := io.ReadFull(reader, header[:]); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return nil
			}
			return err
		}
		flag := header[0]
		dataLen := int(binary.BigEndian.Uint32(header[1:]))

		var data []byte
		if dataLen > 0 {
			data = make([]byte, dataLen)
			if _, err := io.ReadFull(reader, data); err != nil {
				return err
			}
		}

		payload, err := decodeFramePayload(flag, data)
		if err != nil {
			return fmt.Errorf("cursor: decompress frame: %w", err)
		}
		if flag&flagEndStream != 0 {
			// The end-of-stream frame is where Cursor reports rejections (plan
			// limits, outdated client, bad token). Never drop it.
			return parseEndStream(payload)
		}
		if len(payload) == 0 {
			continue
		}
		chunk, errDecode := DecodeStreamUnifiedChatResponse(payload)
		if errDecode != nil || chunk == nil {
			continue
		}
		fn(chunk.Thinking, chunk.Content)
	}
}

// DecodeMaybeGzip reads a full response body, transparently decompressing gzip.
// Needed because the Cursor headers set accept-encoding explicitly, so net/http
// does not auto-decompress the response.
func DecodeMaybeGzip(r io.Reader) ([]byte, error) {
	body, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	if len(body) > 2 && body[0] == 0x1f && body[1] == 0x8b {
		zr, errGzip := gzip.NewReader(bytes.NewReader(body))
		if errGzip != nil {
			return nil, errGzip
		}
		defer func() { _ = zr.Close() }()
		return io.ReadAll(zr)
	}
	return body, nil
}
