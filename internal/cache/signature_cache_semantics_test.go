package cache

import (
	"testing"
	"time"
)

// These tests pin the four signature-cache semantic guarantees the Phase C
// LRU swap must preserve:
//
//   1. Sliding TTL on read (refresh-on-access).
//   2. Gemini miss sentinel (returned for empty/miss/expired keys when the
//      model group resolves to "gemini").
//   3. ClearSignatureCache(modelName) group-clear by model group key.
//   4. ClearSignatureCache("") full clear of every group.
//
// The clock package var is overridden to exercise TTL passage without
// sleeping. Production behavior is unchanged.

const validSemanticsSig = "valid_semantics_signature_ABCDEFGHIJKLMNOPQRSTUVWXYZ_0123456789"

func withClockReset(t *testing.T) {
	t.Helper()
	prev := clock
	t.Cleanup(func() {
		clock = prev
		ClearSignatureCache("")
	})
	ClearSignatureCache("")
}

func setClock(at time.Time) {
	clock = func() time.Time { return at }
}

func TestSignatureCache_SlidingTTL_RefreshOnRead(t *testing.T) {
	withClockReset(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	setClock(base)
	CacheSignature("claude-sonnet-4-5", "thinking text", validSemanticsSig)

	// Read at +2h: hits (within 3h TTL) and refreshes the timestamp.
	setClock(base.Add(2 * time.Hour))
	if got := GetCachedSignature("claude-sonnet-4-5", "thinking text"); got != validSemanticsSig {
		t.Fatalf("expected hit at +2h, got %q", got)
	}

	// Read at +4h: original timestamp would be expired (4h > 3h TTL), but
	// the +2h read refreshed it; new gap is 2h < 3h so still hits.
	setClock(base.Add(4 * time.Hour))
	if got := GetCachedSignature("claude-sonnet-4-5", "thinking text"); got != validSemanticsSig {
		t.Fatalf("expected sliding-refresh hit at +4h, got %q", got)
	}
}

func TestSignatureCache_ExpiresAfterTTL_NoIntermediateReads(t *testing.T) {
	withClockReset(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	setClock(base)
	CacheSignature("claude-sonnet-4-5", "stale text", validSemanticsSig)

	// Read at +4h with no intermediate reads — entry expires (4h > 3h TTL).
	setClock(base.Add(4 * time.Hour))
	if got := GetCachedSignature("claude-sonnet-4-5", "stale text"); got != "" {
		t.Fatalf("expected empty after TTL with no refresh, got %q", got)
	}
}

func TestSignatureCache_GeminiSentinel_OnEmptyText(t *testing.T) {
	withClockReset(t)
	if got := GetCachedSignature("gemini-3-pro-preview", ""); got != "skip_thought_signature_validator" {
		t.Fatalf("expected gemini sentinel for empty text, got %q", got)
	}
}

func TestSignatureCache_GeminiSentinel_OnMiss(t *testing.T) {
	withClockReset(t)
	if got := GetCachedSignature("gemini-3-pro-preview", "no-such-text"); got != "skip_thought_signature_validator" {
		t.Fatalf("expected gemini sentinel for miss, got %q", got)
	}
}

func TestSignatureCache_GeminiSentinel_OnExpired(t *testing.T) {
	withClockReset(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	setClock(base)
	CacheSignature("gemini-3-pro-preview", "text", validSemanticsSig)

	setClock(base.Add(4 * time.Hour))
	if got := GetCachedSignature("gemini-3-pro-preview", "text"); got != "skip_thought_signature_validator" {
		t.Fatalf("expected gemini sentinel after expiry, got %q", got)
	}
}

func TestSignatureCache_NonGemini_NoSentinel_OnExpired(t *testing.T) {
	withClockReset(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	setClock(base)
	CacheSignature("claude-sonnet-4-5", "text", validSemanticsSig)

	setClock(base.Add(4 * time.Hour))
	if got := GetCachedSignature("claude-sonnet-4-5", "text"); got != "" {
		t.Fatalf("expected empty (non-gemini) after expiry, got %q", got)
	}
}

func TestSignatureCache_NonGemini_EmptyText_ReturnsEmpty(t *testing.T) {
	withClockReset(t)
	if got := GetCachedSignature("claude-sonnet-4-5", ""); got != "" {
		t.Fatalf("expected empty for non-gemini empty text, got %q", got)
	}
	if got := GetCachedSignature("gpt-5.5", ""); got != "" {
		t.Fatalf("expected empty for non-gemini empty text, got %q", got)
	}
}

func TestSignatureCache_GroupClear_ByModel_OnlyClearsTargetGroup(t *testing.T) {
	withClockReset(t)
	CacheSignature("claude-sonnet-4-5", "text-claude", validSemanticsSig)
	CacheSignature("gemini-3-pro-preview", "text-gemini", validSemanticsSig)
	CacheSignature("gpt-5.5", "text-gpt", validSemanticsSig)

	// Clear only the claude group (any model name resolving to "claude" group).
	ClearSignatureCache("claude-anything")

	if got := GetCachedSignature("claude-sonnet-4-5", "text-claude"); got != "" {
		t.Fatalf("expected claude group cleared, got %q", got)
	}
	if got := GetCachedSignature("gemini-3-pro-preview", "text-gemini"); got != validSemanticsSig {
		t.Fatalf("expected gemini group intact, got %q", got)
	}
	if got := GetCachedSignature("gpt-5.5", "text-gpt"); got != validSemanticsSig {
		t.Fatalf("expected gpt group intact, got %q", got)
	}
}

func TestSignatureCache_FullClear_RemovesAllGroups(t *testing.T) {
	withClockReset(t)
	CacheSignature("claude-sonnet-4-5", "text-claude", validSemanticsSig)
	CacheSignature("gemini-3-pro-preview", "text-gemini", validSemanticsSig)
	CacheSignature("gpt-5.5", "text-gpt", validSemanticsSig)

	ClearSignatureCache("")

	if got := GetCachedSignature("claude-sonnet-4-5", "text-claude"); got != "" {
		t.Fatalf("expected claude cleared, got %q", got)
	}
	if got := GetCachedSignature("gpt-5.5", "text-gpt"); got != "" {
		t.Fatalf("expected gpt cleared, got %q", got)
	}
	// After full clear, gemini lookups still return the miss sentinel.
	if got := GetCachedSignature("gemini-3-pro-preview", "text-gemini"); got != "skip_thought_signature_validator" {
		t.Fatalf("expected gemini cleared with miss sentinel, got %q", got)
	}
}

func TestSignatureCache_OuterMapBoundedByMaxGroupCount(t *testing.T) {
	withClockReset(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// Insert MaxGroupCount + 50 distinct unknown-model-name groups. Each
	// unknown name resolves to its own outer-map key under GetModelGroup,
	// so without the bound this would grow unboundedly. Stagger the clock
	// so lastAccess timestamps differ and LRU eviction is deterministic.
	const overflow = 50
	for i := 0; i < MaxGroupCount+overflow; i++ {
		setClock(base.Add(time.Duration(i) * time.Second))
		modelName := "unknown-model-" + intToStr(i)
		CacheSignature(modelName, "text-"+intToStr(i), validSemanticsSig)
	}

	// Cap holds: groupCount must never exceed MaxGroupCount steady-state
	// (allow +1 for racy concurrent inserts; this test is sequential so
	// the cap is exact).
	if got := groupCount.Load(); got > int64(MaxGroupCount) {
		t.Fatalf("expected groupCount <= %d, got %d", MaxGroupCount, got)
	}

	// LRU policy: the earliest-inserted (oldest lastAccess) groups should
	// have been evicted. Verify the first-inserted group is gone and a
	// recently-inserted group is still present.
	setClock(base.Add(time.Duration(MaxGroupCount+overflow) * time.Second))
	if got := GetCachedSignature("unknown-model-0", "text-0"); got != "" {
		t.Fatalf("expected oldest group evicted, got %q", got)
	}
	recentIdx := MaxGroupCount + overflow - 1
	recentName := "unknown-model-" + intToStr(recentIdx)
	if got := GetCachedSignature(recentName, "text-"+intToStr(recentIdx)); got != validSemanticsSig {
		t.Fatalf("expected recent group intact, got %q", got)
	}
}

func intToStr(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	neg := i < 0
	if neg {
		i = -i
	}
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

func TestSignatureCache_RefreshOnRead_PersistsTimestamp(t *testing.T) {
	withClockReset(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	setClock(base)
	CacheSignature("claude-sonnet-4-5", "text", validSemanticsSig)

	// Read at +2h refreshes timestamp to +2h.
	setClock(base.Add(2 * time.Hour))
	_ = GetCachedSignature("claude-sonnet-4-5", "text")

	// At +4h after the refresh, gap from refreshed timestamp = 2h < 3h: hit.
	setClock(base.Add(4 * time.Hour))
	if got := GetCachedSignature("claude-sonnet-4-5", "text"); got != validSemanticsSig {
		t.Fatalf("first refreshed read at +4h: expected hit, got %q", got)
	}

	// At +6.5h after another refresh at +4h, gap = 2.5h < 3h: still hit.
	setClock(base.Add(6*time.Hour + 30*time.Minute))
	if got := GetCachedSignature("claude-sonnet-4-5", "text"); got != validSemanticsSig {
		t.Fatalf("second refreshed read at +6.5h: expected hit, got %q", got)
	}
}
