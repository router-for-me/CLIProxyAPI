package payloadcompress

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func liveConfig() Config {
	cfg := testConfig(EngineLite)
	cfg.CachePrefix = CachePrefixRewrite
	cfg.LiveZone = LiveZoneConfig{Enabled: true}
	return cfg
}

// liveNoisy is repetitive tool output with blank runs, which lite collapses.
func liveNoisy() string {
	return strings.Repeat("compiling pkg/main: no issues found\n\n\n", 40)
}

// liveBody builds a body with tools tool results. Growing the count keeps every
// earlier byte identical, which is what a live-zone reuse has to detect.
func liveBody(tools int) []byte {
	b := []byte(`{"model":"` + cachePrefixTestModel + `","max_tokens":1024,` +
		`"system":"You are a careful coding agent.","messages":[` +
		`{"role":"user","content":"run the go tests and show me the output"}`)
	for i := 0; i < tools; i++ {
		b = append(b, `,{"role":"tool","tool_call_id":"c`+itoa(i)+`","content":`+quote(liveNoisy())...)
		b = append(b, '}')
	}
	return append(b, ']', '}')
}

func TestSessionHeaderWinsOverFingerprint(t *testing.T) {
	body := liveBody(1)
	if got := SessionID("explicit-1", "m", "p", "", body); got != "explicit-1" {
		t.Fatalf("header must win, got %q", got)
	}
	if got := SessionID("  ", "m", "p", "", body); got == "" {
		t.Fatal("a blank header must fall back to the fingerprint")
	}
}

func withModel(body []byte, model string) []byte {
	return bytes.Replace(body, []byte(`"model":"`+cachePrefixTestModel+`"`), []byte(`"model":"`+model+`"`), 1)
}

func TestSessionFingerprintTracksIdentityParts(t *testing.T) {
	body := liveBody(1)
	base := SessionID("", "model-a", "prov", "conn", body)

	if SessionID("", "model-a", "prov", "conn", withModel(body, "other-model")) == base {
		t.Error("a model change must produce a new session")
	}
	if SessionID("", "model-a", "other", "conn", body) == base {
		t.Error("a provider change must produce a new session")
	}
	if got := SessionID("", "model-a", "prov", "conn", liveBody(2)); got != base {
		t.Error("appending a tool result must keep the session")
	}
	changed := bytes.Replace(body, []byte("run the go tests"), []byte("delete the cache dir"), 1)
	if SessionID("", "model-a", "prov", "conn", changed) == base {
		t.Error("a different first user message must produce a new session")
	}
}

func TestLiveZoneReusesTheCompressedPrefix(t *testing.T) {
	resetLiveZone()
	t.Cleanup(resetLiveZone)
	cfg := liveConfig()
	body := liveBody(2)

	first, firstStats := ApplySession(cfg, cachePrefixTestModel, "s1", body)
	if !firstStats.Applied || bytes.Equal(first, body) {
		t.Fatalf("turn 1 must compress: %+v", firstStats)
	}

	// Repeating the request must return identical bytes while running no engine
	// at all, which is what the empty Saved map reports.
	second, secondStats := ApplySession(cfg, cachePrefixTestModel, "s1", body)
	if !bytes.Equal(second, first) {
		t.Fatal("a reused prefix must reproduce the previous output byte for byte")
	}
	if len(secondStats.Saved) != 0 {
		t.Fatalf("reuse must skip the engines, got %v", secondStats.Saved)
	}
}

func TestLiveZoneCompressesOnlyTheNewTail(t *testing.T) {
	resetLiveZone()
	t.Cleanup(resetLiveZone)
	cfg := liveConfig()
	model := cachePrefixTestModel

	first, _ := ApplySession(cfg, model, "s1", liveBody(2))
	second, secondStats := ApplySession(cfg, model, "s1", liveBody(3))
	if len(secondStats.Saved) == 0 {
		t.Fatal("an appended turn must still be compressed")
	}
	// The frozen region is everything but the closing "]}", so it must survive
	// the appended message untouched or the provider cache breaks.
	if !bytes.HasPrefix(second, first[:len(first)-2]) {
		t.Fatal("the reused prefix must come out byte-identical")
	}
	if len(second) <= len(first) {
		t.Fatal("the appended turn must appear in the output")
	}
}

func TestLiveZoneDiscardsReuseAfterAPrefixEdit(t *testing.T) {
	resetLiveZone()
	t.Cleanup(resetLiveZone)
	cfg := liveConfig()
	model := cachePrefixTestModel
	body := liveBody(2)
	ApplySession(cfg, model, "s1", body)

	edited := bytes.Replace(body, []byte("run the go tests"), []byte("run the linters"), 1)
	if _, stats := ApplySession(cfg, model, "s1", edited); len(stats.Saved) == 0 {
		t.Fatal("an edited prefix must be recompressed, never served from the zone")
	}
}

func TestLiveZoneIgnoresShadowedResults(t *testing.T) {
	resetLiveZone()
	t.Cleanup(resetLiveZone)
	cfg := liveConfig()
	cfg.Shadow = true
	ApplySession(cfg, cachePrefixTestModel, "s1", liveBody(1))

	if entry := liveZone.entries[liveZoneKey(cachePrefixTestModel, "s1")]; entry != nil {
		t.Fatal("shadow mode must not store a result the caller never received")
	}
}

func TestLiveZoneIgnoresExpiredEntries(t *testing.T) {
	resetLiveZone()
	t.Cleanup(resetLiveZone)
	cfg := liveConfig()
	model := cachePrefixTestModel
	body := liveBody(1)
	ApplySession(cfg, model, "s1", body)

	key := liveZoneKey(model, "s1")
	liveZone.mu.Lock()
	entry := liveZone.entries[key]
	if entry != nil {
		entry.at = time.Now().Add(-time.Hour)
	}
	liveZone.mu.Unlock()
	if entry == nil {
		t.Fatal("the first request must be stored")
	}

	if _, stats := ApplySession(cfg, model, "s1", body); len(stats.Saved) == 0 {
		t.Fatal("an expired entry must not be reused")
	}
}

func TestLiveZoneOffIsPlainApply(t *testing.T) {
	resetLiveZone()
	t.Cleanup(resetLiveZone)
	cfg := liveConfig()
	cfg.LiveZone.Enabled = false
	body := liveBody(2)

	got, stats := ApplySession(cfg, cachePrefixTestModel, "s1", body)
	want, wantStats := Apply(cfg, cachePrefixTestModel, body)
	if !bytes.Equal(got, want) || stats.Applied != wantStats.Applied {
		t.Fatal("with live zone off ApplySession must be exactly Apply")
	}
	if liveZone.entries[liveZoneKey(cachePrefixTestModel, "s1")] != nil {
		t.Fatal("live zone off must not populate the cache")
	}
}
