package auth

import (
	"fmt"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestResolveClaudeRequestIdentityPreservesValidIdentity(t *testing.T) {
	t.Parallel()

	existingUserID := claudeUserIDForSession("12345678-1234-1234-1234-123456789abc")
	payload := []byte(fmt.Sprintf(`{"metadata":{"trace":"keep","user_id":%q},"messages":[{"role":"user","content":"hello"}]}`, existingUserID))

	normalizedPayload, identity, errResolve := ResolveClaudeRequestIdentity(payload)
	if errResolve != nil {
		t.Fatalf("ResolveClaudeRequestIdentity() error = %v", errResolve)
	}
	if identity.UserID != existingUserID {
		t.Fatalf("identity.UserID = %q, want %q", identity.UserID, existingUserID)
	}
	if gjson.GetBytes(normalizedPayload, "metadata.trace").String() != "keep" {
		t.Fatalf("unrelated metadata was not preserved: %s", normalizedPayload)
	}
	if string(normalizedPayload) != string(payload) {
		t.Fatalf("valid payload changed: got %s, want %s", normalizedPayload, payload)
	}
}

func TestResolveClaudeRequestIdentityDerivesStableIdentityFromFirstUserMessage(t *testing.T) {
	t.Parallel()

	payloads := [][]byte{
		[]byte(`{"model":"claude-sonnet","messages":[{"role":"user","content":"你好，分析这个问题"}]}`),
		[]byte(`{"model":"claude-haiku","system":"different","messages":[{"role":"user","content":[{"type":"text","text":"你好，分析这个问题"}]},{"role":"assistant","content":"later"}]}`),
	}

	var firstIdentity ClaudeRequestIdentity
	for index, payload := range payloads {
		normalizedPayload, identity, errResolve := ResolveClaudeRequestIdentity(payload)
		if errResolve != nil {
			t.Fatalf("ResolveClaudeRequestIdentity() #%d error = %v", index, errResolve)
		}
		if !identity.Deterministic {
			t.Fatalf("identity #%d should be deterministic", index)
		}
		if !IsValidClaudeUserID(identity.UserID) {
			t.Fatalf("identity #%d is invalid: %q", index, identity.UserID)
		}
		if gjson.GetBytes(normalizedPayload, "metadata.user_id").String() != identity.UserID {
			t.Fatalf("payload #%d user_id does not match identity", index)
		}
		if index == 0 {
			firstIdentity = identity
			continue
		}
		if identity.UserID != firstIdentity.UserID {
			t.Fatalf("equivalent first messages produced different user IDs: %q and %q", firstIdentity.UserID, identity.UserID)
		}
	}
}

func TestResolveClaudeRequestIdentityUsesEarliestUserQueryAcrossBootstrapChanges(t *testing.T) {
	t.Parallel()

	baselinePayload := []byte(`{"messages":[{"role":"user","content":"<user_info>stable</user_info><ide_state>state-a</ide_state>"},{"role":"assistant","content":"ready"},{"role":"user","content":"<user_query>\n分析这个问题\n</user_query>"},{"role":"user","content":"<user_query>later question</user_query>"}]}`)
	changedBootstrapPayload := []byte(`{"messages":[{"role":"user","content":"<user_info>stable</user_info><ide_state>state-b</ide_state>"},{"role":"assistant","content":"ready"},{"role":"user","content":[{"type":"text","text":"<user_query>\n分析这个问题\n</user_query>"}]},{"role":"user","content":"<user_query>later question</user_query>"}]}`)
	changedQueryPayload := []byte(`{"messages":[{"role":"user","content":"<user_info>stable</user_info><ide_state>state-a</ide_state>"},{"role":"assistant","content":"ready"},{"role":"user","content":"<user_query>\n分析另一个问题\n</user_query>"},{"role":"user","content":"<user_query>later question</user_query>"}]}`)

	_, baselineIdentity, errBaseline := ResolveClaudeRequestIdentity(baselinePayload)
	if errBaseline != nil {
		t.Fatalf("baseline ResolveClaudeRequestIdentity() error = %v", errBaseline)
	}
	_, changedBootstrapIdentity, errChangedBootstrap := ResolveClaudeRequestIdentity(changedBootstrapPayload)
	if errChangedBootstrap != nil {
		t.Fatalf("changed-bootstrap ResolveClaudeRequestIdentity() error = %v", errChangedBootstrap)
	}
	_, changedQueryIdentity, errChangedQuery := ResolveClaudeRequestIdentity(changedQueryPayload)
	if errChangedQuery != nil {
		t.Fatalf("changed-query ResolveClaudeRequestIdentity() error = %v", errChangedQuery)
	}

	if !baselineIdentity.Deterministic || !changedBootstrapIdentity.Deterministic || !changedQueryIdentity.Deterministic {
		t.Fatal("user-query requests should produce deterministic identities")
	}
	if baselineIdentity.UserID != changedBootstrapIdentity.UserID {
		t.Fatalf("bootstrap-only changes produced different user IDs: %q and %q", baselineIdentity.UserID, changedBootstrapIdentity.UserID)
	}
	if baselineIdentity.UserID == changedQueryIdentity.UserID {
		t.Fatalf("different user queries produced the same user ID: %q", baselineIdentity.UserID)
	}
}

func TestResolveClaudeRequestIdentityPreservesMultimodalContentWithUserQuery(t *testing.T) {
	t.Parallel()

	baselinePayload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"<ide_state>state-a</ide_state>"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aW1hZ2UtYQ=="}},{"type":"text","text":"<user_query>explain the image</user_query>"}]}]}`)
	changedAmbientTextPayload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"<ide_state>state-b</ide_state>"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aW1hZ2UtYQ=="}},{"type":"text","text":"<user_query>explain the image</user_query>"}]}]}`)
	changedImagePayload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"<ide_state>state-a</ide_state>"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aW1hZ2UtYg=="}},{"type":"text","text":"<user_query>explain the image</user_query>"}]}]}`)

	_, baselineIdentity, errBaseline := ResolveClaudeRequestIdentity(baselinePayload)
	if errBaseline != nil {
		t.Fatalf("baseline ResolveClaudeRequestIdentity() error = %v", errBaseline)
	}
	_, changedAmbientTextIdentity, errChangedAmbient := ResolveClaudeRequestIdentity(changedAmbientTextPayload)
	if errChangedAmbient != nil {
		t.Fatalf("changed-ambient ResolveClaudeRequestIdentity() error = %v", errChangedAmbient)
	}
	_, changedImageIdentity, errChangedImage := ResolveClaudeRequestIdentity(changedImagePayload)
	if errChangedImage != nil {
		t.Fatalf("changed-image ResolveClaudeRequestIdentity() error = %v", errChangedImage)
	}

	if baselineIdentity.UserID != changedAmbientTextIdentity.UserID {
		t.Fatalf("sibling ambient text changed the user ID: %q and %q", baselineIdentity.UserID, changedAmbientTextIdentity.UserID)
	}
	if baselineIdentity.UserID == changedImageIdentity.UserID {
		t.Fatalf("different image content produced the same user ID: %q", baselineIdentity.UserID)
	}
}

func TestResolveClaudeRequestIdentityFallsBackForInvalidUserQueryMarkup(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name        string
		markup      string
		changedText string
	}{
		{
			name:        "unclosed",
			markup:      "<user_query><ide_state>state-a</ide_state>question",
			changedText: "<user_query><ide_state>state-b</ide_state>question",
		},
		{
			name:        "repeated",
			markup:      "<user_query><ide_state>state-a</ide_state>one</user_query><user_query>two</user_query>",
			changedText: "<user_query><ide_state>state-b</ide_state>one</user_query><user_query>two</user_query>",
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			payload := []byte(fmt.Sprintf(`{"messages":[{"role":"user","content":%q},{"role":"user","content":"<user_query>later question</user_query>"}]}`, testCase.markup))
			changedPayload := []byte(fmt.Sprintf(`{"messages":[{"role":"user","content":%q},{"role":"user","content":"<user_query>later question</user_query>"}]}`, testCase.changedText))

			_, identity, errResolve := ResolveClaudeRequestIdentity(payload)
			if errResolve != nil {
				t.Fatalf("ResolveClaudeRequestIdentity() error = %v", errResolve)
			}
			_, repeatedIdentity, errRepeated := ResolveClaudeRequestIdentity(payload)
			if errRepeated != nil {
				t.Fatalf("repeated ResolveClaudeRequestIdentity() error = %v", errRepeated)
			}
			_, changedIdentity, errChanged := ResolveClaudeRequestIdentity(changedPayload)
			if errChanged != nil {
				t.Fatalf("changed ResolveClaudeRequestIdentity() error = %v", errChanged)
			}

			if !identity.Deterministic || !changedIdentity.Deterministic {
				t.Fatal("invalid markup should fall back to deterministic full-message hashing")
			}
			if identity.UserID != repeatedIdentity.UserID {
				t.Fatalf("fallback identity was not stable: %q and %q", identity.UserID, repeatedIdentity.UserID)
			}
			if identity.UserID == changedIdentity.UserID {
				t.Fatalf("invalid markup was incorrectly extracted from different full messages: %q", identity.UserID)
			}
		})
	}
}

func TestResolveClaudeRequestIdentityFallsBackForAmbiguousUserQueryParts(t *testing.T) {
	t.Parallel()

	firstPayload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"<user_query>first</user_query>"},{"type":"text","text":"<user_query>second</user_query>"}]},{"role":"user","content":"<user_query>later question</user_query>"}]}`)
	secondPayload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"<user_query>first</user_query>"},{"type":"text","text":"<user_query>changed second</user_query>"}]},{"role":"user","content":"<user_query>later question</user_query>"}]}`)

	_, firstIdentity, errFirst := ResolveClaudeRequestIdentity(firstPayload)
	if errFirst != nil {
		t.Fatalf("first ResolveClaudeRequestIdentity() error = %v", errFirst)
	}
	_, secondIdentity, errSecond := ResolveClaudeRequestIdentity(secondPayload)
	if errSecond != nil {
		t.Fatalf("second ResolveClaudeRequestIdentity() error = %v", errSecond)
	}
	if firstIdentity.UserID == secondIdentity.UserID {
		t.Fatalf("ambiguous query parts did not fall back to full-message hashing: %q", firstIdentity.UserID)
	}
}

func TestResolveClaudeRequestIdentityUsesDifferentTypedPartBoundaries(t *testing.T) {
	t.Parallel()

	firstPayload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"ab"},{"type":"text","text":"c"}]}]}`)
	secondPayload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"a"},{"type":"text","text":"bc"}]}]}`)

	_, firstIdentity, errFirst := ResolveClaudeRequestIdentity(firstPayload)
	if errFirst != nil {
		t.Fatalf("first ResolveClaudeRequestIdentity() error = %v", errFirst)
	}
	_, secondIdentity, errSecond := ResolveClaudeRequestIdentity(secondPayload)
	if errSecond != nil {
		t.Fatalf("second ResolveClaudeRequestIdentity() error = %v", errSecond)
	}
	if firstIdentity.UserID == secondIdentity.UserID {
		t.Fatalf("different typed part boundaries produced the same user ID: %q", firstIdentity.UserID)
	}
}

func TestResolveClaudeRequestIdentitySkipsPureToolResult(t *testing.T) {
	t.Parallel()

	withToolResult := []byte(`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-1","content":"output"}]},{"role":"assistant","content":"continue"},{"role":"user","content":"actual user question"}]}`)
	directQuestion := []byte(`{"messages":[{"role":"user","content":"actual user question"}]}`)

	_, skippedIdentity, errSkipped := ResolveClaudeRequestIdentity(withToolResult)
	if errSkipped != nil {
		t.Fatalf("tool-result ResolveClaudeRequestIdentity() error = %v", errSkipped)
	}
	_, directIdentity, errDirect := ResolveClaudeRequestIdentity(directQuestion)
	if errDirect != nil {
		t.Fatalf("direct ResolveClaudeRequestIdentity() error = %v", errDirect)
	}
	if skippedIdentity.UserID != directIdentity.UserID {
		t.Fatalf("pure tool_result was not skipped: %q != %q", skippedIdentity.UserID, directIdentity.UserID)
	}
}

func TestResolveClaudeRequestIdentityUsesRandomFallbackWithoutUserAuthoredMessage(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-1","content":"output"}]}]}`)
	_, firstIdentity, errFirst := ResolveClaudeRequestIdentity(payload)
	if errFirst != nil {
		t.Fatalf("first ResolveClaudeRequestIdentity() error = %v", errFirst)
	}
	_, secondIdentity, errSecond := ResolveClaudeRequestIdentity(payload)
	if errSecond != nil {
		t.Fatalf("second ResolveClaudeRequestIdentity() error = %v", errSecond)
	}
	if firstIdentity.Deterministic || secondIdentity.Deterministic {
		t.Fatal("tool-result-only requests should use random identities")
	}
	if firstIdentity.UserID == secondIdentity.UserID {
		t.Fatalf("random fallback reused user ID %q", firstIdentity.UserID)
	}
}

func TestResolveClaudeRequestIdentityNormalizesBase64Whitespace(t *testing.T) {
	t.Parallel()

	firstPayload := []byte(`{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}}]}]}`)
	secondPayload := []byte(`{"messages":[{"role":"user","content":[{"source":{"data":"aGVs\nbG8=","media_type":"image/png","type":"base64"},"type":"image"}]}]}`)

	_, firstIdentity, errFirst := ResolveClaudeRequestIdentity(firstPayload)
	if errFirst != nil {
		t.Fatalf("first ResolveClaudeRequestIdentity() error = %v", errFirst)
	}
	_, secondIdentity, errSecond := ResolveClaudeRequestIdentity(secondPayload)
	if errSecond != nil {
		t.Fatalf("second ResolveClaudeRequestIdentity() error = %v", errSecond)
	}
	if firstIdentity.UserID != secondIdentity.UserID {
		t.Fatalf("equivalent base64 payloads produced different IDs: %q and %q", firstIdentity.UserID, secondIdentity.UserID)
	}
}

func TestParseClaudeUserIDRejectsLegacyLooseFormat(t *testing.T) {
	t.Parallel()

	legacyUserID := "user_" + strings.Repeat("1", 64) + "_account__session_12345678-1234-1234-1234-123456789abc"
	if _, valid := ParseClaudeUserID(legacyUserID); valid {
		t.Fatalf("ParseClaudeUserID() accepted legacy loose format %q", legacyUserID)
	}
}
