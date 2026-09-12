package helps

import (
	"fmt"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	"github.com/tidwall/gjson"
)

var benchmarkSanitizeCodexInputItemIDsOutput []byte

func TestSanitizeCodexInputItemIDsBoundaries(t *testing.T) {
	id64 := strings.Repeat("a", 64)
	id65 := strings.Repeat("b", 65)
	unicode65 := strings.Repeat("界", 65)
	body := []byte(`{"input":[{"id":"` + id64 + `"},{"id":"` + id65 + `"},{"id":"` + unicode65 + `"}]}`)

	got := SanitizeCodexInputItemIDs(body)

	if actual := gjson.GetBytes(got, "input.0.id").String(); actual != id64 {
		t.Fatalf("64-character ID changed: %q", actual)
	}
	for _, path := range []string{"input.1.id", "input.2.id"} {
		actual := gjson.GetBytes(got, path).String()
		if len([]rune(actual)) != 64 {
			t.Fatalf("%s length = %d, want 64: %q", path, len([]rune(actual)), actual)
		}
	}
}

func TestSanitizeCodexInputItemIDsShortensPairedCallIDs(t *testing.T) {
	callID64 := strings.Repeat("a", 64)
	callID65 := strings.Repeat("b", 65)
	callID86 := strings.Repeat("c", 86)
	callID87 := strings.Repeat("界", 87)
	body := []byte(`{"input":[` +
		`{"type":"function_call","call_id":"` + callID64 + `","name":"read","arguments":"{}"},` +
		`{"type":"function_call_output","call_id":"` + callID64 + `","output":"done"},` +
		`{"type":"function_call","call_id":"` + callID65 + `","name":"read","arguments":"{}"},` +
		`{"type":"function_call_output","call_id":"` + callID65 + `","output":"done"},` +
		`{"type":"custom_tool_call","call_id":"` + callID86 + `","name":"lookup","input":"{}"},` +
		`{"type":"custom_tool_call_output","call_id":"` + callID86 + `","output":"done"},` +
		`{"type":"function_call","call_id":"` + callID87 + `","name":"read","arguments":"{}"},` +
		`{"type":"function_call_output","call_id":"` + callID87 + `","output":"done"}` +
		`]}`)

	first := SanitizeCodexInputItemIDs(body)
	second := SanitizeCodexInputItemIDs(body)
	normalizedAgain := SanitizeCodexInputItemIDs(first)

	if actual := gjson.GetBytes(first, "input.0.call_id").String(); actual != callID64 {
		t.Fatalf("64-character call_id changed: %q", actual)
	}
	for _, pair := range [][2]int{{2, 3}, {4, 5}, {6, 7}} {
		callID := gjson.GetBytes(first, fmt.Sprintf("input.%d.call_id", pair[0])).String()
		outputCallID := gjson.GetBytes(first, fmt.Sprintf("input.%d.call_id", pair[1])).String()
		if callID != outputCallID {
			t.Fatalf("paired call IDs differ at input.%d and input.%d: %q != %q", pair[0], pair[1], callID, outputCallID)
		}
		if len([]rune(callID)) != codexCallIDLimit {
			t.Fatalf("input.%d.call_id length = %d, want %d: %q", pair[0], len([]rune(callID)), codexCallIDLimit, callID)
		}
	}
	if string(first) != string(second) {
		t.Fatalf("call_id shortening is not deterministic: first=%s second=%s", first, second)
	}
	if string(first) != string(normalizedAgain) {
		t.Fatalf("call_id shortening is not idempotent: first=%s normalized_again=%s", first, normalizedAgain)
	}
}

func TestSanitizeCodexInputItemIDsAvoidsExistingCallIDCollision(t *testing.T) {
	longCallID := strings.Repeat("cursor-call-", 8)
	collidingValidCallID := shortenCodexCallIDWithAttempt(longCallID, 0)
	body := []byte(`{"input":[` +
		`{"type":"function_call","call_id":"` + longCallID + `","name":"read","arguments":"{}"},` +
		`{"type":"function_call_output","call_id":"` + longCallID + `","output":"done"},` +
		`{"type":"function_call","call_id":"` + collidingValidCallID + `","name":"other","arguments":"{}"}` +
		`]}`)

	got := SanitizeCodexInputItemIDs(body)
	shortened := gjson.GetBytes(got, "input.0.call_id").String()
	if shortened == collidingValidCallID {
		t.Fatalf("shortened call_id collided with existing valid call_id: %q", shortened)
	}
	if paired := gjson.GetBytes(got, "input.1.call_id").String(); paired != shortened {
		t.Fatalf("paired call_id = %q, want %q", paired, shortened)
	}
	if actual := gjson.GetBytes(got, "input.2.call_id").String(); actual != collidingValidCallID {
		t.Fatalf("existing valid call_id changed: %q", actual)
	}
}

func TestSanitizeCodexInputItemIDsReusesMatchingPreShortenedCallID(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		callType   string
		outputType string
	}{
		{name: "function tool", callType: "function_call", outputType: "function_call_output"},
		{name: "custom tool", callType: "custom_tool_call", outputType: "custom_tool_call_output"},
	} {
		for _, order := range []struct {
			name       string
			firstType  string
			secondType string
			firstLong  bool
		}{
			{name: "long call first", firstType: testCase.callType, secondType: testCase.outputType, firstLong: true},
			{name: "short output first", firstType: testCase.outputType, secondType: testCase.callType, firstLong: false},
		} {
			t.Run(testCase.name+"/"+order.name, func(t *testing.T) {
				longCallID := strings.Repeat("cursor-call-", 8)
				shortCallID := shortenCodexCallIDWithAttempt(longCallID, 0)
				firstCallID, secondCallID := shortCallID, longCallID
				if order.firstLong {
					firstCallID, secondCallID = longCallID, shortCallID
				}
				body := []byte(fmt.Sprintf(`{"input":[{"type":%q,"call_id":%q},{"type":%q,"call_id":%q}]}`, order.firstType, firstCallID, order.secondType, secondCallID))

				first := SanitizeCodexInputItemIDs(body)
				normalizedAgain := SanitizeCodexInputItemIDs(first)
				for index := range 2 {
					if actual := gjson.GetBytes(first, fmt.Sprintf("input.%d.call_id", index)).String(); actual != shortCallID {
						t.Fatalf("input.%d.call_id = %q, want matching pre-shortened ID %q; payload=%s", index, actual, shortCallID, first)
					}
				}
				if string(first) != string(normalizedAgain) {
					t.Fatalf("pre-shortened pair normalization is not idempotent: first=%s normalized_again=%s", first, normalizedAgain)
				}
			})
		}
	}
}

func TestSanitizeCodexInputItemIDsReusesMatchingLaterAttemptCallID(t *testing.T) {
	longCallID := strings.Repeat("cursor-call-", 8)
	firstAttempt := shortenCodexCallIDWithAttempt(longCallID, 0)
	matchingLaterAttempt := shortenCodexCallIDWithAttempt(longCallID, 1)
	body := []byte(`{"input":[` +
		`{"type":"function_call","call_id":"` + firstAttempt + `","name":"other","arguments":"{}"},` +
		`{"type":"function_call","call_id":"` + longCallID + `","name":"lookup","arguments":"{}"},` +
		`{"type":"function_call_output","call_id":"` + matchingLaterAttempt + `","output":"done"}` +
		`]}`)

	got := SanitizeCodexInputItemIDs(body)
	if actual := gjson.GetBytes(got, "input.0.call_id").String(); actual != firstAttempt {
		t.Fatalf("existing colliding call ID changed: %q", actual)
	}
	for _, path := range []string{"input.1.call_id", "input.2.call_id"} {
		if actual := gjson.GetBytes(got, path).String(); actual != matchingLaterAttempt {
			t.Fatalf("%s = %q, want matching later-attempt ID %q; payload=%s", path, actual, matchingLaterAttempt, got)
		}
	}
}

func TestSanitizeCodexInputItemIDsReusesClaudeSanitizedPreShortenedCallID(t *testing.T) {
	longCallID := "call.with/slashes/" + strings.Repeat("cursor-call-", 6)
	claudeCallID := util.SanitizeClaudeToolID(longCallID)
	claudeShortened := shortenCodexCallIDWithAttempt(claudeCallID, 0)
	if claudeCallID == longCallID || claudeShortened == shortenCodexCallIDWithAttempt(longCallID, 0) {
		t.Fatalf("invalid test setup: original=%q claude=%q shortened=%q", longCallID, claudeCallID, claudeShortened)
	}
	body := []byte(`{"input":[` +
		`{"type":"function_call","call_id":"` + longCallID + `","name":"lookup","arguments":"{}"},` +
		`{"type":"function_call_output","call_id":"` + claudeShortened + `","output":"done"}` +
		`]}`)

	got := SanitizeCodexInputItemIDs(body)
	for _, path := range []string{"input.0.call_id", "input.1.call_id"} {
		if actual := gjson.GetBytes(got, path).String(); actual != claudeShortened {
			t.Fatalf("%s = %q, want Claude-visible ID %q; payload=%s", path, actual, claudeShortened, got)
		}
	}
}

func TestSanitizeCodexInputItemIDsDoesNotReusePreShortenedIDAcrossToolKinds(t *testing.T) {
	longCallID := strings.Repeat("cursor-call-", 8)
	shortCallID := shortenCodexCallIDWithAttempt(longCallID, 0)
	body := []byte(`{"input":[` +
		`{"type":"function_call","call_id":"` + longCallID + `"},` +
		`{"type":"custom_tool_call_output","call_id":"` + shortCallID + `"}` +
		`]}`)

	got := SanitizeCodexInputItemIDs(body)
	if actual := gjson.GetBytes(got, "input.0.call_id").String(); actual == shortCallID {
		t.Fatalf("function call reused unrelated custom-tool output ID: %q", actual)
	}
	if actual := gjson.GetBytes(got, "input.1.call_id").String(); actual != shortCallID {
		t.Fatalf("existing custom-tool output ID changed: %q", actual)
	}
}

func TestSanitizeResponsesCallIDsChangesOnlyCallIDs(t *testing.T) {
	longCallID := strings.Repeat("cursor-call-", 8)
	longReasoningID := "reasoning_" + strings.Repeat("a", 64)
	body := []byte(`{"input":[` +
		`{"type":"message","id":"item_message","role":"user","content":"before"},` +
		`{"type":"reasoning","id":"` + longReasoningID + `","encrypted_content":"encrypted","summary":[]},` +
		`{"type":"function_call","id":"item_call","call_id":"` + longCallID + `","name":"lookup","arguments":"{}"},` +
		`{"type":"function_call_output","call_id":"` + longCallID + `","output":"done"}` +
		`]}`)

	got := SanitizeResponsesCallIDs(body)
	if actual := gjson.GetBytes(got, "input.0.id").String(); actual != "item_message" {
		t.Fatalf("message ID changed: %q", actual)
	}
	if actual := gjson.GetBytes(got, "input.1.id").String(); actual != longReasoningID {
		t.Fatalf("reasoning ID changed: %q", actual)
	}
	if actual := gjson.GetBytes(got, "input.1.encrypted_content").String(); actual != "encrypted" {
		t.Fatalf("reasoning item changed or was dropped: %q; payload=%s", actual, got)
	}
	if actual := gjson.GetBytes(got, "input.2.id").String(); actual != "item_call" {
		t.Fatalf("function-call item ID changed: %q", actual)
	}
	callID := gjson.GetBytes(got, "input.2.call_id").String()
	if callID == longCallID || len([]rune(callID)) != codexCallIDLimit {
		t.Fatalf("overlong call ID was not shortened: %q", callID)
	}
	if outputCallID := gjson.GetBytes(got, "input.3.call_id").String(); outputCallID != callID {
		t.Fatalf("paired call IDs differ: %q != %q", callID, outputCallID)
	}
}

func TestSanitizeCodexInputItemIDsShortensOnlyOverlongCallID(t *testing.T) {
	longCallID := strings.Repeat("call-", 20)
	body := []byte(`{"input":[{"type":"function_call","call_id":"` + longCallID + `","name":"read","arguments":"{}"}]}`)

	got := SanitizeCodexInputItemIDs(body)
	if actual := gjson.GetBytes(got, "input.0.call_id").String(); actual == longCallID || len([]rune(actual)) != codexCallIDLimit {
		t.Fatalf("overlong call_id was not shortened: %q", actual)
	}
}

func TestSanitizeCodexInputItemIDsLeavesValidCallIDsByteForByteUnchanged(t *testing.T) {
	body := []byte(`{ "model": "gpt-5", "input": [ { "type": "function_call", "call_id": "call_123", "name": "read", "arguments": "{}" } ] }`)

	got := SanitizeCodexInputItemIDs(body)
	if string(got) != string(body) {
		t.Fatalf("valid payload changed byte-for-byte: got=%q want=%q", got, body)
	}
}

func TestSanitizeCodexInputItemIDsNormalizesMessageIDs(t *testing.T) {
	const invalidID = "item_74ec40c883248ebb4885ec84"
	body := []byte(`{"input":[` +
		`{"type":"message","id":"` + invalidID + `","role":"user"},` +
		`{"type":"message","id":"msg-1","role":"assistant"},` +
		`{"type":"function_call","id":"item_call","call_id":"call-1"}` +
		`]}`)

	first := SanitizeCodexInputItemIDs(body)
	second := SanitizeCodexInputItemIDs(body)

	if got := gjson.GetBytes(first, "input.0.id").String(); got != "msg_"+invalidID {
		t.Fatalf("message ID = %q, want msg-prefixed ID", got)
	}
	if got := gjson.GetBytes(first, "input.1.id").String(); got != "msg-1" {
		t.Fatalf("valid message ID changed: %q", got)
	}
	if got := gjson.GetBytes(first, "input.2.id").String(); got != "fc_item_call" {
		t.Fatalf("function_call ID was not normalized: %q", got)
	}
	if string(first) != string(second) {
		t.Fatalf("message ID normalization is not deterministic: first=%s second=%s", first, second)
	}
}

func TestSanitizeCodexInputItemIDsNormalizesResponseItemIDs(t *testing.T) {
	const (
		messageID            = "item_message"
		reasoningID          = "item_reasoning"
		functionCallID       = "item_function_call"
		functionCallOutputID = "item_function_call_output"
	)
	body := []byte(`{"input":[` +
		`{"type":"message","id":"` + messageID + `"},` +
		`{"type":"reasoning","id":"` + reasoningID + `"},` +
		`{"type":"function_call","id":"` + functionCallID + `","call_id":"call-1"},` +
		`{"type":"function_call_output","id":"` + functionCallOutputID + `","call_id":"call-1"},` +
		`{"type":"reasoning","id":"rs-existing"},` +
		`{"type":"function_call","id":"fc-existing","call_id":"call-2"},` +
		`{"type":"message","id":"msg-existing"}` +
		`]}`)

	got := SanitizeCodexInputItemIDs(body)
	want := []string{
		"msg_" + messageID,
		"rs_" + reasoningID,
		"fc_" + functionCallID,
		functionCallOutputID,
		"rs-existing",
		"fc-existing",
		"msg-existing",
	}

	for index, expected := range want {
		path := fmt.Sprintf("input.%d.id", index)
		if actual := gjson.GetBytes(got, path).String(); actual != expected {
			t.Fatalf("%s = %q, want %q; payload=%s", path, actual, expected, got)
		}
	}

	if second := SanitizeCodexInputItemIDs(body); string(second) != string(got) {
		t.Fatalf("normalization is not deterministic: first=%s second=%s", got, second)
	}
}

func TestSanitizeCodexInputItemIDsAvoidsNormalizationCollisions(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		itemType string
		prefix   string
	}{
		{name: "message", itemType: "message", prefix: "msg_"},
		{name: "reasoning", itemType: "reasoning", prefix: "rs_"},
		{name: "function call", itemType: "function_call", prefix: "fc_"},
		{name: "custom tool call", itemType: "custom_tool_call", prefix: "ctc_"},
		{name: "custom tool call output", itemType: "custom_tool_call_output", prefix: "ctco_"},
	} {
		for _, idCase := range []struct {
			name      string
			invalidID string
		}{
			{name: "short", invalidID: "item_collision"},
			{name: "overlong", invalidID: strings.Repeat("x", codexInputItemIDLimit-len([]rune(testCase.prefix))+1)},
		} {
			prefixedID := testCase.prefix + idCase.invalidID
			for _, order := range []struct {
				name          string
				ids           [2]string
				prefixedIndex int
			}{
				{name: "local first", ids: [2]string{idCase.invalidID, prefixedID}, prefixedIndex: 1},
				{name: "prefixed first", ids: [2]string{prefixedID, idCase.invalidID}, prefixedIndex: 0},
			} {
				t.Run(testCase.name+"/"+idCase.name+"/"+order.name, func(t *testing.T) {
					body := []byte(fmt.Sprintf(`{"input":[{"type":%q,"id":%q},{"type":%q,"id":%q}]}`, testCase.itemType, order.ids[0], testCase.itemType, order.ids[1]))

					first := SanitizeCodexInputItemIDs(body)
					second := SanitizeCodexInputItemIDs(body)
					normalizedAgain := SanitizeCodexInputItemIDs(first)
					ids := [2]string{
						gjson.GetBytes(first, "input.0.id").String(),
						gjson.GetBytes(first, "input.1.id").String(),
					}

					if ids[0] == ids[1] {
						t.Fatalf("distinct IDs collided after normalization: %q; payload=%s", ids[0], first)
					}
					for index, id := range ids {
						if !strings.HasPrefix(id, testCase.prefix) {
							t.Fatalf("input.%d.id = %q, want prefix %q", index, id, testCase.prefix)
						}
						if len([]rune(id)) > codexInputItemIDLimit {
							t.Fatalf("input.%d.id length = %d, want at most %d: %q", index, len([]rune(id)), codexInputItemIDLimit, id)
						}
					}
					if len([]rune(prefixedID)) <= codexInputItemIDLimit && ids[order.prefixedIndex] != prefixedID {
						t.Fatalf("existing valid ID changed: got %q want %q", ids[order.prefixedIndex], prefixedID)
					}
					if string(first) != string(second) {
						t.Fatalf("collision resolution is not deterministic: first=%s second=%s", first, second)
					}
					if string(first) != string(normalizedAgain) {
						t.Fatalf("collision resolution is not idempotent: first=%s normalized_again=%s", first, normalizedAgain)
					}
				})
			}
		}
	}
}

func TestSanitizeCodexInputItemIDsNormalizesCustomToolCallIDs(t *testing.T) {
	const invalidID = "item_44e13caebc1ddf25f1337cbe"
	body := []byte(`{"input":[{"type":"custom_tool_call","id":"` + invalidID + `","call_id":"call-1","name":"lookup","input":"{}"}]}`)

	got := SanitizeCodexInputItemIDs(body)
	if actual := gjson.GetBytes(got, "input.0.id").String(); actual != "ctc_"+invalidID {
		t.Fatalf("custom_tool_call ID = %q, want ctc-prefixed ID", actual)
	}
}

func TestSanitizeCodexInputItemIDsNormalizesCustomToolCallOutputIDs(t *testing.T) {
	const (
		invalidID = "item_44e13caebc1ddf25f1337cbe_output"
		validID   = "ctco-existing"
	)
	body := []byte(`{"input":[` +
		`{"type":"custom_tool_call_output","id":"` + invalidID + `","call_id":"call-1","output":"done"},` +
		`{"type":"custom_tool_call_output","id":"` + validID + `","call_id":"call-2","output":"done"}` +
		`]}`)

	first := SanitizeCodexInputItemIDs(body)
	second := SanitizeCodexInputItemIDs(body)
	normalizedAgain := SanitizeCodexInputItemIDs(first)

	if actual := gjson.GetBytes(first, "input.0.id").String(); actual != "ctco_"+invalidID {
		t.Fatalf("custom_tool_call_output ID = %q, want ctco-prefixed ID", actual)
	}
	if actual := gjson.GetBytes(first, "input.1.id").String(); actual != validID {
		t.Fatalf("valid custom_tool_call_output ID changed: %q", actual)
	}
	if string(first) != string(second) {
		t.Fatalf("custom_tool_call_output ID normalization is not deterministic: first=%s second=%s", first, second)
	}
	if string(first) != string(normalizedAgain) {
		t.Fatalf("custom_tool_call_output ID normalization is not idempotent: first=%s normalized_again=%s", first, normalizedAgain)
	}
}

func TestSanitizeCodexInputItemIDsDropsOverlongEncryptedReasoningItem(t *testing.T) {
	longReasoningID := "rs_" + strings.Repeat("a", 64)
	shortReasoningID := "rs_" + strings.Repeat("b", 48)
	longCallID := strings.Repeat("call-item-", 8)
	body := []byte(`{"input":[` +
		`{"type":"message","id":"msg-1","role":"user","content":"before"},` +
		`{"type":"reasoning","id":"` + longReasoningID + `","encrypted_content":"gAAAA-encrypted","summary":[{"type":"summary_text","text":"drop me"}]},` +
		`{"type":"reasoning","id":"` + shortReasoningID + `","encrypted_content":"gAAAA-encrypted","summary":[]},` +
		`{"type":"function_call","id":"` + longCallID + `","call_id":"call-1","name":"lookup","arguments":"{}"}` +
		`]}`)

	got := SanitizeCodexInputItemIDs(body)
	input := gjson.GetBytes(got, "input").Array()

	if len(input) != 3 {
		t.Fatalf("input length = %d, want 3: %s", len(input), got)
	}
	if gotID := input[0].Get("id").String(); gotID != "msg-1" {
		t.Fatalf("input.0.id = %q, want msg-1", gotID)
	}
	if gotID := input[1].Get("id").String(); gotID != shortReasoningID {
		t.Fatalf("short encrypted reasoning id changed: %q", gotID)
	}
	if gotID := input[2].Get("id").String(); gotID == longCallID || len([]rune(gotID)) != 64 {
		t.Fatalf("ordinary overlong id was not shortened: %q", gotID)
	}
}

func TestSanitizeCodexInputItemIDsShortensOverlongReasoningWithoutEncryptedContent(t *testing.T) {
	longReasoningID := "rs_" + strings.Repeat("a", 64)
	for _, testCase := range []struct {
		name             string
		encryptedContent string
	}{
		{name: "missing"},
		{name: "empty", encryptedContent: `,"encrypted_content":""`},
		{name: "null", encryptedContent: `,"encrypted_content":null`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			body := []byte(`{"input":[{"type":"reasoning","id":"` + longReasoningID + `"` + testCase.encryptedContent + `,"summary":[]}]}`)

			got := SanitizeCodexInputItemIDs(body)
			input := gjson.GetBytes(got, "input").Array()
			if len(input) != 1 {
				t.Fatalf("input length = %d, want 1: %s", len(input), got)
			}
			gotID := input[0].Get("id").String()
			if gotID == longReasoningID || len([]rune(gotID)) != 64 {
				t.Fatalf("overlong reasoning id was not shortened: %q", gotID)
			}
		})
	}
}

func TestSanitizeCodexInputItemIDsAvoidsExistingIDCollision(t *testing.T) {
	longID := strings.Repeat("grok-item-", 10)
	collidingValidID := shortenCodexInputItemID(longID)
	body := []byte(`{"input":[{"id":"` + longID + `"},{"id":"` + collidingValidID + `"}]}`)

	first := SanitizeCodexInputItemIDs(body)
	second := SanitizeCodexInputItemIDs(body)

	shortened := gjson.GetBytes(first, "input.0.id").String()
	if shortened == collidingValidID {
		t.Fatalf("shortened ID collided with an existing valid ID: %q", shortened)
	}
	if len([]rune(shortened)) > 64 {
		t.Fatalf("shortened ID length = %d, want at most 64", len([]rune(shortened)))
	}
	if actual := gjson.GetBytes(first, "input.1.id").String(); actual != collidingValidID {
		t.Fatalf("existing valid ID changed: %q", actual)
	}
	if actual := gjson.GetBytes(second, "input.0.id").String(); actual != shortened {
		t.Fatalf("collision resolution is not deterministic: first=%q second=%q", shortened, actual)
	}
}

func TestSanitizeCodexInputItemIDsLeavesUnsupportedPayloadsUnchanged(t *testing.T) {
	for _, body := range [][]byte{
		[]byte(`not-json`),
		[]byte(`{"input":{"id":"item-1"}}`),
		[]byte(`{"input":[1,{"id":2},{"id":"item-1"}]}`),
	} {
		if got := string(SanitizeCodexInputItemIDs(body)); got != string(body) {
			t.Fatalf("payload changed: got=%q want=%q", got, body)
		}
	}
}

func BenchmarkSanitizeCodexInputItemIDsLargeNoopPayload(b *testing.B) {
	body := []byte(`{"input":[{"type":"message","id":"msg_1","role":"user","content":"` + strings.Repeat("x", 8<<20) + `"}]}`)
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for b.Loop() {
		benchmarkSanitizeCodexInputItemIDsOutput = SanitizeCodexInputItemIDs(body)
	}
}

func BenchmarkSanitizeCodexInputItemIDsLargeHistory(b *testing.B) {
	var payload strings.Builder
	payload.Grow(64 << 10)
	payload.WriteString(`{"input":[`)
	for index := range 1000 {
		if index > 0 {
			payload.WriteByte(',')
		}
		fmt.Fprintf(&payload, `{"type":"message","id":"msg_%d","role":"user","content":"x"}`, index)
	}
	payload.WriteString(`]}`)
	body := []byte(payload.String())

	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for b.Loop() {
		benchmarkSanitizeCodexInputItemIDsOutput = SanitizeCodexInputItemIDs(body)
	}
}

func BenchmarkSanitizeCodexInputItemIDsLargeToolHistory(b *testing.B) {
	var payload strings.Builder
	payload.Grow(128 << 10)
	payload.WriteString(`{"input":[`)
	for index := range 500 {
		if index > 0 {
			payload.WriteByte(',')
		}
		fmt.Fprintf(&payload, `{"type":"function_call","call_id":"call_%d","name":"read","arguments":"{}"},`, index)
		fmt.Fprintf(&payload, `{"type":"function_call_output","call_id":"call_%d","output":"done"}`, index)
	}
	payload.WriteString(`]}`)
	body := []byte(payload.String())

	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for b.Loop() {
		benchmarkSanitizeCodexInputItemIDsOutput = SanitizeCodexInputItemIDs(body)
	}
}
