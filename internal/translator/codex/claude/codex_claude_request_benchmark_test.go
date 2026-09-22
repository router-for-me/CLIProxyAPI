package claude

import (
	"strconv"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func BenchmarkConvertClaudeRequestToCodexLargeHistory(b *testing.B) {
	for _, turns := range []int{16, 64} {
		b.Run(strconv.Itoa(turns)+"_turns", func(b *testing.B) {
			request := largeClaudeRequest(turns, 32, 8*1024)
			if !gjson.ValidBytes(request) {
				b.Fatal("benchmark generated an invalid Claude request")
			}
			if result := ConvertClaudeRequestToCodex("gpt-5.4", request, false); !gjson.ValidBytes(result) {
				b.Fatal("translator generated invalid Codex JSON")
			}
			b.ReportAllocs()
			b.SetBytes(int64(len(request)))
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				ConvertClaudeRequestToCodex("gpt-5.4", request, false)
			}
		})
	}
}

func largeClaudeRequest(turns, toolCount, payloadSize int) []byte {
	payload := strings.Repeat("x", payloadSize)
	var request strings.Builder
	request.Grow((turns + toolCount) * payloadSize)
	request.WriteString(`{"model":"claude-test","system":[{"type":"text","text":"`)
	request.WriteString(payload)
	request.WriteString(`"}],"messages":[`)

	for i := 0; i < turns; i++ {
		if i > 0 {
			request.WriteByte(',')
		}
		request.WriteString(`{"role":"assistant","content":[{"type":"text","text":"`)
		request.WriteString(payload)
		request.WriteString(`"},{"type":"tool_use","id":"toolu_`)
		request.WriteString(strconv.Itoa(i))
		request.WriteString(`","name":"tool_`)
		request.WriteString(strconv.Itoa(i % toolCount))
		request.WriteString(`","input":{"value":"`)
		request.WriteString(payload)
		request.WriteString(`"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_`)
		request.WriteString(strconv.Itoa(i))
		request.WriteString(`","content":[{"type":"text","text":"`)
		request.WriteString(payload)
		request.WriteString(`"}]}]}`)
	}

	request.WriteString(`],"tools":[`)
	for i := 0; i < toolCount; i++ {
		if i > 0 {
			request.WriteByte(',')
		}
		request.WriteString(`{"name":"tool_`)
		request.WriteString(strconv.Itoa(i))
		request.WriteString(`","description":"`)
		request.WriteString(payload)
		request.WriteString(`","input_schema":{"type":"object","properties":{"value":{"type":"string"}}}}`)
	}
	request.WriteString(`]}`)
	return []byte(request.String())
}
