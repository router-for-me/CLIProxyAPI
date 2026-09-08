package auth

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/credentialweight"
)

// modelWeightKey normalizes a model identifier for per-model weight lookups. The
// thinking suffix is stripped the same way selector state keys are built, and the
// result is lower-cased so lookups are case-insensitive like registry model matching.
func modelWeightKey(model string) string {
	return strings.ToLower(canonicalModelKey(model))
}

// ParseModelWeights parses an optional per-model weight override table. Accepted
// inputs are a JSON object string, a decoded JSON object (map[string]any), or an
// already-typed map[string]int64. Keys are normalized with modelWeightKey and values
// follow the same rules as the account-level weight (non-positive normalizes to zero,
// which excludes the credential for that model only). A nil or empty input yields nil.
func ParseModelWeights(value any) (map[string]int64, error) {
	if value == nil {
		return nil, nil
	}
	var raw map[string]any
	switch typed := value.(type) {
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return nil, nil
		}
		if errUnmarshal := json.Unmarshal([]byte(trimmed), &raw); errUnmarshal != nil {
			return nil, fmt.Errorf("model_weights must be a JSON object: %w", errUnmarshal)
		}
	case map[string]any:
		raw = typed
	case map[string]int64:
		out := make(map[string]int64, len(typed))
		for model, weight := range typed {
			key := modelWeightKey(model)
			if key == "" {
				return nil, fmt.Errorf("model_weights contains an empty model key")
			}
			normalized, errNormalize := credentialweight.Normalize(weight)
			if errNormalize != nil {
				return nil, fmt.Errorf("model_weights[%s]: %w", model, errNormalize)
			}
			out[key] = normalized
		}
		if len(out) == 0 {
			return nil, nil
		}
		return out, nil
	default:
		return nil, fmt.Errorf("model_weights must be a JSON object")
	}
	if len(raw) == 0 {
		return nil, nil
	}
	out := make(map[string]int64, len(raw))
	for model, rawWeight := range raw {
		key := modelWeightKey(model)
		if key == "" {
			return nil, fmt.Errorf("model_weights contains an empty model key")
		}
		weight, errParse := credentialweight.ParseValue(rawWeight)
		if errParse != nil {
			return nil, fmt.Errorf("model_weights[%s]: %w", model, errParse)
		}
		out[key] = weight
	}
	return out, nil
}

// EncodeModelWeights serializes a normalized per-model weight table into the
// canonical attribute form (a JSON object with sorted keys). Empty input yields "".
func EncodeModelWeights(weights map[string]int64) string {
	if len(weights) == 0 {
		return ""
	}
	keys := make([]string, 0, len(weights))
	for key := range weights {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var builder strings.Builder
	builder.WriteByte('{')
	for index, key := range keys {
		if index > 0 {
			builder.WriteByte(',')
		}
		encodedKey, _ := json.Marshal(key)
		builder.Write(encodedKey)
		builder.WriteByte(':')
		builder.WriteString(fmt.Sprintf("%d", weights[key]))
	}
	builder.WriteByte('}')
	return builder.String()
}

// authModelWeights returns the per-model weight table for an auth. The attribute form
// (set by the synthesizer or the management API) takes precedence over raw metadata,
// mirroring authWeight. Invalid tables are treated as absent so the account-level
// weight keeps applying; validation happens earlier via ValidateAuthWeight.
func authModelWeights(auth *Auth) map[string]int64 {
	if auth == nil {
		return nil
	}
	if raw, ok := auth.Attributes[AttributeModelWeights]; ok {
		if strings.TrimSpace(raw) == "" {
			return nil
		}
		weights, errParse := ParseModelWeights(raw)
		if errParse != nil {
			return nil
		}
		return weights
	}
	if raw, ok := auth.Metadata[AttributeModelWeights]; ok {
		weights, errParse := ParseModelWeights(raw)
		if errParse != nil {
			return nil
		}
		return weights
	}
	return nil
}

// authWeightForModel returns the routing weight of an auth for the requested model.
// When the auth defines model_weights and the (normalized) model is present, that
// value wins; otherwise the account-level weight applies exactly as before. An empty
// model always resolves to the account-level weight.
func authWeightForModel(auth *Auth, model string) int64 {
	if auth == nil {
		return credentialweight.Default
	}
	if key := modelWeightKey(model); key != "" {
		if weights := authModelWeights(auth); len(weights) > 0 {
			if weight, ok := weights[key]; ok {
				return weight
			}
		}
	}
	return authWeight(auth)
}
