package wstunnel

import (
	"encoding/binary"
	"unicode/utf8"

	"github.com/gorilla/websocket"
)

// frameInspector checks client framing before Gorilla can synchronously write a
// protocol-error close. Data payloads are skipped; only Close payloads are kept.
// Compression is disabled, so no RSV bits are permitted.
type frameInspector struct {
	header       [14]byte
	headerBytes  int
	headerSize   int
	remaining    uint64
	opcode       byte
	fragmented   bool
	closePayload []byte
	failed       bool
	stopped      bool
}

func (s *frameInspector) inspect(raw []byte) bool {
	if s.failed {
		return false
	}
	if s.headerSize == 0 {
		s.headerSize = 2
	}
	for len(raw) > 0 && !s.stopped {
		if s.headerBytes < s.headerSize {
			s.header[s.headerBytes] = raw[0]
			s.headerBytes++
			raw = raw[1:]
			if s.headerBytes == 2 {
				if !s.inspectHeader() {
					s.failed = true
					return false
				}
			}
			if s.headerBytes == s.headerSize {
				switch s.header[1] & 0x7f {
				case 126:
					s.remaining = uint64(binary.BigEndian.Uint16(s.header[2:4]))
				case 127:
					s.remaining = binary.BigEndian.Uint64(s.header[2:10])
					// Gorilla rejects lengths that overflow its signed counter.
					if s.remaining>>63 != 0 {
						s.failed = true
						return false
					}
				default:
					s.remaining = uint64(s.header[1] & 0x7f)
				}
				if s.remaining == 0 {
					s.finishFrame()
				}
			}
			continue
		}

		size := min(uint64(len(raw)), s.remaining)
		if s.opcode == websocket.CloseMessage {
			for _, b := range raw[:size] {
				mask := s.header[s.headerSize-4+len(s.closePayload)%4]
				s.closePayload = append(s.closePayload, b^mask)
			}
		}
		raw = raw[size:]
		s.remaining -= size
		if s.remaining == 0 && !s.finishFrame() {
			s.failed = true
			return false
		}
	}
	return true
}

func (s *frameInspector) inspectHeader() bool {
	first, second := s.header[0], s.header[1]
	if first&0x70 != 0 || second&0x80 == 0 {
		return false
	}
	s.opcode = first & 0x0f
	final := first&0x80 != 0
	switch s.opcode {
	case websocket.TextMessage, websocket.BinaryMessage:
		if s.fragmented {
			return false
		}
		s.fragmented = !final
	case 0:
		if !s.fragmented {
			return false
		}
		s.fragmented = !final
	case websocket.CloseMessage, websocket.PingMessage, websocket.PongMessage:
		if !final || second&0x7f > 125 {
			return false
		}
	default:
		return false
	}
	s.headerSize = 6 // Two base bytes and the mandatory four-byte client mask.
	switch second & 0x7f {
	case 126:
		s.headerSize += 2
	case 127:
		s.headerSize += 8
	}
	return true
}

func (s *frameInspector) finishFrame() bool {
	if s.opcode == websocket.CloseMessage {
		// RFC 6455 also forbids a one-byte Close payload, which Gorilla accepts.
		if len(s.closePayload) == 1 {
			return false
		}
		if len(s.closePayload) >= 2 {
			code := int(binary.BigEndian.Uint16(s.closePayload))
			// Match Gorilla v1.5.3's received-code allowlist, not all 1xxx codes.
			switch code {
			case 1000, 1001, 1002, 1003, 1007, 1008, 1009, 1010, 1011, 1012, 1013:
			default:
				if code < 3000 || code > 4999 {
					return false
				}
			}
			if !utf8.Valid(s.closePayload[2:]) {
				return false
			}
		}
		// Gorilla stops reading at a valid Close; trailing bytes are irrelevant.
		s.stopped = true
	}
	s.headerBytes = 0
	s.headerSize = 2
	s.closePayload = s.closePayload[:0]
	return true
}
