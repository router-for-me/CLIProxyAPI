package openai

import (
	"context"
)

func ConvertJoyCodeStreamToOpenAI(ctx context.Context, model string, originalRequest, translatedRequest, chunk []byte, state *any) [][]byte {
	if len(chunk) == 0 {
		return nil
	}
	return [][]byte{chunk}
}

func ConvertJoyCodeNonStreamToOpenAI(ctx context.Context, model string, originalRequest, translatedRequest, response []byte, param *any) []byte {
	if len(response) == 0 {
		return nil
	}
	return response
}
