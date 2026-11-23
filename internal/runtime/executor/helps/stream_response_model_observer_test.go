package helps

import (
	"context"
	"strings"
	"testing"
)

func TestStreamResponseModelObserver_ChunkSplit(t *testing.T) {
	// JSON split across network chunk boundaries (拆包)
	reporter := newMultiProviderTestReporter(context.Background(), "openai-compat", "dall-e-3", nil)
	observer := NewStreamResponseModelObserver(reporter)

	chunks := []string{
		`data: {"id":"img_1","cre`,
		`ated":1700000000,"mo`,
		`del":"dall-e-3","data":[]}` + "\n\n",
	}

	for _, c := range chunks {
		observer.Feed([]byte(c))
	}
	observer.Finish()

	if got := reporter.ResponseModel(); got != "dall-e-3" {
		t.Fatalf("ResponseModel() = %q, want %q", got, "dall-e-3")
	}
}

func TestStreamResponseModelObserver_MultiEventChunk(t *testing.T) {
	// Multiple SSE events packed into a single chunk (合包)
	reporter := newMultiProviderTestReporter(context.Background(), "openai-compat", "dall-e-3", nil)
	observer := NewStreamResponseModelObserver(reporter)

	chunk := "event: ping\ndata: {}\n\nevent: progress\ndata: {\"percent\":50}\n\nevent: completion\ndata: {\"model\":\"dall-e-3\",\"status\":\"completed\"}\n\n"
	observer.Feed([]byte(chunk))
	observer.Finish()

	if got := reporter.ResponseModel(); got != "dall-e-3" {
		t.Fatalf("ResponseModel() = %q, want %q", got, "dall-e-3")
	}
}

func TestStreamResponseModelObserver_EventPrefix(t *testing.T) {
	// Chunks starting with event: prefix
	reporter := newMultiProviderTestReporter(context.Background(), "openai-compat", "flux-pro", nil)
	observer := NewStreamResponseModelObserver(reporter)

	observer.Feed([]byte("event: image_generation\n"))
	observer.Feed([]byte("data: {\"model\":\"flux-pro\"}\n\n"))
	observer.Finish()

	if got := reporter.ResponseModel(); got != "flux-pro" {
		t.Fatalf("ResponseModel() = %q, want %q", got, "flux-pro")
	}
}

func TestStreamResponseModelObserver_MultiLineDataEvent(t *testing.T) {
	// Multi-line data fields within one SSE event
	reporter := newMultiProviderTestReporter(context.Background(), "openai-compat", "dall-e-3", nil)
	observer := NewStreamResponseModelObserver(reporter)

	event := "event: message\ndata: {\ndata: \"model\": \"dall-e-3\"\ndata: }\n\n"
	observer.Feed([]byte(event))
	observer.Finish()

	if got := reporter.ResponseModel(); got != "dall-e-3" {
		t.Fatalf("ResponseModel() = %q, want %q", got, "dall-e-3")
	}
}

func TestStreamResponseModelObserver_OverflowProtection(t *testing.T) {
	// A chunk larger than maxBound without newlines (e.g. huge base64 payload)
	// must be discarded without growing unbounded, and the subsequent valid event must still be parsed.
	reporter := newMultiProviderTestReporter(context.Background(), "openai-compat", "dall-e-3", nil)
	observer := NewStreamResponseModelObserver(reporter)

	hugeLine := "data: " + strings.Repeat("A", defaultMaxStreamModelBufferBound+1000)
	observer.Feed([]byte(hugeLine))
	// Feed end of huge line, then a valid event
	observer.Feed([]byte("\n\nevent: result\ndata: {\"model\":\"dall-e-3\"}\n\n"))
	observer.Finish()

	if got := reporter.ResponseModel(); got != "dall-e-3" {
		t.Fatalf("ResponseModel() = %q, want %q", got, "dall-e-3")
	}
}

func TestStreamResponseModelObserver_FinishFlushesIncompleteLine(t *testing.T) {
	// Non-SSE stream that ends without a trailing newline
	reporter := newMultiProviderTestReporter(context.Background(), "openai-compat", "dall-e-3", nil)
	observer := NewStreamResponseModelObserver(reporter)

	observer.Feed([]byte(`{"created":123,"mo`))
	observer.Feed([]byte(`del":"dall-e-3"}`))
	observer.Finish()

	if got := reporter.ResponseModel(); got != "dall-e-3" {
		t.Fatalf("ResponseModel() = %q, want %q", got, "dall-e-3")
	}
}

func TestStreamResponseModelObserver_RepeatedEmptyDataBounded(t *testing.T) {
	// Repeated "data:\n" without an empty line must not grow memory unbounded.
	// The budget and line count limits must drop the event, keep frame bounded,
	// and resume parsing normally after the event boundary.
	reporter := newMultiProviderTestReporter(context.Background(), "openai-compat", "dall-e-3", nil)
	observer := NewStreamResponseModelObserver(reporter)

	// Feed 5000 empty data lines in chunks
	emptyDataChunk := strings.Repeat("data:\n", 100)
	for range 50 {
		observer.Feed([]byte(emptyDataChunk))
	}

	// Frame must be cleared due to event overflow, keeping memory strictly bounded
	if len(observer.frame) > defaultMaxLinesPerStreamEvent {
		t.Fatalf("frame len = %d, exceeded max lines per event %d", len(observer.frame), defaultMaxLinesPerStreamEvent)
	}
	if len(observer.frame) != 0 {
		t.Fatalf("expected frame to be dropped after overflow, got len = %d", len(observer.frame))
	}

	// Terminate the overflowed event and feed a valid event
	observer.Feed([]byte("\n\nevent: completion\ndata: {\"model\":\"dall-e-3\"}\n\n"))
	observer.Finish()

	if got := reporter.ResponseModel(); got != "dall-e-3" {
		t.Fatalf("ResponseModel() = %q, want %q", got, "dall-e-3")
	}
}

func TestStreamResponseModelObserver_EventOverflowDropsEventUntilBoundary(t *testing.T) {
	// A multi-line event exceeding maxBound must be discarded up to the event boundary.
	reporter := newMultiProviderTestReporter(context.Background(), "openai-compat", "dall-e-3", nil)
	observer := NewStreamResponseModelObserver(reporter)

	// Send an event with lines totaling well over 64 KiB
	line := "data: " + strings.Repeat("x", 1024) + "\n"
	for range 100 {
		observer.Feed([]byte(line))
	}

	if len(observer.frame) != 0 {
		t.Fatalf("expected frame to be dropped after overflow, got len = %d", len(observer.frame))
	}

	// Event boundary, followed by a valid event
	observer.Feed([]byte("\n\ndata: {\"model\":\"dall-e-3\"}\n\n"))
	observer.Finish()

	if got := reporter.ResponseModel(); got != "dall-e-3" {
		t.Fatalf("ResponseModel() = %q, want %q", got, "dall-e-3")
	}
}
