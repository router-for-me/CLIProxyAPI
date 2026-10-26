package executor

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// codexBenchPayload builds a Responses request of about size bytes whose turns mix common
// words and random identifiers, so it compresses like real conversation history (~3:1).
func codexBenchPayload(size int) []byte {
	r := rand.New(rand.NewSource(1))
	words := strings.Fields("the function return error context request response model input output tool call result json string value index token cache stream event message user assistant content type text file path line import package struct interface")
	var b strings.Builder
	b.WriteString(`{"model":"gpt-5.4","input":[`)
	for b.Len() < size {
		b.WriteString(`{"role":"user","content":"`)
		for i, n := 0, 20+r.Intn(80); i < n; i++ {
			b.WriteString(words[r.Intn(len(words))])
			if r.Intn(9) == 0 {
				fmt.Fprintf(&b, "_%x", r.Int63())
			}
			b.WriteByte(' ')
		}
		b.WriteString(`"},`)
	}
	b.WriteString(`{"role":"user","content":"hi"}]}`)
	return []byte(b.String())
}

// BenchmarkCodexRequestCompression builds compressed Codex requests concurrently, as a busy
// proxy does, through the same path as Execute.
func BenchmarkCodexRequestCompression(b *testing.B) {
	for _, size := range []int{16 << 10, 256 << 10, 2 << 20} {
		payload := codexBenchPayload(size)
		b.Run(fmt.Sprintf("%dKiB", size>>10), func(b *testing.B) {
			executor := NewCodexExecutor(codexCompressionConfig())
			auth := codexCompressionAuth()
			target := "https://chatgpt.com/backend-api/codex/responses"
			b.SetBytes(int64(len(payload)))
			b.ReportAllocs()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					req, _, errCache := executor.cacheHelper(b.Context(), sdktranslator.FormatOpenAIResponse, target, cliproxyexecutor.Request{Payload: payload}, payload, auth)
					if errCache != nil {
						b.Fatal(errCache)
					}
					if req.Header.Get("Content-Encoding") != "zstd" {
						b.Fatal("request not compressed")
					}
					_ = req.Body.Close()
				}
			})
		})
	}
}
