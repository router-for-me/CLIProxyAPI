package helps

import (
	"fmt"
	"strings"
	"testing"

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

func TestSanitizeCodexInputItemIDsNormalizesCustomToolCallIDs(t *testing.T) {
	const invalidID = "item_44e13caebc1ddf25f1337cbe"
	body := []byte(`{"input":[{"type":"custom_tool_call","id":"` + invalidID + `","call_id":"call-1","name":"lookup","input":"{}"}]}`)

	got := SanitizeCodexInputItemIDs(body)
	if actual := gjson.GetBytes(got, "input.0.id").String(); actual != "ctc_"+invalidID {
		t.Fatalf("custom_tool_call ID = %q, want ctc-prefixed ID", actual)
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
