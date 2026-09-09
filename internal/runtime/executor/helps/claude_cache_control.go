package helps

import (
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
)

// ClaudeRequestsExtendedCacheTTL reports an explicit, paired 1h cache request.
// Callers decide whether the client is trusted to own its cache policy.
func ClaudeRequestsExtendedCacheTTL(headers http.Header, body []byte) bool {
	hasBeta := false
	for _, value := range HeaderValuesCaseInsensitive(headers, "Anthropic-Beta") {
		for _, beta := range strings.Split(value, ",") {
			if strings.TrimSpace(beta) == "extended-cache-ttl-2025-04-11" {
				hasBeta = true
			}
		}
	}
	if !hasBeta {
		return false
	}
	hasTTL := func(block gjson.Result) bool {
		return block.Get("cache_control.type").String() == "ephemeral" && block.Get("cache_control.ttl").String() == "1h"
	}
	for _, field := range []string{"tools", "system"} {
		for _, block := range gjson.GetBytes(body, field).Array() {
			if hasTTL(block) {
				return true
			}
		}
	}
	for _, message := range gjson.GetBytes(body, "messages").Array() {
		for _, block := range message.Get("content").Array() {
			if hasTTL(block) {
				return true
			}
		}
	}
	return false
}
