package session

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestExtractCanonicalTurnsProtocols(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		format sdktranslator.Format
		body   string
		roles  []string
	}{
		{
			name:   "openai chat",
			format: sdktranslator.FormatOpenAI,
			body:   `{"messages":[{"role":"system","content":"Be helpful"},{"role":"user","content":"hello"},{"role":"assistant","content":"hi"}]}`,
			roles:  []string{"system", "user", "assistant"},
		},
		{
			name:   "claude messages",
			format: sdktranslator.FormatClaude,
			body:   `{"system":[{"type":"text","text":"Be helpful"}],"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]},{"role":"assistant","content":"hi"}]}`,
			roles:  []string{"system", "user", "assistant"},
		},
		{
			name:   "openai responses",
			format: sdktranslator.FormatOpenAIResponse,
			body:   `{"instructions":"Be helpful","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}]}`,
			roles:  []string{"system", "user", "assistant"},
		},
		{
			name:   "gemini",
			format: sdktranslator.FormatGemini,
			body:   `{"systemInstruction":{"parts":[{"text":"Be helpful"}]},"contents":[{"role":"user","parts":[{"text":"hello"}]},{"role":"model","parts":[{"text":"hi"}]}]}`,
			roles:  []string{"system", "user", "assistant"},
		},
		{
			name:   "interactions",
			format: sdktranslator.FormatInteractions,
			body:   `{"system_instruction":"Be helpful","input":[{"type":"user_input","content":[{"type":"text","text":"hello"}]},{"type":"model_output","content":[{"type":"text","text":"hi"}]}]}`,
			roles:  []string{"system", "user", "assistant"},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			turns := ExtractCanonicalTurns(test.format, []byte(test.body))
			if len(turns) != len(test.roles) {
				t.Fatalf("ExtractCanonicalTurns() returned %d turns, want %d: %#v", len(turns), len(test.roles), turns)
			}
			for index, wantRole := range test.roles {
				if turns[index].Role != wantRole {
					t.Fatalf("turn %d role = %q, want %q", index, turns[index].Role, wantRole)
				}
			}
		})
	}
}

func TestExtractCanonicalTurnsBoundsTurnAllocation(t *testing.T) {
	t.Parallel()

	var payload strings.Builder
	payload.WriteString(`{"messages":[`)
	for index := 0; index < maxCanonicalTurns+32; index++ {
		if index > 0 {
			payload.WriteByte(',')
		}
		_, _ = fmt.Fprintf(&payload, `{"role":"user","content":"turn-%d"}`, index)
	}
	payload.WriteString(`]}`)

	turns := ExtractCanonicalTurns(sdktranslator.FormatOpenAI, []byte(payload.String()))
	if len(turns) != maxCanonicalTurns {
		t.Fatalf("ExtractCanonicalTurns() returned %d turns, want %d", len(turns), maxCanonicalTurns)
	}
	if cap(turns) > 2*maxCanonicalTurns {
		t.Fatalf("ExtractCanonicalTurns() capacity = %d, want bounded by %d", cap(turns), 2*maxCanonicalTurns)
	}
}

func TestExtractCanonicalTurnsBoundsPartsPerTurn(t *testing.T) {
	t.Parallel()

	var payload strings.Builder
	payload.WriteString(`{"messages":[{"role":"user","content":[`)
	for index := 0; index < maxCanonicalPartsPerTurn+32; index++ {
		if index > 0 {
			payload.WriteByte(',')
		}
		_, _ = fmt.Fprintf(&payload, `"part-%d"`, index)
	}
	payload.WriteString(`]}]}`)

	turns := ExtractCanonicalTurns(sdktranslator.FormatOpenAI, []byte(payload.String()))
	if len(turns) != 1 {
		t.Fatalf("ExtractCanonicalTurns() returned %d turns, want 1", len(turns))
	}
	parts := turns[0].Parts
	if len(parts) != maxCanonicalPartsPerTurn+1 {
		t.Fatalf("turn has %d parts, want %d bounded parts plus one marker", len(parts), maxCanonicalPartsPerTurn+1)
	}
	marker := parts[len(parts)-1]
	if marker.Value != "<truncated:32 parts>" {
		t.Fatalf("truncation marker = %q, want <truncated:32 parts>", marker.Value)
	}

	// The bounded fingerprint must be deterministic across independent extractions.
	againTurns := ExtractCanonicalTurns(sdktranslator.FormatOpenAI, []byte(payload.String()))
	if len(againTurns) != 1 {
		t.Fatalf("ExtractCanonicalTurns() returned %d turns, want 1", len(againTurns))
	}
	if FastTurnFingerprint(againTurns[0]) != FastTurnFingerprint(turns[0]) {
		t.Fatal("FastTurnFingerprint() is not deterministic for truncated turns")
	}
	// A payload with one fewer part must produce a different fingerprint.
	var small strings.Builder
	small.WriteString(`{"messages":[{"role":"user","content":[`)
	for index := 0; index < maxCanonicalPartsPerTurn+31; index++ {
		if index > 0 {
			small.WriteByte(',')
		}
		_, _ = fmt.Fprintf(&small, `"part-%d"`, index)
	}
	small.WriteString(`]}]}`)
	smallTurns := ExtractCanonicalTurns(sdktranslator.FormatOpenAI, []byte(small.String()))
	if len(smallTurns) != 1 {
		t.Fatalf("ExtractCanonicalTurns() returned %d turns, want 1", len(smallTurns))
	}
	if FastTurnFingerprint(smallTurns[0]) == FastTurnFingerprint(turns[0]) {
		t.Fatal("truncation marker did not distinguish differently-sized part lists")
	}

	// External CanonicalTurn with unbounded Parts must also be safely bounded by normalizeCanonicalTurn / FastTurnFingerprint.
	rawParts := make([]CanonicalPart, 0, maxCanonicalPartsPerTurn+50)
	for index := 0; index < maxCanonicalPartsPerTurn+50; index++ {
		rawParts = append(rawParts, CanonicalPart{Kind: "text", Value: fmt.Sprintf("ext-part-%d", index)})
	}
	unboundedTurn := CanonicalTurn{Role: "user", Parts: rawParts}
	fp := FastTurnFingerprint(unboundedTurn)
	if fp == "" {
		t.Fatal("FastTurnFingerprint returned empty for unbounded turn")
	}
}

func TestExtractCanonicalTurnsGrowthAndCrossProtocolNormalization(t *testing.T) {
	t.Parallel()

	firstOpenAI := []byte(`{"messages":[{"role":"system","content":"Be helpful"},{"role":"user","content":"hello"}]}`)
	grownOpenAI := []byte(`{"messages":[{"role":"system","content":"Be helpful"},{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":"continue"}]}`)
	firstGemini := []byte(`{"systemInstruction":{"parts":[{"text":"Be helpful"}]},"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`)

	openAITurns := ExtractCanonicalTurns(sdktranslator.FormatOpenAI, firstOpenAI)
	grownTurns := ExtractCanonicalTurns(sdktranslator.FormatOpenAI, grownOpenAI)
	geminiTurns := ExtractCanonicalTurns(sdktranslator.FormatGemini, firstGemini)
	if len(openAITurns) != 2 || len(grownTurns) != 4 || len(geminiTurns) != 2 {
		t.Fatalf("unexpected turn counts: openai=%d grown=%d gemini=%d", len(openAITurns), len(grownTurns), len(geminiTurns))
	}
	for index := range openAITurns {
		if FastTurnFingerprint(openAITurns[index]) != FastTurnFingerprint(geminiTurns[index]) {
			t.Fatalf("cross-protocol fingerprint %d differs", index)
		}
		if FastTurnFingerprint(openAITurns[index]) != FastTurnFingerprint(grownTurns[index]) {
			t.Fatalf("conversation growth changed prefix fingerprint %d", index)
		}
	}
}

func TestExtractCanonicalTurnsIgnoresStructuredReasoningParts(t *testing.T) {
	t.Parallel()

	left := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"private left","signature":"sig-left"},{"type":"text","text":"visible"}]}]}`)
	right := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"private right","signature":"sig-right"},{"type":"text","text":"visible"}]}]}`)
	leftTurns := ExtractCanonicalTurns(sdktranslator.FormatClaude, left)
	rightTurns := ExtractCanonicalTurns(sdktranslator.FormatClaude, right)
	if len(leftTurns) != 1 || len(rightTurns) != 1 {
		t.Fatalf("unexpected turn counts: left=%d right=%d", len(leftTurns), len(rightTurns))
	}
	if len(leftTurns[0].Parts) != 1 || len(rightTurns[0].Parts) != 1 {
		t.Fatalf("structured reasoning was retained: left=%#v right=%#v", leftTurns, rightTurns)
	}
	if FastTurnFingerprint(leftTurns[0]) != FastTurnFingerprint(rightTurns[0]) {
		t.Fatal("structured reasoning changed the canonical fingerprint")
	}
}

func TestFastTurnFingerprintNormalizesVolatileAndReasoningContent(t *testing.T) {
	t.Parallel()

	left := CanonicalTurn{
		Role: "developer",
		Parts: []CanonicalPart{{
			Kind:  "text",
			Value: "run <think>temporary reasoning</think> at 2026-08-24T10:20:30Z for 11111111-1111-4111-8111-111111111111",
		}},
	}
	right := CanonicalTurn{
		Role: "system",
		Parts: []CanonicalPart{{
			Kind:  "text",
			Value: "run at 2027-09-25T11:21:31Z for 22222222-2222-4222-8222-222222222222",
		}},
	}
	if FastTurnFingerprint(left) != FastTurnFingerprint(right) {
		t.Fatal("volatile system values or reasoning tags changed the fingerprint")
	}
}

func TestExtractCanonicalTurnsSortsParallelToolCalls(t *testing.T) {
	t.Parallel()

	left := []byte(`{"messages":[{"role":"assistant","content":[{"type":"tool_call","id":"b","name":"second"},{"type":"tool_call","id":"a","name":"first"}]}]}`)
	right := []byte(`{"messages":[{"role":"assistant","content":[{"type":"tool_call","id":"a","name":"first"},{"type":"tool_call","id":"b","name":"second"}]}]}`)
	leftTurns := ExtractCanonicalTurns(sdktranslator.FormatOpenAI, left)
	rightTurns := ExtractCanonicalTurns(sdktranslator.FormatOpenAI, right)
	if len(leftTurns) != 1 || len(rightTurns) != 1 {
		t.Fatalf("unexpected turn counts: left=%d right=%d", len(leftTurns), len(rightTurns))
	}
	if FastTurnFingerprint(leftTurns[0]) != FastTurnFingerprint(rightTurns[0]) {
		t.Fatal("parallel tool-call reordering changed the fingerprint")
	}
}

func TestExtractCanonicalTurnsSortsGeminiFunctionParts(t *testing.T) {
	t.Parallel()

	left := []byte(`{"contents":[{"role":"model","parts":[{"functionCall":{"name":"second","args":{"value":2}}},{"functionCall":{"name":"first","args":{"value":1}}}]}]}`)
	right := []byte(`{"contents":[{"role":"model","parts":[{"functionCall":{"name":"first","args":{"value":1}}},{"functionCall":{"name":"second","args":{"value":2}}}]}]}`)
	leftTurns := ExtractCanonicalTurns(sdktranslator.FormatGemini, left)
	rightTurns := ExtractCanonicalTurns(sdktranslator.FormatGemini, right)
	if len(leftTurns) != 1 || len(rightTurns) != 1 {
		t.Fatalf("unexpected turn counts: left=%d right=%d", len(leftTurns), len(rightTurns))
	}
	for _, part := range leftTurns[0].Parts {
		if part.Kind != "tool:function_call" {
			t.Fatalf("Gemini functionCall part kind = %q, want tool:function_call", part.Kind)
		}
	}
	if FastTurnFingerprint(leftTurns[0]) != FastTurnFingerprint(rightTurns[0]) {
		t.Fatal("Gemini functionCall reordering changed the fingerprint")
	}
}

func TestFastTurnFingerprintLargePartUsesBoundedSampling(t *testing.T) {
	t.Parallel()

	large := strings.Repeat("a", 40*1024)
	turns := ExtractCanonicalTurns(sdktranslator.FormatOpenAI, []byte(fmt.Sprintf(`{"messages":[{"role":"user","content":%q}]}`, large)))
	if len(turns) != 1 || len(turns[0].Parts) != 1 {
		t.Fatalf("unexpected extracted large turn: %#v", turns)
	}
	part := turns[0].Parts[0]
	if !part.Sampled || part.OriginalSize <= largePartThreshold || len(part.Value) > sparseFingerprintBytes {
		t.Fatalf("large part was not bounded: sampled=%v size=%d sample=%d", part.Sampled, part.OriginalSize, len(part.Value))
	}

	changed := large[:20*1024] + "b" + large[20*1024+1:]
	changedTurns := ExtractCanonicalTurns(sdktranslator.FormatOpenAI, []byte(fmt.Sprintf(`{"messages":[{"role":"user","content":%q}]}`, changed)))
	if FastTurnFingerprint(turns[0]) == FastTurnFingerprint(changedTurns[0]) {
		t.Fatal("middle change was not represented in sparse fingerprint")
	}
}

func TestFastTurnFingerprintLargePartDigestDetectsUnsampledDifference(t *testing.T) {
	t.Parallel()

	// 60 KiB payload: sparse sample takes 4 KiB head, 4 KiB middle (at 28 KiB..32 KiB), 4 KiB tail (56 KiB..60 KiB).
	// Modifying a byte at 10 KiB (in the unsampled gap between head and middle) would collide under naive sampling.
	base := strings.Repeat("a", 60*1024)
	modified := base[:10*1024] + "z" + base[10*1024+1:]

	leftTurns := ExtractCanonicalTurns(sdktranslator.FormatOpenAI, []byte(fmt.Sprintf(`{"messages":[{"role":"user","content":%q}]}`, base)))
	rightTurns := ExtractCanonicalTurns(sdktranslator.FormatOpenAI, []byte(fmt.Sprintf(`{"messages":[{"role":"user","content":%q}]}`, modified)))
	if len(leftTurns) != 1 || len(rightTurns) != 1 {
		t.Fatalf("unexpected turn counts: left=%d right=%d", len(leftTurns), len(rightTurns))
	}
	if leftTurns[0].Parts[0].Value != rightTurns[0].Parts[0].Value {
		t.Fatalf("sparse sample unexpectedly differed: gap modification was expected to produce equal sparse samples")
	}
	if leftTurns[0].Parts[0].Digest == rightTurns[0].Parts[0].Digest {
		t.Fatalf("digests unexpectedly matched for different payloads")
	}
	if FastTurnFingerprint(leftTurns[0]) == FastTurnFingerprint(rightTurns[0]) {
		t.Fatalf("full-payload digest failed to distinguish unsampled differences")
	}
}

func TestMerklePrefixMatcherLongestPrefixAndBranchAffinity(t *testing.T) {
	t.Parallel()

	matcher := NewMerklePrefixMatcher(time.Minute)
	defer matcher.Clear()
	namespace := "lcp:v1:test:model:caller"
	first := turnsFromTexts("one", "two")
	branch := turnsFromTexts("one", "two", "branch")
	other := turnsFromTexts("one", "different")

	sessionID := matcher.Bind(namespace, first, "auth-a")
	if sessionID == "" {
		t.Fatal("Bind() returned empty session ID")
	}
	if match, ok := matcher.Match(namespace, branch); !ok || match.AuthID != "auth-a" || match.PrefixLength != 2 || match.SessionID != sessionID {
		t.Fatalf("branch match = %#v, %v; want auth-a, prefix 2, session %q", match, ok, sessionID)
	}
	if got := matcher.Bind(namespace, branch, "auth-a"); got != sessionID {
		t.Fatalf("branch Bind() session = %q, want %q", got, sessionID)
	}
	if match, ok := matcher.Match(namespace, other); !ok || match.PrefixLength != 1 || match.AuthID != "auth-a" {
		t.Fatalf("rollback/divergence match = %#v, %v; want auth-a at prefix 1", match, ok)
	}
}

func TestMerklePrefixMatcherSkipsInstructionOnlyPrefixes(t *testing.T) {
	t.Parallel()

	matcher := NewMerklePrefixMatcher(time.Minute)
	defer matcher.Clear()
	namespace := "lcp:v1:instructions:model:caller"
	matcher.Bind(namespace, []CanonicalTurn{{Role: "system", Parts: []CanonicalPart{{Kind: "text", Value: "same instructions"}}}}, "auth-a")
	if _, ok := matcher.Match(namespace, []CanonicalTurn{{Role: "system", Parts: []CanonicalPart{{Kind: "text", Value: "same instructions"}}}}); ok {
		t.Fatal("system-only prefix should not create an affinity match")
	}

	first := []CanonicalTurn{
		{Role: "system", Parts: []CanonicalPart{{Kind: "text", Value: "same instructions"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "first"}}},
	}
	other := []CanonicalTurn{
		{Role: "system", Parts: []CanonicalPart{{Kind: "text", Value: "same instructions"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "different"}}},
	}
	matcher.Bind(namespace, first, "auth-a")
	if match, ok := matcher.Match(namespace, other); ok {
		t.Fatalf("generic system prefix incorrectly matched: %#v", match)
	}
}

func TestMerklePrefixMatcherPrefixEntryBound(t *testing.T) {
	t.Parallel()

	matcher := NewMerklePrefixMatcherWithConfig(MerklePrefixMatcherConfig{
		TTL:         time.Minute,
		MaxTurns:    4,
		MaxGroups:   10,
		MaxPrefixes: 5,
	})
	defer matcher.Clear()
	namespace := "lcp:v1:prefix-bound:model:caller"
	short := turnsFromTexts("short-1", "short-2")
	long := turnsFromTexts("long-1", "long-2", "long-3", "long-4")
	matcher.Bind(namespace, short, "auth-a")
	matcher.Bind(namespace, long, "auth-b")
	if _, ok := matcher.Match(namespace, short); ok {
		t.Fatal("oldest group survived the prefix-entry bound")
	}
	if match, ok := matcher.Match(namespace, long); !ok || match.AuthID != "auth-b" {
		t.Fatalf("newer group was evicted unexpectedly: %#v, %v", match, ok)
	}
}

func TestMerklePrefixMatcherTTLAndAuthInvalidation(t *testing.T) {
	t.Parallel()

	current := time.Now()
	matcher := NewMerklePrefixMatcherWithConfig(MerklePrefixMatcherConfig{
		TTL: 5 * time.Minute,
		NowFunc: func() time.Time {
			return current
		},
	})
	namespace := "lcp:v1:ttl:model:caller"
	turns := turnsFromTexts("one")
	matcher.Bind(namespace, turns, "auth-a")
	if match, ok := matcher.Match(namespace, turns); !ok || match.AuthID != "auth-a" {
		t.Fatalf("initial match = %#v, %v", match, ok)
	}

	// Advance mock clock past TTL without wall-clock sleep
	current = current.Add(10 * time.Minute)
	if _, ok := matcher.Match(namespace, turns); ok {
		t.Fatal("expired matcher entry remained available")
	}

	matcher.Bind(namespace, turns, "auth-a")
	matcher.InvalidateAuth("auth-a")
	if _, ok := matcher.Match(namespace, turns); ok {
		t.Fatal("InvalidateAuth() left an auth binding behind")
	}
}

func TestMerklePrefixMatcherConcurrentAccess(t *testing.T) {
	matcher := NewMerklePrefixMatcher(time.Minute)
	defer matcher.Clear()
	namespace := "lcp:v1:concurrent:model:caller"
	turns := turnsFromTexts("one", "two", "three")
	var wg sync.WaitGroup
	for index := 0; index < 16; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			for iteration := 0; iteration < 100; iteration++ {
				if index%2 == 0 {
					matcher.Bind(namespace, turns, "auth-a")
				} else {
					matcher.Match(namespace, turns)
				}
			}
		}(index)
	}
	wg.Wait()
	if match, ok := matcher.Match(namespace, turns); !ok || match.AuthID == "" {
		t.Fatalf("final concurrent match = %#v, %v", match, ok)
	}
}

func TestMerklePrefixMatcherRollingPrefixSessionIDDifferentSystemPrompts(t *testing.T) {
	t.Parallel()

	matcher := NewMerklePrefixMatcher(time.Minute)
	defer matcher.Clear()
	namespace := "lcp:v1:test:model:caller"

	// Two conversations with different system instructions but identical first user prompt
	conv1 := []CanonicalTurn{
		{Role: "system", Parts: []CanonicalPart{{Kind: "text", Value: "system instruction AAA"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "common question"}}},
	}
	conv2 := []CanonicalTurn{
		{Role: "system", Parts: []CanonicalPart{{Kind: "text", Value: "system instruction BBB"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "common question"}}},
	}

	sessionID1 := matcher.Bind(namespace, conv1, "auth-1")
	sessionID2 := matcher.Bind(namespace, conv2, "auth-2")

	if sessionID1 == "" || sessionID2 == "" {
		t.Fatalf("expected non-empty session IDs, got sessionID1=%q, sessionID2=%q", sessionID1, sessionID2)
	}

	// Rolling prefix ensures that because turn 0 differs, the derived session ID for turn 2 differs
	if sessionID1 == sessionID2 {
		t.Fatalf("expected distinct session IDs for conversations with different system prompts, got same: %q", sessionID1)
	}
}

func TestMerklePrefixMatcherConfigBoundsSanitization(t *testing.T) {
	t.Parallel()

	matcher := NewMerklePrefixMatcherWithConfig(MerklePrefixMatcherConfig{
		MaxTurns:    10,
		MaxPrefixes: 2, // Less than MaxTurns
	})
	defer matcher.Clear()

	if matcher.maxPrefixes < matcher.maxTurns {
		t.Fatalf("matcher.maxPrefixes = %d, want >= maxTurns (%d)", matcher.maxPrefixes, matcher.maxTurns)
	}
}

func TestNormalizeCanonicalTurnToolPartsDigestTieBreak(t *testing.T) {
	t.Parallel()

	turn1 := CanonicalTurn{
		Role: "assistant",
		Parts: []CanonicalPart{
			{Kind: "tool:call", Value: "same_value", Digest: "digest_b"},
			{Kind: "tool:call", Value: "same_value", Digest: "digest_a"},
		},
	}
	turn2 := CanonicalTurn{
		Role: "assistant",
		Parts: []CanonicalPart{
			{Kind: "tool:call", Value: "same_value", Digest: "digest_a"},
			{Kind: "tool:call", Value: "same_value", Digest: "digest_b"},
		},
	}

	norm1 := normalizeCanonicalTurn(turn1)
	norm2 := normalizeCanonicalTurn(turn2)

	if norm1.Parts[0].Digest != "digest_a" || norm2.Parts[0].Digest != "digest_a" {
		t.Fatalf("tool parts were not sorted deterministically by Digest: %#v vs %#v", norm1, norm2)
	}
	if FastTurnFingerprint(norm1) != FastTurnFingerprint(norm2) {
		t.Fatal("fingerprints differed for tool parts in different input order")
	}
}

func BenchmarkFastTurnFingerprint(b *testing.B) {
	turn := CanonicalTurn{
		Role: "user",
		Parts: []CanonicalPart{{
			Kind:  "text",
			Value: strings.Repeat("benchmark ", 128),
		}},
	}
	b.ReportAllocs()
	for b.Loop() {
		_ = FastTurnFingerprint(turn)
	}
}

func TestFastTurnFingerprintLargeSystemPromptTimestampMasking(t *testing.T) {
	t.Parallel()

	base := strings.Repeat("System prompt instructions for large context payload. ", 400) // > 16 KiB
	prompt1 := base + "\nTimestamp: 2026-08-28T12:00:00Z\nUUID: 12345678-1234-4234-8234-123456789abc\n"
	prompt2 := base + "\nTimestamp: 2026-08-28T12:05:00Z\nUUID: 87654321-4321-4321-8321-cba987654321\n"

	payload1 := []byte(fmt.Sprintf(`{"messages":[{"role":"system","content":%q},{"role":"user","content":"hello"}]}`, prompt1))
	payload2 := []byte(fmt.Sprintf(`{"messages":[{"role":"system","content":%q},{"role":"user","content":"hello"}]}`, prompt2))

	turns1 := ExtractCanonicalTurns(sdktranslator.FormatOpenAI, payload1)
	turns2 := ExtractCanonicalTurns(sdktranslator.FormatOpenAI, payload2)

	if len(turns1) != 2 || len(turns2) != 2 {
		t.Fatalf("unexpected turn counts: %d vs %d", len(turns1), len(turns2))
	}
	if !turns1[0].Parts[0].Sampled {
		t.Fatal("expected system part to be sampled")
	}

	fp1 := FastTurnFingerprint(turns1[0])
	fp2 := FastTurnFingerprint(turns2[0])
	if fp1 != fp2 {
		t.Fatalf("large system prompt fingerprints differed across dynamic timestamps:\nfp1=%s\nfp2=%s", fp1, fp2)
	}
}

func TestMerklePrefixMatcherTouchFingerprintsDelayedSuccessProtection(t *testing.T) {
	t.Parallel()

	matcher := NewMerklePrefixMatcher(time.Hour)
	namespace := "lcp:v1:openai:model:caller-test"
	fingerprints := []string{"fp-1", "fp-2"}
	minPrefixLength := 1

	// Initial binding to auth-A
	matcher.BindFingerprints(namespace, fingerprints, minPrefixLength, "auth-A")
	match, ok := matcher.MatchFingerprints(namespace, fingerprints, minPrefixLength)
	if !ok || match.AuthID != "auth-A" {
		t.Fatalf("initial match = %v, want auth-A", match)
	}

	// Failover rebinds sequence to auth-B
	matcher.BindFingerprints(namespace, fingerprints, minPrefixLength, "auth-B")
	match, ok = matcher.MatchFingerprints(namespace, fingerprints, minPrefixLength)
	if !ok || match.AuthID != "auth-B" {
		t.Fatalf("rebound match = %v, want auth-B", match)
	}

	// Delayed success from old request on auth-A calls TouchFingerprints
	refreshed := matcher.TouchFingerprints(namespace, fingerprints, minPrefixLength, "auth-A")
	if refreshed {
		t.Fatal("TouchFingerprints with outdated auth-A succeeded unexpectedly")
	}

	// Active binding must remain auth-B
	match, ok = matcher.MatchFingerprints(namespace, fingerprints, minPrefixLength)
	if !ok || match.AuthID != "auth-B" {
		t.Fatalf("match after delayed success = %v, want auth-B", match)
	}

	// Success from current auth-B refreshes properly
	if !matcher.TouchFingerprints(namespace, fingerprints, minPrefixLength, "auth-B") {
		t.Fatal("TouchFingerprints with current auth-B failed")
	}
}

func TestMerklePrefixMatcherTouchExpiredEntry(t *testing.T) {
	t.Parallel()

	current := time.Now()
	matcher := NewMerklePrefixMatcherWithConfig(MerklePrefixMatcherConfig{
		TTL: 10 * time.Minute,
		NowFunc: func() time.Time {
			return current
		},
	})
	namespace := "lcp:v1:test:model:caller"
	turns := turnsFromTexts("alpha", "beta")
	fingerprints, minPrefixLength := matcher.Prepare(turns)
	if len(fingerprints) == 0 {
		t.Fatal("Prepare returned empty fingerprints")
	}

	matcher.BindFingerprints(namespace, fingerprints, minPrefixLength, "auth-A")
	// Advance mock clock past TTL without wall-clock sleep
	current = current.Add(25 * time.Minute)

	// After TTL expires, Match should not find the expired binding
	if _, ok := matcher.MatchFingerprints(namespace, fingerprints, minPrefixLength); ok {
		t.Fatal("expected match to fail after TTL expiry")
	}

	// Touch after expiration rebinds freshly with new TTL rather than reviving expired state
	if !matcher.TouchFingerprints(namespace, fingerprints, minPrefixLength, "auth-A") {
		t.Fatal("TouchFingerprints should succeed and re-bind")
	}

	match, ok := matcher.MatchFingerprints(namespace, fingerprints, minPrefixLength)
	if !ok || match.AuthID != "auth-A" {
		t.Fatalf("expected active match after fresh touch, got match=%v, ok=%v", match, ok)
	}
}

func TestExtractCanonicalTurnsAntigravityNestedRequest(t *testing.T) {
	t.Parallel()

	nested := []byte(`{
		"project_id": "proj-123",
		"request": {
			"systemInstruction": {"parts":[{"text":"system prompt"}]},
			"contents": [
				{"role":"user","parts":[{"text":"hello antigravity"}]}
			]
		}
	}`)
	turns := ExtractCanonicalTurns(sdktranslator.FormatAntigravity, nested)
	if len(turns) != 2 {
		t.Fatalf("len(turns) = %d, want 2", len(turns))
	}
	if turns[0].Role != "system" || turns[1].Role != "user" {
		t.Fatalf("unexpected turn roles: %+v", turns)
	}
}

func TestMerklePrefixMatcherFingerprintsBounding(t *testing.T) {
	t.Parallel()

	matcher := NewMerklePrefixMatcherWithConfig(MerklePrefixMatcherConfig{
		MaxTurns: 10,
		TTL:      time.Hour,
	})

	fps := make([]string, 50)
	for i := range fps {
		fps[i] = fmt.Sprintf("fp-%d", i)
	}

	// Binding 50 fingerprints with MaxTurns=10 should truncate to 10
	sid := matcher.BindFingerprints("ns", fps, 2, "auth-1")
	if sid == "" {
		t.Fatal("BindFingerprints failed")
	}

	// Match should succeed with 50 fingerprints (truncated to 10)
	match, ok := matcher.MatchFingerprints("ns", fps, 2)
	if !ok || match.PrefixLength != 10 {
		t.Fatalf("expected 10 matched turns, got %d (ok=%v)", match.PrefixLength, ok)
	}

	// Touch and Remove should also handle 50 fingerprints cleanly
	if !matcher.TouchFingerprints("ns", fps, 2, "auth-1") {
		t.Fatal("TouchFingerprints failed")
	}
	if !matcher.RemoveFingerprints("ns", fps, "auth-1") {
		t.Fatal("RemoveFingerprints failed")
	}
}

func BenchmarkExtractCanonicalTurns(b *testing.B) {
	payload := []byte(`{
		"messages": [
			{"role": "system", "content": "You are a helpful coding assistant with UUID 123e4567-e89b-12d3-a456-426614174000 at 2026-08-29T12:00:00Z."},
			{"role": "user", "content": "Write a fast Go function."},
			{"role": "assistant", "content": "Here is the code in Go."},
			{"role": "user", "content": "Now add benchmark tests."}
		]
	}`)
	b.ReportAllocs()
	for b.Loop() {
		_ = ExtractCanonicalTurns(sdktranslator.FormatOpenAI, payload)
	}
}

func BenchmarkMerklePrefixMatcherMatch(b *testing.B) {
	matcher := NewMerklePrefixMatcher(time.Hour)
	turns := turnsFromTexts("one", "two", "three", "four", "five", "six", "seven", "eight")
	matcher.Bind("lcp:v1:benchmark:model:caller", turns, "auth-a")
	b.ReportAllocs()
	for b.Loop() {
		_, _ = matcher.Match("lcp:v1:benchmark:model:caller", turns)
	}
}

func TestMerklePrefixMatcherForkLineageTree(t *testing.T) {
	t.Parallel()

	matcher := NewMerklePrefixMatcher(time.Hour)
	defer matcher.Clear()
	namespace := "lcp:v1:test-fork:model:caller"

	// 4 test sequences:
	// Seq 1: 1 2 3 4 5 6 7 A
	// Seq 2: 1 2 3 4 5 6 7 8
	// Seq 3: 1 2 3 C 12
	// Seq 4: 1 2 3 C D
	seq1 := turnsFromTexts("1", "2", "3", "4", "5", "6", "7", "A")
	seq2 := turnsFromTexts("1", "2", "3", "4", "5", "6", "7", "8")
	seq3 := turnsFromTexts("1", "2", "3", "C", "12")
	seq4 := turnsFromTexts("1", "2", "3", "C", "D")

	// 1. Seq 1: Initial root establishment
	res1 := matcher.BindWithResult(namespace, seq1, "auth-root")
	if res1.SessionID == "" {
		t.Fatal("seq1 bind returned empty session ID")
	}
	if res1.IsFork {
		t.Fatal("seq1 unexpectedly marked as fork")
	}
	if res1.ParentSessionID != "" {
		t.Fatalf("seq1 parentSessionID = %q, want empty", res1.ParentSessionID)
	}

	// 2. Seq 2: Forks from Seq 1 at depth 7 ("8" vs "A")
	match2, ok2 := matcher.Match(namespace, seq2)
	if !ok2 {
		t.Fatal("seq2 match failed")
	}
	if !match2.IsFork {
		t.Fatal("seq2 should be recognized as a true fork")
	}
	if match2.PrefixLength != 7 {
		t.Fatalf("seq2 prefix length = %d, want 7", match2.PrefixLength)
	}
	if match2.AuthID != "auth-root" {
		t.Fatalf("seq2 authID = %q, want auth-root", match2.AuthID)
	}
	if match2.SessionID == res1.SessionID {
		t.Fatalf("seq2 sessionID = %q should differ from seq1 root %q", match2.SessionID, res1.SessionID)
	}
	if match2.ParentSessionID == "" {
		t.Fatal("seq2 parentSessionID should not be empty")
	}
	res2 := matcher.BindWithResult(namespace, seq2, "auth-root")
	if res2.SessionID != match2.SessionID || res2.ParentSessionID != match2.ParentSessionID {
		t.Fatalf("seq2 bind result mismatch: bind=%+v, match=%+v", res2, match2)
	}

	// 3. Seq 3: Forks from root tree at depth 3 ("C" vs "4")
	match3, ok3 := matcher.Match(namespace, seq3)
	if !ok3 {
		t.Fatal("seq3 match failed")
	}
	if !match3.IsFork {
		t.Fatal("seq3 should be recognized as a true fork")
	}
	if match3.PrefixLength != 3 {
		t.Fatalf("seq3 prefix length = %d, want 3", match3.PrefixLength)
	}
	if match3.AuthID != "auth-root" {
		t.Fatalf("seq3 authID = %q, want auth-root", match3.AuthID)
	}
	if match3.SessionID == res1.SessionID || match3.SessionID == res2.SessionID {
		t.Fatalf("seq3 sessionID = %q collided with seq1=%q or seq2=%q", match3.SessionID, res1.SessionID, res2.SessionID)
	}
	if match3.ParentSessionID == "" || match3.ParentSessionID == match2.ParentSessionID {
		t.Fatalf("seq3 parentSessionID = %q, should point to prefix 3 (distinct from seq2 prefix 7 parent %q)", match3.ParentSessionID, match2.ParentSessionID)
	}
	res3 := matcher.BindWithResult(namespace, seq3, "auth-root")
	if res3.SessionID != match3.SessionID || res3.ParentSessionID != match3.ParentSessionID {
		t.Fatalf("seq3 bind result mismatch: bind=%+v, match=%+v", res3, match3)
	}

	// 4. Seq 4: Nested fork from Seq 3 at depth 4 ("D" vs "12")
	match4, ok4 := matcher.Match(namespace, seq4)
	if !ok4 {
		t.Fatal("seq4 match failed")
	}
	if !match4.IsFork {
		t.Fatal("seq4 should be recognized as a true fork")
	}
	if match4.PrefixLength != 4 {
		t.Fatalf("seq4 prefix length = %d, want 4", match4.PrefixLength)
	}
	if match4.AuthID != "auth-root" {
		t.Fatalf("seq4 authID = %q, want auth-root", match4.AuthID)
	}
	if match4.SessionID == res1.SessionID || match4.SessionID == res2.SessionID || match4.SessionID == res3.SessionID {
		t.Fatalf("seq4 sessionID = %q collided with earlier sessions", match4.SessionID)
	}
	// Crucial: seq4's parent should be seq3's branch session ID (prefix 4: 1 2 3 C)
	if match4.ParentSessionID != res3.SessionID {
		t.Fatalf("seq4 parentSessionID = %q, want seq3 session ID %q", match4.ParentSessionID, res3.SessionID)
	}
	res4 := matcher.BindWithResult(namespace, seq4, "auth-root")
	if res4.SessionID != match4.SessionID || res4.ParentSessionID != match4.ParentSessionID {
		t.Fatalf("seq4 bind result mismatch: bind=%+v, match=%+v", res4, match4)
	}

	// 5. Linear continuation of Seq 4 (Seq 4.2: 1 2 3 C D E)
	seq4Cont := turnsFromTexts("1", "2", "3", "C", "D", "E")
	match4Cont, ok4Cont := matcher.Match(namespace, seq4Cont)
	if !ok4Cont {
		t.Fatal("seq4Cont match failed")
	}
	if match4Cont.IsFork {
		t.Fatal("seq4Cont is a linear continuation, should NOT be marked as fork")
	}
	if match4Cont.PrefixLength != 5 {
		t.Fatalf("seq4Cont prefix length = %d, want 5", match4Cont.PrefixLength)
	}
	if match4Cont.SessionID != res4.SessionID {
		t.Fatalf("seq4Cont sessionID = %q, want identical to seq4 %q across linear growth", match4Cont.SessionID, res4.SessionID)
	}
	if match4Cont.ParentSessionID != res4.ParentSessionID {
		t.Fatalf("seq4Cont parentSessionID = %q, want %q", match4Cont.ParentSessionID, res4.ParentSessionID)
	}
}

func BenchmarkMerklePrefixMatcherFork(b *testing.B) {
	matcher := NewMerklePrefixMatcher(time.Hour)
	trunk := turnsFromTexts("1", "2", "3", "4", "5", "6", "7", "A")
	fork := turnsFromTexts("1", "2", "3", "4", "5", "6", "7", "8")
	matcher.Bind("lcp:v1:benchmark:fork", trunk, "auth-a")

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		_, _ = matcher.Match("lcp:v1:benchmark:fork", fork)
	}
}

func TestMerklePrefixMatcherShorterGroupDoesNotMaskLongerTrajectory(t *testing.T) {
	t.Parallel()

	matcher := NewMerklePrefixMatcher(time.Hour)
	defer matcher.Clear()
	namespace := "lcp:v1:mask-test:model:caller"

	// 1. Bind long trajectory [1, 2, 3]
	longTrunk := turnsFromTexts("1", "2", "3")
	matcher.Bind(namespace, longTrunk, "auth-root")

	// 2. Bind shorter exact-prefix group [1, 2] afterwards (more recently accessed)
	shortPrefix := turnsFromTexts("1", "2")
	matcher.Bind(namespace, shortPrefix, "auth-root")

	// 3. Query a fork [1, 2, 4] diverging at turn 3 from the long trunk
	fork := turnsFromTexts("1", "2", "4")
	match, ok := matcher.Match(namespace, fork)
	if !ok {
		t.Fatal("fork match failed")
	}
	if !match.IsFork {
		t.Fatal("fork must NOT be masked by shorter exact-prefix group [1, 2]")
	}
	if match.PrefixLength != 2 {
		t.Fatalf("prefix length = %d, want 2", match.PrefixLength)
	}
}

func TestMerklePrefixMatcherRemoveFingerprintsBeforeGenerationGuard(t *testing.T) {
	t.Parallel()

	matcher := NewMerklePrefixMatcher(time.Hour)
	defer matcher.Clear()
	namespace := "lcp:v1:gen-test:model:caller"

	turns := turnsFromTexts("1", "2")
	fps, minPrefix := matcher.Prepare(turns)

	// 1. Initial bind at generation G1
	res1 := matcher.BindFingerprintsWithResult(namespace, fps, minPrefix, "auth-1")
	g1 := res1.AccessNumber
	if g1 == 0 {
		t.Fatal("expected non-zero access generation")
	}

	// 2. Concurrent success touches entry and advances to generation G2 > G1
	if !matcher.TouchFingerprints(namespace, fps, minPrefix, "auth-1") {
		t.Fatal("TouchFingerprints failed")
	}

	// 3. Stale failure from request 1 attempts to remove with generation G1
	removedStale := matcher.RemoveFingerprintsBefore(namespace, fps, "auth-1", g1)
	if removedStale {
		t.Fatal("stale RemoveFingerprintsBefore should not delete entry refreshed by newer touch")
	}

	// Entry must remain active
	if _, ok := matcher.MatchFingerprints(namespace, fps, minPrefix); !ok {
		t.Fatal("entry should still be present after stale removal attempt")
	}

	// 4. Current failure with generation 0 (unconditional) removes it
	removedCurrent := matcher.RemoveFingerprints(namespace, fps, "auth-1")
	if !removedCurrent {
		t.Fatal("unconditional RemoveFingerprints should succeed")
	}
	if _, ok := matcher.MatchFingerprints(namespace, fps, minPrefix); ok {
		t.Fatal("entry should be removed after unconditional removal")
	}
}

func TestMerklePrefixMatcherClearPreservesMonotonicGeneration(t *testing.T) {
	t.Parallel()
	matcher := NewMerklePrefixMatcher(time.Hour)
	namespace := "lcp:v1:test:model:caller"
	fps := []string{"fp-1", "fp-2"}
	minPrefix := 1

	// Bind initial sequence and record generation
	res1 := matcher.BindFingerprintsWithResult(namespace, fps, minPrefix, "auth-old")
	if res1.AccessNumber == 0 {
		t.Fatal("expected non-zero generation for initial bind")
	}

	// Clear matcher
	matcher.Clear()

	// Rebind same sequence under new auth post-clear
	res2 := matcher.BindFingerprintsWithResult(namespace, fps, minPrefix, "auth-new")
	if res2.AccessNumber <= res1.AccessNumber {
		t.Fatalf("expected generation to increase monotonically across Clear(), got %d <= %d", res2.AccessNumber, res1.AccessNumber)
	}

	// Pre-clear generation should NOT be able to evict post-clear binding
	if matcher.RemoveFingerprintsBefore(namespace, fps, "auth-new", res1.AccessNumber) {
		t.Fatal("stale pre-clear generation should not evict post-clear binding")
	}

	// The binding must remain intact
	match, ok := matcher.MatchFingerprints(namespace, fps, minPrefix)
	if !ok || match.AuthID != "auth-new" {
		t.Fatalf("binding should remain intact, got ok=%v match=%+v", ok, match)
	}
}

func TestMerklePrefixMatcherCompactionSubsequenceContinuation(t *testing.T) {
	t.Parallel()

	matcher := NewMerklePrefixMatcher(time.Hour)
	defer matcher.Clear()
	namespace := "lcp:v1:test-compaction:model:caller"

	// Pre-compaction sequence: 5 turns
	initial := turnsFromTexts("turn1", "turn2", "turn3", "turn4", "turn5")
	initialRes := matcher.BindWithResult(namespace, initial, "auth-parent")
	if initialRes.SessionID == "" {
		t.Fatal("initial bind returned empty session ID")
	}

	// Post-compaction sequence: early turns (1..3) collapsed into summary, preserving turns 4..5 and appending turn 6
	compacted := turnsFromTexts("compacted-summary-of-1-to-3", "turn4", "turn5", "turn6")
	match, ok := matcher.Match(namespace, compacted)
	if !ok {
		t.Fatal("expected match across context compaction continuation, but got LCP miss")
	}
	if match.AuthID != "auth-parent" {
		t.Fatalf("authID = %q, want auth-parent", match.AuthID)
	}
	if match.ParentSessionID != initialRes.SessionID {
		t.Fatalf("parentSessionID = %q, want %q", match.ParentSessionID, initialRes.SessionID)
	}
	if !match.IsCompaction {
		t.Fatal("expected IsCompaction = true")
	}
	if match.IsFork {
		t.Fatal("expected IsFork = false for in-place compaction milestone")
	}
	if match.NodeKind != "compaction" {
		t.Fatalf("nodeKind = %q, want compaction", match.NodeKind)
	}

	// Bind post-compaction continuation
	resCompacted := matcher.BindWithResult(namespace, compacted, "auth-parent")
	if resCompacted.ParentSessionID != initialRes.SessionID {
		t.Fatalf("bind parentSessionID = %q, want %q", resCompacted.ParentSessionID, initialRes.SessionID)
	}
	if !resCompacted.IsCompaction {
		t.Fatal("expected bind IsCompaction = true")
	}
	if resCompacted.IsFork {
		t.Fatal("expected bind IsFork = false")
	}
	if resCompacted.NodeKind != "compaction" {
		t.Fatalf("bind nodeKind = %q, want compaction", resCompacted.NodeKind)
	}

	// Subsequent turn grows linearly from compaction boundary
	subsequent := turnsFromTexts("compacted-summary-of-1-to-3", "turn4", "turn5", "turn6", "turn7")
	subMatch, okSub := matcher.Match(namespace, subsequent)
	if !okSub {
		t.Fatal("subsequent turn match failed")
	}
	if subMatch.SessionID != resCompacted.SessionID {
		t.Fatalf("subMatch sessionID = %q, want %q", subMatch.SessionID, resCompacted.SessionID)
	}
	if subMatch.AuthID != "auth-parent" {
		t.Fatalf("subMatch authID = %q, want auth-parent", subMatch.AuthID)
	}
	if subMatch.IsFork {
		t.Fatal("subsequent turn should continue linearly on trunk, not fork")
	}

	// Multi-tenant isolation: other tenant in different caller namespace must not coalesce
	otherNamespace := "lcp:v1:test-compaction:model:other-caller"
	if _, okOther := matcher.Match(otherNamespace, compacted); okOther {
		t.Fatal("compacted sequence unexpectedly matched across caller namespaces")
	}
}

func TestMerklePrefixMatcherSlidingWindowTruncation(t *testing.T) {
	t.Parallel()

	matcher := NewMerklePrefixMatcher(time.Hour)
	defer matcher.Clear()
	namespace := "lcp:v1:test-sliding-window:model:caller"

	// Parent sequence: 4 turns [A, B, C, D]
	parent := turnsFromTexts("turnA", "turnB", "turnC", "turnD")
	parentRes := matcher.BindWithResult(namespace, parent, "auth-parent")

	// Sliding-window truncated continuation: [C, D, E] (turns A and B dropped without summary)
	truncated := turnsFromTexts("turnC", "turnD", "turnE")
	match, ok := matcher.Match(namespace, truncated)
	if !ok {
		t.Fatal("expected match across pure sliding-window truncation, got miss")
	}
	if match.AuthID != "auth-parent" {
		t.Fatalf("authID = %q, want auth-parent", match.AuthID)
	}
	if match.ParentSessionID != parentRes.SessionID {
		t.Fatalf("parentSessionID = %q, want %q", match.ParentSessionID, parentRes.SessionID)
	}
	if !match.IsCompaction {
		t.Fatal("expected IsCompaction = true")
	}
	if match.IsFork {
		t.Fatal("expected IsFork = false")
	}
	if match.NodeKind != "compaction" {
		t.Fatalf("nodeKind = %q, want compaction", match.NodeKind)
	}
}

func TestMerklePrefixMatcherCompactionSystemPromptIsolation(t *testing.T) {
	t.Parallel()

	matcher := NewMerklePrefixMatcher(time.Hour)
	defer matcher.Clear()
	namespace := "lcp:v1:test-sys-isolation:model:caller"

	// Session A with system instruction A
	sessA := []CanonicalTurn{
		{Role: "system", Parts: []CanonicalPart{{Kind: "text", Value: "System instruction A"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "turn1"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "turn2"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "turn3"}}},
	}
	matcher.BindWithResult(namespace, sessA, "auth-A")

	// Candidate with DIFFERENT system instruction B, sharing common tail turns
	candB := []CanonicalTurn{
		{Role: "system", Parts: []CanonicalPart{{Kind: "text", Value: "System instruction B"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "turn2"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "turn3"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "turn4"}}},
	}
	if _, ok := matcher.Match(namespace, candB); ok {
		t.Fatal("different system instructions in same namespace must not coalesce across compaction")
	}
}

func TestMerklePrefixMatcherCompactionAmbiguityRejection(t *testing.T) {
	t.Parallel()

	matcher := NewMerklePrefixMatcher(time.Hour)
	defer matcher.Clear()
	namespace := "lcp:v1:test-ambiguity:model:caller"

	// Two distinct active sessions in same namespace that happen to share identical tail turns
	sess1 := turnsFromTexts("branch1", "turn2", "turn3", "sharedTailA", "sharedTailB")
	sess2 := turnsFromTexts("branch2", "turnX", "turnY", "sharedTailA", "sharedTailB")
	matcher.BindWithResult(namespace, sess1, "auth-1")
	matcher.BindWithResult(namespace, sess2, "auth-2")

	// Candidate matching both with identical overlap length
	cand := turnsFromTexts("summary", "sharedTailA", "sharedTailB", "newTurn")
	if _, ok := matcher.Match(namespace, cand); ok {
		t.Fatal("ambiguous candidate matching multiple active sessions equally must be rejected")
	}
}

func TestMerklePrefixMatcherCompactionLongConversationActualTail(t *testing.T) {
	t.Parallel()

	matcher := NewMerklePrefixMatcher(time.Hour)
	defer matcher.Clear()
	namespace := "lcp:v1:test-long-tail:model:caller"

	// Long conversation with 1200 turns (exceeds default maxTurns=1024 prefix window)
	totalTurns := 1200
	texts := make([]string, totalTurns)
	for i := 0; i < totalTurns; i++ {
		texts[i] = fmt.Sprintf("turn-%d content for 1200-turn conversation", i)
	}
	parentTurns := turnsFromTexts(texts...)
	parentRes := matcher.BindWithResult(namespace, parentTurns, "auth-parent-long")
	if parentRes.SessionID == "" {
		t.Fatal("parent bind returned empty session ID")
	}

	// Compacted continuation: summary replaces early turns, keeping the actual tail (turns 1190..1199) and adding 1200
	candTexts := make([]string, 0, 15)
	candTexts = append(candTexts, "summary of turns 0-1189")
	for i := 1190; i < totalTurns; i++ {
		candTexts = append(candTexts, texts[i])
	}
	candTexts = append(candTexts, "new turn 1200")
	candTurns := turnsFromTexts(candTexts...)

	match, ok := matcher.Match(namespace, candTurns)
	if !ok {
		t.Fatal("expected match for compacted continuation of 1200-turn conversation")
	}
	if match.AuthID != "auth-parent-long" {
		t.Fatalf("authID = %q, want auth-parent-long", match.AuthID)
	}
	if match.ParentSessionID != parentRes.SessionID {
		t.Fatalf("parentSessionID = %q, want %q", match.ParentSessionID, parentRes.SessionID)
	}
	if !match.IsCompaction {
		t.Fatal("expected IsCompaction = true")
	}
	if match.IsFork {
		t.Fatal("expected IsFork = false")
	}
}

func TestMerklePrefixMatcherCompactionMultiSystemTurnIsolation(t *testing.T) {
	t.Parallel()

	matcher := NewMerklePrefixMatcher(time.Hour)
	defer matcher.Clear()
	namespace := "lcp:v1:test-multi-sys:model:caller"

	// Parent session with two system turns
	parent := []CanonicalTurn{
		{Role: "system", Parts: []CanonicalPart{{Kind: "text", Value: "System instruction part 1"}}},
		{Role: "system", Parts: []CanonicalPart{{Kind: "text", Value: "Developer instruction part 2"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "turn1"}}},
		{Role: "assistant", Parts: []CanonicalPart{{Kind: "text", Value: "ack1"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "turn2"}}},
	}
	parentRes := matcher.BindWithResult(namespace, parent, "auth-multi-sys")

	// Candidate A: same first system turn, different second system turn -> REJECT
	candDiffSecond := []CanonicalTurn{
		{Role: "system", Parts: []CanonicalPart{{Kind: "text", Value: "System instruction part 1"}}},
		{Role: "system", Parts: []CanonicalPart{{Kind: "text", Value: "DIFFERENT second part"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "summary"}}},
		{Role: "assistant", Parts: []CanonicalPart{{Kind: "text", Value: "ack1"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "turn2"}}},
	}
	if _, ok := matcher.Match(namespace, candDiffSecond); ok {
		t.Fatal("candidate with different second system turn must be rejected")
	}

	// Candidate B: missing system turns completely -> REJECT
	candNoSys := []CanonicalTurn{
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "summary without sys"}}},
		{Role: "assistant", Parts: []CanonicalPart{{Kind: "text", Value: "ack1"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "turn2"}}},
	}
	if _, ok := matcher.Match(namespace, candNoSys); ok {
		t.Fatal("candidate missing system turns must be rejected against parent with system turns")
	}

	// Candidate C: identical system turns -> MATCH
	candIdenticalSys := []CanonicalTurn{
		{Role: "system", Parts: []CanonicalPart{{Kind: "text", Value: "System instruction part 1"}}},
		{Role: "system", Parts: []CanonicalPart{{Kind: "text", Value: "Developer instruction part 2"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "summary with sys"}}},
		{Role: "assistant", Parts: []CanonicalPart{{Kind: "text", Value: "ack1"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "turn2"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "turn3"}}},
	}
	matchC, okC := matcher.Match(namespace, candIdenticalSys)
	if !okC {
		t.Fatal("candidate with identical multi-system turns must match")
	}
	if matchC.ParentSessionID != parentRes.SessionID {
		t.Fatalf("parentSessionID = %q, want %q", matchC.ParentSessionID, parentRes.SessionID)
	}
}

func TestMerklePrefixMatcherCompactionBucketBound(t *testing.T) {
	t.Parallel()

	matcher := NewMerklePrefixMatcher(time.Hour)
	defer matcher.Clear()
	namespace := "lcp:v1:test-bucket-bound:model:caller"

	// Register 25 distinct sessions sharing identical tail turns (exceeds maxTailsPerKey=16)
	for i := 0; i < 25; i++ {
		sess := turnsFromTexts(fmt.Sprintf("unique-head-%d", i), "turnX", "sharedTailA", "sharedTailB")
		matcher.BindWithResult(namespace, sess, fmt.Sprintf("auth-%d", i))
	}

	// Candidate matching the common tail: should be rejected due to ambiguity, without panicking or infinite loop
	cand := turnsFromTexts("summary", "sharedTailA", "sharedTailB", "newTurn")
	if _, ok := matcher.Match(namespace, cand); ok {
		t.Fatal("matching ambiguous bucket must be safely rejected")
	}
}

func TestMerklePrefixMatcherCompactionBucketBoundOverflowRemoval(t *testing.T) {
	t.Parallel()

	matcher := NewMerklePrefixMatcher(time.Hour)
	defer matcher.Clear()
	namespace := "lcp:v1:test-overflow-rem:model:caller"

	// 1. Register 18 distinct sessions sharing identical tail turns (maxTailsPerKey=16)
	sessList := make([][]CanonicalTurn, 18)
	for i := 0; i < 18; i++ {
		sessList[i] = turnsFromTexts(fmt.Sprintf("unique-head-%d", i), "turnX", "sharedTailA", "sharedTailB")
		matcher.BindWithResult(namespace, sessList[i], fmt.Sprintf("auth-%d", i))
	}

	cand := turnsFromTexts("summary", "sharedTailA", "sharedTailB", "newTurn")

	// 2. 18 sessions: overflow state. Match must be rejected.
	if _, ok := matcher.Match(namespace, cand); ok {
		t.Fatal("expected match to be rejected on overflowed bucket (18 items)")
	}

	// 3. Remove 1 session (leaving 17 items > 16): still overflowed. Match must be rejected.
	matcher.Remove(namespace, sessList[17], "auth-17")
	if _, ok := matcher.Match(namespace, cand); ok {
		t.Fatal("expected match to still be rejected on overflowed bucket (17 items)")
	}

	// 4. Re-add session 17 and remove sessions 1..15, leaving sessions 0, 16, 17 (count 3 <= 16)
	matcher.BindWithResult(namespace, sessList[17], "auth-17")
	for i := 1; i < 16; i++ {
		matcher.Remove(namespace, sessList[i], fmt.Sprintf("auth-%d", i))
	}
	// With 3 remaining sessions sharing tail, must still be rejected due to ambiguity
	if _, okAmbiguous := matcher.Match(namespace, cand); okAmbiguous {
		t.Fatal("expected match to be rejected due to ambiguity between remaining sessions 0, 16, 17")
	}

	// 5. Remove sessions 0 and 16, leaving ONLY session 17 (which was added when bucket had > 16 items)
	matcher.Remove(namespace, sessList[0], "auth-0")
	matcher.Remove(namespace, sessList[16], "auth-16")
	matchUnique, okUnique := matcher.Match(namespace, cand)
	if !okUnique {
		t.Fatal("expected unique match for session 17 after removing all other competitors")
	}
	if matchUnique.AuthID != "auth-17" {
		t.Fatalf("authID = %q, want auth-17", matchUnique.AuthID)
	}
}

func TestMerklePrefixMatcherCompactionBucketOverflowExpirationRecovery(t *testing.T) {
	t.Parallel()

	nowMu := sync.Mutex{}
	currentTime := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	matcher := NewMerklePrefixMatcherWithConfig(MerklePrefixMatcherConfig{
		TTL: time.Hour,
		NowFunc: func() time.Time {
			nowMu.Lock()
			defer nowMu.Unlock()
			return currentTime
		},
	})
	defer matcher.Clear()
	namespace := "lcp:v1:test-overflow-exp:model:caller"

	// 1. Register 18 distinct sessions sharing identical tail turns at T0
	sessList := make([][]CanonicalTurn, 18)
	for i := 0; i < 18; i++ {
		sessList[i] = turnsFromTexts(fmt.Sprintf("unique-head-%d", i), "turnX", "sharedTailA", "sharedTailB")
		matcher.BindWithResult(namespace, sessList[i], fmt.Sprintf("auth-%d", i))
	}

	cand := turnsFromTexts("summary", "sharedTailA", "sharedTailB", "newTurn")

	// 2. All 18 sessions alive at T0: overflow rejected
	if _, ok := matcher.Match(namespace, cand); ok {
		t.Fatal("expected match to be rejected on overflowed bucket (18 live items)")
	}

	// 3. Advance clock by 30 minutes, touch session 0 so it expires at T0 + 1h30m
	nowMu.Lock()
	currentTime = currentTime.Add(30 * time.Minute)
	nowMu.Unlock()
	if !matcher.Touch(namespace, sessList[0], "auth-0") {
		t.Fatal("Touch failed on session 0")
	}

	// 4. Advance clock past T0 + 1h (to T0 + 1h15m): sessions 1..17 are now expired, only session 0 is alive
	nowMu.Lock()
	currentTime = currentTime.Add(45 * time.Minute)
	nowMu.Unlock()

	// 5. Match must immediately recover unique affinity to session 0 without waiting for lazy cleanup cycle
	matchUnique, okUnique := matcher.Match(namespace, cand)
	if !okUnique {
		t.Fatal("expected unique match after 17 competing sessions expired")
	}
	if matchUnique.AuthID != "auth-0" {
		t.Fatalf("authID = %q, want auth-0", matchUnique.AuthID)
	}
}

func TestMerklePrefixMatcherCompactionCandidateOver1024Turns(t *testing.T) {
	t.Parallel()

	matcher := NewMerklePrefixMatcher(time.Hour)
	defer matcher.Clear()
	namespace := "lcp:v1:test-long-candidate:model:caller"

	// 1. Parent session with 2000 turns
	totalParentTurns := 2000
	parentTexts := make([]string, totalParentTurns)
	for i := 0; i < totalParentTurns; i++ {
		parentTexts[i] = fmt.Sprintf("parent-turn-%d-payload", i)
	}
	parentTurns := turnsFromTexts(parentTexts...)
	parentRes := matcher.BindWithResult(namespace, parentTurns, "auth-parent-2000")

	// 2. Candidate post-compaction request that is STILL 1100 turns long (> 1024 maxTurns prefix limit)
	candTexts := make([]string, 0, 1100)
	candTexts = append(candTexts, "summary of earlier turns 0..900")
	for i := 901; i < totalParentTurns; i++ {
		candTexts = append(candTexts, parentTexts[i])
	}
	candTexts = append(candTexts, "new-turn-2001") // 1 + 1099 + 1 = 1101 turns
	candTurns := turnsFromTexts(candTexts...)

	match, ok := matcher.Match(namespace, candTurns)
	if !ok {
		t.Fatalf("expected candidate with %d turns (> 1024) to match compaction continuation", len(candTurns))
	}
	if match.AuthID != "auth-parent-2000" {
		t.Fatalf("authID = %q, want auth-parent-2000", match.AuthID)
	}
	if match.ParentSessionID != parentRes.SessionID {
		t.Fatalf("parentSessionID = %q, want %q", match.ParentSessionID, parentRes.SessionID)
	}
	if !match.IsCompaction {
		t.Fatal("expected IsCompaction = true")
	}
}

func TestMerklePrefixMatcherCompactionMiddleSystemTurnIsolation(t *testing.T) {
	t.Parallel()

	matcher := NewMerklePrefixMatcher(time.Hour)
	defer matcher.Clear()
	namespace := "lcp:v1:test-middle-sys:model:caller"

	// Parent has system instruction inserted in the middle of conversation
	parent := []CanonicalTurn{
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "start user"}}},
		{Role: "assistant", Parts: []CanonicalPart{{Kind: "text", Value: "start ack"}}},
		{Role: "system", Parts: []CanonicalPart{{Kind: "text", Value: "middle developer directive"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "step 3"}}},
		{Role: "assistant", Parts: []CanonicalPart{{Kind: "text", Value: "step 3 ack"}}},
	}
	parentRes := matcher.BindWithResult(namespace, parent, "auth-middle-sys")

	// Candidate A: does NOT have the middle developer directive -> REJECT
	candWithout := []CanonicalTurn{
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "summary without middle sys"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "step 3"}}},
		{Role: "assistant", Parts: []CanonicalPart{{Kind: "text", Value: "step 3 ack"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "step 4"}}},
	}
	if _, ok := matcher.Match(namespace, candWithout); ok {
		t.Fatal("candidate missing middle system directive must be rejected")
	}

	// Candidate B: HAS the identical middle developer directive -> MATCH
	candWith := []CanonicalTurn{
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "summary with middle sys"}}},
		{Role: "system", Parts: []CanonicalPart{{Kind: "text", Value: "middle developer directive"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "step 3"}}},
		{Role: "assistant", Parts: []CanonicalPart{{Kind: "text", Value: "step 3 ack"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "step 4"}}},
	}
	matchWith, okWith := matcher.Match(namespace, candWith)
	if !okWith {
		t.Fatal("candidate with matching middle system directive must match")
	}
	if matchWith.ParentSessionID != parentRes.SessionID {
		t.Fatalf("parentSessionID = %q, want %q", matchWith.ParentSessionID, parentRes.SessionID)
	}
}

func TestMerklePrefixMatcherCompactionLongSessionTouchEnvironmentUpdate(t *testing.T) {
	t.Parallel()

	matcher := NewMerklePrefixMatcher(time.Hour)
	defer matcher.Clear()
	namespace := "lcp:v1:test-long-touch-env:model:caller"

	// 1. Initial 1100-turn conversation without system instructions
	totalTurns := 1100
	initialTexts := make([]string, totalTurns)
	for i := 0; i < totalTurns; i++ {
		initialTexts[i] = fmt.Sprintf("initial-turn-%d-text", i)
	}
	initialTurns := turnsFromTexts(initialTexts...)
	parentRes := matcher.BindWithResult(namespace, initialTurns, "auth-long-env")

	// 2. Conversation continues past 1024 turns and appends a new developer/system instruction in later turns
	updatedTurns := make([]CanonicalTurn, 0, totalTurns+3)
	updatedTurns = append(updatedTurns, initialTurns...)
	updatedTurns = append(updatedTurns, CanonicalTurn{
		Role:  "system",
		Parts: []CanonicalPart{{Kind: "text", Value: "Appended mid-conversation developer directive"}},
	})
	updatedTurns = append(updatedTurns, CanonicalTurn{
		Role:  "user",
		Parts: []CanonicalPart{{Kind: "text", Value: "turn 1101"}},
	})
	updatedTurns = append(updatedTurns, CanonicalTurn{
		Role:  "assistant",
		Parts: []CanonicalPart{{Kind: "text", Value: "turn 1101 ack"}},
	})

	// Touch updates the long trajectory with both new tail turns and the updated environment digest
	if !matcher.Touch(namespace, updatedTurns, "auth-long-env") {
		t.Fatal("Touch failed on updated long conversation")
	}

	// 3a. Compacted continuation candidate preserving the new system instruction + tail turns: MUST MATCH
	candMatching := []CanonicalTurn{
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "summary of turns 0..1095"}}},
		{Role: "system", Parts: []CanonicalPart{{Kind: "text", Value: "Appended mid-conversation developer directive"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "turn 1101"}}},
		{Role: "assistant", Parts: []CanonicalPart{{Kind: "text", Value: "turn 1101 ack"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "turn 1102"}}},
	}
	match, ok := matcher.Match(namespace, candMatching)
	if !ok {
		t.Fatal("expected candidate with updated environment digest to match")
	}
	if match.ParentSessionID != parentRes.SessionID {
		t.Fatalf("parentSessionID = %q, want %q", match.ParentSessionID, parentRes.SessionID)
	}

	// 3b. Stale candidate without the updated system instruction: MUST BE REJECTED
	candStale := []CanonicalTurn{
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "summary of turns 0..1095"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "turn 1101"}}},
		{Role: "assistant", Parts: []CanonicalPart{{Kind: "text", Value: "turn 1101 ack"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "turn 1102"}}},
	}
	if _, okStale := matcher.Match(namespace, candStale); okStale {
		t.Fatal("stale candidate missing the updated system directive must be rejected")
	}
}

func TestMerklePrefixMatcherCompactionSystemPromptRemovalViaTouch(t *testing.T) {
	t.Parallel()

	matcher := NewMerklePrefixMatcher(time.Hour)
	defer matcher.Clear()
	namespace := "lcp:v1:test-sys-removal:model:caller"

	// 1. Initial 1100-turn session without system instructions
	totalTurns := 1100
	initialTexts := make([]string, totalTurns)
	for i := 0; i < totalTurns; i++ {
		initialTexts[i] = fmt.Sprintf("sys-rem-turn-%d-text", i)
	}
	initialTurns := turnsFromTexts(initialTexts...)

	// 2. Initial state includes an appended mid-conversation developer directive at turn 1100
	withSysTurns := make([]CanonicalTurn, 0, totalTurns+1)
	withSysTurns = append(withSysTurns, initialTurns...)
	withSysTurns = append(withSysTurns, CanonicalTurn{
		Role:  "system",
		Parts: []CanonicalPart{{Kind: "text", Value: "Temporary developer instruction"}},
	})
	parentRes := matcher.BindWithResult(namespace, withSysTurns, "auth-sys-rem")

	// 3. Conversation continues past 1024 turns and DROPS the temporary developer instruction (empty envDigest)
	withoutSysTurns := make([]CanonicalTurn, 0, totalTurns+2)
	withoutSysTurns = append(withoutSysTurns, initialTurns...)
	withoutSysTurns = append(withoutSysTurns, CanonicalTurn{
		Role:  "user",
		Parts: []CanonicalPart{{Kind: "text", Value: "turn 1101"}},
	})
	withoutSysTurns = append(withoutSysTurns, CanonicalTurn{
		Role:  "assistant",
		Parts: []CanonicalPart{{Kind: "text", Value: "turn 1101 ack"}},
	})

	if !matcher.Touch(namespace, withoutSysTurns, "auth-sys-rem") {
		t.Fatal("Touch failed on updated turns with system instruction removed")
	}

	// 4a. Candidate WITHOUT system prompt: MUST MATCH (envDigest matches updated empty state)
	candNoSys := []CanonicalTurn{
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "summary of turns 0..1095"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "turn 1101"}}},
		{Role: "assistant", Parts: []CanonicalPart{{Kind: "text", Value: "turn 1101 ack"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "turn 1102"}}},
	}
	match, ok := matcher.Match(namespace, candNoSys)
	if !ok {
		t.Fatal("expected candidate without system prompt to match updated state")
	}
	if match.ParentSessionID != parentRes.SessionID {
		t.Fatalf("parentSessionID = %q, want %q", match.ParentSessionID, parentRes.SessionID)
	}

	// 4b. Stale candidate with the old temporary system prompt: MUST BE REJECTED
	candWithOldSys := []CanonicalTurn{
		{Role: "system", Parts: []CanonicalPart{{Kind: "text", Value: "Temporary developer instruction"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "summary of turns 0..1095"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "turn 1101"}}},
		{Role: "assistant", Parts: []CanonicalPart{{Kind: "text", Value: "turn 1101 ack"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "turn 1102"}}},
	}
	if _, okOld := matcher.Match(namespace, candWithOldSys); okOld {
		t.Fatal("stale candidate with old system prompt must be rejected")
	}
}

func TestMerklePrefixMatcherCompactionPreservingInitialPrefix(t *testing.T) {
	t.Parallel()

	matcher := NewMerklePrefixMatcher(time.Hour)
	defer matcher.Clear()
	namespace := "lcp:v1:test-prefix-compaction:model:caller"

	// Parent session with system turn + 4 turns [Sys, A, B, C, D]
	parent := []CanonicalTurn{
		{Role: "system", Parts: []CanonicalPart{{Kind: "text", Value: "System instruction directive"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "turn A"}}},
		{Role: "assistant", Parts: []CanonicalPart{{Kind: "text", Value: "ack B"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "turn C"}}},
		{Role: "assistant", Parts: []CanonicalPart{{Kind: "text", Value: "ack D"}}},
	}
	parentRes := matcher.BindWithResult(namespace, parent, "auth-parent-prefix")

	// Compacted continuation preserves system turn (prefix length 1), summarizes A-B, preserves C-D, and appends E
	// Under naive fork logic, prefix length 1 would be falsely classified as a divergent fork.
	// Compaction tail matching must recognize it as in-place compaction continuation.
	compacted := []CanonicalTurn{
		{Role: "system", Parts: []CanonicalPart{{Kind: "text", Value: "System instruction directive"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "summary of turns A and B"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "turn C"}}},
		{Role: "assistant", Parts: []CanonicalPart{{Kind: "text", Value: "ack D"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "turn E"}}},
	}

	match, ok := matcher.Match(namespace, compacted)
	if !ok {
		t.Fatal("expected match across compaction preserving initial system prefix")
	}
	if match.AuthID != "auth-parent-prefix" {
		t.Fatalf("authID = %q, want auth-parent-prefix", match.AuthID)
	}
	if match.ParentSessionID != parentRes.SessionID {
		t.Fatalf("parentSessionID = %q, want %q", match.ParentSessionID, parentRes.SessionID)
	}
	if !match.IsCompaction {
		t.Fatal("expected IsCompaction = true")
	}
	if match.IsFork {
		t.Fatal("compaction preserving initial prefix must NOT be classified as fork")
	}
	if match.NodeKind != "compaction" {
		t.Fatalf("nodeKind = %q, want compaction", match.NodeKind)
	}
	if match.SessionID == match.ParentSessionID {
		t.Fatalf("sessionID must be distinct from parentSessionID: got %q", match.SessionID)
	}
}

func TestMerklePrefixMatcherCompactionMultiToolBatchTrailingTurns(t *testing.T) {
	t.Parallel()

	matcher := NewMerklePrefixMatcher(time.Hour)
	defer matcher.Clear()
	namespace := "lcp:v1:test-multi-tool:model:caller"

	// Parent sequence [A, B, C, D]
	parent := turnsFromTexts("msgA", "msgB", "msgC", "msgD")
	parentRes := matcher.BindWithResult(namespace, parent, "auth-multi-tool")

	// Compacted sequence preserving C, D, followed by 6 appended turns (multiple tool calls/results + prompt)
	compacted := turnsFromTexts(
		"compacted summary",
		"msgC",
		"msgD",
		"tool_call_1",
		"tool_result_1",
		"tool_call_2",
		"tool_result_2",
		"tool_call_3",
		"tool_result_3",
		"assistant_final_prompt",
	)

	match, ok := matcher.Match(namespace, compacted)
	if !ok {
		t.Fatal("expected match for compaction continuation with 7+ trailing tool turns")
	}
	if match.ParentSessionID != parentRes.SessionID {
		t.Fatalf("parentSessionID = %q, want %q", match.ParentSessionID, parentRes.SessionID)
	}
	if !match.IsCompaction || match.IsFork {
		t.Fatalf("expected (IsCompaction=true, IsFork=false), got (%v, %v)", match.IsCompaction, match.IsFork)
	}
}

func TestMerklePrefixMatcherCompactionPreservingUserPrefix(t *testing.T) {
	t.Parallel()

	matcher := NewMerklePrefixMatcher(time.Hour)
	defer matcher.Clear()
	namespace := "lcp:v1:test-user-prefix:model:caller"

	// Parent session [UserA, UserB, UserC, UserD] (turn 0 is a user message)
	parent := turnsFromTexts("userA", "userB", "userC", "userD")
	parentRes := matcher.BindWithResult(namespace, parent, "auth-user-prefix")

	// Compacted continuation preserves turn 0 ("userA"), summarizes B, preserves C-D, and appends E
	compacted := turnsFromTexts("userA", "summary of turn B", "userC", "userD", "userE")
	match, ok := matcher.Match(namespace, compacted)
	if !ok {
		t.Fatal("expected match for compaction preserving user turn 0")
	}
	if match.ParentSessionID != parentRes.SessionID {
		t.Fatalf("parentSessionID = %q, want %q", match.ParentSessionID, parentRes.SessionID)
	}
	if match.SessionID == match.ParentSessionID {
		t.Fatalf("sessionID must be distinct from parentSessionID: got %q", match.SessionID)
	}
	if !match.IsCompaction || match.IsFork {
		t.Fatalf("expected (IsCompaction=true, IsFork=false), got (%v, %v)", match.IsCompaction, match.IsFork)
	}
}

func TestMerklePrefixMatcherSingleTurnTruncation(t *testing.T) {
	t.Parallel()

	matcher := NewMerklePrefixMatcher(time.Hour)
	defer matcher.Clear()
	namespace := "lcp:v1:test-single-trunc:model:caller"

	// Parent [A, B, C]
	parent := turnsFromTexts("turnA", "turnB", "turnC")
	parentRes := matcher.BindWithResult(namespace, parent, "auth-single-trunc")

	// Candidate [B, C, D] (only 1 turn, turnA, dropped)
	truncated := turnsFromTexts("turnB", "turnC", "turnD")
	match, ok := matcher.Match(namespace, truncated)
	if !ok {
		t.Fatal("expected match for single-turn sliding window truncation [A, B, C] -> [B, C, D]")
	}
	if match.ParentSessionID != parentRes.SessionID {
		t.Fatalf("parentSessionID = %q, want %q", match.ParentSessionID, parentRes.SessionID)
	}
	if match.SessionID == match.ParentSessionID {
		t.Fatalf("sessionID must be distinct from parentSessionID: got %q", match.SessionID)
	}
	if !match.IsCompaction || match.IsFork {
		t.Fatalf("expected (IsCompaction=true, IsFork=false), got (%v, %v)", match.IsCompaction, match.IsFork)
	}
}

func TestMerklePrefixMatcherConsecutiveCompactionSharedTailLineageResolution(t *testing.T) {
	t.Parallel()

	matcher := NewMerklePrefixMatcher(time.Hour)
	defer matcher.Clear()
	namespace := "lcp:v1:test-consec-comp:model:caller"

	// 1. Initial parent session [A, B, C, D]
	sess1 := turnsFromTexts("turnA", "turnB", "turnC", "turnD")
	res1 := matcher.BindWithResult(namespace, sess1, "auth-root")

	// 2. First compaction continuation: [Summary1, C, D]
	sess2 := turnsFromTexts("summary-1", "turnC", "turnD")
	res2 := matcher.BindWithResult(namespace, sess2, "auth-root")
	if res2.ParentSessionID != res1.SessionID {
		t.Fatalf("first compaction parent = %q, want %q", res2.ParentSessionID, res1.SessionID)
	}

	// 3. Second compaction continuation: [Summary2, C, D, E]
	// Both sess1 and sess2 share tail [turnC, turnD] and are active in the namespace.
	// Because sess2 is the direct descendant of sess1 on the same conversation trunk,
	// the matcher must resolve the ambiguity in favor of the leaf session sess2.
	sess3 := turnsFromTexts("summary-2", "turnC", "turnD", "turnE")
	match3, ok3 := matcher.Match(namespace, sess3)
	if !ok3 {
		t.Fatal("expected match for consecutive compaction sharing tail with ancestor")
	}
	if match3.ParentSessionID != res2.SessionID {
		t.Fatalf("second compaction parent = %q, want leaf session %q", match3.ParentSessionID, res2.SessionID)
	}
	if match3.SessionID == match3.ParentSessionID {
		t.Fatalf("sessionID must be distinct from parentSessionID: got %q", match3.SessionID)
	}
	if !match3.IsCompaction || match3.IsFork {
		t.Fatalf("expected (IsCompaction=true, IsFork=false), got (%v, %v)", match3.IsCompaction, match3.IsFork)
	}
}

func TestMerklePrefixMatcherCompactionAmbiguityWithAncestorAndUnrelatedCompetitor(t *testing.T) {
	t.Parallel()

	matcher := NewMerklePrefixMatcher(time.Hour)
	defer matcher.Clear()
	namespace := "lcp:v1:test-comp-ambig-unrel:model:caller"

	// 1. Ancestor A [turnA, turnB, turnC, turnD]
	sessA := turnsFromTexts("turnA", "turnB", "turnC", "turnD")
	resA := matcher.BindWithResult(namespace, sessA, "auth-A")

	// 2. Descendant A_child [summary-A, turnC, turnD]
	sessAChild := turnsFromTexts("summary-A", "turnC", "turnD")
	resAChild := matcher.BindWithResult(namespace, sessAChild, "auth-A")
	if resAChild.ParentSessionID != resA.SessionID {
		t.Fatalf("A_child parent = %q, want %q", resAChild.ParentSessionID, resA.SessionID)
	}

	// 3. Completely unrelated session R [unrel1, unrel2, turnC, turnD]
	sessR := turnsFromTexts("unrel1", "unrel2", "turnC", "turnD")
	matcher.BindWithResult(namespace, sessR, "auth-R")

	// 4. Candidate [summary-new, turnC, turnD, turnE]
	// Matches A, A_child, and R with overlap 2.
	// A is ancestor of A_child, leaving A_child and R as surviving leaves.
	// Because A_child and R are distinct leaves, this must be safely rejected due to ambiguity.
	cand := turnsFromTexts("summary-new", "turnC", "turnD", "turnE")
	if _, ok := matcher.Match(namespace, cand); ok {
		t.Fatal("expected candidate matching both descendant leaf and unrelated competitor to be rejected")
	}
}

func TestMerklePrefixMatcherLongCandidateWithDivergentHeadRejected(t *testing.T) {
	t.Parallel()

	matcher := NewMerklePrefixMatcher(time.Hour)
	defer matcher.Clear()
	namespace := "lcp:v1:test-long-unrelated:model:caller"

	// Parent session [A, B, C, D]
	parent := turnsFromTexts("turnA", "turnB", "turnC", "turnD")
	matcher.BindWithResult(namespace, parent, "auth-parent")

	// Unrelated long candidate with 40 turns: [Q0 ... Q37, C, D]
	// Shares tail [turnC, turnD], but has 38 non-matching turns preceding the tail.
	// Must NOT be treated as compaction continuation.
	unrelTexts := make([]string, 0, 40)
	for i := 0; i < 38; i++ {
		unrelTexts = append(unrelTexts, fmt.Sprintf("unrelated-turn-%d", i))
	}
	unrelTexts = append(unrelTexts, "turnC", "turnD")
	cand := turnsFromTexts(unrelTexts...)

	if _, ok := matcher.Match(namespace, cand); ok {
		t.Fatal("unrelated 40-turn session sharing only 2 trailing turns must be rejected")
	}
}

func BenchmarkMerklePrefixMatcherMatch_100Turns(b *testing.B) {
	matcher := NewMerklePrefixMatcher(time.Hour)
	texts := make([]string, 100)
	for i := 0; i < 100; i++ {
		texts[i] = fmt.Sprintf("turn-%d content for long conversation benchmark", i)
	}
	turns := turnsFromTexts(texts...)
	matcher.Bind("lcp:v1:benchmark:model:caller", turns, "auth-a")
	b.ReportAllocs()
	for b.Loop() {
		_, _ = matcher.Match("lcp:v1:benchmark:model:caller", turns)
	}
}

func turnsFromTexts(values ...string) []CanonicalTurn {
	turns := make([]CanonicalTurn, 0, len(values))
	for _, value := range values {
		turns = append(turns, CanonicalTurn{
			Role: "user",
			Parts: []CanonicalPart{{
				Kind:  "text",
				Value: value,
			}},
		})
	}
	return turns
}
