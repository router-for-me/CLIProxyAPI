package cachex

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

type ValueCodec[V any] interface {
	Encode(v V) (string, error)
	Decode(s string) (V, error)
}

type IntCodec struct{}

func (c IntCodec) Encode(v int) (string, error) {
	return strconv.Itoa(v), nil
}

func (c IntCodec) Decode(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty int value")
	}
	return strconv.Atoi(s)
}

type StringCodec struct{}

func (c StringCodec) Encode(v string) (string, error) { return v, nil }
func (c StringCodec) Decode(s string) (string, error) { return s, nil }

type JSONCodec[V any] struct{}

func (c JSONCodec[V]) Encode(v V) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (c JSONCodec[V]) Decode(s string) (V, error) {
	var v V
	if strings.TrimSpace(s) == "" {
		return v, fmt.Errorf("empty json value")
	}
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return v, err
	}
	return v, nil
}
