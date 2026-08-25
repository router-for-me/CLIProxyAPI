// Package cursor implements a minimal hand-written protobuf codec and Connect-RPC
// framing for the Cursor AIServer messages used by the Cursor provider.
//
// It covers exactly the fields of Cursor's message.proto that are needed to build
// StreamUnifiedChatWithToolsRequest and to decode
// StreamUnifiedChatWithToolsResponse / AvailableModelsResponse.
package cursor

import (
	"encoding/binary"
	"fmt"
)

// ---------- low-level encode helpers ----------

func appendVarint(dst []byte, v uint64) []byte {
	var buf [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(buf[:], v)
	return append(dst, buf[:n]...)
}

func appendTag(dst []byte, field int, wire int) []byte {
	return appendVarint(dst, uint64(field)<<3|uint64(wire))
}

func appendString(dst []byte, field int, s string) []byte {
	if s == "" {
		return dst
	}
	dst = appendTag(dst, field, 2)
	dst = appendVarint(dst, uint64(len(s)))
	return append(dst, s...)
}

func appendInt32(dst []byte, field int, v int32) []byte {
	if v == 0 {
		return dst
	}
	dst = appendTag(dst, field, 0)
	return appendVarint(dst, uint64(uint32(v)))
}

// appendBytesField always writes the field, even when the payload is empty.
func appendBytesField(dst []byte, field int, b []byte) []byte {
	dst = appendTag(dst, field, 2)
	dst = appendVarint(dst, uint64(len(b)))
	return append(dst, b...)
}

func appendMessage(dst []byte, field int, m []byte) []byte {
	if m == nil {
		return dst
	}
	dst = appendTag(dst, field, 2)
	dst = appendVarint(dst, uint64(len(m)))
	return append(dst, m...)
}

// ---------- low-level decode helpers ----------

type protoField struct {
	num  int
	wire int
	uvar uint64
	raw  []byte // length-delimited payload (wire == 2)
}

// parseFields decodes a protobuf message into a flat field list.
func parseFields(data []byte) ([]protoField, error) {
	var out []protoField
	for len(data) > 0 {
		tag, n := binary.Uvarint(data)
		if n <= 0 {
			return nil, fmt.Errorf("cursor proto: bad tag varint")
		}
		data = data[n:]
		f := protoField{num: int(tag >> 3), wire: int(tag & 7)}
		switch f.wire {
		case 0: // varint
			v, m := binary.Uvarint(data)
			if m <= 0 {
				return nil, fmt.Errorf("cursor proto: bad varint field %d", f.num)
			}
			f.uvar = v
			data = data[m:]
		case 1: // 64-bit
			if len(data) < 8 {
				return nil, fmt.Errorf("cursor proto: truncated fixed64 field %d", f.num)
			}
			data = data[8:]
		case 2: // length-delimited
			l, m := binary.Uvarint(data)
			if m <= 0 {
				return nil, fmt.Errorf("cursor proto: bad length field %d", f.num)
			}
			data = data[m:]
			if uint64(len(data)) < l {
				return nil, fmt.Errorf("cursor proto: truncated bytes field %d", f.num)
			}
			f.raw = data[:l]
			data = data[l:]
		case 5: // 32-bit
			if len(data) < 4 {
				return nil, fmt.Errorf("cursor proto: truncated fixed32 field %d", f.num)
			}
			data = data[4:]
		default:
			return nil, fmt.Errorf("cursor proto: unsupported wire type %d", f.wire)
		}
		out = append(out, f)
	}
	return out, nil
}

// ---------- StreamUnifiedChatWithToolsRequest ----------

// ChatMessage is one message of the request conversation.
type ChatMessage struct {
	Content      string
	Role         int32 // 1 = user, 2 = assistant
	MessageID    string
	ChatModeEnum int32 // set for user messages (1 = ask)
	SummaryID    string
}

func (m *ChatMessage) encode() []byte {
	var b []byte
	b = appendString(b, 1, m.Content)
	b = appendInt32(b, 2, m.Role)
	b = appendString(b, 13, m.MessageID)
	b = appendString(b, 32, m.SummaryID)
	b = appendInt32(b, 47, m.ChatModeEnum)
	return b
}

// RequestMetadata mirrors the client metadata the Cursor editor sends.
type RequestMetadata struct {
	OS        string
	Arch      string
	Version   string
	Path      string
	Timestamp string
}

func (m *RequestMetadata) encode() []byte {
	if m == nil {
		return nil
	}
	var b []byte
	b = appendString(b, 1, m.OS)
	b = appendString(b, 2, m.Arch)
	b = appendString(b, 3, m.Version)
	b = appendString(b, 4, m.Path)
	b = appendString(b, 5, m.Timestamp)
	return b
}

// MessageIDRef is an entry of the messageIds list.
type MessageIDRef struct {
	MessageID string
	SummaryID string
	Role      int32
}

func (m *MessageIDRef) encode() []byte {
	var b []byte
	b = appendString(b, 1, m.MessageID)
	b = appendString(b, 2, m.SummaryID)
	b = appendInt32(b, 3, m.Role)
	return b
}

// ChatRequest mirrors StreamUnifiedChatWithToolsRequest.Request.
//
// The UnknownNN fields carry constants the Cursor editor always sends. Their
// meaning is not public, but omitting them makes the API reject the request.
type ChatRequest struct {
	Messages       []ChatMessage
	Unknown2       int32
	Instruction    string
	Unknown4       int32
	ModelName      string
	WebTool        string
	Unknown13      int32
	Unknown19      int32
	ConversationID string
	Metadata       *RequestMetadata
	Unknown27      int32
	MessageIDs     []MessageIDRef
	LargeContext   int32
	Unknown38      int32
	ChatModeEnum   int32
	Unknown47      string
	Unknown48      int32
	Unknown49      int32
	Unknown51      int32
	Unknown53      int32
	ChatMode       string
}

// cursorSetting encodes the fixed CursorSetting submessage used by the client.
func cursorSetting() []byte {
	var unknown6 []byte
	unknown6 = appendBytesField(unknown6, 1, nil)
	unknown6 = appendBytesField(unknown6, 2, nil)

	var b []byte
	b = appendString(b, 1, `cursor\aisettings`)
	b = appendBytesField(b, 3, nil)
	b = appendMessage(b, 6, unknown6)
	b = appendInt32(b, 8, 1)
	b = appendInt32(b, 9, 1)
	return b
}

func (r *ChatRequest) encode() []byte {
	var b []byte
	for i := range r.Messages {
		b = appendMessage(b, 1, r.Messages[i].encode())
	}
	b = appendInt32(b, 2, r.Unknown2)
	if r.Instruction != "" {
		var instr []byte
		instr = appendString(instr, 1, r.Instruction)
		b = appendMessage(b, 3, instr)
	}
	b = appendInt32(b, 4, r.Unknown4)

	var model []byte
	model = appendString(model, 1, r.ModelName)
	model = appendBytesField(model, 4, nil)
	b = appendMessage(b, 5, model)

	b = appendString(b, 8, r.WebTool)
	b = appendInt32(b, 13, r.Unknown13)
	b = appendMessage(b, 15, cursorSetting())
	b = appendInt32(b, 19, r.Unknown19)
	b = appendString(b, 23, r.ConversationID)
	b = appendMessage(b, 26, r.Metadata.encode())
	b = appendInt32(b, 27, r.Unknown27)
	for i := range r.MessageIDs {
		b = appendMessage(b, 30, r.MessageIDs[i].encode())
	}
	b = appendInt32(b, 35, r.LargeContext)
	b = appendInt32(b, 38, r.Unknown38)
	b = appendInt32(b, 46, r.ChatModeEnum)
	b = appendString(b, 47, r.Unknown47)
	b = appendInt32(b, 48, r.Unknown48)
	b = appendInt32(b, 49, r.Unknown49)
	b = appendInt32(b, 51, r.Unknown51)
	b = appendInt32(b, 53, r.Unknown53)
	b = appendString(b, 54, r.ChatMode)
	return b
}

// EncodeStreamUnifiedChatRequest marshals StreamUnifiedChatWithToolsRequest
// (field 1 = Request).
func EncodeStreamUnifiedChatRequest(r *ChatRequest) ([]byte, error) {
	if r == nil {
		return nil, fmt.Errorf("cursor proto: nil request")
	}
	var b []byte
	b = appendMessage(b, 1, r.encode())
	return b, nil
}

// ---------- StreamUnifiedChatWithToolsResponse ----------

// ChatResponseChunk is the decoded payload of one response envelope.
type ChatResponseChunk struct {
	Content  string
	Thinking string
}

// DecodeStreamUnifiedChatResponse extracts message content and thinking text
// from a StreamUnifiedChatWithToolsResponse payload.
func DecodeStreamUnifiedChatResponse(data []byte) (*ChatResponseChunk, error) {
	fields, err := parseFields(data)
	if err != nil {
		return nil, err
	}
	out := &ChatResponseChunk{}
	for _, f := range fields {
		if f.num != 2 || f.wire != 2 { // message
			continue
		}
		msgFields, errMsg := parseFields(f.raw)
		if errMsg != nil {
			return nil, errMsg
		}
		for _, mf := range msgFields {
			switch {
			case mf.num == 1 && mf.wire == 2: // content
				out.Content = string(mf.raw)
			case mf.num == 25 && mf.wire == 2: // thinking
				thFields, errThinking := parseFields(mf.raw)
				if errThinking != nil {
					continue
				}
				for _, tf := range thFields {
					if tf.num == 1 && tf.wire == 2 {
						out.Thinking = string(tf.raw)
					}
				}
			}
		}
	}
	return out, nil
}

// ---------- AvailableModelsResponse ----------

// DecodeAvailableModels extracts model names from an AvailableModelsResponse.
func DecodeAvailableModels(data []byte) ([]string, error) {
	fields, err := parseFields(data)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, f := range fields {
		switch {
		case f.num == 2 && f.wire == 2: // repeated AvailableModel models
			sub, errSub := parseFields(f.raw)
			if errSub != nil {
				continue
			}
			for _, sf := range sub {
				if sf.num == 1 && sf.wire == 2 {
					names = append(names, string(sf.raw))
				}
			}
		case f.num == 1 && f.wire == 2: // repeated string modelNames (fallback)
			names = append(names, string(f.raw))
		}
	}
	return names, nil
}
