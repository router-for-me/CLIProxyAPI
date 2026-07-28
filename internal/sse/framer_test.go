package sse

import (
	"bytes"
	"io"
	"strconv"
	"strings"
	"testing"
)

type chunkReader struct {
	chunks [][]byte
}

func (r *chunkReader) Read(p []byte) (int, error) {
	if len(r.chunks) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.chunks[0])
	r.chunks[0] = r.chunks[0][n:]
	if len(r.chunks[0]) == 0 {
		r.chunks = r.chunks[1:]
	}
	return n, nil
}

func TestNormalizeGluedFramesThreeOrMore(t *testing.T) {
	input := []byte(`data: {"n":1}data: {"n":2}data: {"n":3}`)
	got := string(NormalizeGluedFrames(input))
	want := "data: {\"n\":1}\ndata: {\"n\":2}\ndata: {\"n\":3}"
	if got != want {
		t.Fatalf("normalized = %q, want %q", got, want)
	}
}

func TestNormalizeGluedFramesPreservesMarkerInsideJSONString(t *testing.T) {
	input := []byte(`data: {"text":"literal }data: marker"}`)
	if got := string(NormalizeGluedFrames(input)); got != string(input) {
		t.Fatalf("normalized JSON string marker: %q", got)
	}
}

func TestNormalizeGluedFramesSkipsInStringDataMarkerThenSplitsRealBoundary(t *testing.T) {
	input := []byte(`data: {"text":"literal }data: marker","n":1}data: {"n":2}`)
	want := "data: {\"text\":\"literal }data: marker\",\"n\":1}\ndata: {\"n\":2}"
	if got := string(NormalizeGluedFrames(input)); got != want {
		t.Fatalf("normalized = %q, want %q", got, want)
	}
}

func TestNormalizeGluedFramesSkipsInStringEventMarkerThenSplitsRealBoundary(t *testing.T) {
	input := []byte(`data: {"text":"literal }event: marker","n":1}event: response.completed`)
	want := "data: {\"text\":\"literal }event: marker\",\"n\":1}\n\nevent: response.completed"
	if got := string(NormalizeGluedFrames(input)); got != want {
		t.Fatalf("normalized = %q, want %q", got, want)
	}
}

func TestNormalizeGluedFramesManyFrames(t *testing.T) {
	const frameCount = 5000
	var input strings.Builder
	for i := 0; i < frameCount; i++ {
		input.WriteString(`data: {"n":`)
		input.WriteString(strconv.Itoa(i))
		input.WriteByte('}')
	}
	got := NormalizeGluedFrames([]byte(input.String()))
	if count := bytes.Count(got, []byte("\ndata:")); count != frameCount-1 {
		t.Fatalf("normalized boundaries = %d, want %d", count, frameCount-1)
	}
	if !bytes.HasSuffix(got, []byte(`data: {"n":4999}`)) {
		t.Fatalf("normalized output lost final frame")
	}
}

func BenchmarkNormalizeGluedFramesMany(b *testing.B) {
	var input strings.Builder
	for i := 0; i < 1000; i++ {
		input.WriteString(`data: {"n":`)
		input.WriteString(strconv.Itoa(i))
		input.WriteByte('}')
	}
	payload := []byte(input.String())
	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))
	for i := 0; i < b.N; i++ {
		_ = NormalizeGluedFrames(payload)
	}
}

func TestLineScannerHandlesSplitEventAndDataFields(t *testing.T) {
	reader := &chunkReader{chunks: [][]byte{[]byte("eve"), []byte("nt: response.created\r\nda"), []byte("ta: {\"n\":"), []byte("1}\r\n\r\n")}}
	scanner := NewLineScanner(reader, 1024)
	var got []string
	for scanner.Scan() {
		got = append(got, string(scanner.Bytes()))
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scanner error: %v", err)
	}
	if len(got) != 2 || got[0] != "event: response.created" || got[1] != `data: {"n":1}` {
		t.Fatalf("split fields = %q", got)
	}
}

func TestLineScannerHandlesCRLFAndPartialFinalFrame(t *testing.T) {
	scanner := NewLineScanner(strings.NewReader("event: response.created\r\ndata: {\"n\":1}\r\n\r\ndata: {\"n\":2}"), 1024)
	var got []string
	for scanner.Scan() {
		got = append(got, string(scanner.Bytes()))
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scanner error: %v", err)
	}
	want := []string{"event: response.created", `data: {"n":1}`, `data: {"n":2}`}
	if len(got) != len(want) {
		t.Fatalf("lines = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("line %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestLineScannerBoundsPendingToken(t *testing.T) {
	scanner := NewLineScanner(strings.NewReader(strings.Repeat("x", 128)), 32)
	if scanner.Scan() {
		t.Fatal("oversized token unexpectedly scanned")
	}
	if scanner.Err() == nil {
		t.Fatal("expected bounded scanner error")
	}
}
