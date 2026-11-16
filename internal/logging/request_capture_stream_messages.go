package logging

import (
	"io"
	"time"
)

func (s *CaptureStream) MessageWriter(direction string, kind int) io.WriteCloser {
	if s == nil {
		return discardCaptureWriter{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.failed {
		return discardCaptureWriter{}
	}
	s.flushBodies()
	frame := s.event.FrameCount + 1
	typ := "text"
	if kind == 2 {
		typ = "binary"
	}
	at := time.Now().UTC().Format(time.RFC3339Nano)
	s.write(direction, nil, frame, typ, at, false)
	return &captureMessageWriter{s, direction, frame, typ, at, false}
}

type captureMessageWriter struct {
	stream    *CaptureStream
	direction string
	frame     uint64
	typ, at   string
	closed    bool
}

func (w *captureMessageWriter) Write(data []byte) (int, error) {
	w.stream.mu.Lock()
	defer w.stream.mu.Unlock()
	if !w.closed {
		w.stream.write(w.direction, data, w.frame, w.typ, w.at, false)
	}
	return len(data), nil
}
func (w *captureMessageWriter) Close() error {
	w.stream.mu.Lock()
	defer w.stream.mu.Unlock()
	if !w.closed {
		w.stream.write(w.direction, nil, w.frame, w.typ, w.at, true)
		w.closed = true
	}
	return nil
}
func (w *captureMessageWriter) Abort() {
	w.stream.mu.Lock()
	defer w.stream.mu.Unlock()
	w.closed = true
	w.stream.partialReason = "websocket-copy-interrupted"
}

type discardCaptureWriter struct{}

func (discardCaptureWriter) Write(data []byte) (int, error) { return len(data), nil }
func (discardCaptureWriter) Close() error                   { return nil }
