package gemini

import (
	"bytes"
	"context"

	translatorcommon "github.com/router-for-me/CLIProxyAPI/v6/internal/translator/common"
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
