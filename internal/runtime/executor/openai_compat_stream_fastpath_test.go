package executor

import (
	"bytes"
	"context"
	"testing"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestTranslateOpenAICompatStreamLinePassesThroughSameFormatWithStableCopy(t *testing.T) {
	t.Parallel()

	line := []byte(`data: {"id":"chunk","choices":[{"delta":{"content":"hi"}}]}`)
	var param any
	chunks := translateOpenAICompatStreamLine(context.Background(), sdktranslator.FormatOpenAI, sdktranslator.FormatOpenAI, "gpt-test", nil, nil, line, &param)

	if len(chunks) != 1 {
		t.Fatalf("chunk count = %d, want 1", len(chunks))
	}
	if !bytes.Equal(chunks[0], line) {
		t.Fatalf("chunk = %q, want %q", string(chunks[0]), string(line))
	}

	line[0] = 'X'
	if chunks[0][0] == 'X' {
		t.Fatal("expected passthrough chunk to be stable after scanner buffer mutation")
	}
}
