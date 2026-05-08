package translator

import (
	"bytes"
	"context"
	"testing"
)

func TestRegistryTranslateStreamReturnsByteChunks(t *testing.T) {
	registry := NewRegistry()
	registry.Register(FormatOpenAI, FormatGemini, nil, ResponseTransform{
		Stream: func(ctx context.Context, model string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
			return [][]byte{append([]byte(nil), rawJSON...)}
		},
	})

	got := registry.TranslateStream(context.Background(), FormatGemini, FormatOpenAI, "model", nil, nil, []byte(`{"chunk":true}`), nil)
	if len(got) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(got))
	}
	if !bytes.Equal(got[0], []byte(`{"chunk":true}`)) {
		t.Fatalf("unexpected chunk: %s", got[0])
	}
}

func TestRegistryTranslateNonStreamReturnsBytes(t *testing.T) {
	registry := NewRegistry()
	registry.Register(FormatOpenAI, FormatGemini, nil, ResponseTransform{
		NonStream: func(ctx context.Context, model string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) []byte {
			return append([]byte(nil), rawJSON...)
		},
	})

	got := registry.TranslateNonStream(context.Background(), FormatGemini, FormatOpenAI, "model", nil, nil, []byte(`{"done":true}`), nil)
	if !bytes.Equal(got, []byte(`{"done":true}`)) {
		t.Fatalf("unexpected payload: %s", got)
	}
}

func TestRegistryTranslateTokenCountReturnsBytes(t *testing.T) {
	registry := NewRegistry()
	registry.Register(FormatOpenAI, FormatGemini, nil, ResponseTransform{
		TokenCount: func(ctx context.Context, count int64) []byte {
			return []byte(`{"totalTokens":7}`)
		},
	})

	got := registry.TranslateTokenCount(context.Background(), FormatGemini, FormatOpenAI, 7, []byte(`{"fallback":true}`))
	if !bytes.Equal(got, []byte(`{"totalTokens":7}`)) {
		t.Fatalf("unexpected payload: %s", got)
	}
}
