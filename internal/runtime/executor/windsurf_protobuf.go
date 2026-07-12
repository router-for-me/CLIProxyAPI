package executor

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"fmt"
	"io"
	"math"
)

// Minimal protobuf encoder/decoder for the Windsurf / Devin CLI Connect protocol.
// This intentionally avoids a full protobuf dependency; the message shapes are small
// and stable enough to encode/decode manually.

type protoBuf struct {
	buf *bytes.Buffer
}

func newProtoBuf() *protoBuf {
	return &protoBuf{buf: &bytes.Buffer{}}
}

func (p *protoBuf) bytes() []byte {
	return p.buf.Bytes()
}

func encodeVarint(v uint64) []byte {
	var b [10]byte
	n := 0
	for v >= 0x80 {
		b[n] = byte(v) | 0x80
		v >>= 7
		n++
	}
	b[n] = byte(v)
	n++
	return b[:n]
}

func encodeFieldKey(field int, wireType int) []byte {
	return encodeVarint(uint64(field<<3) | uint64(wireType))
}

func (p *protoBuf) writeVarintField(field int, v uint64) {
	p.buf.Write(encodeFieldKey(field, 0))
	p.buf.Write(encodeVarint(v))
}

func (p *protoBuf) writeStringField(field int, s string) {
	if s == "" {
		return
	}
	p.buf.Write(encodeFieldKey(field, 2))
	p.buf.Write(encodeVarint(uint64(len(s))))
	p.buf.WriteString(s)
}

func (p *protoBuf) writeBytesField(field int, b []byte) {
	p.buf.Write(encodeFieldKey(field, 2))
	p.buf.Write(encodeVarint(uint64(len(b))))
	p.buf.Write(b)
}

func (p *protoBuf) writeFixed64Field(field int, v float64) {
	p.buf.Write(encodeFieldKey(field, 1))
	b := make([]byte, 8)
	binary.LittleEndian.PutUint64(b, math.Float64bits(v))
	p.buf.Write(b)
}

func (p *protoBuf) writeMessageField(field int, fn func(*protoBuf)) {
	nested := newProtoBuf()
	fn(nested)
	p.writeBytesField(field, nested.bytes())
}

// encodeConnectEnvelope wraps a protobuf payload in the Connect protocol framing.
// flags: 0x00 = uncompressed, 0x01 = gzip-compressed.
func encodeConnectEnvelope(payload []byte, flags byte) []byte {
	out := make([]byte, 5+len(payload))
	out[0] = flags
	binary.BigEndian.PutUint32(out[1:5], uint32(len(payload)))
	copy(out[5:], payload)
	return out
}

func encodeCompressedConnectEnvelope(payload []byte) ([]byte, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(payload); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return encodeConnectEnvelope(buf.Bytes(), 0x01), nil
}

// decodeConnectEnvelope parses one or more Connect envelopes from a buffer.
func decodeConnectEnvelopes(data []byte) ([]struct {
	Flags   byte
	Payload []byte
}, error) {
	var out []struct {
		Flags   byte
		Payload []byte
	}
	offset := 0
	for offset < len(data) {
		if offset+5 > len(data) {
			return nil, fmt.Errorf("incomplete connect envelope header")
		}
		flags := data[offset]
		length := int(binary.BigEndian.Uint32(data[offset+1 : offset+5]))
		offset += 5
		if offset+length > len(data) {
			return nil, fmt.Errorf("connect envelope length exceeds data")
		}
		payload := data[offset : offset+length]
		offset += length
		if flags&0x01 != 0 {
			gr, err := gzip.NewReader(bytes.NewReader(payload))
			if err != nil {
				return nil, fmt.Errorf("gzip reader: %w", err)
			}
			decompressed, err := io.ReadAll(gr)
			if err != nil {
				return nil, fmt.Errorf("gzip read: %w", err)
			}
			gr.Close()
			payload = decompressed
		}
		out = append(out, struct {
			Flags   byte
			Payload []byte
		}{Flags: flags, Payload: payload})
	}
	return out, nil
}

// readVarint reads a base-128 varint from data starting at offset.
func readVarint(data []byte, offset int) (uint64, int, error) {
	var value uint64
	var shift uint
	start := offset
	for offset < len(data) {
		b := data[offset]
		offset++
		value |= uint64(b&0x7f) << shift
		if b&0x80 == 0 {
			return value, offset, nil
		}
		shift += 7
		if shift > 63 {
			return 0, start, fmt.Errorf("varint too long")
		}
	}
	return 0, start, fmt.Errorf("unexpected end of varint")
}

// decodeFields decodes top-level protobuf fields from a buffer.
func decodeFields(data []byte) ([]protoField, error) {
	var fields []protoField
	offset := 0
	for offset < len(data) {
		key, newOffset, err := readVarint(data, offset)
		if err != nil {
			return nil, err
		}
		offset = newOffset
		fieldNum := int(key >> 3)
		wireType := int(key & 0x07)
		switch wireType {
		case 0:
			v, newOffset, err := readVarint(data, offset)
			if err != nil {
				return nil, err
			}
			fields = append(fields, protoField{Field: fieldNum, WireType: 0, Value: v})
			offset = newOffset
		case 2:
			length, newOffset, err := readVarint(data, offset)
			if err != nil {
				return nil, err
			}
			offset = newOffset
			end := offset + int(length)
			if end > len(data) {
				return nil, fmt.Errorf("length-delimited field exceeds buffer")
			}
			fields = append(fields, protoField{Field: fieldNum, WireType: 2, Data: data[offset:end]})
			offset = end
		case 1:
			if offset+8 > len(data) {
				return nil, fmt.Errorf("fixed64 field exceeds buffer")
			}
			fields = append(fields, protoField{Field: fieldNum, WireType: 1, Data: data[offset : offset+8]})
			offset += 8
		case 5:
			if offset+4 > len(data) {
				return nil, fmt.Errorf("fixed32 field exceeds buffer")
			}
			fields = append(fields, protoField{Field: fieldNum, WireType: 5, Data: data[offset : offset+4]})
			offset += 4
		default:
			return nil, fmt.Errorf("unsupported wire type %d", wireType)
		}
	}
	return fields, nil
}

type protoField struct {
	Field    int
	WireType int
	Value    uint64
	Data     []byte
}

func (f protoField) AsString() string {
	if f.WireType != 2 {
		return ""
	}
	return string(f.Data)
}

func (f protoField) AsFloat32() float32 {
	if f.WireType != 5 || len(f.Data) != 4 {
		return 0
	}
	return math.Float32frombits(binary.LittleEndian.Uint32(f.Data))
}

func (f protoField) AsFloat64() float64 {
	if f.WireType != 1 || len(f.Data) != 8 {
		return 0
	}
	return math.Float64frombits(binary.LittleEndian.Uint64(f.Data))
}
