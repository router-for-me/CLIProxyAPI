package helps

import (
	"bytes"
	"fmt"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// TokensPerSecondHeader is the gateway-generated generation throughput header.
// Handlers copy it to clients even when passthrough-headers is false.
const TokensPerSecondHeader = "X-CLIProxyAPI-Tokens-Per-Second"

const tokensPerSecondHeader = TokensPerSecondHeader

var usageObjectPaths = []string{
	"usage",
	"response.usage",
	"usageMetadata",
	"response.usageMetadata",
}

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

func usageNodeFromPayload(payload []byte) gjson.Result {
	for _, path := range usageObjectPaths {
		node := gjson.GetBytes(payload, path)
		if node.Exists() && node.IsObject() {
			if tokens := outputTokensFromUsageNode(node); tokens > 0 {
				return node
			}
		}
	}
	for _, path := range usageObjectPaths {
		node := gjson.GetBytes(payload, path)
		if node.Exists() && node.IsObject() {
			return node
		}
	}
	return gjson.Result{}
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
	tps := reporterTokensPerSecond(reporter, outputTokensFromUsageNode(usageNodeFromPayload(trimmed)))
	updated := trimmed
	for _, path := range usageObjectPaths {
		updated = attachTokensPerSecondAt(updated, path, tps)
	}
	return updated
}

func jsonPayloadFromSSE(line []byte) []byte {
	if payload := jsonPayload(line); len(payload) > 0 {
		return payload
	}
	for _, part := range bytes.Split(line, []byte("\n")) {
		trimmed := bytes.TrimSpace(part)
		if !bytes.HasPrefix(trimmed, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(trimmed[len("data:"):])
		if len(data) > 0 && data[0] == '{' {
			return data
		}
	}
	return nil
}

// AttachStreamTokensPerSecond patches an SSE data line the same way.
func AttachStreamTokensPerSecond(line []byte, reporter *UsageReporter) []byte {
	if reporter == nil || !(bytes.Contains(line, []byte(`"usage"`)) || bytes.Contains(line, []byte(`"usageMetadata"`))) {
		return line
	}
	payload := jsonPayloadFromSSE(line)
	if len(payload) == 0 || payload[0] != '{' {
		return line
	}
	updated := AttachTokensPerSecond(payload, reporter)
	if bytes.Equal(updated, payload) {
		return line
	}
	return bytes.Replace(line, payload, updated, 1)
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
	setTokensPerSecondHeader(headers, reporterTokensPerSecond(reporter, outputTokensFromUsageNode(usageNodeFromPayload(payload))))
}
