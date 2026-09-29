package kitutil

import (
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingCodec notes which Codec method each helper reaches and delegates to
// the standard-library default so the helpers still produce real results.
type recordingCodec struct {
	calls []string
}

func (c *recordingCodec) Marshal(v any) ([]byte, error) {
	c.calls = append(c.calls, "Marshal")
	return stdCodec{}.Marshal(v)
}

func (c *recordingCodec) Unmarshal(data []byte, v any) error {
	c.calls = append(c.calls, "Unmarshal")
	return stdCodec{}.Unmarshal(data, v)
}

func (c *recordingCodec) Decode(r io.Reader, v any) error {
	c.calls = append(c.calls, "Decode")
	return stdCodec{}.Decode(r, v)
}

func (c *recordingCodec) Valid(data []byte) bool {
	c.calls = append(c.calls, "Valid")
	return stdCodec{}.Valid(data)
}

func TestJSONHelpersRouteThroughInjectedCodec(t *testing.T) {
	t.Cleanup(func() { SetCodec(stdCodec{}) })

	fake := &recordingCodec{}
	SetCodec(fake)
	// A nil codec must not displace the installed one.
	SetCodec(nil)

	encoded, err := Marshal(map[string]int{"a": 1})
	require.NoError(t, err)
	assert.Equal(t, `{"a":1}`, string(encoded))

	var fromBytes map[string]int
	require.NoError(t, Unmarshal([]byte(`{"b":2}`), &fromBytes))
	assert.Equal(t, map[string]int{"b": 2}, fromBytes)

	var fromString map[string]int
	require.NoError(t, UnmarshalJsonStr(`{"c":3}`, &fromString))
	assert.Equal(t, map[string]int{"c": 3}, fromString)

	var decoded map[string]int
	require.NoError(t, DecodeJson(strings.NewReader(`{"d":4}`), &decoded))
	assert.Equal(t, map[string]int{"d": 4}, decoded)

	assert.True(t, Valid([]byte(`[]`)))
	assert.False(t, Valid([]byte(`[`)))

	converted, err := Any2Type[map[string]int](map[string]any{"e": 5})
	require.NoError(t, err)
	assert.Equal(t, map[string]int{"e": 5}, converted)

	assert.Equal(t, "hello", JsonRawMessageToString(json.RawMessage(`"hello"`)))

	assert.Equal(t, []string{
		"Marshal",   // Marshal
		"Unmarshal", // Unmarshal
		"Unmarshal", // UnmarshalJsonStr
		"Decode",    // DecodeJson
		"Valid",     // Valid (well-formed)
		"Valid",     // Valid (malformed)
		"Marshal",   // Any2Type encode
		"Unmarshal", // Any2Type decode
		"Unmarshal", // JsonRawMessageToString string literal
	}, fake.calls)
}
