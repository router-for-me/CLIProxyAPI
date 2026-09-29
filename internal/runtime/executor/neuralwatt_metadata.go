package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/tidwall/gjson"
)

// neuralwattResponseSink captures Neuralwatt's provider-specific response
// metadata (cost, cache savings, energy, served service tier) into the usage
// record. It is stateless: the compat executor hands it one already-scanned
// SSE line per CaptureStreamChunk call, so no cross-call buffering is needed.
//
// Header reads use case-insensitive lookup (http.Header.Get). Body reads use
// gjson for field-tolerant parsing of the energy object. Numeric header values
// are trimmed and parsed defensively; a parse error drops the field rather
// than storing a sentinel.
type neuralwattResponseSink struct{}

// neuralwattStreamCostCommentPrefix is the SSE comment-line prefix that
// Neuralwatt uses to ship mid-stream cost updates. The remainder of the line,
// after this prefix, is a JSON object with flat numeric fields.
const neuralwattStreamCostCommentPrefix = ": cost "

// neuralwattMaxStreamChunkBytes bounds the per-call chunk size we'll parse for a
// cost comment. The SSE scanner can hand us multi-megabyte lines (e.g. an
// embedded payload), so we drop oversized chunks to avoid retaining them
// forever. 64 KiB is far larger than any legitimate cost comment.
const neuralwattMaxStreamChunkBytes = 64 * 1024

// CaptureResponse extracts the cost/energy/service-tier from a non-streaming
// response. Energy is recorded only when the body explicitly marks the
// measurement as available; an absent or false measurement_available leaves
// EnergyJoules at zero so downstream consumers never see a guessed value.
func (neuralwattResponseSink) CaptureResponse(ctx context.Context, headers http.Header, body []byte) {
	md := captureNeuralwattCostHeaders(headers)
	mergeNeuralwattEnergy(ctx, body, md)
	mergeNeuralwattFlexApplied(headers, md)
	if len(md) > 0 {
		helps.SetProviderUsageMetadata(ctx, "neuralwatt", md)
	}
}

// CaptureStreamHeaders records the served service tier from response headers.
func (neuralwattResponseSink) CaptureStreamHeaders(ctx context.Context, headers http.Header) {
	if headers == nil {
		return
	}
	if tier := strings.TrimSpace(headers.Get("X-NW-Service-Tier")); tier != "" {
		helps.SetProviderResponseServiceTier(ctx, tier)
	}
}

// CaptureStreamChunk parses Neuralwatt's SSE cost comment lines. Chunks are
// already split into single SSE lines by the compat executor's scanner, so
// no cross-call buffering is required: a line beginning with ": cost " is
// fully self-contained. Oversized chunks (>64 KiB) are dropped defensively
// to avoid retaining runaway buffers.
func (neuralwattResponseSink) CaptureStreamChunk(ctx context.Context, chunk []byte) {
	if len(chunk) == 0 || len(chunk) > neuralwattMaxStreamChunkBytes {
		return
	}
	if !bytes.HasPrefix(chunk, []byte(neuralwattStreamCostCommentPrefix)) {
		return
	}
	payload := bytes.TrimSpace(chunk[len(neuralwattStreamCostCommentPrefix):])
	if len(payload) == 0 {
		return
	}
	var parsed map[string]any
	if err := json.Unmarshal(payload, &parsed); err != nil {
		// Wire-format mismatch (not a cost comment after all); a bad line
		// must not fail the response — consumers simply miss this update.
		return
	}
	helps.SetProviderUsageMetadata(ctx, "neuralwatt", parsed)
}

// captureNeuralwattCostHeaders extracts the per-request cost / cache /
// allowance fields from response headers. Returns an empty map when no header
// is present so the caller can skip the SetProviderUsageMetadata call.
func captureNeuralwattCostHeaders(headers http.Header) map[string]any {
	if headers == nil {
		return nil
	}
	md := map[string]any{}
	if raw := headers.Get("X-Request-Cost-USD"); raw != "" {
		if v, ok := parseNeuralwattFloat(raw); ok {
			md["request_cost_usd"] = v
		}
	}
	if raw := headers.Get("X-Cache-Savings-USD"); raw != "" {
		if v, ok := parseNeuralwattFloat(raw); ok {
			md["cache_savings_usd"] = v
		}
	}
	if raw := headers.Get("X-Allowance-Remaining-USD"); raw != "" {
		if v, ok := parseNeuralwattFloat(raw); ok {
			md["allowance_remaining_usd"] = v
		}
	}
	if tier := strings.TrimSpace(headers.Get("X-NW-Service-Tier")); tier != "" {
		md["service_tier"] = tier
	}
	if len(md) == 0 {
		return nil
	}
	return md
}

// mergeNeuralwattEnergy folds energy.* fields into md and sets
// ProviderEnergyJoules only when measurement_available is true. Numeric fields
// are parsed defensively; a missing or false measurement_available gate is
// the dominant case (most calls), so we short-circuit on that first.
func mergeNeuralwattEnergy(ctx context.Context, body []byte, md map[string]any) {
	if len(body) == 0 {
		return
	}
	energy := gjson.GetBytes(body, "energy")
	if !energy.IsObject() {
		return
	}
	if !gjson.GetBytes(body, "energy.measurement_available").Bool() {
		return
	}
	if joules := gjson.GetBytes(body, "energy.energy_joules"); joules.Exists() && joules.Type == gjson.Number {
		helps.SetProviderEnergyJoules(ctx, joules.Float())
	}
	if md == nil {
		return
	}
	if gridID := gjson.GetBytes(body, "energy.grid_id"); gridID.Exists() && gridID.Type == gjson.String {
		md["grid_id"] = gridID.String()
	}
	if carbon := gjson.GetBytes(body, "energy.carbon_g_co2eq"); carbon.Exists() && carbon.Type == gjson.Number {
		md["carbon_g_co2eq"] = carbon.Float()
	}
}

// mergeNeuralwattFlexApplied records whether the request was served under the
// flex tier (a header-only signal not always present in the body).
func mergeNeuralwattFlexApplied(headers http.Header, md map[string]any) {
	if headers == nil || md == nil {
		return
	}
	raw := strings.TrimSpace(headers.Get("X-Flex-Applied"))
	if raw == "" {
		return
	}
	if v, err := strconv.ParseBool(raw); err == nil {
		md["flex_applied"] = v
	}
}

// parseNeuralwattFloat parses a header value, returning the float and true on
// success. Whitespace is trimmed; an empty, malformed, NaN, or Inf value is
// skipped so we never store NaN/zero placeholders. strconv.ParseFloat returns
// err=nil for "NaN"/"Inf" inputs, so the math.IsNaN/IsInf guards are required
// to honour the docstring's guarantee.
func parseNeuralwattFloat(raw string) (float64, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(trimmed, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}
