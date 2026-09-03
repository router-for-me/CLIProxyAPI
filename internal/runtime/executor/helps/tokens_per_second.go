package helps

import (
	"bytes"
	"fmt"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const tokensPerSecondHeader = "X-CLIProxyAPI-Tokens-Per-Second"

func reporterTokensPerSecond(reporter *UsageReporter, outputTokens int64) float64 {
	if reporter == nil {
		return 0
	}
	return usage.TokensPerSecond(outputTokens, reporter.latency(), reporter.ttftDuration())
}

func outputTokensFromUsageNode(node gjson.Result) int64 {
	for _, path := range []string{"completion_tokens", "output_tokens", "candidatesTokenCount"} {
		value := node.Get(path)
		if value.Exists() && value.Type != gjson.Null {
			return value.Int()
		}
	}
	return 0
}

func attachTokensPerSecondAt(jsonBody []byte, path string, tps float64) []byte {
	if tps <= 0 {
		return jsonBody
	}
	node := gjson.GetBytes(jsonBody, path)
	if !node.Exists() || !node.IsObject() {
		return jsonBody
	}
	if node.Get("tokens_per_second").Exists() {
		return jsonBody
	}
	updated, err := sjson.SetBytes(jsonBody, path+".tokens_per_second", tps)
	if err != nil {
		return jsonBody
	}
	return updated
}

// AttachTokensPerSecond writes usage.tokens_per_second when TTFT is known.
func AttachTokensPerSecond(payload []byte, reporter *UsageReporter) []byte {
	if len(payload) == 0 || reporter == nil {
		return payload
	}
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return payload
	}
	usageNode := gjson.GetBytes(trimmed, "usage")
	if !usageNode.Exists() {
		usageNode = gjson.GetBytes(trimmed, "response.usage")
	}
	tps := reporterTokensPerSecond(reporter, outputTokensFromUsageNode(usageNode))
	updated := attachTokensPerSecondAt(trimmed, "usage", tps)
	return attachTokensPerSecondAt(updated, "response.usage", tps)
}

// AttachStreamTokensPerSecond patches an SSE data line the same way.
func AttachStreamTokensPerSecond(line []byte, reporter *UsageReporter) []byte {
	if reporter == nil || !bytes.Contains(line, []byte(`"usage"`)) {
		return line
	}
	payload := jsonPayload(line)
	if len(payload) == 0 || payload[0] != '{' {
		return line
	}
	updated := AttachTokensPerSecond(payload, reporter)
	if bytes.Equal(updated, payload) {
		return line
	}
	prefix := line[:len(line)-len(payload)]
	if bytes.HasPrefix(bytes.TrimSpace(line), []byte("data:")) {
		return append(append([]byte(nil), prefix...), updated...)
	}
	return updated
}

func setTokensPerSecondHeader(headers map[string][]string, tps float64) {
	if headers == nil || tps <= 0 {
		return
	}
	http.Header(headers).Set(tokensPerSecondHeader, fmt.Sprintf("%.3f", tps))
}

// AttachTokensPerSecondHeader writes X-CLIProxyAPI-Tokens-Per-Second when TTFT is known.
func AttachTokensPerSecondHeader(headers map[string][]string, payload []byte, reporter *UsageReporter) {
	attachTokensPerSecondHeader(headers, payload, reporter)
}

func attachTokensPerSecondHeader(headers map[string][]string, payload []byte, reporter *UsageReporter) {
	if reporter == nil {
		return
	}
	usageNode := gjson.GetBytes(payload, "usage")
	if !usageNode.Exists() {
		usageNode = gjson.GetBytes(payload, "response.usage")
	}
	setTokensPerSecondHeader(headers, reporterTokensPerSecond(reporter, outputTokensFromUsageNode(usageNode)))
}
