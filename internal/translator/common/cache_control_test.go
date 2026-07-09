package common

import (
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
