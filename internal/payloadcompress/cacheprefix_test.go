package payloadcompress

import (
	"bytes"
	"strconv"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// These tests pin the two cache-prefix modes against the only property that
// makes them worth having: what the provider sees on the wire across the turns
// of one session. Turning compression on is cheap; keeping the bytes it already
// cached identical turn over turn is the part that costs or saves real money.

const cachePrefixTestModel = "claude-sonnet-4"

// cachePrefixTestConfig runs the default combo. MinBytes is 1 so the test is
// about the prefix, never about the size gate: a body crossing the gate
// mid-conversation changes the prefix once, which is a known discontinuity and
// not what these tests measure.
func cachePrefixTestConfig(cachePrefix string) Config {
	return Config{
		Enabled:      true,
		MinBytes:     1,
		CachePrefix:  cachePrefix,
		DefaultCombo: "standard",
		Combos: []Combo{{
			Name:   "standard",
			Models: []string{"*"},
			Pipeline: []Step{
				{Engine: EngineSessionDedup},
				{Engine: EngineLite},
			},
		}},
	}
}

// noisyOutput is a tool result with everything lite removes: ANSI escapes,
// trailing spaces and a blank run. It is over the 2000-char tool cap, so lite
// rewrites it whether or not it finds the noise.
func noisyOutput(n int) string {
	var b strings.Builder
	b.WriteString("\x1b[32m$ go test ./pkg/")
	b.WriteString(strconv.Itoa(n))
	b.WriteString("\x1b[0m\n")
	for i := 0; i < 40; i++ {
		b.WriteString("ok   \tgithub.com/router-for-me/CLIProxyAPI/pkg")
		b.WriteString(strconv.Itoa(n))
		b.WriteString(" 0.0")
		b.WriteString(strconv.Itoa(i))
		b.WriteString("s   \n")
	}
	b.WriteString("\n\n\n--- FAIL: TestThing\n")
	return b.String()
}

// testCacheMarker is the client-owned breakpoint. It is stripped before wire
// bytes are compared, because it moves between turns while the token stream it
// annotates does not.
const testCacheMarker = `,"cache_control":{"type":"ephemeral"}`

func textMessage(role, text string, mark bool) []byte {
	b := append([]byte(`{"role":"`), role...)
	b = append(b, `","content":[{"type":"text","text":`...)
	b = appendJSONString(b, text)
	if mark {
		b = append(b, testCacheMarker...)
	}
	return append(b, `}]}`...)
}

func chatBody(messages [][]byte) []byte {
	b := append([]byte(`{"model":"`+cachePrefixTestModel+`","max_tokens":1024,`+
		`"system":"You are a careful coding agent.","messages":[`), bytes.Join(messages, []byte(","))...)
	return append(b, `]}`...)
}

// messageRaws returns every message as wire bytes, minus the cache_control
// marker. Comparing bytes rather than decoded text is what catches a transform
// that re-encodes without changing meaning: key order, escaping, or a rewritten
// tool_use id all leave the text identical and shift every byte after them.
func messageRaws(t *testing.T, body []byte) []string {
	t.Helper()
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		t.Fatalf("no messages array in %s", body)
	}
	var out []string
	messages.ForEach(func(_, item gjson.Result) bool {
		out = append(out, strings.ReplaceAll(item.Raw, testCacheMarker, ""))
		return true
	})
	return out
}

// clip keeps a byte-level diff readable in a failure message.
func clip(s string) string {
	if len(s) > 160 {
		return s[:160] + "..."
	}
	return s
}

// TestCachePrefixCompressAppendStable is the contract cache-prefix: compress
// rests on. A real session appends turns; if the compressed form of an earlier
// turn shifts when a later one arrives, every byte after the shift is a cache
// miss, and a miss costs more than the compression saved.
//
// The breakpoint sits on the newest user turn, as Claude Code places it, so the
// assertion covers messages that are inside the provider-cached zone.
func TestCachePrefixCompressAppendStable(t *testing.T) {
	cfg := cachePrefixTestConfig(CachePrefixCompress)
	const turns = 6

	var messages [][]byte
	var previous []string
	cachedZoneCompressed := false

	for turn := 1; turn <= turns; turn++ {
		// Turns 3 and 5 emit the identical tool result, so session-dedup has
		// something to elide and its index bookkeeping is exercised too.
		n := turn
		if turn == 5 {
			n = 3
		}
		messages = append(messages,
			textMessage("user", "run the tests for pkg "+strconv.Itoa(turn), false),
			textMessage("assistant", "running the tests", false),
			textMessage("tool", noisyOutput(n), false),
		)

		// Rebuild rather than mutate, so each turn's body is independent. The
		// marker moves forward every turn and never rewrites a message body.
		marked := make([][]byte, len(messages))
		copy(marked, messages)
		marked[len(marked)-3] = textMessage("user", "run the tests for pkg "+strconv.Itoa(turn), true)
		body := chatBody(marked)

		compressed, _ := Apply(cfg, cachePrefixTestModel, body)
		got := messageRaws(t, compressed)

		if len(previous) > 0 {
			for i := range previous {
				if got[i] != previous[i] {
					t.Fatalf("turn %d: message %d changed since the previous turn, so every later byte misses the cache\n before: %q\n after:  %q",
						turn, i, clip(previous[i]), clip(got[i]))
				}
			}
		}

		// Guard against a green test that proves nothing: lite must have
		// rewritten something inside the cached zone, before the breakpoint.
		raw := messageRaws(t, body)
		for i := 0; i < len(marked)-3; i++ {
			if got[i] != raw[i] {
				cachedZoneCompressed = true
			}
		}

		previous = got
	}

	if !cachedZoneCompressed {
		t.Fatal("nothing before the breakpoint was compressed; the cached zone was preserved, so this test proves nothing")
	}
}

// TestCachePrefixPreserveLeavesCachedZoneRaw pins the default: a segment behind
// the last breakpoint reaches the provider byte for byte, whatever the engines
// would have done to it.
func TestCachePrefixPreserveLeavesCachedZoneRaw(t *testing.T) {
	body := func() []byte {
		var messages [][]byte
		for turn := 1; turn <= 3; turn++ {
			messages = append(messages,
				textMessage("user", "run the tests for pkg "+strconv.Itoa(turn), turn == 3),
				textMessage("assistant", "running the tests", false),
				textMessage("tool", noisyOutput(turn), false),
			)
		}
		return chatBody(messages)
	}()

	compressed, _ := Apply(cachePrefixTestConfig(CachePrefixPreserve), cachePrefixTestModel, body)
	if unsanitized, _ := Apply(cachePrefixTestConfig(""), cachePrefixTestModel, body); !bytes.Equal(unsanitized, compressed) {
		t.Fatal("an unset cache-prefix did not behave as preserve")
	}

	// Message 5 (turn 2's tool result) is behind the turn 3 breakpoint.
	if want := appendJSONString(nil, noisyOutput(2)); !bytes.Contains(compressed, want) {
		t.Fatal("preserve rewrote a segment inside the cached zone")
	}
	// Message 8 (turn 3's tool result) is after it.
	if unwanted := appendJSONString(nil, noisyOutput(3)); bytes.Contains(compressed, unwanted) {
		t.Fatal("preserve left a segment after the breakpoint uncompressed")
	}
}

// TestCachePrefixDefaultsToPreserve guards the claim in Config: anything that
// is not exactly "compress" preserves, so an unset or mistyped config can never
// start rewriting a cached prefix.
func TestCachePrefixDefaultsToPreserve(t *testing.T) {
	for _, configured := range []string{"", "preserve", "PRESERVE", "compress ", "bogus"} {
		cfg := Config{CachePrefix: configured}
		cfg.Sanitize()
		want := CachePrefixPreserve
		if strings.TrimSpace(strings.ToLower(configured)) == CachePrefixCompress {
			want = CachePrefixCompress
		}
		if cfg.CachePrefix != want {
			t.Fatalf("Sanitize(%q) = %q, want %q", configured, cfg.CachePrefix, want)
		}
	}
}
