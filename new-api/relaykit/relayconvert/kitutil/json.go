// Package kitutil holds the dependency-free helpers shared by the conversion
// kit packages (dto, types, relayconvert). It moved out of the host's common
// package as part of the relaykit extraction; common re-exports these for
// host code.
package kitutil

import (
	"bytes"
	"encoding/json"
	"io"
	"unsafe"
)

// Codec is the JSON engine used by every kitutil JSON helper. The host may
// replace it once at startup via SetCodec; relaykit itself never depends on a
// third-party JSON library.
type Codec interface {
	Marshal(v any) ([]byte, error)
	Unmarshal(data []byte, v any) error
	Decode(r io.Reader, v any) error
	Valid(data []byte) bool
}

// stdCodec is the default Codec, backed by encoding/json. It is the only place
// in relaykit that calls the standard library's JSON functions directly.
type stdCodec struct{}

func (stdCodec) Marshal(v any) ([]byte, error) {
	return json.Marshal(v)
}

func (stdCodec) Unmarshal(data []byte, v any) error {
	return json.Unmarshal(data, v)
}

func (stdCodec) Decode(r io.Reader, v any) error {
	return json.NewDecoder(r).Decode(v)
}

func (stdCodec) Valid(data []byte) bool {
	return json.Valid(data)
}

var codec Codec = stdCodec{}

// SetCodec installs the JSON engine behind every kitutil JSON helper, which
// also covers the custom (Un)MarshalJSON methods on relaykit DTOs. Like
// SetLogging it is meant to be called once during host startup before any
// request is served; it is not synchronized against concurrent helper calls.
// A nil codec is ignored so the standard-library default stays in place.
func SetCodec(c Codec) {
	if c == nil {
		return
	}
	codec = c
}

func Unmarshal(data []byte, v any) error {
	return codec.Unmarshal(data, v)
}

func UnmarshalJsonStr(data string, v any) error {
	return codec.Unmarshal(StringToByteSlice(data), v)
}

func DecodeJson(reader io.Reader, v any) error {
	return codec.Decode(reader, v)
}

func Marshal(v any) ([]byte, error) {
	return codec.Marshal(v)
}

// Valid reports whether data is a syntactically valid JSON document.
func Valid(data []byte) bool {
	return codec.Valid(data)
}

func GetJsonType(data json.RawMessage) string {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return "unknown"
	}
	firstChar := trimmed[0]
	switch firstChar {
	case '{':
		return "object"
	case '[':
		return "array"
	case '"':
		return "string"
	case 't', 'f':
		return "boolean"
	case 'n':
		return "null"
	default:
		return "number"
	}
}

// JsonRawMessageToString returns JSON strings as their decoded value and other JSON values as raw text.
func JsonRawMessageToString(data json.RawMessage) string {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return ""
	}
	if trimmed[0] != '"' {
		return string(trimmed)
	}
	var value string
	if err := Unmarshal(trimmed, &value); err != nil {
		return string(trimmed)
	}
	return value
}

func StringToByteSlice(s string) []byte {
	tmp1 := (*[2]uintptr)(unsafe.Pointer(&s))
	tmp2 := [3]uintptr{tmp1[0], tmp1[1], tmp1[1]}
	return *(*[]byte)(unsafe.Pointer(&tmp2))
}

func Any2Type[T any](data any) (T, error) {
	var zero T
	encoded, err := Marshal(data)
	if err != nil {
		return zero, err
	}
	var res T
	err = Unmarshal(encoded, &res)
	if err != nil {
		return zero, err
	}
	return res, nil
}
