package executor

import (
	"bytes"
	"fmt"
	"testing"
)

// legacyCacheControlSequence reproduces the exact pre-merge call sequence that
// lived at the Claude executor call sites. It is the differential oracle for
// applyCacheControlPipeline: every corpus payload must produce byte-identical
// output through both paths, which is the hard guarantee that the read-side
// scan merge introduced no behavioural drift.
func legacyCacheControlSequence(payload []byte, maxBlocks int, doInject bool) []byte {
	out := bytes.Clone(payload)
	if doInject && countCacheControls(out) == 0 {
		out = ensureCacheControl(out)
	}
	out = enforceCacheControlLimit(out, maxBlocks)
	out = normalizeCacheControlTTL(out)
	return out
}

// cacheControlDiffCases is the differential corpus. Names map to the categories
// in the Phase 3 plan (zero-block inject, string conversions, turn counts, tool
// presence, no-op ranges, excess removal per section, TTL downgrades, non-object
// cc, HTML escapes, enforce+normalize combos, invalid JSON, key order).
func cacheControlDiffCases() []struct {
	name    string
	payload []byte
} {
	manyTools := `[`
	for i := 0; i < 50; i++ {
		if i > 0 {
			manyTools += ","
		}
		manyTools += fmt.Sprintf(`{"name":"tool%d","description":"Tool %d","input_schema":{"type":"object"}}`, i, i)
	}
	manyTools += `]`

	return []struct {
		name    string
		payload []byte
	}{
		{
			name:    "zero_blocks_full_inject",
			payload: []byte(`{"model":"m","tools":[{"name":"t1","input_schema":{"type":"object"}},{"name":"t2","input_schema":{"type":"object"}}],"system":[{"type":"text","text":"s1"},{"type":"text","text":"s2"}],"messages":[{"role":"user","content":[{"type":"text","text":"u1"}]},{"role":"assistant","content":[{"type":"text","text":"a1"}]},{"role":"user","content":[{"type":"text","text":"u2"}]}]}`),
		},
		{
			name:    "string_system_conversion",
			payload: []byte(`{"model":"m","system":"long system prompt","messages":[]}`),
		},
		{
			name:    "string_content_conversion",
			payload: []byte(`{"model":"m","messages":[{"role":"user","content":"first user"},{"role":"assistant","content":"reply"},{"role":"user","content":"second user"}]}`),
		},
		{
			name:    "single_user_turn_no_message_inject",
			payload: []byte(`{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"only user"}]}]}`),
		},
		{
			name:    "no_tools_system_and_messages",
			payload: []byte(`{"model":"m","system":[{"type":"text","text":"s1"}],"messages":[{"role":"user","content":[{"type":"text","text":"u1"}]},{"role":"assistant","content":[{"type":"text","text":"a1"}]},{"role":"user","content":[{"type":"text","text":"u2"}]}]}`),
		},
		{
			name:    "one_block_noop",
			payload: []byte(`{"model":"m","tools":[{"name":"t1","cache_control":{"type":"ephemeral"}}]}`),
		},
		{
			name:    "two_blocks_noop",
			payload: []byte(`{"model":"m","tools":[{"name":"t1","cache_control":{"type":"ephemeral"}}],"system":[{"type":"text","text":"s1","cache_control":{"type":"ephemeral"}}]}`),
		},
		{
			name:    "three_blocks_noop",
			payload: []byte(`{"model":"m","tools":[{"name":"t1","cache_control":{"type":"ephemeral"}}],"system":[{"type":"text","text":"s1","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":[{"type":"text","text":"u1","cache_control":{"type":"ephemeral"}}]}]}`),
		},
		{
			name:    "four_blocks_noop",
			payload: []byte(`{"model":"m","tools":[{"name":"t1","cache_control":{"type":"ephemeral"}},{"name":"t2","cache_control":{"type":"ephemeral"}}],"system":[{"type":"text","text":"s1","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":[{"type":"text","text":"u1","cache_control":{"type":"ephemeral"}}]}]}`),
		},
		{
			name:    "excess_system_segment_removal",
			payload: []byte(`{"model":"m","system":[{"type":"text","text":"s1","cache_control":{"type":"ephemeral"}},{"type":"text","text":"s2","cache_control":{"type":"ephemeral"}},{"type":"text","text":"s3","cache_control":{"type":"ephemeral"}},{"type":"text","text":"s4","cache_control":{"type":"ephemeral"}},{"type":"text","text":"s5","cache_control":{"type":"ephemeral"}}]}`),
		},
		{
			name:    "excess_tools_segment_removal",
			payload: []byte(`{"model":"m","tools":[{"name":"t1","cache_control":{"type":"ephemeral"}},{"name":"t2","cache_control":{"type":"ephemeral"}},{"name":"t3","cache_control":{"type":"ephemeral"}},{"name":"t4","cache_control":{"type":"ephemeral"}},{"name":"t5","cache_control":{"type":"ephemeral"}}]}`),
		},
		{
			name:    "excess_messages_segment_removal",
			payload: []byte(`{"model":"m","tools":[{"name":"t1","cache_control":{"type":"ephemeral"}},{"name":"t2","cache_control":{"type":"ephemeral"}}],"system":[{"type":"text","text":"s1","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":[{"type":"text","text":"u1","cache_control":{"type":"ephemeral"}}]},{"role":"user","content":[{"type":"text","text":"u2","cache_control":{"type":"ephemeral"}}]}]}`),
		},
		{
			name:    "ttl_cross_segment_downgrade",
			payload: []byte(`{"model":"m","tools":[{"name":"t1","cache_control":{"type":"ephemeral","ttl":"1h"}}],"system":[{"type":"text","text":"s1","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":[{"type":"text","text":"u1","cache_control":{"type":"ephemeral","ttl":"1h"}}]}]}`),
		},
		{
			name:    "ttl_same_segment_downgrade",
			payload: []byte(`{"model":"m","system":[{"type":"text","text":"s1","cache_control":{"type":"ephemeral"}},{"type":"text","text":"s2","cache_control":{"type":"ephemeral"}},{"type":"text","text":"s3","cache_control":{"type":"ephemeral","ttl":"1h"}}]}`),
		},
		{
			name:    "non_object_cc_triggers_5m",
			payload: []byte(`{"model":"m","tools":[{"name":"t1","cache_control":"ephemeral"}],"system":[{"type":"text","text":"s1","cache_control":{"type":"ephemeral","ttl":"1h"}}]}`),
		},
		{
			name:    "html_escape_chars_preserved",
			payload: []byte(`{"model":"m","tools":[{"name":"t1","cache_control":{"type":"ephemeral","ttl":"1h"}}],"system":[{"type":"text","text":"<system-reminder>foo & bar</system-reminder>","cache_control":{"type":"ephemeral","ttl":"1h"}}],"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`),
		},
		{
			name:    "enforce_then_normalize_combo",
			payload: []byte(`{"model":"m","tools":[{"name":"t1","cache_control":{"type":"ephemeral","ttl":"1h"}},{"name":"t2","cache_control":{"type":"ephemeral"}}],"system":[{"type":"text","text":"s1","cache_control":{"type":"ephemeral","ttl":"1h"}},{"type":"text","text":"s2","cache_control":{"type":"ephemeral","ttl":"1h"}}],"messages":[{"role":"user","content":[{"type":"text","text":"u1","cache_control":{"type":"ephemeral","ttl":"1h"}}]},{"role":"user","content":[{"type":"text","text":"u2","cache_control":{"type":"ephemeral","ttl":"1h"}}]}]}`),
		},
		{
			name:    "key_order_preserved",
			payload: []byte(`{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"u1","cache_control":{"type":"ephemeral","ttl":"1h"}}]}],"tools":[{"name":"t1","cache_control":{"type":"ephemeral"}}],"system":[{"type":"text","text":"s1","cache_control":{"type":"ephemeral"}}]}`),
		},
		{
			name:    "many_tools_claude_code_scenario",
			payload: []byte(fmt.Sprintf(`{"model":"m","tools":%s,"system":[{"type":"text","text":"You are Claude Code"}],"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`, manyTools)),
		},
		{
			name:    "invalid_json",
			payload: []byte(`{"model":"m","tools":[`),
		},
		{
			name:    "empty_payload",
			payload: []byte(``),
		},
		{
			name:    "string_system_with_existing_message_cc",
			payload: []byte(`{"model":"m","system":"sys","messages":[{"role":"user","content":[{"type":"text","text":"u1","cache_control":{"type":"ephemeral"}}]},{"role":"user","content":[{"type":"text","text":"u2"}]}]}`),
		},
	}
}

func TestApplyCacheControlPipeline_MatchesLegacySequence(t *testing.T) {
	maxValues := []int{1, 2, 4}
	for _, tc := range cacheControlDiffCases() {
		for _, maxBlocks := range maxValues {
			for _, doInject := range []bool{true, false} {
				name := fmt.Sprintf("%s/max=%d/inject=%v", tc.name, maxBlocks, doInject)
				t.Run(name, func(t *testing.T) {
					want := legacyCacheControlSequence(tc.payload, maxBlocks, doInject)
					got := applyCacheControlPipeline(bytes.Clone(tc.payload), maxBlocks, doInject)
					if !bytes.Equal(want, got) {
						t.Fatalf("pipeline diverged from legacy sequence.\nwant: %s\ngot:  %s", want, got)
					}
				})
			}
		}
	}
}
