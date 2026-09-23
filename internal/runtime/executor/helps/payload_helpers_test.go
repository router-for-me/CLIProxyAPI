package helps

import (
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// TestPayloadRequestedModel pins the metadata-over-fallback precedence that
// every executor SetRouteModel call site depends on: at executor entry
// req.Model is already the alias-resolved upstream model, so the true client
// name must come from Options.Metadata; the fallback is only a degradation to
// "no substitution info", never the client name itself.
func TestPayloadRequestedModel(t *testing.T) {
	testCases := []struct {
		name     string
		metadata map[string]any
		fallback string
		want     string
	}{
		{
			name:     "metadata string wins over fallback",
			metadata: map[string]any{cliproxyexecutor.RequestedModelMetadataKey: "claude-opus-5-client"},
			fallback: "claude-opus-5-resolved",
			want:     "claude-opus-5-client",
		},
		{
			name:     "metadata string is trimmed",
			metadata: map[string]any{cliproxyexecutor.RequestedModelMetadataKey: "  client-model  "},
			fallback: "resolved-model",
			want:     "client-model",
		},
		{
			name:     "whitespace-only metadata string falls back",
			metadata: map[string]any{cliproxyexecutor.RequestedModelMetadataKey: "   "},
			fallback: "resolved-model",
			want:     "resolved-model",
		},
		{
			name:     "metadata bytes win over fallback",
			metadata: map[string]any{cliproxyexecutor.RequestedModelMetadataKey: []byte("bytes-model")},
			fallback: "resolved-model",
			want:     "bytes-model",
		},
		{
			name:     "metadata bytes are trimmed",
			metadata: map[string]any{cliproxyexecutor.RequestedModelMetadataKey: []byte(" bytes-model ")},
			fallback: "resolved-model",
			want:     "bytes-model",
		},
		{
			name:     "empty metadata bytes fall back",
			metadata: map[string]any{cliproxyexecutor.RequestedModelMetadataKey: []byte("")},
			fallback: "resolved-model",
			want:     "resolved-model",
		},
		{
			name:     "whitespace-only metadata bytes fall back",
			metadata: map[string]any{cliproxyexecutor.RequestedModelMetadataKey: []byte("   ")},
			fallback: "resolved-model",
			want:     "resolved-model",
		},
		{
			name:     "nil metadata value falls back",
			metadata: map[string]any{cliproxyexecutor.RequestedModelMetadataKey: nil},
			fallback: "resolved-model",
			want:     "resolved-model",
		},
		{
			name:     "unsupported value type falls back",
			metadata: map[string]any{cliproxyexecutor.RequestedModelMetadataKey: 42},
			fallback: "resolved-model",
			want:     "resolved-model",
		},
		{
			name:     "absent key falls back",
			metadata: map[string]any{"other": "value"},
			fallback: "resolved-model",
			want:     "resolved-model",
		},
		{
			name:     "nil metadata map falls back",
			metadata: nil,
			fallback: "resolved-model",
			want:     "resolved-model",
		},
		{
			name:     "fallback is trimmed",
			metadata: nil,
			fallback: "  resolved-model  ",
			want:     "resolved-model",
		},
		{
			name:     "both empty stays empty",
			metadata: map[string]any{cliproxyexecutor.RequestedModelMetadataKey: ""},
			fallback: "",
			want:     "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			opts := cliproxyexecutor.Options{Metadata: tc.metadata}
			if got := PayloadRequestedModel(opts, tc.fallback); got != tc.want {
				t.Fatalf("PayloadRequestedModel() = %q, want %q", got, tc.want)
			}
		})
	}
}
