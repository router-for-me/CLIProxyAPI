package main

import (
	"errors"
	"math"
	"strings"
)

// ModelTier is supplied by the host's shared model/pricing map. This plugin
// does not define its own model tiers or infer family from a request claim.
type ModelTier struct {
	Family string  `json:"family"`
	Tier   float64 `json:"tier"`
}

type Ask func(requested, candidate string) bool

type Decision struct {
	Model              string `json:"model"`
	Stopped            bool   `json:"stopped"`
	CrossFamilyConsent bool   `json:"cross_family_consent"`
	Reason             string `json:"reason,omitempty"`
	Line               string `json:"line,omitempty"`
}

// Intercept authorizes one proposed fallback card. It never performs model
// dispatch. Missing data or approval stops on the requested model. Numeric
// tiers increase upward: more capable/costly models have larger values.
func Intercept(requested, candidate, header string, models map[string]ModelTier, retries int, ask Ask) Decision {
	consent := header == "1"
	source, sourceOK := models[normalizeModel(requested)]
	target, targetOK := models[normalizeModel(candidate)]
	if !sourceOK || !targetOK || !validTier(source) || !validTier(target) {
		return stop(requested, consent, "missing_model_tier")
	}
	if target.Tier > source.Tier {
		return stop(requested, consent, "upward_tier")
	}
	if source.Family != target.Family && !consent {
		return stop(requested, false, "cross_family_consent_required")
	}
	if requested == candidate && retries >= 1 {
		return stop(requested, consent, "same_model_retry_exhausted")
	}
	if ask == nil {
		return stop(requested, consent, "ask_required")
	}
	if !ask(requested, candidate) {
		return stop(requested, consent, "ask_denied")
	}
	return Decision{Model: candidate, CrossFamilyConsent: consent}
}

func validTier(model ModelTier) bool {
	return model.Family != "" && !math.IsNaN(model.Tier) && !math.IsInf(model.Tier, 0) && model.Tier >= 0
}

func normalizeModel(model string) string {
	model = stripRunPrefix(model)
	for _, effort := range []string{"high", "low", "medium", "xhigh", "max", "none", "minimal"} {
		for _, suffix := range []string{"-" + effort, "/" + effort, "(" + effort + ")"} {
			if strings.HasSuffix(model, suffix) {
				return strings.TrimSuffix(model, suffix)
			}
		}
	}
	return model
}

func stripRunPrefix(model string) string {
	for _, prefix := range []string{"cpa-base/", "cpa-fast/", "cpa-deep/", "cpa-base:", "cpa-fast:", "cpa-deep:"} {
		if strings.HasPrefix(model, prefix) {
			return strings.TrimPrefix(model, prefix)
		}
	}
	return model
}

func stop(requested string, consent bool, reason string) Decision {
	return Decision{
		Model: requested, Stopped: true, CrossFamilyConsent: consent, Reason: reason,
		Line: "No confirmed lower-tier model in the same family. Stopped on " + requested + ".",
	}
}

var errInvalidGate = errors.New("invalid family_alias_ask_gate metadata")

func main() {}
