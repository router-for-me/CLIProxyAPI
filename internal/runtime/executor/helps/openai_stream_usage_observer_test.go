package helps

import (
	"fmt"
	"strings"
	"testing"
)

func TestOpenAIStreamUsageObserverChunkBoundaries(t *testing.T) {
	const limit = 256 * 1024
	image := `{"type":"image_generation.completed","b64_json":"` + strings.Repeat("A", 100000) + `","usage":{"input_tokens":10,"output_tokens":20,"total_tokens":30}}`
	for _, stream := range []string{
		"event: done\ndata: " + image + "\n\n",
		": keepalive\r\nevent: done\r\ndata: " + image + "\r\n\r\n",
		"data: " + strings.Replace(image, `,"usage":`, ",\ndata: \"usage\":", 1) + "\n\n",
		"data: " + image,
	} {
		for _, chunkSize := range []int{1, 7, 32768, len(stream)} {
			t.Run(fmt.Sprintf("stream_bytes=%d/chunk=%d", len(stream), chunkSize), func(t *testing.T) {
				var b StreamUsageBuffer
				observer := NewOpenAIStreamUsageObserver(&b, limit)
				for remaining := []byte(stream); len(remaining) > 0; {
					n := min(chunkSize, len(remaining))
					observer.Feed(remaining[:n])
					remaining = remaining[n:]
				}
				observer.Finish()
				observer.Finish()
				d, ok := b.Detail()
				if !ok || d.TotalTokens != 30 || d.ImageCount == nil || *d.ImageCount != 1 || b.openAIStreamImages != 1 {
					t.Fatalf("chunk framing lost or duplicated image usage: %+v", d)
				}
			})
		}
	}
}

func TestOpenAIStreamUsageObserverBoundsAndRecovery(t *testing.T) {
	const limit = 1024
	oversize := "data: " + strings.Repeat("A", limit*2) + "\n\n"
	manyLines := strings.Repeat("data: "+strings.Repeat("B", 100)+"\n", 20) + "\n"
	valid := "data: {\"usage\":{\"input_tokens\":1,\"output_tokens\":2,\"total_tokens\":3}}\n\n"
	for _, input := range []string{oversize, manyLines} {
		var b StreamUsageBuffer
		observer := NewOpenAIStreamUsageObserver(&b, limit)
		for _, c := range []byte(input + valid) {
			observer.Feed([]byte{c})
			if len(observer.line) > limit || observer.dataBytes > limit {
				t.Fatal("observer exceeded its buffer bound")
			}
		}
		observer.Finish()
		d, ok := b.Detail()
		if !ok || d.TotalTokens != 3 {
			t.Fatalf("observer did not recover after oversized event: %+v", d)
		}
	}
}
