package executor

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	antigravitygemini "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/antigravity/gemini"
)

func buildSynthetic4KAntigravitySSE() ([]byte, []byte) {
	// ~8MB base64 payload representing a 4K JPEG/PNG inlineData response.
	largeB64 := strings.Repeat("QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVowMTIzNDU2Nzg5", 160000)
	var line1 bytes.Buffer
	line1.WriteString(`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"thoughtSignature":"sig_abc","inlineData":{"mimeType":"image/jpeg","data":"`)
	line1.WriteString(largeB64)
	line1.WriteString(`"}}]}}],"usageMetadata":{"promptTokenCount":15,"candidatesTokenCount":2000,"totalTokenCount":2015},"modelVersion":"gemini-3.1-flash-image","responseId":"resp_1"},"traceId":"trace_1","metadata":{}}`)
	line2 := []byte(`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":""}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":15,"candidatesTokenCount":2000,"totalTokenCount":2015},"modelVersion":"gemini-3.1-flash-image","responseId":"resp_1"},"traceId":"trace_1","metadata":{}}`)
	return line1.Bytes(), line2
}

func BenchmarkAntigravity4KImage_NonStreamPipeline(b *testing.B) {
	line1, line2 := buildSynthetic4KAntigravitySSE()
	reqPayload := []byte(`{"request":{"generationConfig":{"responseModalities":["TEXT","IMAGE"],"imageConfig":{"aspectRatio":"16:9","imageSize":"4K"}}}}`)
	exec := &AntigravityExecutor{}
	b.SetBytes(int64(len(line1) + len(line2)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rawPayloads := make([][]byte, 0, 2)
		for _, rawLine := range [][]byte{line1, line2} {
			if payload := helps.JSONPayload(rawLine); payload != nil {
				rawPayloads = append(rawPayloads, payload)
			}
		}
		assembled := exec.convertStreamChunksToNonStream(rawPayloads)
		converted := antigravitygemini.ConvertAntigravityResponseToGeminiNonStream(context.Background(), "gemini-3.1-flash-image", reqPayload, reqPayload, assembled, nil)
		detail := helps.ParseAntigravityUsage(assembled)
		helps.ApplyGeminiRequestImageConfig(&detail, reqPayload)
		if len(converted) == 0 || detail.ImageCount == nil || *detail.ImageCount != 1 {
			b.Fatal("unexpected result")
		}
	}
}
