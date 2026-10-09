package helps

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"google.golang.org/protobuf/encoding/protowire"
)

// Devin payload rules address the native business fields, never credentials or
// Connect framing. Encoding after the barrier does not inject defaults or sanitize.
type devinPayloadField struct {
	name      string
	kind      protowire.Type
	repeated  bool
	binary    bool
	jsonValue bool
	children  map[protowire.Number]devinPayloadField
}

var devinPayloadFields = map[protowire.Number]devinPayloadField{
	2: {name: "system_prompt", kind: protowire.BytesType},
	3: {name: "prompts", kind: protowire.BytesType, repeated: true, children: map[protowire.Number]devinPayloadField{
		1: {name: "id", kind: protowire.BytesType},
		2: {name: "source", kind: protowire.VarintType},
		3: {name: "content", kind: protowire.BytesType},
		6: {name: "tool_calls", kind: protowire.BytesType, repeated: true, children: map[protowire.Number]devinPayloadField{
			1: {name: "id", kind: protowire.BytesType},
			2: {name: "name", kind: protowire.BytesType},
			3: {name: "arguments", kind: protowire.BytesType},
		}},
		7: {name: "tool_call_id", kind: protowire.BytesType},
		10: {name: "images", kind: protowire.BytesType, repeated: true, children: map[protowire.Number]devinPayloadField{
			1: {name: "data", kind: protowire.BytesType},
			2: {name: "mime_type", kind: protowire.BytesType},
		}},
		11: {name: "thinking", kind: protowire.BytesType},
		12: {name: "signature", kind: protowire.BytesType, binary: true},
		18: {name: "signature_type", kind: protowire.BytesType},
	}},
	8: {name: "completion_config", kind: protowire.BytesType, children: map[protowire.Number]devinPayloadField{
		1: {name: "enabled", kind: protowire.VarintType},
		2: {name: "max_tokens", kind: protowire.VarintType},
		3: {name: "parameter_3", kind: protowire.VarintType},
		5: {name: "temperature", kind: protowire.Fixed64Type},
		7: {name: "top_k", kind: protowire.VarintType},
		8: {name: "top_p", kind: protowire.Fixed64Type},
	}},
	10: {name: "tools", kind: protowire.BytesType, repeated: true, children: map[protowire.Number]devinPayloadField{
		1: {name: "name", kind: protowire.BytesType},
		2: {name: "description", kind: protowire.BytesType},
		3: {name: "parameters", kind: protowire.BytesType, jsonValue: true},
	}},
	16: {name: "cascade_id", kind: protowire.BytesType},
	21: {name: "model", kind: protowire.BytesType},
}

// DevinPayloadDefaultsSource maps caller field presence to the native business
// view so defaults can replace built-in values without replacing caller values.
func DevinPayloadDefaultsSource(native, interactions []byte) []byte {
	out := []byte(`{}`)
	for target, sources := range map[string][]string{
		"model":                         {"model"},
		"system_prompt":                 {"system_instruction", "systemInstruction"},
		"prompts":                       {"input"},
		"tools":                         {"tools"},
		"cascade_id":                    {"session_id", "sessionId", "conversation_id", "previous_interaction_id"},
		"completion_config.max_tokens":  {"generation_config.max_output_tokens", "generationConfig.max_output_tokens"},
		"completion_config.temperature": {"generation_config.temperature", "generationConfig.temperature", "temperature"},
		"completion_config.top_k":       {"generation_config.top_k", "generationConfig.top_k"},
		"completion_config.top_p":       {"generation_config.top_p", "generationConfig.top_p"},
	} {
		var value gjson.Result
		for _, source := range sources {
			value = gjson.GetBytes(interactions, source)
			if value.Exists() {
				break
			}
		}
		if !value.Exists() {
			continue
		}
		if target == "prompts" || target == "tools" {
			value = gjson.GetBytes(native, target)
		}
		if value.Exists() {
			out, _ = sjson.SetRawBytes(out, target, []byte(value.Raw))
		}
	}
	return out
}

// FinalizeDevinPayload projects the already-normalized protobuf business fields
// to JSON for user rules, then replaces only those fields in the wire message.
func FinalizeDevinPayload(wire []byte, finalize PayloadFinalizer) ([]byte, []byte, error) {
	view, errDecode := decodeDevinPayload(wire, devinPayloadFields)
	if errDecode != nil {
		return nil, nil, errDecode
	}
	body, errMarshal := json.Marshal(view)
	if errMarshal != nil {
		return nil, nil, errMarshal
	}
	configured := finalize(body)
	if bytes.Equal(configured, body) {
		return wire, configured, nil
	}
	body = configured
	out, errEncode := encodeDevinPayload(wire, gjson.ParseBytes(body), devinPayloadFields)
	return out, body, errEncode
}

func decodeDevinPayload(wire []byte, fields map[protowire.Number]devinPayloadField) (map[string]any, error) {
	out := make(map[string]any)
	for len(wire) > 0 {
		num, kind, tagLen := protowire.ConsumeTag(wire)
		if tagLen < 0 {
			return nil, protowire.ParseError(tagLen)
		}
		wire = wire[tagLen:]
		n := protowire.ConsumeFieldValue(num, kind, wire)
		if n < 0 {
			return nil, protowire.ParseError(n)
		}
		field, ok := fields[num]
		if ok {
			var value any
			switch kind {
			case protowire.BytesType:
				data, _ := protowire.ConsumeBytes(wire)
				switch {
				case field.children != nil:
					decoded, errDecode := decodeDevinPayload(data, field.children)
					if errDecode != nil {
						return nil, errDecode
					}
					value = decoded
				case field.binary:
					value = base64.StdEncoding.EncodeToString(data)
				case field.jsonValue && json.Valid(data):
					value = json.RawMessage(data)
				default:
					value = string(data)
				}
			case protowire.VarintType:
				value, _ = protowire.ConsumeVarint(wire)
			case protowire.Fixed64Type:
				bits, _ := protowire.ConsumeFixed64(wire)
				value = math.Float64frombits(bits)
			}
			if field.repeated {
				values, _ := out[field.name].([]any)
				out[field.name] = append(values, value)
			} else {
				out[field.name] = value
			}
		}
		wire = wire[n:]
	}
	return out, nil
}

func encodeDevinPayload(original []byte, body gjson.Result, fields map[protowire.Number]devinPayloadField) ([]byte, error) {
	var out []byte
	// Credentials, device metadata, thread ordinals, and protocol flags stay opaque.
	for len(original) > 0 {
		num, kind, tagLen := protowire.ConsumeTag(original)
		if tagLen < 0 {
			return nil, protowire.ParseError(tagLen)
		}
		n := protowire.ConsumeFieldValue(num, kind, original[tagLen:])
		if n < 0 {
			return nil, protowire.ParseError(n)
		}
		if _, ok := fields[num]; !ok {
			out = append(out, original[:tagLen+n]...)
		}
		original = original[tagLen+n:]
	}
	for num := protowire.Number(1); num <= 21; num++ {
		field, ok := fields[num]
		if !ok {
			continue
		}
		value := body.Get(field.name)
		if !value.Exists() || value.Type == gjson.Null {
			continue
		}
		values := []gjson.Result{value}
		if field.repeated {
			values = value.Array()
		}
		for _, item := range values {
			out = protowire.AppendTag(out, num, field.kind)
			switch field.kind {
			case protowire.BytesType:
				data := []byte(item.String())
				if field.children != nil {
					var errEncode error
					data, errEncode = encodeDevinPayload(nil, item, field.children)
					if errEncode != nil {
						return nil, errEncode
					}
				} else if field.binary {
					var errDecode error
					data, errDecode = base64.StdEncoding.DecodeString(item.String())
					if errDecode != nil {
						return nil, fmt.Errorf("Devin payload %s: %w", field.name, errDecode)
					}
				} else if field.jsonValue {
					data = []byte(item.Raw)
				}
				out = protowire.AppendBytes(out, data)
			case protowire.VarintType:
				out = protowire.AppendVarint(out, item.Uint())
			case protowire.Fixed64Type:
				out = protowire.AppendFixed64(out, math.Float64bits(item.Float()))
			}
		}
	}
	return out, nil
}
