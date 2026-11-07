package helps

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	homekv "github.com/router-for-me/CLIProxyAPI/v8/internal/home"
)

func TestClaudeDiagnosticsTracksCompletedMessagePerCredentialSession(t *testing.T) {
	resetClaudeDiagnosticsForTest()
	defer resetClaudeDiagnosticsForTest()

	key, sequence, previous := BeginClaudeDiagnostics("credential-a", "session-a")
	if key == "" || sequence != 1 || previous != "" {
		t.Fatalf("first begin = %q/%d/%q, want key/1/empty", key, sequence, previous)
	}
	CommitClaudeDiagnostics(key, sequence, "msg_first")
	_, secondSequence, previous := BeginClaudeDiagnostics("credential-a", "session-a")
	if secondSequence != 2 || previous != "msg_first" {
		t.Fatalf("second begin = %d/%q, want 2/msg_first", secondSequence, previous)
	}

	_, _, otherSession := BeginClaudeDiagnostics("credential-a", "session-b")
	_, _, otherCredential := BeginClaudeDiagnostics("credential-b", "session-a")
	if otherSession != "" || otherCredential != "" {
		t.Fatalf("diagnostics leaked across identity: session=%q credential=%q", otherSession, otherCredential)
	}
}

func TestClaudeDiagnosticsRejectsExpiredGenerationCommit(t *testing.T) {
	resetClaudeDiagnosticsForTest()
	defer resetClaudeDiagnosticsForTest()

	key, expiredSequence, _ := BeginClaudeDiagnostics("credential", "session")
	claudeDiagnosticsState.Lock()
	entry := claudeDiagnosticsState.entries[key]
	entry.expiresAt = time.Now().Add(-time.Second)
	claudeDiagnosticsState.entries[key] = entry
	claudeDiagnosticsState.Unlock()

	newKey, currentSequence, previous := BeginClaudeDiagnostics("credential", "session")
	if newKey != key || currentSequence <= expiredSequence || previous != "" {
		t.Fatalf("new generation = %q/%d/%q, want same key/new sequence/empty", newKey, currentSequence, previous)
	}
	CommitClaudeDiagnostics(newKey, currentSequence, "msg_current")
	CommitClaudeDiagnostics(key, expiredSequence, "msg_expired")
	_, _, previous = BeginClaudeDiagnostics("credential", "session")
	if previous != "msg_current" {
		t.Fatalf("previous message = %q, want current generation", previous)
	}
}

func TestClaudeDiagnosticsCacheEvictsOldestEntriesWithinCapacity(t *testing.T) {
	resetClaudeDiagnosticsForTest()
	defer resetClaudeDiagnosticsForTest()

	firstKey, firstSequence, _ := BeginClaudeDiagnostics("credential", "session-0")
	var newestKey string
	for index := 1; index <= claudeDiagnosticsMaxEntries; index++ {
		newestKey, _, _ = BeginClaudeDiagnostics("credential", fmt.Sprintf("session-%d", index))
	}

	claudeDiagnosticsState.Lock()
	entryCount := len(claudeDiagnosticsState.entries)
	_, firstFound := claudeDiagnosticsState.entries[firstKey]
	_, newestFound := claudeDiagnosticsState.entries[newestKey]
	claudeDiagnosticsState.Unlock()
	if entryCount > claudeDiagnosticsMaxEntries {
		t.Fatalf("cache entries = %d, want at most %d", entryCount, claudeDiagnosticsMaxEntries)
	}
	if firstFound {
		t.Fatal("oldest diagnostics entry was not evicted")
	}
	if !newestFound {
		t.Fatal("newest diagnostics entry was evicted")
	}

	newKey, newSequence, _ := BeginClaudeDiagnostics("credential", "session-0")
	if newKey != firstKey || newSequence <= firstSequence {
		t.Fatalf("recreated generation = %q/%d, want same key after sequence %d", newKey, newSequence, firstSequence)
	}
	CommitClaudeDiagnostics(newKey, newSequence, "msg_recreated")
	CommitClaudeDiagnostics(firstKey, firstSequence, "msg_evicted")
	_, _, previous := BeginClaudeDiagnostics("credential", "session-0")
	if previous != "msg_recreated" {
		t.Fatalf("previous message = %q, want recreated generation", previous)
	}
}

func TestClaudeDiagnosticsRejectsLateOlderCommit(t *testing.T) {
	resetClaudeDiagnosticsForTest()
	defer resetClaudeDiagnosticsForTest()

	key, first, _ := BeginClaudeDiagnostics("credential", "session")
	_, second, _ := BeginClaudeDiagnostics("credential", "session")
	CommitClaudeDiagnostics(key, second, "msg_newer")
	CommitClaudeDiagnostics(key, first, "msg_older")
	_, _, previous := BeginClaudeDiagnostics("credential", "session")
	if previous != "msg_newer" {
		t.Fatalf("previous message = %q, want newer completed generation", previous)
	}
}

func TestClaudeContinuityTracksRequestIDAndPromptID(t *testing.T) {
	resetClaudeDiagnosticsForTest()
	defer resetClaudeDiagnosticsForTest()

	// Turn 1: New prompt turn
	key, seq1, prevMsg, prevReq, prompt1 := BeginClaudeContinuity("cred-1", "sess-1", true, "")
	if prevMsg != "" || prevReq != "" || prompt1 == "" {
		t.Fatalf("turn 1 = prevMsg:%q prevReq:%q prompt:%q, want empty prevs and fresh prompt", prevMsg, prevReq, prompt1)
	}
	CommitClaudeContinuity(key, seq1, "msg_01aaa", "req_01bbb", prompt1)

	// Turn 1.1: Tool continuation turn (not new prompt)
	_, seq2, prevMsg, prevReq, prompt2 := BeginClaudeContinuity("cred-1", "sess-1", false, "")
	if prevMsg != "msg_01aaa" || prevReq != "req_01bbb" {
		t.Fatalf("turn 1.1 = prevMsg:%q prevReq:%q, want msg_01aaa / req_01bbb", prevMsg, prevReq)
	}
	if prompt2 != prompt1 {
		t.Fatalf("turn 1.1 prompt = %q, want same prompt %q as turn 1", prompt2, prompt1)
	}
	CommitClaudeContinuity(key, seq2, "msg_01ccc", "req_01ddd", prompt2)

	// Turn 2: New prompt turn
	_, _, prevMsg, prevReq, prompt3 := BeginClaudeContinuity("cred-1", "sess-1", true, "")
	if prevMsg != "msg_01ccc" || prevReq != "req_01ddd" {
		t.Fatalf("turn 2 = prevMsg:%q prevReq:%q, want msg_01ccc / req_01ddd", prevMsg, prevReq)
	}
	if prompt3 == prompt1 {
		t.Fatalf("turn 2 prompt = %q, want new prompt different from %q", prompt3, prompt1)
	}
}

func TestClaudeContinuityHelperPredicates(t *testing.T) {
	probeBody := []byte(`{"model":"claude-fable-5-1","max_tokens":1,"messages":[{"role":"user","content":[{"type":"text","text":"Hi","cache_control":{"type":"ephemeral"}}]}]}`)
	if !IsClaudeProbeOrHelperRequest(probeBody) {
		t.Fatal("IsClaudeProbeOrHelperRequest(fable probe) = false, want true")
	}

	quotaProbeBody := []byte(`{"model":"claude-haiku-4-5-20251001","max_tokens":1,"messages":[{"role":"user","content":"quota"}]}`)
	if !IsClaudeProbeOrHelperRequest(quotaProbeBody) {
		t.Fatal("IsClaudeProbeOrHelperRequest(quota probe) = false, want true")
	}

	ordinaryHiMaxTokens1 := []byte(`{"model":"claude-fable-5-1","max_tokens":1,"messages":[{"role":"user","content":"Hi"}]}`)
	if IsClaudeProbeOrHelperRequest(ordinaryHiMaxTokens1) {
		t.Fatal("IsClaudeProbeOrHelperRequest(ordinary Hi max_tokens=1) = true, want false")
	}

	multiTurnMaxTokens1 := []byte(`{"model":"claude-sonnet-5","max_tokens":1,"messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":"what is 1+1?"}]}`)
	if IsClaudeProbeOrHelperRequest(multiTurnMaxTokens1) {
		t.Fatal("IsClaudeProbeOrHelperRequest(multi-turn max_tokens=1) = true, want false")
	}

	withToolsMaxTokens1 := []byte(`{"model":"claude-sonnet-5","max_tokens":1,"messages":[{"role":"user","content":"Hi"}],"tools":[{"name":"t1","description":"tool"}]}`)
	if IsClaudeProbeOrHelperRequest(withToolsMaxTokens1) {
		t.Fatal("IsClaudeProbeOrHelperRequest(tools max_tokens=1) = true, want false")
	}

	titleBody := []byte(`{"model":"claude-haiku-4-5-20251001","output_config":{"format":{"schema":{"properties":{"title":{"type":"string"}}}}},"messages":[{"role":"user","content":"Return a short title summarizing this conversation"}]}`)
	if !IsClaudeProbeOrHelperRequest(titleBody) {
		t.Fatal("IsClaudeProbeOrHelperRequest(title helper) = false, want true")
	}

	ordinaryTitleSchema := []byte(`{"model":"claude-haiku-4-5-20251001","output_config":{"format":{"schema":{"properties":{"title":{"type":"string"}}}}},"messages":[{"role":"user","content":"what is the title of the book?"}]}`)
	if IsClaudeProbeOrHelperRequest(ordinaryTitleSchema) {
		t.Fatal("IsClaudeProbeOrHelperRequest(ordinary title schema) = true, want false")
	}

	relocatedTitleBody := []byte(`{"model":"claude-sonnet-5","system":[{"type":"text","text":"cli-identity"}],"messages":[{"role":"system","content":"Return a short title summarizing this conversation"}]}`)
	if !IsClaudeProbeOrHelperRequest(relocatedTitleBody) {
		t.Fatal("IsClaudeProbeOrHelperRequest(relocated title system in messages) = false, want true")
	}

	toolResultBody := []byte(`{"model":"claude-sonnet-5","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":[{"type":"tool_use","id":"t1"}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}]}`)
	if IsClaudeNewPromptTurn(toolResultBody) {
		t.Fatal("IsClaudeNewPromptTurn(tool_result) = true, want false")
	}

	newPromptBody := []byte(`{"model":"claude-sonnet-5","messages":[{"role":"user","content":"explain sorting"}]}`)
	if !IsClaudeNewPromptTurn(newPromptBody) {
		t.Fatal("IsClaudeNewPromptTurn(user text) = false, want true")
	}

	subagentHeader := http.Header{"X-Claude-Code-Agent-Id": []string{"sub-1"}}
	if !IsClaudeSubagentRequest(subagentHeader, []byte(`{}`)) {
		t.Fatal("IsClaudeSubagentRequest(agent header) = false, want true")
	}

	subagentMetaBody := []byte(`{"metadata":{"user_id":"{\"device_id\":\"dev\",\"session_id\":\"sess\",\"parent_session_id\":\"parent-1\"}"}}`)
	if !IsClaudeSubagentRequest(nil, subagentMetaBody) {
		t.Fatal("IsClaudeSubagentRequest(parent_session_id) = false, want true")
	}

	billingSystemBody := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.258.1e2; cc_entrypoint=cli; cch=00000; cc_prev_req=req_01abc; cc_prompt_id=3c6489dc-badc-42b2-bd28-49f8ebabfedd;"}]}`)
	prevReq, promptID := ExtractClaudeBillingTags(billingSystemBody)
	if prevReq != "req_01abc" || promptID != "3c6489dc-badc-42b2-bd28-49f8ebabfedd" {
		t.Fatalf("ExtractClaudeBillingTags = %q, %q; want req_01abc, 3c6489dc-badc-42b2-bd28-49f8ebabfedd", prevReq, promptID)
	}
}

func TestIsValidClaudePromptID(t *testing.T) {
	v4 := uuid.NewString()
	if !IsValidClaudePromptID(v4) {
		t.Fatalf("IsValidClaudePromptID(%q) = false, want true", v4)
	}

	// UUIDv1 should be rejected
	v1 := "6ba7b810-9dad-11d1-80b4-00c04fd430c8"
	if IsValidClaudePromptID(v1) {
		t.Fatalf("IsValidClaudePromptID(v1 %q) = true, want false", v1)
	}

	// UUIDv4 with non-RFC4122 variant (variant bits 0xxx instead of 10xx, e.g. '0' instead of '8','9','a','b')
	nonRFC4122 := "3c6489dc-badc-42b2-0d28-49f8ebabfedd"
	if IsValidClaudePromptID(nonRFC4122) {
		t.Fatalf("IsValidClaudePromptID(non-RFC4122 variant %q) = true, want false", nonRFC4122)
	}

	// All zeros UUID should be rejected (version 0)
	allZeros := "00000000-0000-0000-0000-000000000000"
	if IsValidClaudePromptID(allZeros) {
		t.Fatalf("IsValidClaudePromptID(all zeros %q) = true, want false", allZeros)
	}

	// Invalid format strings
	for _, invalid := range []string{"", "not-a-uuid", "12345", "3c6489dc-badc-42b2-bd28-49f8ebabfedd-extra"} {
		if IsValidClaudePromptID(invalid) {
			t.Fatalf("IsValidClaudePromptID(%q) = true, want false", invalid)
		}
	}
}

func TestClaudeContinuityConcurrentOverlappingPromptID(t *testing.T) {
	resetClaudeDiagnosticsForTest()
	defer resetClaudeDiagnosticsForTest()

	// Turn 1 starts (in-flight, not committed)
	key, seq1, _, _, prompt1 := BeginClaudeContinuity("cred-1", "sess-1", true, "")

	// Turn 2 starts concurrently before Turn 1 commits
	_, seq2, _, _, prompt2 := BeginClaudeContinuity("cred-1", "sess-1", true, "")
	if prompt1 == prompt2 {
		t.Fatalf("turn 1 and turn 2 got same prompt %q, want distinct", prompt1)
	}

	// Turn 1 completes upstream and commits
	CommitClaudeContinuity(key, seq1, "msg_01aaa", "req_01bbb", prompt1)

	// Turn 1.1 tool continuation starts (not new turn)
	_, _, prevMsg, prevReq, prompt11 := BeginClaudeContinuity("cred-1", "sess-1", false, "")
	if prevMsg != "msg_01aaa" || prevReq != "req_01bbb" {
		t.Fatalf("turn 1.1 prevMsg=%q prevReq=%q, want msg_01aaa / req_01bbb", prevMsg, prevReq)
	}
	if prompt11 != prompt1 {
		t.Fatalf("turn 1.1 prompt=%q, want committed turn 1 prompt %q, not overwritten by uncommitted turn 2 (%q)", prompt11, prompt1, prompt2)
	}

	// Turn 2 completes upstream and commits
	CommitClaudeContinuity(key, seq2, "msg_01ccc", "req_01ddd", prompt2)

	// Turn 2.1 tool continuation starts
	_, _, prevMsg2, prevReq2, prompt21 := BeginClaudeContinuity("cred-1", "sess-1", false, "")
	if prevMsg2 != "msg_01ccc" || prevReq2 != "req_01ddd" {
		t.Fatalf("turn 2.1 prevMsg=%q prevReq=%q, want msg_01ccc / req_01ddd", prevMsg2, prevReq2)
	}
	if prompt21 != prompt2 {
		t.Fatalf("turn 2.1 prompt=%q, want committed turn 2 prompt %q", prompt21, prompt2)
	}
}

func TestClaudeContinuityClearsStaleRequestID(t *testing.T) {
	resetClaudeDiagnosticsForTest()
	defer resetClaudeDiagnosticsForTest()

	// Turn 1: has valid request-id
	key, seq1, _, _, p1 := BeginClaudeContinuity("cred-1", "sess-1", true, "")
	CommitClaudeContinuity(key, seq1, "msg_01aaa", "req_01bbb", p1)

	// Turn 1.1: verify prevReq is req_01bbb
	_, seq2, _, prevReq1, p2 := BeginClaudeContinuity("cred-1", "sess-1", false, "")
	if prevReq1 != "req_01bbb" {
		t.Fatalf("turn 1.1 prevReq = %q, want req_01bbb", prevReq1)
	}

	// Turn 1.1 finishes, but upstream returned no request-id
	CommitClaudeContinuity(key, seq2, "msg_01ccc", "", p2)

	// Turn 2: verify prevReq is empty, NOT stale req_01bbb
	_, _, prevMsg, prevReq2, _ := BeginClaudeContinuity("cred-1", "sess-1", true, "")
	if prevMsg != "msg_01ccc" {
		t.Fatalf("turn 2 prevMsg = %q, want msg_01ccc", prevMsg)
	}
	if prevReq2 != "" {
		t.Fatalf("turn 2 prevReq = %q, want empty (must not retain stale request-id from prior turn)", prevReq2)
	}
}

func TestClaudeContinuity_ExpiredEntryDoesNotInheritPromptID(t *testing.T) {
	resetClaudeDiagnosticsForTest()
	defer resetClaudeDiagnosticsForTest()

	key, _, _, _, p1 := BeginClaudeContinuity("cred-expire", "sess-expire", true, "")
	if p1 == "" {
		t.Fatal("expected non-empty promptID")
	}

	// Expire entry manually
	claudeDiagnosticsState.Lock()
	entry := claudeDiagnosticsState.entries[key]
	entry.expiresAt = time.Now().Add(-time.Second)
	claudeDiagnosticsState.entries[key] = entry
	claudeDiagnosticsState.Unlock()

	// Tool continuation call (isNewPromptTurn = false) without explicit prompt ID on expired entry
	_, _, _, _, p2 := BeginClaudeContinuity("cred-expire", "sess-expire", false, "")
	if p2 == "" {
		t.Fatal("new generation must generate non-empty prompt ID")
	}
	if p2 == p1 {
		t.Fatalf("new generation inherited expired prompt ID: %s", p1)
	}
}

func TestIsClaudeProbeRequest_MultiContentBlocksNotAProbe(t *testing.T) {
	// A request with max_tokens: 1 and multiple content blocks where one happens to be "quota"
	// but another is a regular prompt must NOT be treated as a probe.
	payload := []byte(`{
		"model": "claude-sonnet-5",
		"max_tokens": 1,
		"messages": [{
			"role": "user",
			"content": [
				{"type": "text", "text": "quota"},
				{"type": "text", "text": "Please write an analysis of the codebase."}
			]
		}]
	}`)
	if IsClaudeProbeOrHelperRequest(payload) {
		t.Fatal("multi-content user prompt with non-quota text must not be classified as a probe")
	}
}

func TestIsClaudeProbeRequest_SingleContentBlockWithReminderIsProbe(t *testing.T) {
	// A probe request with a system-reminder block and a single quota block IS a probe.
	payload := []byte(`{
		"model": "claude-sonnet-5",
		"max_tokens": 1,
		"messages": [{
			"role": "user",
			"content": [
				{"type": "text", "text": "<system-reminder>some reminder</system-reminder>"},
				{"type": "text", "text": "quota"}
			]
		}]
	}`)
	if !IsClaudeProbeOrHelperRequest(payload) {
		t.Fatal("probe with system reminder and single quota block must be classified as a probe")
	}
}

func TestClaudePayloadHas1hTTL(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
		want    bool
	}{
		{
			name:    "empty payload",
			payload: []byte(""),
			want:    false,
		},
		{
			name:    "invalid json",
			payload: []byte("not-json"),
			want:    false,
		},
		{
			name:    "no cache control",
			payload: []byte(`{"messages":[{"role":"user","content":"hello"}]}`),
			want:    false,
		},
		{
			name:    "default 5m ephemeral",
			payload: []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hello","cache_control":{"type":"ephemeral"}}]}]}`),
			want:    false,
		},
		{
			name:    "message content with 1h ttl",
			payload: []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hello","cache_control":{"type":"ephemeral","ttl":"1h"}}]}]}`),
			want:    true,
		},
		{
			name:    "system block with 1h ttl",
			payload: []byte(`{"system":[{"type":"text","text":"sys","cache_control":{"type":"ephemeral","ttl":"1h"}}]}`),
			want:    true,
		},
		{
			name:    "tool block with 1h ttl",
			payload: []byte(`{"tools":[{"name":"tool1","cache_control":{"type":"ephemeral","ttl":"1h"}}]}`),
			want:    true,
		},
		{
			name:    "message text mentions ttl 1h without cache control",
			payload: []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"can you set ttl: 1h"}]}]}`),
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClaudePayloadHas1hTTL(tt.payload); got != tt.want {
				t.Fatalf("ClaudePayloadHas1hTTL() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestClaudeSubagentRequests1h(t *testing.T) {
	// 1. Neither header nor payload has 1h
	headers := http.Header{}
	payload := []byte(`{"messages":[{"role":"user","content":"task"}]}`)
	if ClaudeSubagentRequests1h(headers, payload) {
		t.Fatal("ClaudeSubagentRequests1h() = true, want false when no 1h requested")
	}

	// 2. Payload has 1h
	payloadWith1h := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"task","cache_control":{"type":"ephemeral","ttl":"1h"}}]}]}`)
	if !ClaudeSubagentRequests1h(headers, payloadWith1h) {
		t.Fatal("ClaudeSubagentRequests1h() = false, want true when payload has 1h")
	}

	// 3. Header has extended-cache-ttl beta
	headersWithBeta := http.Header{}
	headersWithBeta.Set("Anthropic-Beta", "claude-code-20250219,extended-cache-ttl-2025-04-11")
	if !ClaudeSubagentRequests1h(headersWithBeta, payload) {
		t.Fatal("ClaudeSubagentRequests1h() = false, want true when header has extended-cache-ttl beta")
	}
}

func TestPinClaudeSessionDateAnchorsFirstRequestAndReanchorsAfterTTL(t *testing.T) {
	resetClaudeDiagnosticsForTest()
	defer resetClaudeDiagnosticsForTest()

	key, _, _ := BeginClaudeDiagnostics("credential", "session")
	if got := PinClaudeSessionDate(key, "session", "2026-08-01", 0); got != "2026-08-01" {
		t.Fatalf("first pin = %q, want 2026-08-01", got)
	}
	// Later requests of the same session keep the anchor even when the
	// candidate date has flipped past local midnight.
	if got := PinClaudeSessionDate(key, "session", "2026-08-02", 0); got != "2026-08-01" {
		t.Fatalf("second pin = %q, want anchored 2026-08-01", got)
	}

	// With no retention, continuity TTL expiry preserves the legacy anchor.
	claudeDiagnosticsState.Lock()
	entry := claudeDiagnosticsState.entries[key]
	entry.expiresAt = time.Now().Add(-time.Second)
	claudeDiagnosticsState.entries[key] = entry
	claudeDiagnosticsState.Unlock()
	BeginClaudeDiagnostics("credential", "session")
	if got := PinClaudeSessionDate(key, "session", "2026-08-02", 0); got != "2026-08-02" {
		t.Fatalf("post-TTL pin = %q, want re-anchored 2026-08-02", got)
	}

	// Unknown keys (no continuity entry) fall back to the candidate date,
	// preserving the pre-pinning per-request behaviour.
	if got := PinClaudeSessionDate("unknown-key", "", "2026-08-03", 0); got != "2026-08-03" {
		t.Fatalf("unknown key pin = %q, want candidate 2026-08-03", got)
	}
}

func TestPinClaudeSessionDateRetentionSurvivesContinuityTTLAndCredentialSwitch(t *testing.T) {
	resetClaudeDiagnosticsForTest()
	defer resetClaudeDiagnosticsForTest()

	retention := 7 * 24 * time.Hour
	keyA, _, _ := BeginClaudeDiagnostics("credential-a", "session")
	if got := PinClaudeSessionDate(keyA, "session", "2026-08-01", retention); got != "2026-08-01" {
		t.Fatalf("first pin = %q, want 2026-08-01", got)
	}

	claudeDiagnosticsState.Lock()
	entry := claudeDiagnosticsState.entries[keyA]
	entry.expiresAt = time.Now().Add(-time.Second)
	claudeDiagnosticsState.entries[keyA] = entry
	claudeDiagnosticsState.lastCleanup = time.Time{}
	claudeDiagnosticsState.Unlock()
	BeginClaudeDiagnostics("credential-a", "session")
	if got := PinClaudeSessionDate(keyA, "session", "2026-08-02", retention); got != "2026-08-01" {
		t.Fatalf("post-continuity-TTL pin = %q, want anchored 2026-08-01", got)
	}

	keyB, _, _ := BeginClaudeDiagnostics("credential-b", "session")
	if got := PinClaudeSessionDate(keyB, "session", "2026-08-02", retention); got != "2026-08-01" {
		t.Fatalf("post-credential-switch pin = %q, want anchored 2026-08-01", got)
	}

	claudeDiagnosticsState.Lock()
	delete(claudeDiagnosticsState.entries, keyA)
	delete(claudeDiagnosticsState.entries, keyB)
	for pinKey := range claudeDiagnosticsState.datePins {
		pin := claudeDiagnosticsState.datePins[pinKey]
		pin.expiresAt = time.Now().Add(-time.Second)
		claudeDiagnosticsState.datePins[pinKey] = pin
	}
	claudeDiagnosticsState.lastCleanup = time.Time{}
	claudeDiagnosticsState.Unlock()
	keyC, _, _ := BeginClaudeDiagnostics("credential-c", "session")
	if got := PinClaudeSessionDate(keyC, "session", "2026-08-02", retention); got != "2026-08-02" {
		t.Fatalf("post-pin-TTL pin = %q, want re-anchored 2026-08-02", got)
	}
}

type fakeClaudeSessionDatePinKVClient struct {
	mu           sync.Mutex
	values       map[string][]byte
	err          error
	casErr       error
	getCalls     int
	setNXCalls   int
	casCalls     int
	lastSetNXTTL time.Duration
	lastCASTTL   time.Duration
	onKVGet      func()
}

func (f *fakeClaudeSessionDatePinKVClient) KVGet(_ context.Context, key string) ([]byte, bool, error) {
	f.mu.Lock()
	f.getCalls++
	if f.err != nil {
		err := f.err
		f.mu.Unlock()
		return nil, false, err
	}
	raw, ok := f.values[key]
	var cloned []byte
	if ok {
		cloned = append([]byte(nil), raw...)
	}
	hook := f.onKVGet
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	if !ok {
		return nil, false, nil
	}
	return cloned, true, nil
}

func (f *fakeClaudeSessionDatePinKVClient) KVSetNX(_ context.Context, key string, value []byte, ttl time.Duration) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setNXCalls++
	if f.err != nil {
		return false, f.err
	}
	f.lastSetNXTTL = ttl
	if _, exists := f.values[key]; exists {
		return false, nil
	}
	f.values[key] = append([]byte(nil), value...)
	return true, nil
}

func (f *fakeClaudeSessionDatePinKVClient) KVCompareAndSwap(_ context.Context, key string, expected []byte, expectedExists bool, value []byte, ttl time.Duration) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.casCalls++
	if f.err != nil {
		return false, f.err
	}
	if f.casErr != nil {
		return false, f.casErr
	}
	f.lastCASTTL = ttl
	cur, exists := f.values[key]
	if exists != expectedExists {
		return false, nil
	}
	if expectedExists && !bytes.Equal(cur, expected) {
		return false, nil
	}
	f.values[key] = append([]byte(nil), value...)
	return true, nil
}

func TestPinClaudeSessionDateKVPersistsPermanentlyAndIgnoresRetention(t *testing.T) {
	resetClaudeDiagnosticsForTest()
	defer resetClaudeDiagnosticsForTest()

	fakeKV := &fakeClaudeSessionDatePinKVClient{values: make(map[string][]byte)}
	prevClientFunc := currentClaudeSessionDatePinKVClient
	currentClaudeSessionDatePinKVClient = func() (claudeSessionDatePinKVClient, bool, error) {
		return fakeKV, true, nil
	}
	defer func() {
		currentClaudeSessionDatePinKVClient = prevClientFunc
	}()

	if !IsClaudeSessionDatePinKVActive() {
		t.Fatal("IsClaudeSessionDatePinKVActive() = false, want true")
	}

	// First request with retention = 0 still persists permanently in KV.
	keyA, _, _ := BeginClaudeDiagnostics("credential-a", "session-kv")
	if got := PinClaudeSessionDate(keyA, "session-kv", "2026-08-01", 0); got != "2026-08-01" {
		t.Fatalf("first KV pin = %q, want 2026-08-01", got)
	}
	if fakeKV.setNXCalls != 1 || fakeKV.casCalls != 0 {
		t.Fatalf("KV writes after first pin: setNX=%d cas=%d, want 1/0", fakeKV.setNXCalls, fakeKV.casCalls)
	}
	if fakeKV.lastSetNXTTL != 0 {
		t.Fatalf("KVSetNX TTL = %v, want 0 (permanent)", fakeKV.lastSetNXTTL)
	}
	wantKVKey := "cpa:claude:session-date-pin:" + ClaudeSessionDatePinKey("session-kv")
	if string(fakeKV.values[wantKVKey]) != "2026-08-01" {
		t.Fatalf("KV stored value at %q = %q, want 2026-08-01", wantKVKey, string(fakeKV.values[wantKVKey]))
	}

	// Lookup ignores retention <= 0 when KV is active.
	if got := LookupClaudeSessionDatePin("session-kv", 0); got != "2026-08-01" {
		t.Fatalf("LookupClaudeSessionDatePin(retention=0) = %q, want 2026-08-01", got)
	}

	// Simulate process restart (wiping all in-memory state) and credential switch.
	resetClaudeDiagnosticsForTest()
	keyB, _, _ := BeginClaudeDiagnostics("credential-b", "session-kv")
	if got := PinClaudeSessionDate(keyB, "session-kv", "2026-09-15", 0); got != "2026-08-01" {
		t.Fatalf("post-restart KV pin = %q, want anchored 2026-08-01", got)
	}
	if fakeKV.setNXCalls != 1 || fakeKV.casCalls != 0 {
		t.Fatalf("subsequent request must perform zero KV writes: setNX=%d cas=%d, want 1/0", fakeKV.setNXCalls, fakeKV.casCalls)
	}

	// Invalid input date is never written to KV.
	keyInvalid, _, _ := BeginClaudeDiagnostics("credential-a", "session-invalid-date")
	if got := PinClaudeSessionDate(keyInvalid, "session-invalid-date", "not-a-valid-date", 0); got != "not-a-valid-date" {
		t.Fatalf("invalid date pin = %q, want unchanged input", got)
	}
	if fakeKV.setNXCalls != 1 || fakeKV.casCalls != 0 {
		t.Fatalf("invalid date must not write to KV: setNX=%d cas=%d, want 1/0", fakeKV.setNXCalls, fakeKV.casCalls)
	}
}

func TestPinClaudeSessionDateKVConcurrentCorruptedRepairUsesCAS(t *testing.T) {
	resetClaudeDiagnosticsForTest()
	defer resetClaudeDiagnosticsForTest()

	fakeKV := &fakeClaudeSessionDatePinKVClient{values: make(map[string][]byte)}
	prevClientFunc := currentClaudeSessionDatePinKVClient
	currentClaudeSessionDatePinKVClient = func() (claudeSessionDatePinKVClient, bool, error) {
		return fakeKV, true, nil
	}
	defer func() {
		currentClaudeSessionDatePinKVClient = prevClientFunc
	}()

	wantKVKey := "cpa:claude:session-date-pin:" + ClaudeSessionDatePinKey("session-cas")
	fakeKV.values[wantKVKey] = []byte("corrupted-value")

	key1, _, _ := BeginClaudeDiagnostics("cred-1", "session-cas")
	key2, _, _ := BeginClaudeDiagnostics("cred-2", "session-cas")

	// Synchronize the first two KVGet calls so both callers deterministically
	// observe the same corrupted bytes before either executes KVCompareAndSwap.
	var getBarrier sync.WaitGroup
	getBarrier.Add(2)
	var barrierOnce sync.Mutex
	barrierCount := 0
	fakeKV.onKVGet = func() {
		barrierOnce.Lock()
		barrierCount++
		waitForBoth := barrierCount <= 2
		barrierOnce.Unlock()
		if waitForBoth {
			getBarrier.Done()
			getBarrier.Wait()
		}
	}

	var (
		wg   sync.WaitGroup
		got1 string
		got2 string
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		got1 = PinClaudeSessionDate(key1, "session-cas", "2026-08-01", 0)
	}()
	go func() {
		defer wg.Done()
		got2 = PinClaudeSessionDate(key2, "session-cas", "2026-08-02", 0)
	}()
	wg.Wait()
	fakeKV.onKVGet = nil

	if got1 != got2 {
		t.Fatalf("concurrent CAS repair returned divergent dates: %q vs %q", got1, got2)
	}
	if got1 != "2026-08-01" && got1 != "2026-08-02" {
		t.Fatalf("concurrent CAS repair date = %q, want 2026-08-01 or 2026-08-02", got1)
	}
	if string(fakeKV.values[wantKVKey]) != got1 {
		t.Fatalf("KV value after concurrent repair = %q, want %q", string(fakeKV.values[wantKVKey]), got1)
	}
	if fakeKV.casCalls != 2 {
		t.Fatalf("CAS repair calls: cas=%d, want 2", fakeKV.casCalls)
	}
	if fakeKV.lastCASTTL != 0 {
		t.Fatalf("KVCompareAndSwap TTL = %v, want 0 (permanent)", fakeKV.lastCASTTL)
	}

	// When KVCompareAndSwap returns ErrCompareAndSwapUnsupported, concurrent callers
	// must still converge on a single deterministic date via the mutex-protected L1
	// fallback without performing an unconditional KV overwrite race.
	resetClaudeDiagnosticsForTest()
	fakeKV.values[wantKVKey] = []byte("corrupted-again")
	fakeKV.casErr = homekv.ErrCompareAndSwapUnsupported
	key3, _, _ := BeginClaudeDiagnostics("cred-3", "session-cas")
	key4, _, _ := BeginClaudeDiagnostics("cred-4", "session-cas")

	var unsupportedBarrier sync.WaitGroup
	unsupportedBarrier.Add(2)
	unsupportedCount := 0
	fakeKV.onKVGet = func() {
		barrierOnce.Lock()
		unsupportedCount++
		waitForBoth := unsupportedCount <= 2
		barrierOnce.Unlock()
		if waitForBoth {
			unsupportedBarrier.Done()
			unsupportedBarrier.Wait()
		}
	}

	var (
		wgUnsupported sync.WaitGroup
		got3          string
		got4          string
	)
	wgUnsupported.Add(2)
	go func() {
		defer wgUnsupported.Done()
		got3 = PinClaudeSessionDate(key3, "session-cas", "2026-08-05", 0)
	}()
	go func() {
		defer wgUnsupported.Done()
		got4 = PinClaudeSessionDate(key4, "session-cas", "2026-08-06", 0)
	}()
	wgUnsupported.Wait()
	fakeKV.onKVGet = nil

	if got3 != got4 {
		t.Fatalf("concurrent CAS-unsupported fallback returned divergent dates: %q vs %q", got3, got4)
	}
	if got3 != "2026-08-05" && got3 != "2026-08-06" {
		t.Fatalf("concurrent CAS-unsupported fallback date = %q, want 2026-08-05 or 2026-08-06", got3)
	}
	if got := PinClaudeSessionDate(key3, "session-cas", "2026-08-07", 0); got != got3 {
		t.Fatalf("subsequent call after CAS-unsupported fallback = %q, want %q", got, got3)
	}
}

func TestPinClaudeSessionDateKVTransientFailureAndRecovery(t *testing.T) {
	resetClaudeDiagnosticsForTest()
	defer resetClaudeDiagnosticsForTest()

	fakeKV := &fakeClaudeSessionDatePinKVClient{values: make(map[string][]byte)}
	var clientErr error
	prevClientFunc := currentClaudeSessionDatePinKVClient
	currentClaudeSessionDatePinKVClient = func() (claudeSessionDatePinKVClient, bool, error) {
		if clientErr != nil {
			return nil, true, clientErr
		}
		return fakeKV, true, nil
	}
	defer func() {
		currentClaudeSessionDatePinKVClient = prevClientFunc
	}()

	// Part 1: KV is unavailable on initial anchor and across midnight, then recovers.
	clientErr = errors.New("home kv transiently unavailable")
	if !IsClaudeSessionDatePinKVActive() {
		t.Fatal("IsClaudeSessionDatePinKVActive() during transient KV error = false, want true")
	}

	keyA, _, _ := BeginClaudeDiagnostics("cred-a", "session-transient-1")
	if got := PinClaudeSessionDate(keyA, "session-transient-1", "2026-08-01", 0); got != "2026-08-01" {
		t.Fatalf("initial pin during KV outage = %q, want 2026-08-01", got)
	}
	if got := LookupClaudeSessionDatePin("session-transient-1", 0); got != "2026-08-01" {
		t.Fatalf("LookupClaudeSessionDatePin during KV outage (retention=0) = %q, want 2026-08-01", got)
	}

	// Across midnight with a credential switch while KV is still down (with operation-level error).
	clientErr = nil
	fakeKV.err = errors.New("kv network timeout")
	keyB, _, _ := BeginClaudeDiagnostics("cred-b", "session-transient-1")
	if got := LookupClaudeSessionDatePin("session-transient-1", 0); got != "2026-08-01" {
		t.Fatalf("LookupClaudeSessionDatePin across midnight during KV timeout = %q, want 2026-08-01", got)
	}
	if got := PinClaudeSessionDate(keyB, "session-transient-1", "2026-08-02", 0); got != "2026-08-01" {
		t.Fatalf("PinClaudeSessionDate across midnight during KV timeout = %q, want 2026-08-01", got)
	}

	// KV recovers on day 3: PinClaudeSessionDate backfills the original 2026-08-01 anchor into KV.
	fakeKV.err = nil
	if got := PinClaudeSessionDate(keyB, "session-transient-1", "2026-08-03", 0); got != "2026-08-01" {
		t.Fatalf("PinClaudeSessionDate after KV recovery = %q, want original anchor 2026-08-01", got)
	}
	wantKVKey1 := "cpa:claude:session-date-pin:" + ClaudeSessionDatePinKey("session-transient-1")
	if string(fakeKV.values[wantKVKey1]) != "2026-08-01" {
		t.Fatalf("KV backfilled date = %q, want 2026-08-01", string(fakeKV.values[wantKVKey1]))
	}

	// Part 2: Mid-session transient KV outage after L1 mirror is populated from KV.
	keyC, _, _ := BeginClaudeDiagnostics("cred-c", "session-transient-2")
	if got := PinClaudeSessionDate(keyC, "session-transient-2", "2026-08-10", 0); got != "2026-08-10" {
		t.Fatalf("initial healthy KV pin = %q, want 2026-08-10", got)
	}
	// Simulate restart, then populate L1 via Lookup while KV is healthy.
	resetClaudeDiagnosticsForTest()
	if got := LookupClaudeSessionDatePin("session-transient-2", 0); got != "2026-08-10" {
		t.Fatalf("LookupClaudeSessionDatePin after restart = %q, want 2026-08-10", got)
	}
	// KV goes down mid-session across midnight with retention = 0.
	fakeKV.err = errors.New("mid-session kv error")
	keyD, _, _ := BeginClaudeDiagnostics("cred-d", "session-transient-2")
	if got := LookupClaudeSessionDatePin("session-transient-2", 0); got != "2026-08-10" {
		t.Fatalf("LookupClaudeSessionDatePin during mid-session outage = %q, want 2026-08-10", got)
	}
	if got := PinClaudeSessionDate(keyD, "session-transient-2", "2026-08-11", 0); got != "2026-08-10" {
		t.Fatalf("PinClaudeSessionDate during mid-session outage = %q, want 2026-08-10", got)
	}
}
