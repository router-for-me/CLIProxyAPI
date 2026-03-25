package proto

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
)

const (
	// ConnectEndStreamFlag marks the end-of-stream frame (trailers).
	ConnectEndStreamFlag byte = 0x02
	// ConnectCompressionFlag indicates the payload is compressed (not supported).
	ConnectCompressionFlag byte = 0x01
	// ConnectFrameHeaderSize is the fixed 5-byte frame header.
	ConnectFrameHeaderSize = 5
)

// FrameConnectMessage wraps a protobuf payload in a Connect frame.
// Frame format: [1 byte flags][4 bytes payload length (big-endian)][payload]
func FrameConnectMessage(data []byte, flags byte) []byte {
	frame := make([]byte, ConnectFrameHeaderSize+len(data))
	frame[0] = flags
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(data)))
	copy(frame[5:], data)
	return frame
}

// ParseConnectFrame extracts one frame from a buffer.
// Returns (flags, payload, bytesConsumed, ok).
// ok is false when the buffer is too short for a complete frame.
func ParseConnectFrame(buf []byte) (flags byte, payload []byte, consumed int, ok bool) {
	if len(buf) < ConnectFrameHeaderSize {
		return 0, nil, 0, false
	}
	flags = buf[0]
	length := binary.BigEndian.Uint32(buf[1:5])
	total := ConnectFrameHeaderSize + int(length)
	if len(buf) < total {
		return 0, nil, 0, false
	}
	return flags, buf[5:total], total, true
}

// ParseConnectEndStream parses a Connect end-of-stream frame payload (JSON).
// Returns nil if there is no error in the trailer.
func ParseConnectEndStream(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	var trailer struct {
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &trailer); err != nil {
		return fmt.Errorf("failed to parse Connect end stream: %w", err)
	}
	if trailer.Error != nil {
		code := trailer.Error.Code
		if code == "" {
			code = "unknown"
		}
		msg := trailer.Error.Message
		if msg == "" {
			msg = "Unknown error"
		}
		return fmt.Errorf("Connect error %s: %s", code, msg)
	}
	return nil
}
