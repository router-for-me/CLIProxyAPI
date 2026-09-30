package common

import (
	"reflect"
	"testing"

	"github.com/tidwall/gjson"
)

func TestAlignOpenAIToolCallMessages(t *testing.T) {
	t.Run("empty or single message unchanged", func(t *testing.T) {
		if got := AlignOpenAIToolCallMessages(nil); got != nil {
			t.Fatalf("expected nil, got %v", got)
		}
		single := [][]byte{[]byte(`{"role":"user","content":"hi"}`)}
		if got := AlignOpenAIToolCallMessages(single); !reflect.DeepEqual(got, single) {
			t.Fatalf("expected single message unchanged")
		}
	})

	t.Run("already adjacent messages remain unchanged", func(t *testing.T) {
		messages := [][]byte{
			[]byte(`{"role":"user","content":"start"}`),
			[]byte(`{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"f1"}}]}`),
			[]byte(`{"role":"tool","tool_call_id":"call_1","content":"res1"}`),
			[]byte(`{"role":"assistant","content":"done"}`),
		}
		got := AlignOpenAIToolCallMessages(messages)
		if len(got) != len(messages) {
			t.Fatalf("expected %d messages, got %d", len(messages), len(got))
		}
		for i := range messages {
			if string(got[i]) != string(messages[i]) {
				t.Fatalf("mismatch at %d: got %s, want %s", i, got[i], messages[i])
			}
		}
	})

	t.Run("reorders separated tool results to follow assistant", func(t *testing.T) {
		messages := [][]byte{
			[]byte(`{"role":"user","content":"start"}`),
			[]byte(`{"role":"assistant","tool_calls":[{"id":"call_a","type":"function"},{"id":"call_b","type":"function"}]}`),
			[]byte(`{"role":"assistant","content":"thinking part","reasoning_content":"step 1"}`),
			[]byte(`{"role":"user","content":"system reminder"}`),
			[]byte(`{"role":"tool","tool_call_id":"call_b","content":"res_b"}`),
			[]byte(`{"role":"tool","tool_call_id":"call_a","content":"res_a"}`),
			[]byte(`{"role":"user","content":"final prompt"}`),
		}

		got := AlignOpenAIToolCallMessages(messages)
		if len(got) != 7 {
			t.Fatalf("expected 7 messages, got %d", len(got))
		}

		// Expected order:
		// 0: user "start"
		// 1: assistant tool_calls
		// 2: tool call_b
		// 3: tool call_a
		// 4: assistant "thinking part"
		// 5: user "system reminder"
		// 6: user "final prompt"
		roles := make([]string, len(got))
		for i, m := range got {
			roles[i] = gjson.GetBytes(m, "role").String()
		}
		wantRoles := []string{"user", "assistant", "tool", "tool", "assistant", "user", "user"}
		if !reflect.DeepEqual(roles, wantRoles) {
			t.Fatalf("unexpected roles: got %v, want %v", roles, wantRoles)
		}

		if gjson.GetBytes(got[2], "tool_call_id").String() != "call_b" {
			t.Errorf("got[2] tool_call_id = %s, want call_b", gjson.GetBytes(got[2], "tool_call_id").String())
		}
		if gjson.GetBytes(got[3], "tool_call_id").String() != "call_a" {
			t.Errorf("got[3] tool_call_id = %s, want call_a", gjson.GetBytes(got[3], "tool_call_id").String())
		}
		if gjson.GetBytes(got[4], "reasoning_content").String() != "step 1" {
			t.Errorf("got[4] reasoning_content = %s, want step 1", gjson.GetBytes(got[4], "reasoning_content").String())
		}
		if gjson.GetBytes(got[5], "content").String() != "system reminder" {
			t.Errorf("got[5] content = %s, want system reminder", gjson.GetBytes(got[5], "content").String())
		}
	})

	t.Run("leaves incomplete tool calls untouched", func(t *testing.T) {
		// Assistant calls call_1 and call_2, but only call_1 has a result
		messages := [][]byte{
			[]byte(`{"role":"assistant","tool_calls":[{"id":"call_1"},{"id":"call_2"}]}`),
			[]byte(`{"role":"user","content":"middle"}`),
			[]byte(`{"role":"tool","tool_call_id":"call_1","content":"res1"}`),
		}
		got := AlignOpenAIToolCallMessages(messages)
		for i := range messages {
			if string(got[i]) != string(messages[i]) {
				t.Fatalf("incomplete history should be untouched, but differed at %d", i)
			}
		}
	})

	t.Run("leaves orphan tool messages untouched", func(t *testing.T) {
		messages := [][]byte{
			[]byte(`{"role":"user","content":"start"}`),
			[]byte(`{"role":"tool","tool_call_id":"call_orphan","content":"orphan"}`),
		}
		got := AlignOpenAIToolCallMessages(messages)
		for i := range messages {
			if string(got[i]) != string(messages[i]) {
				t.Fatalf("orphan history should be untouched, but differed at %d", i)
			}
		}
	})

	t.Run("leaves ambiguous duplicate call IDs untouched", func(t *testing.T) {
		messages := [][]byte{
			[]byte(`{"role":"assistant","tool_calls":[{"id":"dup_call"}]}`),
			[]byte(`{"role":"assistant","tool_calls":[{"id":"dup_call"}]}`),
			[]byte(`{"role":"user","content":"middle"}`),
			[]byte(`{"role":"tool","tool_call_id":"dup_call","content":"res"}`),
		}
		got := AlignOpenAIToolCallMessages(messages)
		for i := range messages {
			if string(got[i]) != string(messages[i]) {
				t.Fatalf("ambiguous history should be untouched, but differed at %d", i)
			}
		}
	})

	t.Run("leaves assistant with mixed empty ID untouched", func(t *testing.T) {
		// Assistant calls one empty ID and one valid call_a
		messages := [][]byte{
			[]byte(`{"role":"assistant","tool_calls":[{"id":""},{"id":"call_a"}]}`),
			[]byte(`{"role":"user","content":"reminder"}`),
			[]byte(`{"role":"tool","tool_call_id":"call_a","content":"res_a"}`),
		}
		got := AlignOpenAIToolCallMessages(messages)
		for i := range messages {
			if string(got[i]) != string(messages[i]) {
				t.Fatalf("mixed empty ID history should be untouched, but differed at %d", i)
			}
		}
	})

	t.Run("preserves numeric precision and raw byte content", func(t *testing.T) {
		largeIntJSON := `{"custom_id":9223372036854775807,"precise_ratio":0.12345678901234567,"role":"assistant","tool_calls":[{"function":{"name":"p"},"id":"call_p","type":"function"}]}`
		toolJSON := `{"content":"ok","role":"tool","timestamp_ns":1789703256000000001,"tool_call_id":"call_p"}`
		userJSON := `{"content":"intervening","role":"user"}`

		messages := [][]byte{
			[]byte(largeIntJSON),
			[]byte(userJSON),
			[]byte(toolJSON),
		}

		got := AlignOpenAIToolCallMessages(messages)
		if len(got) != 3 {
			t.Fatalf("expected 3 messages, got %d", len(got))
		}
		if string(got[0]) != largeIntJSON {
			t.Fatalf("assistant message changed: got %s, want %s", string(got[0]), largeIntJSON)
		}
		if string(got[1]) != toolJSON {
			t.Fatalf("tool message changed: got %s, want %s", string(got[1]), toolJSON)
		}
		if string(got[2]) != userJSON {
			t.Fatalf("user message changed: got %s, want %s", string(got[2]), userJSON)
		}
	})

	t.Run("leaves causal violation untouched when result precedes call", func(t *testing.T) {
		messages := [][]byte{
			[]byte(`{"role":"tool","tool_call_id":"call_future","content":"res"}`),
			[]byte(`{"role":"assistant","tool_calls":[{"id":"call_future"}]}`),
		}
		got := AlignOpenAIToolCallMessages(messages)
		for i := range messages {
			if string(got[i]) != string(messages[i]) {
				t.Fatalf("causal violation history should be untouched, but differed at %d", i)
			}
		}
	})

	t.Run("reorders multiple interleaved assistant turns independently", func(t *testing.T) {
		messages := [][]byte{
			[]byte(`{"role":"assistant","tool_calls":[{"id":"call_1"}]}`),
			[]byte(`{"role":"user","content":"reminder 1"}`),
			[]byte(`{"role":"tool","tool_call_id":"call_1","content":"res 1"}`),
			[]byte(`{"role":"assistant","tool_calls":[{"id":"call_2"}]}`),
			[]byte(`{"role":"user","content":"reminder 2"}`),
			[]byte(`{"role":"tool","tool_call_id":"call_2","content":"res 2"}`),
		}
		got := AlignOpenAIToolCallMessages(messages)
		if len(got) != 6 {
			t.Fatalf("expected 6 messages, got %d", len(got))
		}
		roles := make([]string, len(got))
		for i, m := range got {
			roles[i] = gjson.GetBytes(m, "role").String()
		}
		wantRoles := []string{"assistant", "tool", "user", "assistant", "tool", "user"}
		if !reflect.DeepEqual(roles, wantRoles) {
			t.Fatalf("unexpected roles: got %v, want %v", roles, wantRoles)
		}
		if gjson.GetBytes(got[1], "tool_call_id").String() != "call_1" {
			t.Errorf("got[1] tool_call_id = %s, want call_1", gjson.GetBytes(got[1], "tool_call_id").String())
		}
		if gjson.GetBytes(got[4], "tool_call_id").String() != "call_2" {
			t.Errorf("got[4] tool_call_id = %s, want call_2", gjson.GetBytes(got[4], "tool_call_id").String())
		}
	})
}
