package auth

import (
	"encoding/json"
	"maps"
	"math"
	"unicode/utf8"

	"github.com/MoeForge/moego/ojson"
)

// Policy metadata is a bounded JSON value tree. Bound both depth and the copied
// data before cloning Auth, whose general-purpose clone does not detect cycles.
// Separate allowances keep a large immutable attribute from consuming the
// credential metadata budget. Attributes may carry a bounded serialized catalog:
// a 256 KiB JSON string can cost up to 512 KiB when quoted again, leaving 128 KiB
// for keys and other configuration. Neither allowance depends on attribute names.
const (
	policyMetadataBudget  = 64 << 10
	policyAttributeBudget = 640 << 10
)

func snapshotAuthForPolicy(auth *Auth) (*Auth, error) {
	if auth == nil {
		return nil, authExecutionRefusal{}
	}
	metadataBudget := policyMetadataBudget
	metadata, ok := copyPolicyValue(auth.Metadata, 0, &metadataBudget)
	if !ok {
		return nil, authExecutionRefusal{}
	}
	attributeBudget := policyAttributeBudget
	for key, value := range auth.Attributes {
		chargePolicyString(key, &attributeBudget)
		chargePolicyString(value, &attributeBudget)
		if attributeBudget < 0 {
			return nil, authExecutionRefusal{}
		}
	}
	value := *auth
	value.Metadata, value.Storage, value.Runtime, value.CodexRefreshPatch = nil, nil, nil, nil
	snapshot := value.Clone()
	detachPolicyQuota(&snapshot.Quota)
	for _, state := range snapshot.ModelStates {
		if state != nil {
			detachPolicyQuota(&state.Quota)
			if state.credentialQuotaShadow != nil {
				detachPolicyQuota(&state.credentialQuotaShadow.Quota)
			}
		}
	}
	snapshot.Metadata, _ = metadata.(map[string]any)
	return snapshot, nil
}

func detachPolicyQuota(quota *QuotaState) {
	quota.Signals = maps.Clone(quota.Signals)
	quota.cooldownSignals = maps.Clone(quota.cooldownSignals)
}

func copyPolicyValue(value any, depth int, budget *int) (any, bool) {
	*budget -= 8
	if depth > 64 || *budget < 0 {
		return nil, false
	}
	switch value := value.(type) {
	case nil, bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		*budget -= 24
		return value, *budget >= 0
	case float64:
		*budget -= 32
		return value, *budget >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
	case float32:
		*budget -= 32
		return value, *budget >= 0 && !math.IsNaN(float64(value)) && !math.IsInf(float64(value), 0)
	case json.Number:
		*budget -= len(value)
		return value, *budget >= 0 && len(value) > 0 && (value[0] == '-' || value[0] >= '0' && value[0] <= '9') && ojson.Valid(string(value))
	case string:
		chargePolicyString(value, budget)
		return value, *budget >= 0
	case map[string]any:
		if value == nil {
			return map[string]any(nil), true
		}
		out := make(map[string]any, min(len(value), *budget/8))
		for key, child := range value {
			chargePolicyString(key, budget)
			copied, ok := copyPolicyValue(child, depth+1, budget)
			if !ok {
				return nil, false
			}
			out[key] = copied
		}
		return out, true
	case map[string]string:
		if value == nil {
			return map[string]string(nil), true
		}
		out := make(map[string]string, min(len(value), *budget/8))
		for key, child := range value {
			chargePolicyString(key, budget)
			chargePolicyString(child, budget)
			if *budget < 0 {
				return nil, false
			}
			out[key] = child
		}
		return out, true
	case []any:
		if value == nil {
			return []any(nil), true
		}
		out := make([]any, 0, min(len(value), *budget/8))
		for _, child := range value {
			copied, ok := copyPolicyValue(child, depth+1, budget)
			if !ok {
				return nil, false
			}
			out = append(out, copied)
		}
		return out, true
	case []string:
		if value == nil {
			return []string(nil), true
		}
		for _, child := range value {
			chargePolicyString(child, budget)
			if *budget < 0 {
				return nil, false
			}
		}
		if *budget < 0 {
			return nil, false
		}
		out := make([]string, len(value))
		copy(out, value)
		return out, true
	case []byte:
		if value == nil {
			return []byte(nil), true
		}
		*budget -= 2 * len(value)
		if *budget < 0 {
			return nil, false
		}
		out := make([]byte, len(value))
		copy(out, value)
		return out, true
	default:
		return nil, false
	}
}

func chargePolicyString(value string, budget *int) {
	*budget -= 4
	for _, r := range value {
		switch {
		case r < 0x20 || r == '<' || r == '>' || r == '&' || r == '\u2028' || r == '\u2029':
			*budget -= 6
		case r == '"' || r == '\\':
			*budget -= 2
		default:
			*budget -= utf8.RuneLen(r)
		}
		if *budget < 0 {
			return
		}
	}
}
