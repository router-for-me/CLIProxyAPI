package executor

import (
	"context"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
)

func TestNeuralwattSinkCapturesCostHeadersAndEnergy(t *testing.T) {
	ctx := helps.EnsureProviderUsageMetadata(context.Background())
	headers := http.Header{}
	headers.Set("X-Request-Cost-USD", "0.003400")
	headers.Set("X-Cache-Savings-USD", "0.027")
	headers.Set("X-Allowance-Remaining-USD", "47.66")
	headers.Set("X-NW-Service-Tier", "flex")
	body := []byte(`{"usage":{"prompt_tokens":10,"completion_tokens":5},"energy":{"energy_joules":42.5,"measurement_available":true,"grid_id":"us-west-2","carbon_g_co2eq":3.2}}`)

	neuralwattResponseSink{}.CaptureResponse(ctx, headers, body)

	md := helps.ProviderUsageMetadataFromContext(ctx)
	if md.EnergyJoules != 42.5 {
		t.Fatalf("EnergyJoules = %v, want 42.5", md.EnergyJoules)
	}
	nw, ok := md.Metadata["neuralwatt"].(map[string]any)
	if !ok {
		t.Fatalf("neuralwatt metadata missing: %+v", md.Metadata)
	}
	if nw["request_cost_usd"] != 0.0034 {
		t.Fatalf("request_cost_usd = %v, want 0.0034", nw["request_cost_usd"])
	}
}

func TestNeuralwattSinkOmitsEnergyWhenUnmeasured(t *testing.T) {
	ctx := helps.EnsureProviderUsageMetadata(context.Background())
	body := []byte(`{"usage":{"prompt_tokens":1},"energy":{"measurement_available":false}}`)

	neuralwattResponseSink{}.CaptureResponse(ctx, http.Header{}, body)

	md := helps.ProviderUsageMetadataFromContext(ctx)
	if md.EnergyJoules != 0 {
		t.Fatalf("EnergyJoules = %v, want 0 for an unmeasured response", md.EnergyJoules)
	}
}

func TestNeuralwattSinkParsesStreamCostComment(t *testing.T) {
	ctx := helps.EnsureProviderUsageMetadata(context.Background())
	sink := neuralwattResponseSink{}
	sink.CaptureStreamHeaders(ctx, http.Header{"X-Nw-Service-Tier": []string{"standard"}})
	sink.CaptureStreamChunk(ctx, []byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"))
	sink.CaptureStreamChunk(ctx, []byte(": cost {\"request_cost_usd\": 0.0034, \"cache_savings_usd\": 0.027, \"allowance_remaining_usd\": 47.66}\n"))
	sink.CaptureStreamChunk(ctx, []byte("data: [DONE]\n\n"))

	md := helps.ProviderUsageMetadataFromContext(ctx)
	nw, ok := md.Metadata["neuralwatt"].(map[string]any)
	if !ok {
		t.Fatalf("neuralwatt metadata missing: %+v", md.Metadata)
	}
	if nw["request_cost_usd"] != 0.0034 {
		t.Fatalf("request_cost_usd = %v, want 0.0034", nw["request_cost_usd"])
	}
}
