package common

import (
	"fmt"
	"testing"

	"github.com/tidwall/gjson"
)

func TestAttachCacheControl_CopiesObject(t *testing.T) {
	src := gjson.Parse(`{"text":"hi","cache_control":{"type":"ephemeral","ttl":"5m"}}`)
	dst := []byte(`{"type":"text","text":"hi"}`)

	out := AttachCacheControl(dst, src)
	if got := gjson.GetBytes(out, "cache_control.type").String(); got != "ephemeral" {
		t.Fatalf("cache_control.type = %q, want ephemeral; out=%s", got, out)
	}
	if got := gjson.GetBytes(out, "cache_control.ttl").String(); got != "5m" {
		t.Fatalf("cache_control.ttl = %q, want 5m; out=%s", got, out)
	}
}

func TestAttachCacheControl_IgnoresMissing(t *testing.T) {
	src := gjson.Parse(`{"text":"hi"}`)
	dst := []byte(`{"type":"text","text":"hi"}`)

	out := AttachCacheControl(dst, src)
	if gjson.GetBytes(out, "cache_control").Exists() {
		t.Fatalf("cache_control should be absent; out=%s", out)
	}
}

func TestAttachMessageCacheControl_PromotesStringContent(t *testing.T) {
	src := gjson.Parse(`{"role":"user","content":"hi","cache_control":{"type":"ephemeral"}}`)
	msg := []byte(`{"role":"user","content":"hi"}`)

	out := AttachMessageCacheControl(msg, src)
	if got := gjson.GetBytes(out, "content.0.type").String(); got != "text" {
		t.Fatalf("content.0.type = %q, want text; out=%s", got, out)
	}
	if got := gjson.GetBytes(out, "content.0.text").String(); got != "hi" {
		t.Fatalf("content.0.text = %q, want hi; out=%s", got, out)
	}
	if got := gjson.GetBytes(out, "content.0.cache_control.type").String(); got != "ephemeral" {
		t.Fatalf("content.0.cache_control.type = %q, want ephemeral; out=%s", got, out)
	}
}

func TestAttachMessageCacheControl_SkipsWhenLastPartHasCacheControl(t *testing.T) {
	src := gjson.Parse(`{"cache_control":{"type":"ephemeral","ttl":"1h"}}`)
	msg := []byte(`{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral"}}]}`)

	out := AttachMessageCacheControl(msg, src)
	if gjson.GetBytes(out, "content.0.cache_control.ttl").Exists() {
		t.Fatalf("part-level cache_control should win; out=%s", out)
	}
}

func TestAttachToolMessageCacheControl_HoistsPartLevel(t *testing.T) {
	src := gjson.Parse(`{"role":"tool","content":[{"type":"text","text":"4","cache_control":{"type":"ephemeral"}}],"cache_control":{"type":"ephemeral","ttl":"1h"}}`)
	msg := []byte(`{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":[{"type":"text","text":"4"}]}]}`)

	out := AttachToolMessageCacheControl(msg, src)
	if got := gjson.GetBytes(out, "content.0.cache_control.type").String(); got != "ephemeral" {
		t.Fatalf("tool_result cache_control.type = %q, want ephemeral; out=%s", got, out)
	}
	// Part-level does not carry ttl, so ttl should be absent
	if gjson.GetBytes(out, "content.0.cache_control.ttl").Exists() {
		t.Fatalf("part-level should take precedence over message-level; out=%s", out)
	}
}

func TestAttachToolMessageCacheControl_InvalidPartDoesNotBlockMessageLevel(t *testing.T) {
	testCases := []struct {
		name       string
		partCCJSON string
	}{
		{name: "empty object", partCCJSON: `{}`},
		{name: "invalid type string", partCCJSON: `{"type":"invalid"}`},
		{name: "whitespace padded type", partCCJSON: `{"type":" ephemeral "}`},
		{name: "non-string type", partCCJSON: `{"type":123}`},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			src := gjson.Parse(fmt.Sprintf(`{"role":"tool","content":[{"type":"text","text":"4","cache_control":%s}],"cache_control":{"type":"ephemeral","ttl":"1h"}}`, tc.partCCJSON))
			msg := []byte(`{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":[{"type":"text","text":"4"}]}]}`)

			out := AttachToolMessageCacheControl(msg, src)
			if got := gjson.GetBytes(out, "content.0.cache_control.type").String(); got != "ephemeral" {
				t.Fatalf("[%s] expected message-level ephemeral to win; got %q; out=%s", tc.name, got, out)
			}
			if got := gjson.GetBytes(out, "content.0.cache_control.ttl").String(); got != "1h" {
				t.Fatalf("[%s] expected message-level ttl=1h; got %q; out=%s", tc.name, got, out)
			}
		})
	}
}

func TestAttachToolMessageCacheControl_NoToolResultLeavesMessageUntouched(t *testing.T) {
	src := gjson.Parse(`{"role":"tool","cache_control":{"type":"ephemeral"}}`)
	msg := []byte(`{"role":"user","content":[{"type":"text","text":"just text"}]}`)

	out := AttachToolMessageCacheControl(msg, src)
	if gjson.GetBytes(out, "content.0.cache_control").Exists() {
		t.Fatalf("expected text block to not receive cache_control; out=%s", out)
	}
}
