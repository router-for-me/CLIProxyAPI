package signature

import "strings"

type SignatureProvider string

const (
	SignatureProviderUnknown      SignatureProvider = "unknown"
	SignatureProviderClaude       SignatureProvider = "claude"
	SignatureProviderGemini       SignatureProvider = "gemini"
	SignatureProviderGeminiBypass SignatureProvider = "gemini_bypass"
	SignatureProviderGPT          SignatureProvider = "gpt"
	// SignatureProviderKimi is identified by fixed signature size rather than by
	// an envelope. See kimi_validation.go for the empirical basis and its limits.
	SignatureProviderKimi SignatureProvider = "kimi"
	// SignatureProviderGrok is a target-only family. DetectSignatureProvider never
	// returns it: xAI emits no envelope, no version byte and no fixed length, and
	// its ciphertext is statistically indistinguishable from uniform random bytes,
	// so any positive claim would also capture every other opaque blob. Grok
	// handling is provenance-first - establish the target from the model or route,
	// then use InspectGrokEncryptedContent as a replay-safety shape check.
	SignatureProviderGrok SignatureProvider = "grok"
)

type SignatureBlockKind string

const (
	SignatureBlockKindUnknown            SignatureBlockKind = "unknown"
	SignatureBlockKindClaudeThinking     SignatureBlockKind = "claude_thinking"
	SignatureBlockKindGeminiModelPart    SignatureBlockKind = "gemini_model_part"
	SignatureBlockKindGeminiFunctionCall SignatureBlockKind = "gemini_function_call"
	SignatureBlockKindGPTReasoning       SignatureBlockKind = "gpt_reasoning"
)

type SignatureCompatibilityAction string

const (
	SignatureActionPreserve                SignatureCompatibilityAction = "preserve"
	SignatureActionDropBlock               SignatureCompatibilityAction = "drop_block"
	SignatureActionDropSignature           SignatureCompatibilityAction = "drop_signature"
	SignatureActionReplaceWithGeminiBypass SignatureCompatibilityAction = "replace_with_gemini_bypass"
	SignatureActionNoCompatibleReplacement SignatureCompatibilityAction = "no_compatible_replacement"
)

type SignatureCompatibilityDecision struct {
	TargetProvider       SignatureProvider
	DetectedProvider     SignatureProvider
	BlockKind            SignatureBlockKind
	Compatible           bool
	Action               SignatureCompatibilityAction
	ReplacementSignature string
	NormalizedSignature  string
	Reason               string
}

// SignatureProviderFromModelName maps common model names to the provider family
// whose signed history can be safely replayed for that model.
func SignatureProviderFromModelName(modelName string) SignatureProvider {
	lower := strings.ToLower(strings.TrimSpace(modelName))
	switch {
	case strings.Contains(lower, "claude"):
		return SignatureProviderClaude
	case strings.Contains(lower, "gemini"):
		return SignatureProviderGemini
	case strings.Contains(lower, "gpt"),
		strings.Contains(lower, "openai"),
		strings.Contains(lower, "codex"),
		strings.HasPrefix(lower, "o1"),
		strings.HasPrefix(lower, "o3"),
		strings.HasPrefix(lower, "o4"):
		return SignatureProviderGPT
	case strings.Contains(lower, "kimi"),
		strings.Contains(lower, "moonshot"),
		strings.HasPrefix(lower, "k2"),
		strings.HasPrefix(lower, "k3"):
		return SignatureProviderKimi
	case strings.Contains(lower, "grok"):
		return SignatureProviderGrok
	default:
		return SignatureProviderUnknown
	}
}

// selfDescribingSignatureFirstChars are the base64 first characters that a
// self-describing provider envelope can produce. A base64 first character is
// exactly the first payload byte shifted right by two, so a single character
// comparison rules out every known envelope without decoding anything:
//
//	'C' -> 0x08..0x0b : Claude CAIS (0x08)
//	'E' -> 0x10..0x13 : Claude single-layer (0x12), Gemini protobuf_field_2 (0x12)
//	'R' -> 0x44..0x47 : Claude double-layer R (0x45, inner 'E')
//	'g' -> 0x80..0x83 : GPT Fernet reasoning (0x80)
//
// Gemini's ascii_uuid envelope is deliberately absent. Its first byte is the
// first hex character of the UUID, which spreads over 'M', 'N', 'O', 'Y' and 'Z'
// depending on the value, and it is never a replay-safe envelope: it resolves to
// SignatureProviderUnknown whether or not it reaches the validators, and Gemini
// model parts recover it through the bypass sentinel keyed on block kind. Listing
// one of its five possible characters would only look like coverage.
//
// Any provider added here must also be validated in
// DetectSignatureProviderForBlock, otherwise its signatures would fall through
// to the residual class. TestSelfDescribingSignatureFirstChars_CoversEveryKnownEnvelope
// fails when a replay-safe envelope is missing from this set.
const selfDescribingSignatureFirstChars = "CERg"

// base64AlphabetSet builds a byte lookup table for the alphanumeric base64 core
// plus the alphabet-specific characters in extra. Signature charset validation
// runs over multi-kilobyte payloads, and a comparison chain over base64 text
// mispredicts on nearly every byte because the characters are effectively random;
// a single table load is branch-free and measures about an order of magnitude
// faster on the observed corpora.
func base64AlphabetSet(extra string) [256]bool {
	var set [256]bool
	for c := byte('A'); c <= 'Z'; c++ {
		set[c] = true
	}
	for c := byte('a'); c <= 'z'; c++ {
		set[c] = true
	}
	for c := byte('0'); c <= '9'; c++ {
		set[c] = true
	}
	for i := 0; i < len(extra); i++ {
		set[extra[i]] = true
	}
	return set
}

// maybeSelfDescribingSignatureEnvelope reports whether rawSignature can possibly
// be a self-describing provider envelope. It is a structural pre-filter, not a
// classifier: a false result is conclusive, a true result only narrows the
// candidate set. Opaque ciphertext that carries no envelope (xAI/Grok
// encrypted_content) is uniformly distributed over the byte space, so this
// rejects roughly 92% of it with one comparison and no allocation.
func maybeSelfDescribingSignatureEnvelope(rawSignature string) bool {
	if rawSignature == "" {
		return false
	}
	return strings.IndexByte(selfDescribingSignatureFirstChars, rawSignature[0]) >= 0
}

// DetectSignatureProvider classifies the provider family that can replay
// rawSignature. It intentionally uses Claude strict validation before Gemini
// detection because Gemini 3 signatures also decode from an E-prefixed base64
// string and can look Claude-like under shallow prefix checks.
func DetectSignatureProvider(rawSignature string) SignatureProvider {
	return DetectSignatureProviderForBlock(rawSignature, SignatureBlockKindUnknown)
}

// DetectSignatureProviderForBlock classifies rawSignature with block-kind
// context. UUID-shaped payloads are deliberately not classified as replay-safe
// provider signatures; callers targeting Gemini should replace them with the
// bypass sentinel.
func DetectSignatureProviderForBlock(rawSignature string, blockKind SignatureBlockKind) SignatureProvider {
	sig := strings.TrimSpace(rawSignature)
	if sig == "" {
		return SignatureProviderUnknown
	}

	if prefixedProvider, unprefixed, ok := SplitSignatureProviderPrefix(sig); ok {
		switch prefixedProvider {
		case SignatureProviderGemini:
			if IsGeminiThoughtSignatureBypass(unprefixed) {
				return SignatureProviderGeminiBypass
			}
			if isRecognizedGeminiProviderSignature(unprefixed, blockKind) {
				return SignatureProviderGemini
			}
		case SignatureProviderClaude:
			if IsValidClaudeThinkingSignature(unprefixed, ClaudeSignatureValidationOptions{Strict: true}) || IsValidClaudeCAISSignature(unprefixed) {
				return SignatureProviderClaude
			}
		case SignatureProviderGPT:
			if IsValidGPTReasoningSignature(unprefixed) {
				return SignatureProviderGPT
			}
		}
		return SignatureProviderUnknown
	}
	if strings.Contains(sig, "#") {
		return SignatureProviderUnknown
	}

	// The bypass sentinel is a plain literal rather than an envelope, so it must
	// be matched before the structural pre-filter below rejects it.
	if IsGeminiThoughtSignatureBypass(sig) {
		return SignatureProviderGeminiBypass
	}
	// Probes run from the strongest marker to the weakest:
	//   1. GPT carries the literal "gAAAA" prefix, which pins both the version
	//      byte and the high timestamp bytes.
	//   2. Claude CAIS carries marker 0x08 plus a literal "claude-" model text.
	//   3. Claude single/double-layer carries marker 0x12 plus the same literal.
	//   4. Gemini validates wire shape only and has no literal to anchor on, so
	//      it is the weakest judge and goes last.
	//
	// This ordering is defense in depth rather than a correctness requirement:
	// Claude envelopes carry extra top-level fields beyond the container, which
	// fails the single-record shape Gemini requires, so the two families stay
	// separable in either order. TestGeminiEnvelopeNeverClaimsClaudeSignatures
	// pins that invariant so a looser Gemini envelope check cannot make the
	// order silently start mattering.
	//
	// The envelope pre-filter gates only the envelope probes. A blob that cannot
	// be an envelope skips straight to the size probe below rather than returning
	// early, because Kimi's uniformly distributed base64 starts with one of
	// "CERg" about 6% of the time and would otherwise be dropped by whichever
	// side of the gate it happened to land on.
	if maybeSelfDescribingSignatureEnvelope(sig) {
		if IsValidGPTReasoningSignature(sig) {
			return SignatureProviderGPT
		}
		if IsValidClaudeCAISSignature(sig) {
			return SignatureProviderClaude
		}
		if IsValidClaudeThinkingSignature(sig, ClaudeSignatureValidationOptions{Strict: true}) {
			return SignatureProviderClaude
		}
		if isRecognizedGeminiProviderSignature(sig, blockKind) {
			return SignatureProviderGemini
		}
	}
	// Kimi carries no envelope, so it can only be claimed once every
	// self-describing probe above has declined. Ordering it last means a length
	// coincidence can never capture another provider's signature, and a future
	// drift in Kimi's sizes costs Kimi its own identification rather than
	// corrupting a neighbouring family.
	if IsValidKimiThinkingSignature(sig) {
		return SignatureProviderKimi
	}
	return SignatureProviderUnknown
}

func IsSignatureCompatibleWithProvider(targetProvider SignatureProvider, rawSignature string) bool {
	decision := DecideSignatureCompatibility(targetProvider, rawSignature, SignatureBlockKindUnknown)
	return decision.Compatible
}

// DecideSignatureCompatibility returns the safe handling policy for replaying a
// signed block into targetProvider.
func DecideSignatureCompatibility(targetProvider SignatureProvider, rawSignature string, blockKind SignatureBlockKind) SignatureCompatibilityDecision {
	return DecideSignatureCompatibilityForModel(targetProvider, "", rawSignature, blockKind)
}

// DecideSignatureCompatibilityForModel returns the safe handling policy for replaying a
// signed block into targetProvider for targetModel.
func DecideSignatureCompatibilityForModel(targetProvider SignatureProvider, targetModel string, rawSignature string, blockKind SignatureBlockKind) SignatureCompatibilityDecision {
	targetProvider = normalizeSignatureTargetProvider(targetProvider)
	if blockKind == "" {
		blockKind = SignatureBlockKindUnknown
	}

	detected := DetectSignatureProviderForBlock(rawSignature, blockKind)
	decision := SignatureCompatibilityDecision{
		TargetProvider:   targetProvider,
		DetectedProvider: detected,
		BlockKind:        blockKind,
	}

	if signatureProviderMatchesTarget(targetProvider, detected) {
		decision.Compatible = true
		decision.Action = SignatureActionPreserve
		decision.NormalizedSignature = normalizeCompatibleSignatureForProvider(targetProvider, rawSignature, blockKind)
		decision.Reason = claudeCompatibleSignatureReason(targetProvider, rawSignature, targetModel)
		return decision
	}

	decision.Compatible = false
	switch targetProvider {
	case SignatureProviderGemini:
		if blockKind == SignatureBlockKindGeminiFunctionCall || blockKind == SignatureBlockKindGeminiModelPart || blockKind == SignatureBlockKindUnknown {
			decision.Action = SignatureActionReplaceWithGeminiBypass
			decision.ReplacementSignature = GeminiSkipThoughtSignatureValidator
			decision.Reason = "Gemini can bypass synthetic or incompatible model-part signatures with the documented sentinel"
			return decision
		}
		decision.Action = SignatureActionDropBlock
		decision.Reason = "signature is not compatible with Gemini and this block is not a bypass-safe Gemini model part"
	case SignatureProviderClaude:
		decision.Action = SignatureActionDropBlock
		decision.Reason = "Claude has no cross-provider bypass sentinel for thinking blocks"
	case SignatureProviderGPT:
		decision.Action = SignatureActionDropBlock
		decision.Reason = "GPT reasoning encrypted_content cannot be synthesized from another provider signature"
	case SignatureProviderKimi:
		// Kimi is the only target that can keep the reasoning text when the
		// signature does not match. Its Messages endpoint never reads the field
		// back: a mutated, truncated, non-base64 or absent signature all return
		// 200, because reasoning continuity there travels in OpenAI-style
		// reasoning_content instead. Dropping the block would discard recoverable
		// thinking text for no upstream benefit, so drop only the signature.
		decision.Action = SignatureActionDropSignature
		decision.Reason = "Kimi does not validate replayed thinking signatures, so the block survives without one"
	case SignatureProviderGrok:
		// xAI decrypts encrypted_content and rejects the request with 400
		// "Could not decrypt" when the blob is foreign or mutated, so a
		// non-matching value has to leave with the block.
		decision.Action = SignatureActionDropBlock
		decision.Reason = "xAI verifies encrypted_content on replay and rejects foreign or mutated blobs"
	default:
		decision.Action = SignatureActionNoCompatibleReplacement
		decision.Reason = "unknown target provider"
	}
	return decision
}

func SplitSignatureProviderPrefix(rawSignature string) (SignatureProvider, string, bool) {
	prefix, rest, ok := strings.Cut(strings.TrimSpace(rawSignature), "#")
	if !ok {
		return SignatureProviderUnknown, rawSignature, false
	}
	provider := SignatureProviderFromCachePrefix(prefix)
	if provider == SignatureProviderUnknown {
		return SignatureProviderUnknown, rawSignature, false
	}
	return provider, strings.TrimSpace(rest), true
}

// SignatureProviderFromCachePrefix maps this repo's explicit provider-prefix
// envelope to a provider family. This is intentionally stricter than
// SignatureProviderFromModelName so arbitrary model names such as
// "claude-cache#..." cannot be mistaken for trusted provider provenance.
func SignatureProviderFromCachePrefix(prefix string) SignatureProvider {
	switch strings.ToLower(strings.TrimSpace(prefix)) {
	case "claude", "anthropic", "cais", "claude-cais", "claude_cais", "ccmax", "claude-code-max", "claude_code_max":
		return SignatureProviderClaude
	case "gemini", "google":
		return SignatureProviderGemini
	case "openai", "gpt", "codex":
		return SignatureProviderGPT
	default:
		return SignatureProviderUnknown
	}
}

// SignaturePayloadWithoutProviderPrefix strips this repo's provider cache prefix
// when present. The returned string is the value that should be replayed to an
// upstream provider.
func SignaturePayloadWithoutProviderPrefix(rawSignature string) string {
	if _, unprefixed, ok := SplitSignatureProviderPrefix(rawSignature); ok {
		return unprefixed
	}
	return strings.TrimSpace(rawSignature)
}

// CompatibleSignatureForProvider returns a replayable provider-native signature
// for targetProvider. It strips this repo's provider prefix and normalizes
// Claude signatures to the format expected by the target when possible.
func CompatibleSignatureForProvider(targetProvider SignatureProvider, rawSignature string) (string, bool) {
	return CompatibleSignatureForProviderBlock(targetProvider, rawSignature, SignatureBlockKindUnknown)
}

// CompatibleSignatureForProviderBlock returns a replayable provider-native
// signature for targetProvider when the source block kind is known.
func CompatibleSignatureForProviderBlock(targetProvider SignatureProvider, rawSignature string, blockKind SignatureBlockKind) (string, bool) {
	decision := DecideSignatureCompatibility(targetProvider, rawSignature, blockKind)
	if !decision.Compatible || decision.NormalizedSignature == "" {
		return "", false
	}
	return decision.NormalizedSignature, true
}

// CompatibleAntigravityClaudeThinkingSignature returns the double-layer R-form
// required by Antigravity Claude replay. It only accepts signatures that are
// strictly identifiable as Claude, so Gemini E-prefixed envelopes cannot slip
// through the looser Antigravity bypass normalization path.
func CompatibleAntigravityClaudeThinkingSignature(rawSignature string) (string, bool) {
	if DetectSignatureProviderForBlock(rawSignature, SignatureBlockKindClaudeThinking) != SignatureProviderClaude {
		return "", false
	}
	normalized, err := NormalizeClaudeThinkingSignature(
		SignaturePayloadWithoutProviderPrefix(rawSignature),
		ClaudeSignatureValidationOptions{Strict: true},
	)
	if err != nil {
		return "", false
	}
	return normalized, true
}

// claudeCompatibleSignatureReason explains why a matching signature is
// replayable. Claude CAIS signatures carry the issuing model inside the payload,
// so the embedded model and the target model are both reported to make signature
// decisions traceable in debug logs.
func claudeCompatibleSignatureReason(targetProvider SignatureProvider, rawSignature, targetModel string) string {
	const genericReason = "signature provider matches target provider"
	if targetProvider != SignatureProviderClaude {
		return genericReason
	}
	info, err := InspectClaudeCAISSignature(SignaturePayloadWithoutProviderPrefix(rawSignature))
	if err != nil {
		return genericReason
	}
	reason := "valid Claude CAIS signature with embedded model " + info.ModelText + " is compatible with any Claude target"
	if trimmedModel := strings.TrimSpace(targetModel); trimmedModel != "" {
		reason += ", including target model " + trimmedModel
	}
	return reason
}

func normalizeSignatureTargetProvider(provider SignatureProvider) SignatureProvider {
	switch provider {
	case SignatureProviderGeminiBypass:
		return SignatureProviderGemini
	default:
		return provider
	}
}

func signatureProviderMatchesTarget(target, detected SignatureProvider) bool {
	switch target {
	case SignatureProviderGemini:
		return detected == SignatureProviderGemini || detected == SignatureProviderGeminiBypass
	case SignatureProviderClaude:
		return detected == SignatureProviderClaude
	case SignatureProviderGPT:
		return detected == SignatureProviderGPT
	case SignatureProviderKimi:
		return detected == SignatureProviderKimi
	default:
		// SignatureProviderGrok is deliberately absent. Detection never yields it,
		// so a Grok target must decide replay safety from provenance plus
		// InspectGrokEncryptedContent rather than from a detected-provider match.
		return false
	}
}

func normalizeCompatibleSignatureForProvider(targetProvider SignatureProvider, rawSignature string, blockKind SignatureBlockKind) string {
	payload := SignaturePayloadWithoutProviderPrefix(rawSignature)
	switch normalizeSignatureTargetProvider(targetProvider) {
	case SignatureProviderClaude:
		if IsValidClaudeCAISSignature(payload) {
			return payload
		}
		normalized, err := NormalizeClaudeProviderNativeThinkingSignature(payload)
		if err != nil {
			return ""
		}
		return normalized
	case SignatureProviderGemini:
		if IsGeminiThoughtSignatureBypass(payload) {
			return payload
		}
		if isRecognizedGeminiProviderSignature(payload, blockKind) {
			return payload
		}
	case SignatureProviderGPT:
		if IsValidGPTReasoningSignature(payload) {
			return payload
		}
	case SignatureProviderKimi:
		if IsValidKimiThinkingSignature(payload) {
			return payload
		}
	}
	return ""
}

func isRecognizedGeminiProviderSignature(rawSignature string, blockKind SignatureBlockKind) bool {
	if IsValidClaudeCAISSignature(rawSignature) {
		return false
	}
	if IsValidGeminiThoughtSignature(rawSignature, GeminiThoughtSignatureValidationOptions{RequireKnownEnvelope: true}) {
		return true
	}
	return false
}
