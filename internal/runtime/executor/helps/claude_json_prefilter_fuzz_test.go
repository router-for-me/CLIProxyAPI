package helps

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// referenceIsClaudeTitleHelperRequest is isClaudeTitleHelperRequest without
// the jsonMayContainASCII pre-filter (the v8.0.11 implementation).
func referenceIsClaudeTitleHelperRequest(body []byte) bool {
	props := gjson.GetBytes(body, "output_config.format.schema.properties")
	if props.Exists() {
		if props.Get("title").Exists() && len(props.Map()) == 1 {
			return isClaudeTitleHelperInstruction(body)
		}
		return false
	}
	matchesSystemTitleInstruction := func(t string) bool {
		return strings.Contains(t, "naming a coding session") ||
			strings.Contains(t, "Return a short title") ||
			strings.Contains(t, "Write the title in the predominant language")
	}
	system := gjson.GetBytes(body, "system")
	if system.IsArray() {
		for _, part := range system.Array() {
			if matchesSystemTitleInstruction(part.Get("text").String()) {
				return true
			}
		}
	} else if matchesSystemTitleInstruction(system.String()) {
		return true
	}
	messages := gjson.GetBytes(body, "messages")
	if messages.IsArray() {
		for _, msg := range messages.Array() {
			if msg.Get("role").String() == "system" {
				content := msg.Get("content")
				if content.IsArray() {
					for _, part := range content.Array() {
						if matchesSystemTitleInstruction(part.Get("text").String()) {
							return true
						}
					}
				} else if matchesSystemTitleInstruction(content.String()) {
					return true
				}
			}
		}
	}
	return false
}

// referenceClaudePayloadHas1hTTL is ClaudePayloadHas1hTTL without the
// jsonMayContainASCII pre-filter (the v8.0.11 implementation).
func referenceClaudePayloadHas1hTTL(payload []byte) bool {
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return false
	}
	has1h := false
	checkBlock := func(item gjson.Result) bool {
		cc := item.Get("cache_control")
		if cc.IsObject() && cc.Get("ttl").String() == "1h" {
			has1h = true
			return false
		}
		return true
	}
	for _, key := range []string{"tools", "system"} {
		if arr := gjson.GetBytes(payload, key); arr.IsArray() {
			arr.ForEach(func(_, item gjson.Result) bool {
				return checkBlock(item)
			})
			if has1h {
				return true
			}
		}
	}
	if messages := gjson.GetBytes(payload, "messages"); messages.IsArray() {
		messages.ForEach(func(_, msg gjson.Result) bool {
			content := msg.Get("content")
			if content.IsArray() {
				content.ForEach(func(_, item gjson.Result) bool {
					return checkBlock(item)
				})
			}
			return !has1h
		})
	}
	return has1h
}

// bs is one backslash; the seeds below build JSON escapes from it.
const bs = `\`

var prefilterSeeds = []string{
	``,
	`{}`,
	`not json`,
	`{"system":"naming a coding session"}`,
	`{"system":"naming a` + bs + `u0020coding session"}`,
	`{"system":[{"text":"Return a short ` + bs + `u0074itle"}]}`,
	`{"system":"Return a short title`,
	`{"messages":[{"role":"system","content":[{"text":"Write the title in the predominant language"}]}]}`,
	`{"messages":[{"role":"system","content":"naming a coding session"}`,
	`{"output_config":{"format":{"schema":{"properties":{"title":{}}}}},"messages":[{"content":"<session>"}]}`,
	`{"output_` + bs + `u0063onfig":{"format":{"schema":{"properties":{"title":{}}}}},"system":"Return a short title"}`,
	`{"output_config":{"format":{"schema":{"properties":{"title":{},"x":{}}}}}}`,
	`{"system":"` + bs + `ud83d` + bs + `ude00 naming a coding session"}`,
	`{"system":"` + bs + `/naming a coding session` + bs + `"}`,
	`{"system":"` + bs + bs + `u0041 naming"}`,
	`{"system":"` + bs + `u00`,
	`{"messages":[{"content":[{"cache_control":{"ttl":"1h"}}]}]}`,
	`{"messages":[{"content":[{"cache_control":{"ttl":"` + bs + `u0031h"}}]}]}`,
	`{"tools":[{"cache_control":{"ttl":"1` + bs + `u0068"}}]}`,
	`{"system":[{"cache_control":{"ttl":"5m"}}],"note":"1h"}`,
	`{"tools":[{"cache_control":{"ttl":"1h"}}]}`,
	`{"messages":[{"content":[{"cache_control":{"ttl":1}}]}]}`,
}

func checkPrefilterEquivalence(t *testing.T, body []byte) {
	t.Helper()
	if got, want := isClaudeTitleHelperRequest(body), referenceIsClaudeTitleHelperRequest(body); got != want {
		t.Fatalf("isClaudeTitleHelperRequest(%q) = %v, reference %v", body, got, want)
	}
	if got, want := ClaudePayloadHas1hTTL(body), referenceClaudePayloadHas1hTTL(body); got != want {
		t.Fatalf("ClaudePayloadHas1hTTL(%q) = %v, reference %v", body, got, want)
	}
}

func TestPrefilterMatchesReferenceOnSeeds(t *testing.T) {
	for _, seed := range prefilterSeeds {
		checkPrefilterEquivalence(t, []byte(seed))
	}
}

// FuzzPrefilterMatchesReference checks the pre-filtered classifiers against
// the full walks on arbitrary bytes: go test -fuzz FuzzPrefilterMatchesReference
func FuzzPrefilterMatchesReference(f *testing.F) {
	for _, seed := range prefilterSeeds {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		checkPrefilterEquivalence(t, body)
	})
}
