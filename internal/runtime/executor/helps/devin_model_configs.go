package helps

import (
	"errors"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"google.golang.org/protobuf/encoding/protowire"
)

// DevinGetCliModelConfigsPath is the Connect-RPC unary endpoint listing the account's available models.
const DevinGetCliModelConfigsPath = "/exa.api_server_pb.ApiServerService/GetCliModelConfigs"

// BuildDevinGetCliModelConfigsRequest serializes a GetCliModelConfigs request protobuf.
func BuildDevinGetCliModelConfigsRequest(apiKey, deviceSeed string) []byte {
	var body []byte
	body = protowire.AppendTag(body, 1, protowire.BytesType)
	body = protowire.AppendBytes(body, BuildDevinClientMetadataBytes(apiKey, deviceSeed, ""))
	return body
}

// ParseDevinCliModelConfigs decodes a GetCliModelConfigs response protobuf.
func ParseDevinCliModelConfigs(b []byte) ([]registry.DevinUpstreamModel, error) {
	var results []registry.DevinUpstreamModel
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return nil, errors.New("malformed protobuf tag")
		}
		b = b[n:]

		if num == 1 && typ == protowire.BytesType {
			sub, m := protowire.ConsumeBytes(b)
			if m < 0 {
				return nil, errors.New("malformed protobuf submessage")
			}
			b = b[m:]
			if model := parseDevinCliModelConfig(sub); model.UID != "" {
				results = append(results, model)
			}
			continue
		}
		skip := protowire.ConsumeFieldValue(num, typ, b)
		if skip < 0 {
			return nil, errors.New("failed to skip field")
		}
		b = b[skip:]
	}
	return results, nil
}

func parseDevinCliModelConfig(b []byte) registry.DevinUpstreamModel {
	var m registry.DevinUpstreamModel
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			break
		}
		b = b[n:]

		switch {
		case num == 1 && typ == protowire.BytesType: // label
			if val, l := protowire.ConsumeString(b); l >= 0 {
				m.Label = val
				b = b[l:]
				continue
			}
		case num == 5 && typ == protowire.VarintType: // multimodal
			if val, l := protowire.ConsumeVarint(b); l >= 0 {
				m.Multimodal = val == 1
				b = b[l:]
				continue
			}
		case num == 10 && typ == protowire.VarintType: // vendor
			if val, l := protowire.ConsumeVarint(b); l >= 0 {
				m.VendorID = val
				b = b[l:]
				continue
			}
		case num == 18 && typ == protowire.VarintType: // context length
			if val, l := protowire.ConsumeVarint(b); l >= 0 {
				m.ContextLength = int(val)
				b = b[l:]
				continue
			}
		case num == 22 && typ == protowire.BytesType: // chat_model_uid
			if val, l := protowire.ConsumeString(b); l >= 0 {
				m.UID = val
				b = b[l:]
				continue
			}
		}

		skip := protowire.ConsumeFieldValue(num, typ, b)
		if skip < 0 {
			break
		}
		b = b[skip:]
	}
	return m
}
