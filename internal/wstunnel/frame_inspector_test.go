package wstunnel

import (
	"encoding/binary"
	"fmt"
	"testing"
)

func maskedTunnelFrame(first byte, payload []byte, lengthBytes int) []byte {
	raw := []byte{first, 0x80 | byte(len(payload))}
	switch lengthBytes {
	case 2:
		raw[1] = 0xfe
		raw = binary.BigEndian.AppendUint16(raw, uint16(len(payload)))
	case 8:
		raw[1] = 0xff
		raw = binary.BigEndian.AppendUint64(raw, uint64(len(payload)))
	}
	mask := [4]byte{0x12, 0x34, 0x56, 0x78}
	raw = append(raw, mask[:]...)
	for i, b := range payload {
		raw = append(raw, b^mask[i%4])
	}
	return raw
}

func TestFrameInspector_RejectsFatalFramesAtEveryReadBoundary(t *testing.T) {
	tests := []struct {
		name string
		raw  []byte
	}{
		{"RSV1", []byte{0xc2, 0x80}},
		{"RSV2", []byte{0xa2, 0x80}},
		{"RSV3", []byte{0x92, 0x80}},
		{"unmasked", []byte{0x82, 0}},
		{"unexpected continuation", []byte{0x80, 0x80}},
		{"fragmented close", []byte{0x08, 0x80}},
		{"fragmented ping", []byte{0x09, 0x80}},
		{"fragmented pong", []byte{0x0a, 0x80}},
		{"extended close length", []byte{0x88, 0xfe}},
		{"extended ping length", []byte{0x89, 0xff}},
		{"extended pong length", []byte{0x8a, 0xfe}},
		{"one-byte close", maskedTunnelFrame(0x88, []byte{0}, 0)},
		{"invalid UTF-8 close reason", maskedTunnelFrame(0x88, []byte{0x03, 0xe8, 0xff}, 0)},
		{"truncated UTF-8 close reason", maskedTunnelFrame(0x88, []byte{0x03, 0xe8, 0xe4, 0xb8}, 0)},
		{"signed length overflow", []byte{0x82, 0xff, 0x80, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}},
	}
	for _, opcode := range []byte{3, 4, 5, 6, 7, 11, 12, 13, 14, 15} {
		tests = append(tests, struct {
			name string
			raw  []byte
		}{fmt.Sprintf("reserved opcode %d", opcode), []byte{0x80 | opcode, 0x80}})
	}
	for _, first := range []byte{0x81, 0x82} {
		raw := append(maskedTunnelFrame(0x01, []byte("fragment"), 0), first, 0x80)
		tests = append(tests, struct {
			name string
			raw  []byte
		}{fmt.Sprintf("new data before FIN %d", first), raw})
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for split := 0; split <= len(tc.raw); split++ {
				var inspector frameInspector
				if inspector.inspect(tc.raw[:split]) && inspector.inspect(tc.raw[split:]) {
					t.Fatalf("accepted fatal frame with split at byte %d: %x", split, tc.raw)
				}
				if !inspector.failed || inspector.inspect(nil) {
					t.Fatal("fatal framing error was not permanent")
				}
			}
		})
	}
}

func TestFrameInspector_CloseCodeAllowlistMatchesGorilla(t *testing.T) {
	for code := 0; code <= 65535; code++ {
		payload := binary.BigEndian.AppendUint16(nil, uint16(code))
		var inspector frameInspector
		got := inspector.inspect(maskedTunnelFrame(0x88, payload, 0))
		want := code >= 3000 && code <= 4999
		switch code {
		case 1000, 1001, 1002, 1003, 1007, 1008, 1009, 1010, 1011, 1012, 1013:
			want = true
		}
		if got != want {
			t.Fatalf("close code %d: accepted=%t, want %t", code, got, want)
		}
	}
}

func TestFrameInspector_ValidFragmentationAndControlAtEveryReadBoundary(t *testing.T) {
	raw := maskedTunnelFrame(0x01, []byte("first fragment"), 0)
	raw = append(raw, maskedTunnelFrame(0x89, []byte("ping"), 0)...)
	raw = append(raw, maskedTunnelFrame(0x00, nil, 0)...)
	raw = append(raw, maskedTunnelFrame(0x8a, []byte("pong"), 0)...)
	raw = append(raw, maskedTunnelFrame(0x80, []byte("final fragment"), 0)...)
	// Gorilla accepts non-minimal extended encodings for data frames.
	raw = append(raw, maskedTunnelFrame(0x82, []byte{0x83, 0x00, 0xff}, 2)...)
	raw = append(raw, maskedTunnelFrame(0x82, make([]byte, 65536), 8)...)
	raw = append(raw, maskedTunnelFrame(0x88, []byte{0x03, 0xe8, 0xe4, 0xb8, 0xad}, 0)...)
	// A valid Close ends Gorilla's reader; subsequent bytes must not be inspected.
	raw = append(raw, 0x83, 0x00)
	for _, size := range []int{1, 2, 3, 4, 5, 6, 7, 8, 13, 14, 127, 4096, len(raw)} {
		t.Run(fmt.Sprintf("read size %d", size), func(t *testing.T) {
			var inspector frameInspector
			for offset := 0; offset < len(raw); offset += size {
				if !inspector.inspect(raw[offset:min(offset+size, len(raw))]) {
					t.Fatalf("rejected valid input at offset %d", offset)
				}
			}
			if !inspector.stopped || cap(inspector.closePayload) > 256 {
				t.Fatal("valid Close did not stop inspection with bounded storage")
			}
		})
	}
	var emptyClose frameInspector
	if !emptyClose.inspect(maskedTunnelFrame(0x88, nil, 0)) || !emptyClose.stopped {
		t.Fatal("empty Close was rejected")
	}
}
