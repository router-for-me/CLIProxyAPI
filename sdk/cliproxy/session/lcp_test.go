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
