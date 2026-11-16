package logging

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

func gzipCapture(t *testing.T, p []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	if _, err := w.Write(p); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func zstdCapture(t *testing.T, p []byte) []byte {
	t.Helper()
	w, err := zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1))
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	return w.EncodeAll(p, nil)
}

func TestNormalizeCaptureReadableHTTPEncodings(t *testing.T) {
	text := []byte(`{"input":"Claude and Codex readable prompt"}`)
	for _, test := range []struct {
		encoding string
		wire     []byte
	}{
		{"", text}, {"gzip", gzipCapture(t, text)}, {"zstd", zstdCapture(t, text)}, {"gzip, zstd", zstdCapture(t, gzipCapture(t, text))},
	} {
		r := NormalizeCapture(CaptureRecord{State: "complete", RawRequest: test.wire, RawResponse: test.wire, RequestEncoding: test.encoding, ResponseEncoding: test.encoding})
		if r.Request != string(text) || r.Response != string(text) || !r.RequestDecoded || !r.ResponseDecoded || r.State != "complete" || r.RawRequest != nil {
			t.Fatalf("%s: state=%s reason=%s text=%q", test.encoding, r.State, r.Reason, r.Request)
		}
	}
}
func TestNormalizeCaptureDecodedLimitsCorruptionAndUnsupported(t *testing.T) {
	for _, encoding := range []string{"gzip", "zstd"} {
		expanded := bytes.Repeat([]byte("x"), CaptureBodyLimit*8)
		compressed := gzipCapture(t, expanded)
		if encoding == "zstd" {
			compressed = zstdCapture(t, expanded)
		}
		r := NormalizeCapture(CaptureRecord{State: "complete", RawRequest: compressed, RequestEncoding: encoding})
		if len(r.Request) != CaptureBodyLimit || r.State != "partial" || !strings.Contains(r.Reason, "decoded-byte-limit") {
			t.Fatalf("%s: %d %s", encoding, len(r.Request), r.Reason)
		}
	}
	corrupt := gzipCapture(t, []byte("readable prefix"))
	corrupt = corrupt[:len(corrupt)-5]
	r := NormalizeCapture(CaptureRecord{State: "complete", RawRequest: corrupt, RequestEncoding: "gzip"})
	if r.State != "partial" || !strings.Contains(r.Request, "readable prefix") {
		t.Fatalf("corrupt stream not partial: %+v", r)
	}
	r = NormalizeCapture(CaptureRecord{State: "complete", RawRequest: []byte{0xff, 1, 2}, RequestEncoding: "br"})
	if r.State != "partial" || r.RequestDecoded || !strings.Contains(r.Request, "Base64 wire body") {
		t.Fatal("unsupported encoding misrepresented")
	}
}
func TestCaptureMessagesBoundsLiveRevisionsAndImmutablePrefixes(t *testing.T) {
	var notifications atomic.Int64
	m := NewCaptureMessages(func() { notifications.Add(1) })
	p := []byte(`{"type":"response.create","input":"readable"}`)
	m.Append("request", 1, p)
	p[0] = 'x'
	m.Append("response", 1, []byte(`{"type":"response.output_text.delta","delta":"hello"}`))
	r := NormalizeCapture(CaptureRecord{Messages: m})
	if r.State != "pending" || r.Format != "websocket" || len(r.Frames) != 2 || r.Frames[0].Payload[0] != '{' || notifications.Load() != 1 {
		t.Fatalf("bad live capture: %+v", r)
	}
	m.Close(false)
	r = NormalizeCapture(CaptureRecord{Messages: m})
	if r.State != "complete" {
		t.Fatal("clean close not finalized")
	}
	m = NewCaptureMessages(nil)
	for i := 0; i < 10; i++ {
		m.Append("request", 1, bytes.Repeat([]byte("a"), CaptureFrameLimit+1))
	}
	r = NormalizeCapture(CaptureRecord{Messages: m})
	total := 0
	for _, f := range r.Frames {
		total += len(f.Payload)
		if len(f.Payload) > CaptureFrameLimit {
			t.Fatal("frame cap exceeded")
		}
	}
	if total > CaptureBodyLimit || r.State != "partial" {
		t.Fatal("direction byte limit not enforced")
	}
	m = NewCaptureMessages(nil)
	for i := 0; i < CaptureFrameCount+1; i++ {
		m.Append("response", 1, []byte("x"))
	}
	r = NormalizeCapture(CaptureRecord{Messages: m})
	if len(r.Frames) != CaptureFrameCount || r.State != "partial" {
		t.Fatal("frame count not bounded")
	}
}
func TestCaptureWebsocketDoesNotStarveHTTP(t *testing.T) {
	s := testCaptureSpool(t)
	for i := 0; i < 32; i++ {
		if s.StartCapture(captureBase("websocket"+fmt.Sprint(i))) == nil {
			t.Fatal("websocket capture refused")
		}
	}
	if s.StartCapture(captureBase("http")) == nil {
		t.Fatal("HTTP capture refused")
	}
}

func TestCaptureMessagesConcurrentSnapshots(t *testing.T) {
	m := NewCaptureMessages(nil)
	var wg sync.WaitGroup
	for _, direction := range []string{"request", "response"} {
		wg.Add(1)
		go func(d string) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				m.Append(d, 1, []byte(`{"delta":"text"}`))
			}
		}(direction)
	}
	for i := 0; i < 100; i++ {
		_ = NormalizeCapture(CaptureRecord{Messages: m})
	}
	wg.Wait()
	m.Close(false)
	r := NormalizeCapture(CaptureRecord{Messages: m})
	if len(r.Frames) != CaptureFrameCount || r.State != "partial" {
		t.Fatalf("unsafe concurrent capture: %d %s", len(r.Frames), r.State)
	}
}

func TestCaptureHeadTailAndCuts(t *testing.T) {
	half := CaptureBodyLimit / 2
	body := append(bytes.Repeat([]byte("h"), half), bytes.Repeat([]byte("m"), 17)...)
	body = append(body, bytes.Repeat([]byte("t"), half)...)
	for _, encoding := range []string{"", "gzip", "zstd"} {
		wire := body
		if encoding == "gzip" {
			wire = gzipCapture(t, body)
		}
		if encoding == "zstd" {
			wire = zstdCapture(t, body)
		}
		r := NormalizeCapture(CaptureRecord{RawRequest: wire, RequestEncoding: encoding})
		if r.RequestCut == nil || r.RequestCut.Offset != half || *r.RequestCut.Omitted != 17 || r.Request != strings.Repeat("h", half)+strings.Repeat("t", half) {
			t.Fatalf("bad %s head/tail cut", encoding)
		}
	}
	var b CaptureBuffer
	for _, part := range [][]byte{body[:half-1], body[half-1 : half+12], body[half+12:]} {
		_, _ = b.Write(part)
	}
	r := NormalizeCapture(CaptureRecord{RawRequest: b.Bytes(), RequestCut: b.Cut()})
	if r.RequestCut == nil || *r.RequestCut.Omitted != 17 || !strings.HasSuffix(r.Request, "tttt") {
		t.Fatal("stream ring failed")
	}
	exact := NormalizeCapture(CaptureRecord{RawRequest: body[:CaptureBodyLimit]})
	if exact.RequestCut != nil {
		t.Fatal("exact bound cut")
	}
	utf := append(bytes.Repeat([]byte("a"), half-1), []byte("€middle€")...)
	utf = append(utf, bytes.Repeat([]byte("z"), half-1)...)
	r = NormalizeCapture(CaptureRecord{RawRequest: utf})
	if r.RequestCut == nil || r.RequestCut.Offset != half-1 || strings.Contains(r.Request, "�") {
		t.Fatal("split UTF-8 character")
	}
	wire := gzipCapture(t, body)
	unknown := int64(1)
	r = NormalizeCapture(CaptureRecord{RawRequest: wire, RequestEncoding: "gzip", RequestCut: &CaptureCut{Offset: len(wire) - 1, Omitted: &unknown}})
	if r.RequestCut == nil || r.RequestCut.Omitted != nil || r.RequestCut.Offset != len(r.Request) {
		t.Fatal("encoded wire cut must be unknown")
	}
}
func TestCaptureMessagesRetainsEarliestAndLatest(t *testing.T) {
	m := NewCaptureMessages(nil)
	for i := 0; i < CaptureFrameCount+10; i++ {
		m.Append("request", 1, []byte("x"))
	}
	frames, _, _, _ := m.Snapshot()
	if len(frames) != CaptureFrameCount || frames[0].Index != 1 || frames[len(frames)-1].Index != CaptureFrameCount+10 || frames[CaptureFrameCount/2].Index == CaptureFrameCount/2+1 {
		t.Fatal("missing head/tail index gap")
	}
	m = NewCaptureMessages(nil)
	for i := 0; i < 5; i++ {
		m.Append("request", 1, bytes.Repeat([]byte("x"), CaptureBodyLimit/4))
	}
	frames, _, _, _ = m.Snapshot()
	if frames[0].Index != 1 || frames[len(frames)-1].Index != 5 || len(frames) != 4 {
		t.Fatal("direction budget lost earliest/latest")
	}
	m = NewCaptureMessages(nil)
	m.Append("request", 1, bytes.Repeat([]byte("x"), CaptureFrameLimit+1))
	frames, _, _, _ = m.Snapshot()
	if frames[0].Cut == nil || !frames[0].Truncated || *frames[0].Cut.Omitted != 1 {
		t.Fatal("frame cut missing")
	}
}

func TestCaptureMessagesCoalescesBurstAndCancelsOnClose(t *testing.T) {
	const burstMessages = 32
	revisions := make(chan int, burstMessages)
	var m *CaptureMessages
	m = NewCaptureMessages(func() {
		frames, _, _, _ := m.Snapshot()
		revisions <- len(frames)
	})
	defer m.Close(false)
	for i := 0; i < burstMessages; i++ {
		m.Append("response", 1, []byte("delta"))
	}
	if count := <-revisions; count != 1 {
		t.Fatalf("immediate revision has %d frames", count)
	}
	deadline := time.NewTimer(captureLiveRevisionInterval * 2)
	defer deadline.Stop()
	select {
	case count := <-revisions:
		if count != burstMessages {
			t.Fatalf("trailing revision has %d frames", count)
		}
	case <-deadline.C:
		t.Fatal("trailing revision missing")
	}
	if len(revisions) != 0 {
		t.Fatal("burst produced extra revisions")
	}
	m.Append("response", 1, []byte("after trailing"))
	m.Close(false)
	// Simulate a timer callback already dispatched when Close stopped the timer.
	m.flushNotify()
	m.Append("response", 1, []byte("after close"))
	if len(revisions) != 0 || m.timer != nil {
		t.Fatal("close retained live revision work")
	}
}
