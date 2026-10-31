package helps

import (
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/tiktoken-go/tokenizer"
)

type countingCodec struct {
	tokenizer.Codec
	calls   atomic.Int64
	maxSize atomic.Int64
}

func (c *countingCodec) Count(text string) (int, error) {
	c.calls.Add(1)
	if size := int64(len(text)); size > c.maxSize.Load() {
		c.maxSize.Store(size)
	}
	return c.Codec.Count(text)
}

func resetSegmentTokenCache() {
	segmentTokenCacheMu.Lock()
	clear(segmentTokenCache)
	segmentTokenCacheMu.Unlock()
}

func longClaudeRequest(tag string, tools, messages int) []byte {
	var sb strings.Builder
	sb.WriteString(`{"system":"You are a coding assistant.","tools":[`)
	for i := 0; i < tools; i++ {
		if i > 0 {
			sb.WriteByte(',')
		}
		fmt.Fprintf(&sb, `{"name":"tool_%s_%d","description":%q,"input_schema":{"type":"object","properties":{"path":{"type":"string"}}}}`,
			tag, i, strings.Repeat("Reads and writes files, runs commands, searches code. ", 20))
	}
	sb.WriteString(`],"messages":[`)
	for i := 0; i < messages; i++ {
		if i > 0 {
			sb.WriteByte(',')
		}
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		fmt.Fprintf(&sb, `{"role":%q,"content":[{"type":"text","text":%q}]}`, role,
			strings.Repeat(fmt.Sprintf("message %s-%d: func main() { return } error: undefined x. ", tag, i), 15))
	}
	sb.WriteString(`]}`)
	return []byte(sb.String())
}

func TestCountClaudeInputTokens_CacheSkipsRepeatedSegments(t *testing.T) {
	enc, err := claudeInputTokenizer()
	if err != nil {
		t.Fatalf("tokenizer: %v", err)
	}
	resetSegmentTokenCache()
	codec := &countingCodec{Codec: enc}
	payload := longClaudeRequest("a", 5, 6)

	cold, err := countClaudeInputTokens(codec, payload)
	if err != nil {
		t.Fatal(err)
	}
	if codec.maxSize.Load() < segmentTokenCacheMinBytes {
		t.Fatalf("cold run never tokenized a long segment (max %d bytes); test payload is too small", codec.maxSize.Load())
	}
	codec.maxSize.Store(0)
	warm, err := countClaudeInputTokens(codec, payload)
	if err != nil {
		t.Fatal(err)
	}
	if warm != cold {
		t.Fatalf("warm count = %d, want %d", warm, cold)
	}
	// Every long segment is a cache hit; only short segments are tokenized again.
	if largest := codec.maxSize.Load(); largest >= segmentTokenCacheMinBytes {
		t.Fatalf("warm run tokenized a %d byte segment, want none of %d bytes or more", largest, segmentTokenCacheMinBytes)
	}

	// Appending one message only tokenizes that message.
	codec.maxSize.Store(0)
	if _, err = countClaudeInputTokens(codec, longClaudeRequest("a", 5, 7)); err != nil {
		t.Fatal(err)
	}
	if codec.maxSize.Load() < segmentTokenCacheMinBytes {
		t.Fatalf("new long message was not tokenized (max %d bytes)", codec.maxSize.Load())
	}
}

func TestCountClaudeInputTokens_CachedCountStaysCloseToJoinedCount(t *testing.T) {
	enc, err := claudeInputTokenizer()
	if err != nil {
		t.Fatalf("tokenizer: %v", err)
	}
	resetSegmentTokenCache()
	payload := longClaudeRequest("b", 4, 8)

	segments, err := collectClaudeInputTokenSegments(payload)
	if err != nil {
		t.Fatal(err)
	}
	exact, err := enc.Count(strings.Join(segments, "\n"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := countClaudeInputTokens(enc, payload)
	if err != nil {
		t.Fatal(err)
	}
	// Merging across a segment boundary can differ by at most one token per boundary.
	diff := int(got) - exact
	if diff < 0 {
		diff = -diff
	}
	if diff > len(segments) {
		t.Fatalf("cached count = %d, joined count = %d, diff %d exceeds %d boundaries", got, exact, diff, len(segments))
	}
}

func BenchmarkCountClaudeInputTokens_ColdVsWarm(b *testing.B) {
	enc, err := claudeInputTokenizer()
	if err != nil {
		b.Fatalf("tokenizer: %v", err)
	}
	payload := longClaudeRequest("c", 40, 40)
	b.Run("cold", func(b *testing.B) {
		b.SetBytes(int64(len(payload)))
		for i := 0; i < b.N; i++ {
			resetSegmentTokenCache()
			if _, err := countClaudeInputTokens(enc, payload); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("warm", func(b *testing.B) {
		resetSegmentTokenCache()
		if _, err := countClaudeInputTokens(enc, payload); err != nil {
			b.Fatal(err)
		}
		b.SetBytes(int64(len(payload)))
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, err := countClaudeInputTokens(enc, payload); err != nil {
				b.Fatal(err)
			}
		}
	})
}
