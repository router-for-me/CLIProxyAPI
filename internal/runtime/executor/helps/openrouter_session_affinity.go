package helps

import (
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
)

// ApplyOpenRouterSessionAffinity gives OpenRouter an explicit sticky-routing key.
func ApplyOpenRouterSessionAffinity(req *http.Request, payload []byte) {
	if req == nil || req.URL == nil || !isOpenRouterHost(req.URL.Hostname()) || req.Header.Get("X-Session-ID") != "" {
		return
	}
	if promptCacheKey := strings.TrimSpace(gjson.GetBytes(payload, "prompt_cache_key").String()); promptCacheKey != "" {
		req.Header.Set("X-Session-ID", promptCacheKey)
	}
}

func isOpenRouterHost(host string) bool {
	host = strings.TrimSpace(strings.ToLower(host))
	return host == "openrouter.ai" || strings.HasSuffix(host, ".openrouter.ai")
}
