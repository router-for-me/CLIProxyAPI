package executor

import (
	"encoding/json"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/tidwall/sjson"
)

// neuralwattAllowedServiceTiers is the whitelist of values we will stamp into
// outgoing Neuralwatt payloads. Neuralwatt's OpenAI-compatible schema accepts
// "standard" upstream, but the project's internal config uses "default" as the
// canonical synonym (see internal/config/config_types.go CodexKey.ServiceTier
// doc + internal/watcher/synthesizer/config.go which round-trips the value
// verbatim). Allowing both keeps the executor consistent with the synthesizer
// while letting the API speak its upstream-native dialect via "default".
//
// Values outside this set are intentionally NOT mapped or coerced — an unknown
// attribute value must never reach the upstream; if the user typo'd their
// config, omitting the field lets the upstream apply its own default and the
// mismatch becomes a configuration bug visible in logs instead of a 400.
var neuralwattAllowedServiceTiers = map[string]struct{}{
	"default": {},
	"flex":    {},
}

// applyNeuralwattServiceTier returns req.Payload with service_tier stamped on
// it when the credential configures one of the supported values ("default" or
// "flex"). Unknown, empty, or absent values leave the payload untouched so
// the upstream's own default applies. The original payload is never mutated:
// callers (NeuralwattExecutor.Execute / ExecuteStream) hand in req.Payload by
// value but the slice header may still be shared elsewhere in the request
// pipeline, so we only ever return a freshly-allocated []byte from
// sjson.SetBytes.
func applyNeuralwattServiceTier(payload []byte, auth *cliproxyauth.Auth) []byte {
	if auth == nil {
		return payload
	}
	tier, ok := auth.Attributes["service_tier"]
	if !ok {
		return payload
	}
	if _, allowed := neuralwattAllowedServiceTiers[tier]; !allowed {
		return payload
	}
	if len(payload) == 0 || !json.Valid(payload) {
		// Leave empty or malformed bodies untouched: the upstream (or a downstream
		// validator) should reject them, not us fabricating a fresh document. The
		// upstream would otherwise receive a valid but semantically meaningless
		// {"service_tier":"<tier>"} body that may actually be accepted.
		return payload
	}
	out, err := sjson.SetBytes(payload, "service_tier", tier)
	if err != nil {
		// sjson.SetBytes only errors on invalid paths (e.g. a non-numeric array
		// index). Our path is a plain string key so this branch is unreachable
		// in practice — kept as a defensive passthrough.
		return payload
	}
	return out
}
