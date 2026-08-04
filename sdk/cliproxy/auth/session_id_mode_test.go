package auth

import (
	"fmt"
	"testing"
)

func claudeCodeSessionPayload(sessionID, firstUserMsg string) []byte {
	return []byte(fmt.Sprintf(`{
		"metadata":{"user_id":"{\"device_id\":\"dev-1\",\"account_uuid\":\"acc-1\",\"session_id\":\"%s\"}"},
		"system":[{"type":"text","text":"You are a helpful subagent."}],
		"messages":[{"role":"user","content":"%s"}]
	}`, sessionID, firstUserMsg))
}

func TestExtractSessionIDs_ContentHashModeDistinguishesSubagents(t *testing.T) {
	t.Parallel()

	const sid = "11111111-2222-3333-4444-555555555555"
	payloadA := claudeCodeSessionPayload(sid, "explore the auth package")
	payloadB := claudeCodeSessionPayload(sid, "patch the conductor retry loop")

	idA, _ := extractSessionIDsWithMode(nil, payloadA, nil, SessionIDModeContentHash)
	idB, _ := extractSessionIDsWithMode(nil, payloadB, nil, SessionIDModeContentHash)
	if idA == "" || idB == "" {
		t.Fatalf("expected non-empty IDs, got %q and %q", idA, idB)
	}
	if idA == idB {
		t.Fatalf("content-hash mode: distinct prompts under one session_id must yield distinct IDs, both = %q", idA)
	}

	// Same content must remain stable across calls.
	idA2, _ := extractSessionIDsWithMode(nil, claudeCodeSessionPayload(sid, "explore the auth package"), nil, SessionIDModeContentHash)
	if idA != idA2 {
		t.Fatalf("content-hash mode: identical payloads produced different IDs: %q vs %q", idA, idA2)
	}

	// Default mode keeps legacy behavior: session_id alone, so both collapse.
	defA, _ := extractSessionIDsWithMode(nil, payloadA, nil, SessionIDModeDefault)
	defB, _ := extractSessionIDsWithMode(nil, payloadB, nil, SessionIDModeDefault)
	if defA != "claude:"+sid || defA != defB {
		t.Fatalf("default mode changed: got %q and %q, want both %q", defA, defB, "claude:"+sid)
	}
}

func TestExtractSessionIDs_ContentHashModeFullContentNotPrefix(t *testing.T) {
	t.Parallel()

	// Subagent prompts share a long identical prefix (system-reminder and
	// project-context blocks) and differ only near the end; a truncated prefix
	// hash would collapse them onto one binding.
	const sid = "22222222-3333-4444-5555-666666666666"
	sharedPrefix := ""
	for i := 0; i < 40; i++ {
		sharedPrefix += "shared context block line providing project instructions. "
	}
	payloadA := claudeCodeSessionPayload(sid, sharedPrefix+"task: explore the auth package")
	payloadB := claudeCodeSessionPayload(sid, sharedPrefix+"task: patch the retry loop")

	idA, _ := extractSessionIDsWithMode(nil, payloadA, nil, SessionIDModeContentHash)
	idB, _ := extractSessionIDsWithMode(nil, payloadB, nil, SessionIDModeContentHash)
	if idA == idB {
		t.Fatalf("content-hash mode hashed only a prefix: prompts differing after a long shared prefix collapsed to %q", idA)
	}
}

func TestExtractSessionIDs_ContentHashModeFallbackInherit(t *testing.T) {
	t.Parallel()

	const sid = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	firstTurn := claudeCodeSessionPayload(sid, "kick off the task")
	secondTurn := []byte(fmt.Sprintf(`{
		"metadata":{"user_id":"{\"session_id\":\"%s\"}"},
		"system":[{"type":"text","text":"You are a helpful subagent."}],
		"messages":[
			{"role":"user","content":"kick off the task"},
			{"role":"assistant","content":"done with step one"}
		]
	}`, sid))

	firstPrimary, firstFallback := extractSessionIDsWithMode(nil, firstTurn, nil, SessionIDModeContentHash)
	if firstFallback != "" {
		t.Fatalf("first turn should have no fallback ID, got %q", firstFallback)
	}
	secondPrimary, secondFallback := extractSessionIDsWithMode(nil, secondTurn, nil, SessionIDModeContentHash)
	if secondPrimary == firstPrimary {
		t.Fatalf("second turn primary should include assistant hash and differ from first turn")
	}
	if secondFallback != firstPrimary {
		t.Fatalf("second turn fallback %q must match first turn primary %q to inherit the binding", secondFallback, firstPrimary)
	}
}

func TestExtractSessionIDs_ContentHashModeHeaderSession(t *testing.T) {
	t.Parallel()

	// The explicit Claude Code session header takes the same content-hash
	// treatment as the metadata session.
	payloadA := claudeCodeSessionPayload("ignored", "explore the auth package")
	headers := make(map[string][]string)
	headers["X-Claude-Code-Session-Id"] = []string{"33333333-4444-5555-6666-777777777777"}

	idHeader, _ := extractSessionIDsWithMode(headers, payloadA, nil, SessionIDModeContentHash)
	idDefault, _ := extractSessionIDsWithMode(headers, payloadA, nil, SessionIDModeDefault)
	if idHeader == idDefault {
		t.Fatalf("content-hash mode should fold message hash into header-derived session ID: %q", idHeader)
	}
}
