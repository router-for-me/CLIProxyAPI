package helps

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
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
