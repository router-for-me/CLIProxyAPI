package helps

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// TokensPerSecondHeader is the gateway-generated generation throughput header.
// Handlers copy it to clients even when passthrough-headers is false.
const TokensPerSecondHeader = "X-CLIProxyAPI-Tokens-Per-Second"

// TokensPerSecondGatewayMarker marks that TokensPerSecondHeader was set by the
// gateway (not an upstream). Handlers only forward TPS when this marker is present
// and carries the process-local GatewayMeasuredTPSMarkerValue (upstream cannot forge it).
const TokensPerSecondGatewayMarker = "X-CLIProxyAPI-Gateway-Measured-TPS"

const tokensPerSecondHeader = TokensPerSecondHeader
const tokensPerSecondGatewayMarker = TokensPerSecondGatewayMarker

// gatewayMeasuredTPSMarkerValue is a process-local secret written into
// TokensPerSecondGatewayMarker. Upstream-supplied marker values never match.
var gatewayMeasuredTPSMarkerValue = newGatewayMeasuredTPSMarkerValue()

func newGatewayMeasuredTPSMarkerValue() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Extremely unlikely; fall back to a non-empty distinct token.
		return "gateway-local-tps-marker"
	}
	return hex.EncodeToString(b[:])
}

// GatewayMeasuredTPSMarkerValue returns the process-local marker value the gateway
// writes when it measures tokens/sec. Tests and handlers use this for provenance checks.
func GatewayMeasuredTPSMarkerValue() string {
	return gatewayMeasuredTPSMarkerValue
}

// IsGatewayMeasuredTPSMarker reports whether v is the process-local gateway marker.
func IsGatewayMeasuredTPSMarker(v string) bool {
	return v != "" && v == gatewayMeasuredTPSMarkerValue
}

// StripTokensPerSecondHeaders removes gateway telemetry headers so upstream-supplied
// TPS / marker pairs cannot establish provenance before the gateway attaches its own.
func StripTokensPerSecondHeaders(headers http.Header) {
	if headers == nil {
		return
	}
	headers.Del(tokensPerSecondHeader)
	headers.Del(tokensPerSecondGatewayMarker)
}

var usageObjectPaths = []string{
	"usage",
	"response.usage",
	"usageMetadata",
	"response.usageMetadata",
	"interaction.usage",
}

func reporterTokensPerSecond(reporter *UsageReporter, outputTokens int64) float64 {
	if reporter == nil {
		return 0
	}
	return usage.TokensPerSecond(outputTokens, reporter.latency(), reporter.ttftDuration())
}

func outputTokensFromUsageNode(node gjson.Result) int64 {
	for _, path := range []string{"completion_tokens", "output_tokens", "total_output_tokens", "candidatesTokenCount"} {
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
	// Always overwrite upstream-supplied tokens_per_second with the gateway measurement.
	updated, err := sjson.SetBytes(jsonBody, path+".tokens_per_second", tps)
	if err != nil {
		return jsonBody
	}
	return updated
}

// MeasuredTokensPerSecond returns gateway TPS for payload usage using the
// reporter's current latency/TTFT. Call this before response translation so
// post-generation gateway work is excluded from the response/header denominator.
func MeasuredTokensPerSecond(payload []byte, reporter *UsageReporter) float64 {
	if len(payload) == 0 || reporter == nil {
		return 0
	}
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return 0
	}
	return reporterTokensPerSecond(reporter, outputTokensFromUsageNode(usageNodeFromPayload(trimmed)))
}

// AttachTokensPerSecondRate writes a precomputed tokens_per_second into usage objects.
func AttachTokensPerSecondRate(payload []byte, tps float64) []byte {
	if len(payload) == 0 || tps <= 0 {
		return payload
	}
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return payload
	}
	updated := trimmed
	for _, path := range usageObjectPaths {
		updated = attachTokensPerSecondAt(updated, path, tps)
	}
	return updated
}

// AttachTokensPerSecond writes usage.tokens_per_second when TTFT is known.
func AttachTokensPerSecond(payload []byte, reporter *UsageReporter) []byte {
	return AttachTokensPerSecondRate(payload, MeasuredTokensPerSecond(payload, reporter))
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
	h := http.Header(headers)
	h.Set(tokensPerSecondHeader, fmt.Sprintf("%.3f", tps))
	h.Set(tokensPerSecondGatewayMarker, gatewayMeasuredTPSMarkerValue)
}

// AttachTokensPerSecondHeaderRate writes X-CLIProxyAPI-Tokens-Per-Second from a
// precomputed rate. Upstream-supplied TPS/marker headers are stripped first so
// only a gateway measurement can establish provenance for mergeGatewayTelemetryHeaders.
func AttachTokensPerSecondHeaderRate(headers map[string][]string, tps float64) {
	StripTokensPerSecondHeaders(http.Header(headers))
	setTokensPerSecondHeader(headers, tps)
}

// AttachTokensPerSecondHeader writes X-CLIProxyAPI-Tokens-Per-Second when TTFT is known.
// Upstream-supplied TPS/marker headers are stripped first so only a gateway measurement
// can establish provenance for mergeGatewayTelemetryHeaders.
func AttachTokensPerSecondHeader(headers map[string][]string, payload []byte, reporter *UsageReporter) {
	attachTokensPerSecondHeader(headers, payload, reporter)
}

func attachTokensPerSecondHeader(headers map[string][]string, payload []byte, reporter *UsageReporter) {
	AttachTokensPerSecondHeaderRate(headers, MeasuredTokensPerSecond(payload, reporter))
}
