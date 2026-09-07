package autorouter

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// TierThresholds controls the upper bounds used to classify a weighted score.
// Values must satisfy 0 <= SimpleMax < MediumMax < ComplexMax <= 1.
type TierThresholds struct {
	SimpleMax  float64 `json:"simple_max"`
	MediumMax  float64 `json:"medium_max"`
	ComplexMax float64 `json:"complex_max"`
}

// KeywordTierRule is a deterministic literal override. When multiple rules
// match, the highest matching tier wins regardless of rule order.
type KeywordTierRule struct {
	ID       string   `json:"id"`
	Tier     Tier     `json:"tier"`
	Keywords []string `json:"keywords"`
}

// MatchedKeywordRule records only the rule and keywords that matched a request.
// It never contains the request text.
type MatchedKeywordRule struct {
	ID       string   `json:"id"`
	Tier     Tier     `json:"tier"`
	Keywords []string `json:"keywords"`
}

// ProfileConfig is the mutable scoring policy for one Auto Router.
type ProfileConfig struct {
	Thresholds       TierThresholds         `json:"thresholds"`
	Weights          map[ScoreField]float64 `json:"weights"`
	KeywordTierRules []KeywordTierRule      `json:"keyword_tier_rules"`
}

// Profile is a versioned scoring policy loaded for a router at runtime.
type Profile struct {
	ProfileConfig
	ProfileVersion int64  `json:"profile_version"`
	ProfileHash    string `json:"profile_hash"`
}

// compiledKeywordRule is one keyword-tier rule with its keywords already
// normalized, so request-time matching never re-normalizes rule definitions.
type compiledKeywordRule struct {
	id       string
	tier     Tier
	keywords []string
}

// CompiledProfile is a profile preprocessed for scoring: the config is
// normalized, every rule keyword is normalized once, and the identity hash is
// fixed. Building one costs the full NormalizeProfile + hash work; scoring
// with one skips that work entirely (see ScoreWithProfileCompiled).
type CompiledProfile struct {
	Config           ProfileConfig
	KeywordTierRules []compiledKeywordRule
	// Hash is the profile identity ("sha256:…"), identical to ProfileHash(Config).
	Hash string
	// Version is copied from the profile row (0 for the built-in default).
	Version int64
}

// CompileProfile validates, normalizes, and precompiles a profile config.
func CompileProfile(config ProfileConfig) (CompiledProfile, error) {
	normalized, err := NormalizeProfile(config)
	if err != nil {
		return CompiledProfile{}, err
	}
	hash, err := ProfileHash(normalized)
	if err != nil {
		return CompiledProfile{}, err
	}
	rules := make([]compiledKeywordRule, 0, len(normalized.KeywordTierRules))
	for _, r := range normalized.KeywordTierRules {
		keywords := make([]string, 0, len(r.Keywords))
		for _, kw := range r.Keywords {
			if kw = normalizeKeywordText(kw); kw != "" {
				keywords = append(keywords, kw)
			}
		}
		rules = append(rules, compiledKeywordRule{id: r.ID, tier: r.Tier, keywords: keywords})
	}
	return CompiledProfile{
		Config:           normalized,
		KeywordTierRules: rules,
		Hash:             hash,
	}, nil
}

// DefaultCompiledProfile compiles the built-in scoring policy.
func DefaultCompiledProfile() CompiledProfile {
	c, _ := CompileProfile(DefaultProfileConfig())
	c.Version = 1
	return c
}

// compiledMatchedRules is the compiled-path twin of matchedKeywordRules: it
// walks precompiled rules over pre-normalized text with no per-request
// normalization work.
func (p *CompiledProfile) compiledMatchedRules(text string) []MatchedKeywordRule {
	if p == nil {
		return nil
	}
	text = " " + text + " "
	matched := make([]MatchedKeywordRule, 0)
	for _, rule := range p.KeywordTierRules {
		keywords := make([]string, 0)
		for _, keyword := range rule.keywords {
			if keyword != "" && strings.Contains(text, " "+keyword+" ") {
				keywords = append(keywords, keyword)
			}
		}
		if len(keywords) == 0 {
			continue
		}
		sort.Strings(keywords)
		matched = append(matched, MatchedKeywordRule{ID: rule.id, Tier: rule.tier, Keywords: keywords})
	}
	return matched
}

// DefaultProfileConfig returns a fresh copy of the built-in scoring policy.
func DefaultProfileConfig() ProfileConfig {
	weights := make(map[ScoreField]float64, len(DefaultWeights))
	for field, weight := range DefaultWeights {
		weights[field] = weight
	}
	return ProfileConfig{
		Thresholds: TierThresholds{
			SimpleMax:  SimpleMax,
			MediumMax:  MediumMax,
			ComplexMax: ComplexMax,
		},
		Weights:          weights,
		KeywordTierRules: []KeywordTierRule{},
	}
}

// DefaultProfile returns the built-in policy with a deterministic hash.
func DefaultProfile() Profile {
	config := DefaultProfileConfig()
	hash, _ := ProfileHash(config)
	return Profile{ProfileConfig: config, ProfileVersion: 1, ProfileHash: hash}
}

// NormalizeProfile trims and sorts a profile so persistence and hashing are
// deterministic. It returns an error when the resulting configuration is invalid.
func NormalizeProfile(config ProfileConfig) (ProfileConfig, error) {
	out := ProfileConfig{
		Thresholds: config.Thresholds,
		Weights:    make(map[ScoreField]float64, len(config.Weights)),
	}
	for field, weight := range config.Weights {
		out.Weights[ScoreField(strings.ToLower(strings.TrimSpace(string(field))))] = weight
	}
	out.KeywordTierRules = make([]KeywordTierRule, 0, len(config.KeywordTierRules))
	for _, rule := range config.KeywordTierRules {
		normalized := KeywordTierRule{
			ID:   strings.TrimSpace(rule.ID),
			Tier: Tier(strings.ToLower(strings.TrimSpace(string(rule.Tier)))),
		}
		seen := map[string]bool{}
		for _, keyword := range rule.Keywords {
			keyword = normalizeKeywordText(keyword)
			if keyword == "" || seen[keyword] {
				continue
			}
			seen[keyword] = true
			normalized.Keywords = append(normalized.Keywords, keyword)
		}
		sort.Strings(normalized.Keywords)
		out.KeywordTierRules = append(out.KeywordTierRules, normalized)
	}
	sort.Slice(out.KeywordTierRules, func(i, j int) bool {
		if out.KeywordTierRules[i].ID != out.KeywordTierRules[j].ID {
			return out.KeywordTierRules[i].ID < out.KeywordTierRules[j].ID
		}
		if out.KeywordTierRules[i].Tier != out.KeywordTierRules[j].Tier {
			return out.KeywordTierRules[i].Tier < out.KeywordTierRules[j].Tier
		}
		return strings.Join(out.KeywordTierRules[i].Keywords, "\x00") < strings.Join(out.KeywordTierRules[j].Keywords, "\x00")
	})
	if err := ValidateProfile(out); err != nil {
		return ProfileConfig{}, err
	}
	return out, nil
}

// ValidateProfile validates thresholds, weights, and literal keyword rules.
func ValidateProfile(config ProfileConfig) error {
	t := config.Thresholds
	if t.SimpleMax < 0 || t.MediumMax < 0 || t.ComplexMax < 0 ||
		t.SimpleMax >= t.MediumMax || t.MediumMax >= t.ComplexMax || t.ComplexMax > 1 {
		return fmt.Errorf("thresholds must satisfy 0 <= simple_max < medium_max < complex_max <= 1")
	}
	if len(config.Weights) != len(DefaultWeights) {
		return fmt.Errorf("weights must contain all %d canonical score fields", len(DefaultWeights))
	}
	positive := false
	for field, weight := range config.Weights {
		if _, ok := DefaultWeights[field]; !ok {
			return fmt.Errorf("unknown score field %q", field)
		}
		if weight < 0 {
			return fmt.Errorf("weight %q must not be negative", field)
		}
		if weight > 0 && field != FieldSimpleIndic {
			positive = true
		}
	}
	if !positive {
		return fmt.Errorf("at least one non-penalty score weight must be positive")
	}
	if len(config.KeywordTierRules) > 100 {
		return fmt.Errorf("keyword_tier_rules must contain at most 100 rules")
	}
	seenIDs := map[string]bool{}
	for _, rule := range config.KeywordTierRules {
		if rule.ID == "" {
			return fmt.Errorf("keyword rule id is required")
		}
		if seenIDs[rule.ID] {
			return fmt.Errorf("duplicate keyword rule id %q", rule.ID)
		}
		seenIDs[rule.ID] = true
		if !isCanonicalTier(rule.Tier) {
			return fmt.Errorf("keyword rule %q has invalid tier %q", rule.ID, rule.Tier)
		}
		if len(rule.Keywords) == 0 || len(rule.Keywords) > 20 {
			return fmt.Errorf("keyword rule %q must contain 1 to 20 keywords", rule.ID)
		}
		for _, keyword := range rule.Keywords {
			if normalizeKeywordText(keyword) == "" {
				return fmt.Errorf("keyword rule %q contains an empty keyword", rule.ID)
			}
		}
	}
	return nil
}

// ProfileHash returns a stable SHA-256 identity for the canonical profile
// configuration. Version and hash fields are intentionally excluded.
func ProfileHash(config ProfileConfig) (string, error) {
	normalized, err := NormalizeProfile(config)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return "", fmt.Errorf("marshal profile: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func isCanonicalTier(tier Tier) bool {
	for _, candidate := range TierOrder {
		if tier == candidate {
			return true
		}
	}
	return false
}

// normalizeKeywordText lowercases a keyword and treats punctuation as word
// separators, allowing literal phrases to match normal request punctuation.
func normalizeKeywordText(text string) string {
	var b strings.Builder
	lastSpace := true
	for _, r := range strings.ToLower(text) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastSpace = false
			continue
		}
		if !lastSpace {
			b.WriteByte(' ')
			lastSpace = true
		}
	}
	return strings.TrimSpace(b.String())
}

// matchedKeywordRules returns deterministic matches for the supplied rules.
// The text must already be normalized (normalizeKeywordText applied by the
// caller exactly once per request); keywords are normalized here because they
// are short and rule definitions may arrive raw from the profile store.
func matchedKeywordRules(text string, rules []KeywordTierRule) []MatchedKeywordRule {
	text = " " + text + " "
	matched := make([]MatchedKeywordRule, 0)
	for _, rule := range rules {
		keywords := make([]string, 0)
		for _, keyword := range rule.Keywords {
			keyword = normalizeKeywordText(keyword)
			if keyword != "" && strings.Contains(text, " "+keyword+" ") {
				keywords = append(keywords, keyword)
			}
		}
		if len(keywords) == 0 {
			continue
		}
		sort.Strings(keywords)
		matched = append(matched, MatchedKeywordRule{ID: rule.ID, Tier: rule.Tier, Keywords: keywords})
	}
	return matched
}

// DecisionSnapshot is the explainability payload persisted with a routed event.
type DecisionSnapshot struct {
	ProfileVersion   int64                  `json:"profile_version"`
	ProfileHash      string                 `json:"profile_hash"`
	ProfileSnapshot  ProfileConfig          `json:"profile_snapshot"`
	ScoreTotal       float64                `json:"score_total"`
	ScoreFields      map[ScoreField]float64 `json:"score_fields"`
	ReasoningMarkers int                    `json:"reasoning_markers"`
	ScoredTier       Tier                   `json:"scored_tier"`
	EffectiveTier    Tier                   `json:"effective_tier"`
	DecisionCause    string                 `json:"decision_cause"`
	MatchedRules     []MatchedKeywordRule   `json:"matched_rules,omitempty"`
	MappingTier      Tier                   `json:"mapping_tier"`
	FallbackChain    []Tier                 `json:"fallback_chain,omitempty"`
	TargetModel      string                 `json:"target_model"`
}
