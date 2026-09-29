package common

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/go-playground/validator/v10"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestJsonRawMessageToString(t *testing.T) {
	tests := []struct {
		name string
		data json.RawMessage
		want string
	}{
		{
			name: "object",
			data: json.RawMessage(`{"city":"Paris","days":0,"strict":false}`),
			want: `{"city":"Paris","days":0,"strict":false}`,
		},
		{
			name: "string",
			data: json.RawMessage(`"{\"city\":\"Paris\",\"days\":0,\"strict\":false}"`),
			want: `{"city":"Paris","days":0,"strict":false}`,
		},
		{
			name: "null",
			data: json.RawMessage(`null`),
			want: "",
		},
		{
			name: "empty",
			data: nil,
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, JsonRawMessageToString(tt.data))
		})
	}
}

func TestDecodeJsonWithValidation(t *testing.T) {
	type request struct {
		Code string `json:"code" binding:"required"`
	}
	for _, test := range []struct {
		name, body      string
		validationError bool
		decodeError     bool
	}{
		{name: "valid", body: `{"code":"123456"}`},
		{name: "missing required field", body: `{}`, validationError: true},
		{name: "empty required field", body: `{"code":""}`, validationError: true},
		{name: "malformed JSON", body: `{"code":`, decodeError: true},
		{name: "wrong field type", body: `{"code":123456}`, decodeError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var value request
			err := DecodeJsonWithValidation(strings.NewReader(test.body), &value)
			if test.validationError {
				var validationErrors validator.ValidationErrors
				require.ErrorAs(t, err, &validationErrors)
				require.Len(t, validationErrors, 1)
				assert.Equal(t, "Code", validationErrors[0].Field())
				assert.Equal(t, "required", validationErrors[0].Tag())
				return
			}
			if test.decodeError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "123456", value.Code)
		})
	}
	// Existing decoding callers opt into validation explicitly.
	var unvalidated request
	require.NoError(t, DecodeJson(strings.NewReader(`{}`), &unvalidated))
}

// TestHostJSONCodecConformance runs through the codec injected by common's
// init() and locks the encoding semantics the relay DTOs depend on. A future
// engine swap in hostJSONCodec must keep every case here green.
func TestHostJSONCodecConformance(t *testing.T) {
	type embedded struct {
		Content any `json:"content"`
	}
	type shadowed struct {
		embedded
		Content any `json:"content,omitempty"`
	}
	type anyFields struct {
		Nil  any `json:"nil,omitempty"`
		Str  any `json:"str,omitempty"`
		Int  any `json:"int,omitempty"`
		Bool any `json:"bool,omitempty"`
	}
	type rawFields struct {
		Obj    json.RawMessage `json:"obj"`
		Arr    json.RawMessage `json:"arr"`
		Nested json.RawMessage `json:"nested"`
		Str    json.RawMessage `json:"str"`
	}
	type numberField struct {
		N json.Number `json:"n"`
	}
	type pointerZeros struct {
		Count   *int  `json:"count,omitempty"`
		Enabled *bool `json:"enabled,omitempty"`
	}
	zero := 0
	off := false

	t.Run("marshal", func(t *testing.T) {
		for _, tt := range []struct {
			name string
			in   any
			want string
		}{
			{
				name: "shallowest field shadows embedded content and nil is omitted",
				in:   shadowed{embedded: embedded{Content: "inner"}},
				want: `{}`,
			},
			{
				name: "shallowest field keeps empty string content",
				in:   shadowed{embedded: embedded{Content: "inner"}, Content: ""},
				want: `{"content":""}`,
			},
			{
				name: "omitempty on any drops nil but keeps zero values",
				in:   anyFields{Str: "", Int: 0, Bool: false},
				want: `{"str":"","int":0,"bool":false}`,
			},
			{
				name: "map keys are sorted",
				in:   map[string]any{"z": 1, "a": 2, "m": 3},
				want: `{"a":2,"m":3,"z":1}`,
			},
			{
				name: "html characters are escaped",
				in:   map[string]string{"s": `<a href="x">&</a>`},
				want: `{"s":"\u003ca href=\"x\"\u003e\u0026\u003c/a\u003e"}`,
			},
			{
				name: "explicit pointer zeros are kept",
				in:   pointerZeros{Count: &zero, Enabled: &off},
				want: `{"count":0,"enabled":false}`,
			},
			{
				name: "nil pointers are omitted",
				in:   pointerZeros{},
				want: `{}`,
			},
		} {
			t.Run(tt.name, func(t *testing.T) {
				encoded, err := Marshal(tt.in)
				require.NoError(t, err)
				assert.Equal(t, tt.want, string(encoded))
			})
		}
	})

	t.Run("raw message passthrough", func(t *testing.T) {
		input := `{"obj":{},"arr":[],"nested":{"k":[1,2]},"str":"x"}`
		var value rawFields
		require.NoError(t, UnmarshalJsonStr(input, &value))
		assert.Equal(t, `{}`, string(value.Obj))
		assert.Equal(t, `[]`, string(value.Arr))
		assert.Equal(t, `{"k":[1,2]}`, string(value.Nested))
		assert.Equal(t, `"x"`, string(value.Str))
		encoded, err := Marshal(value)
		require.NoError(t, err)
		assert.Equal(t, input, string(encoded))
	})

	t.Run("json.Number keeps large integers exact", func(t *testing.T) {
		input := `{"n":18446744073686646784}`
		var value numberField
		require.NoError(t, Unmarshal([]byte(input), &value))
		assert.Equal(t, json.Number("18446744073686646784"), value.N)
		encoded, err := Marshal(value)
		require.NoError(t, err)
		assert.Equal(t, input, string(encoded))
	})

	t.Run("explicit zeros survive unmarshal into pointers", func(t *testing.T) {
		var value pointerZeros
		require.NoError(t, Unmarshal([]byte(`{"count":0,"enabled":false}`), &value))
		require.NotNil(t, value.Count)
		require.NotNil(t, value.Enabled)
		assert.Equal(t, 0, *value.Count)
		assert.False(t, *value.Enabled)

		var absent pointerZeros
		require.NoError(t, Unmarshal([]byte(`{}`), &absent))
		assert.Nil(t, absent.Count)
		assert.Nil(t, absent.Enabled)
	})

	t.Run("relaykit DTO round trip", func(t *testing.T) {
		raw := []byte(`{
			"model":"kimi-k3",
			"messages":[
				{"role":"system","tools":[{"type":"function","function":{"name":"get_current_time","description":"Get the current time of a city","parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}}]},
				{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_current_time","arguments":"{\"city\":\"Beijing\"}"}}]}
			]
		}`)
		var req dto.GeneralOpenAIRequest
		require.NoError(t, Unmarshal(raw, &req))
		encoded, err := Marshal(req)
		require.NoError(t, err)

		messages := gjson.GetBytes(encoded, "messages").Array()
		require.Len(t, messages, 2)

		// Kimi K3 dynamic tool loading: tools survive and no content key is emitted.
		assert.Equal(t, "system", messages[0].Get("role").String())
		assert.JSONEq(t, gjson.GetBytes(raw, "messages.0.tools").Raw, messages[0].Get("tools").Raw)
		assert.False(t, messages[0].Get("content").Exists())

		// Assistant tool-call replay still carries an explicit "content": null.
		assistantContent := messages[1].Get("content")
		assert.True(t, assistantContent.Exists())
		assert.Equal(t, gjson.Null, assistantContent.Type)
		assert.JSONEq(t, gjson.GetBytes(raw, "messages.1.tool_calls").Raw, messages[1].Get("tool_calls").Raw)
	})
}
