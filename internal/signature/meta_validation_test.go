package signature

import (
	"encoding/base64"
	"strings"
	"testing"
)

// testMetaReasoningSignature builds a synthetic Meta Muse envelope with the
// observed fixed header and a power-of-two payload bucket. The body bytes are
// deterministic filler; recognition is a transport-shape check, so no real
// captured value is needed (or permitted) here.
func testMetaReasoningSignature(payloadLen int) string {
	return base64.RawURLEncoding.EncodeToString(testMetaReasoningSignatureBytes(payloadLen, nil))
}

func testMetaReasoningSignatureBytes(payloadLen int, mutate func([]byte)) []byte {
	decoded := make([]byte, metaReasoningSignatureOverheadLen+payloadLen)
	copy(decoded, metaReasoningSignatureHeader)
	for i := len(metaReasoningSignatureHeader); i < len(decoded); i++ {
		decoded[i] = byte(i*31 + 7)
	}
	if mutate != nil {
		mutate(decoded)
	}
	return decoded
}

// testMetaRouterSuffix builds a synthetic router suffix in the observed
// OpenRouter shape: a single run of 67 alphanumeric characters.
func testMetaRouterSuffix() string {
	return strings.Repeat("Ab3", 23)[:67]
}

func TestInspectMetaReasoningSignature_ValidBuckets(t *testing.T) {
	for _, tc := range []struct {
		payload  int
		exponent int
	}{
		{1024, 0},
		{2048, 1},
		{4096, 2},
		{32768, 5},
	} {
		sig := testMetaReasoningSignature(tc.payload)
		info, err := InspectMetaReasoningSignature(sig)
		if err != nil {
			t.Fatalf("payload %d: InspectMetaReasoningSignature error = %v", tc.payload, err)
		}
		if info.DecodedLen != tc.payload+metaReasoningSignatureOverheadLen || info.PayloadLen != tc.payload || info.BucketExponent != tc.exponent {
			t.Fatalf("payload %d: info = %+v", tc.payload, info)
		}
		if !IsValidMetaReasoningSignature(sig) {
			t.Fatalf("payload %d: IsValidMetaReasoningSignature = false", tc.payload)
		}
		if got := DetectSignatureProvider(sig); got != SignatureProviderMeta {
			t.Fatalf("payload %d: DetectSignatureProvider = %q, want %q", tc.payload, got, SignatureProviderMeta)
		}
	}
}

func TestInspectMetaReasoningSignature_RejectsMalformed(t *testing.T) {
	valid := testMetaReasoningSignature(1024)
	encode := func(payloadLen int, mutate func([]byte)) string {
		return base64.RawURLEncoding.EncodeToString(testMetaReasoningSignatureBytes(payloadLen, mutate))
	}
	nonCanonical := func() string {
		const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
		last := strings.IndexByte(alphabet, valid[len(valid)-1])
		for i := 0; i < len(alphabet); i++ {
			if i != last && i&0x30 == last&0x30 {
				return valid[:len(valid)-1] + string(alphabet[i])
			}
		}
		return valid
	}()
	cases := map[string]string{
		"empty":               "",
		"mock prefix only":    "Q-PaDg-mock-muse-spark-encrypted-reasoning-payload",
		"wrong literal start": "R" + valid[1:],
		"header byte flipped": encode(1024, func(b []byte) { b[2] ^= 0xff }),
		"version byte zero":   encode(1024, func(b []byte) { b[4] = 0x00 }),
		"payload below min":   encode(512, nil),
		"payload not power2":  encode(1025, nil),
		"payload off by one":  encode(2047, nil),
		"padded encoding":     valid + "=",
		"standard base64 +":   valid[:100] + "+" + valid[101:],
		"embedded whitespace": valid[:100] + " " + valid[100:],
		"length mod4 is 1":    valid[:len(valid)-1],
		"non-canonical bits":  nonCanonical,
	}
	for name, sig := range cases {
		if _, err := InspectMetaReasoningSignature(sig); err == nil {
			t.Errorf("%s: InspectMetaReasoningSignature unexpectedly succeeded", name)
		}
		if got := DetectSignatureProvider(sig); got == SignatureProviderMeta {
			t.Errorf("%s: DetectSignatureProvider = %q, must not claim Meta", name, got)
		}
	}
}

func TestInspectMetaReasoningSignature_RouterSuffixed(t *testing.T) {
	native := testMetaReasoningSignature(2048)
	suffix := testMetaRouterSuffix()
	routed := native + "." + suffix

	info, err := InspectMetaReasoningSignature(routed)
	if err != nil {
		t.Fatalf("InspectMetaReasoningSignature(routed) error = %v", err)
	}
	if info.RouterSuffix != suffix {
		t.Fatalf("RouterSuffix = %q, want %q", info.RouterSuffix, suffix)
	}
	nativeInfo, err := InspectMetaReasoningSignature(native)
	if err != nil {
		t.Fatalf("InspectMetaReasoningSignature(native) error = %v", err)
	}
	if nativeInfo.RouterSuffix != "" {
		t.Fatalf("native RouterSuffix = %q, want empty", nativeInfo.RouterSuffix)
	}
	if info.DecodedLen != nativeInfo.DecodedLen || info.PayloadLen != nativeInfo.PayloadLen || info.BucketExponent != nativeInfo.BucketExponent {
		t.Fatalf("routed info = %+v, want same envelope fields as native %+v", info, nativeInfo)
	}
	if got := DetectSignatureProvider(routed); got != SignatureProviderMeta {
		t.Fatalf("DetectSignatureProvider(routed) = %q, want %q", got, SignatureProviderMeta)
	}
	if got, ok := CompatibleSignatureForProvider(SignatureProviderMeta, routed); !ok || got != routed {
		t.Fatalf("CompatibleSignatureForProvider(Meta, routed) = (%q..., %v), want verbatim routed value", got[:16], ok)
	}

	// The routed form is still a foreign envelope to non-Meta targets.
	decision := DecideSignatureCompatibility(SignatureProviderGPT, routed, SignatureBlockKindUnknown)
	if decision.Compatible || decision.DetectedProvider != SignatureProviderMeta || decision.Action != SignatureActionDropBlock {
		t.Fatalf("GPT target decision for routed = %+v, want detected Meta + drop_block", decision)
	}
}

func TestInspectMetaReasoningSignature_RejectsMalformedRouterSuffix(t *testing.T) {
	native := testMetaReasoningSignature(1024)
	suffix := testMetaRouterSuffix()
	encode := func(payloadLen int, mutate func([]byte)) string {
		return base64.RawURLEncoding.EncodeToString(testMetaReasoningSignatureBytes(payloadLen, mutate))
	}
	cases := map[string]string{
		"trailing dot":           native + ".",
		"oversized suffix":       native + "." + strings.Repeat("a", maxMetaRouterSuffixLen+1),
		"suffix with dash":       native + "." + suffix[:60] + "-" + suffix[61:],
		"suffix with underscore": native + "." + suffix[:60] + "_" + suffix[61:],
		"suffix with padding":    native + "." + suffix[:60] + "=" + suffix[61:],
		"suffix with plus":       native + "." + suffix[:60] + "+" + suffix[61:],
		"suffix with slash":      native + "." + suffix[:60] + "/" + suffix[61:],
		"suffix with space":      native + "." + suffix[:60] + " " + suffix[61:],
		"second dot":             native + "." + suffix[:60] + "." + suffix[61:],
		"bad native header":      encode(1024, func(b []byte) { b[2] ^= 0xff }) + "." + suffix,
		"non-bucket native":      encode(1025, nil) + "." + suffix,
		"suffix only":            "." + suffix,
	}
	for name, sig := range cases {
		if _, err := InspectMetaReasoningSignature(sig); err == nil {
			t.Errorf("%s: InspectMetaReasoningSignature unexpectedly succeeded", name)
		}
		if got := DetectSignatureProvider(sig); got == SignatureProviderMeta {
			t.Errorf("%s: DetectSignatureProvider = %q, must not claim Meta", name, got)
		}
	}
}

func TestMetaSignatureCompatibility(t *testing.T) {
	sig := testMetaReasoningSignature(2048)

	decision := DecideSignatureCompatibility(SignatureProviderMeta, sig, SignatureBlockKindUnknown)
	if !decision.Compatible || decision.Action != SignatureActionPreserve || decision.NormalizedSignature != sig {
		t.Fatalf("Meta target decision = %+v, want preserve exact signature", decision)
	}
	if got, ok := CompatibleSignatureForProvider(SignatureProviderMeta, sig); !ok || got != sig {
		t.Fatalf("CompatibleSignatureForProvider(Meta) = (%q..., %v), want exact signature", got[:16], ok)
	}

	gptDecision := DecideSignatureCompatibility(SignatureProviderGPT, sig, SignatureBlockKindUnknown)
	if gptDecision.Compatible || gptDecision.Action != SignatureActionDropBlock {
		t.Fatalf("GPT target decision = %+v, want drop_block", gptDecision)
	}

	foreign := testGPTReasoningSignature()
	metaDecision := DecideSignatureCompatibility(SignatureProviderMeta, foreign, SignatureBlockKindUnknown)
	if metaDecision.Compatible || metaDecision.DetectedProvider != SignatureProviderGPT || metaDecision.Action != SignatureActionDropBlock {
		t.Fatalf("Meta target with GPT signature decision = %+v, want detected GPT + drop_block", metaDecision)
	}
}

func TestSignatureProviderFromModelName_Meta(t *testing.T) {
	for _, model := range []string{"muse-wonder-full-1.0-contributor", "muse-spark-1.3", "Muse-Wonder-1.0", "meta/muse-spark-1.2", "meta/muse", "muse", "meta-muse-spark", "muse_spark_1_3"} {
		if got := SignatureProviderFromModelName(model); got != SignatureProviderMeta {
			t.Errorf("SignatureProviderFromModelName(%q) = %q, want %q", model, got, SignatureProviderMeta)
		}
	}
	// The match is anchored to the "muse-" model-id boundary so unrelated
	// names containing the substring "muse" stay unmapped.
	for _, model := range []string{"museum-1", "amuse-model", "meta-llama-4"} {
		if got := SignatureProviderFromModelName(model); got != SignatureProviderUnknown {
			t.Errorf("SignatureProviderFromModelName(%q) = %q, want %q", model, got, SignatureProviderUnknown)
		}
	}
}

func TestMetaSignatureCompatibility_OtherTargets(t *testing.T) {
	sig := testMetaReasoningSignature(1024)
	for _, tc := range []struct {
		target SignatureProvider
		action SignatureCompatibilityAction
	}{
		{SignatureProviderClaude, SignatureActionDropBlock},
		{SignatureProviderGemini, SignatureActionReplaceWithGeminiBypass},
		{SignatureProviderKimi, SignatureActionDropSignature},
		{SignatureProviderGrok, SignatureActionDropBlock},
	} {
		decision := DecideSignatureCompatibility(tc.target, sig, SignatureBlockKindUnknown)
		if decision.Compatible || decision.DetectedProvider != SignatureProviderMeta || decision.Action != tc.action {
			t.Errorf("target %q: decision = %+v, want detected Meta + action %q", tc.target, decision, tc.action)
		}
	}
}

func TestSignatureProviderFromModelName_NamespacedTailWins(t *testing.T) {
	for model, want := range map[string]SignatureProvider{
		"openai-compatible/muse-spark-1.3":       SignatureProviderMeta,
		"muse-proxy/gpt-5.4":                     SignatureProviderGPT,
		"openrouter/anthropic/claude-sonnet-4-6": SignatureProviderClaude,
	} {
		if got := SignatureProviderFromModelName(model); got != want {
			t.Errorf("SignatureProviderFromModelName(%q) = %q, want %q", model, got, want)
		}
	}
}

func TestMetaCachePrefixUnsupported(t *testing.T) {
	// No producer emits provider-prefixed Meta signatures, so unlike GPT or
	// Claude there is deliberately no cache-prefix alias for Meta yet.
	sig := testMetaReasoningSignature(1024)
	for _, prefixed := range []string{"meta#" + sig, "muse#" + sig} {
		if got := DetectSignatureProvider(prefixed); got != SignatureProviderUnknown {
			t.Errorf("DetectSignatureProvider(%q...) = %q, want %q", prefixed[:12], got, SignatureProviderUnknown)
		}
		if _, ok := CompatibleSignatureForProvider(SignatureProviderMeta, prefixed); ok {
			t.Errorf("CompatibleSignatureForProvider(Meta, %q...) unexpectedly compatible", prefixed[:12])
		}
	}
}
