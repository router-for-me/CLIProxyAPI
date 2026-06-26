package gemini

import (
	"bytes"
	"context"

<<<<<<< HEAD:pkg/llmproxy/translator/gemini/gemini/gemini_gemini_response.go
	translatorcommon "github.com/kooshapari/CLIProxyAPI/v7/internal/translator/common"
=======
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/common"
>>>>>>> upstream/main:internal/translator/gemini/gemini/gemini_gemini_response.go
)

// PassthroughGeminiResponseStream forwards Gemini responses unchanged.
func PassthroughGeminiResponseStream(_ context.Context, _ string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, _ *any) [][]byte {
	if bytes.HasPrefix(rawJSON, []byte("data:")) {
		rawJSON = bytes.TrimSpace(rawJSON[5:])
	}

	if bytes.Equal(rawJSON, []byte("[DONE]")) {
		return [][]byte{}
	}

	return [][]byte{rawJSON}
}

// PassthroughGeminiResponseNonStream forwards Gemini responses unchanged.
func PassthroughGeminiResponseNonStream(_ context.Context, _ string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, _ *any) []byte {
	return rawJSON
}

func GeminiTokenCount(ctx context.Context, count int64) []byte {
	return translatorcommon.GeminiTokenCountJSON(count)
}
