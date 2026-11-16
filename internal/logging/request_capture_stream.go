package logging

import (
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"sync"
	"time"
)

// The JSON allowance includes base64 and event metadata while bounding each stream's uncommitted memory.
const capturePendingBytes = CaptureChunkBytes * 2

type CaptureStream struct {
	mu            sync.Mutex
	spool         *CaptureSpool
	generation    uint64
	event         CaptureEvent
	hashes        map[string]hash.Hash
	pending       []CaptureEvent
	pendingBytes  int
	bodies        map[string][]byte
	timer         *time.Timer
	closed        bool
	failed        bool
	partialReason string
	finalRecord   *CaptureRecord
}

func (s *CaptureSpool) StartCapture(r CaptureRecord) *CaptureStream {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil || !s.enabled {
		return nil
	}
	r.State, r.Reason = "pending", "capture-open"
	stream := &CaptureStream{spool: s, generation: s.generation, event: CaptureEvent{CaptureRecord: r, Event: "start"}, hashes: map[string]hash.Hash{"request": sha256.New(), "response": sha256.New()}, bodies: map[string][]byte{}}
	if err := s.appendBatchLocked([]CaptureEvent{stream.event}, s.generation); err != nil {
		return nil
	}
	s.streams[r.ID] = stream
	return stream
}
func (s *CaptureStream) queue(e CaptureEvent) bool {
	encoded, err := encodeCaptureEvent(e)
	if err != nil {
		s.failed = true
		return false
	}
	if s.pendingBytes+len(encoded) > capturePendingBytes && len(s.pending) > 0 {
		if !s.flushEvents() {
			return false
		}
	}
	s.pending = append(s.pending, e)
	s.pendingBytes += len(encoded)
	if s.timer == nil {
		s.timer = time.AfterFunc(captureLiveRevisionInterval, func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.timer = nil
			if !s.closed {
				s.flushBodies()
				s.flushEvents()
			}
		})
	}
	return true
}
func (s *CaptureStream) flushEvents() bool {
	if len(s.pending) == 0 {
		return true
	}
	if err := s.spool.appendBatch(s.pending, s.generation); err != nil {
		s.failed = true
		return false
	}
	s.pending = nil
	s.pendingBytes = 0
	return true
}
func (s *CaptureStream) write(direction string, data []byte, frame uint64, typ, at string, end bool) {
	if s.closed || s.failed || s.hashes[direction] == nil {
		return
	}
	count := &s.event.RequestBytes
	if direction == "response" {
		count = &s.event.ResponseBytes
	}
	emit := frame != 0
	for len(data) > 0 || end || emit {
		n := min(len(data), CaptureChunkBytes)
		chunk := append([]byte{}, data[:n]...)
		e := s.event
		e.Event = "data"
		e.FrameCount = max(e.FrameCount, frame)
		e.Chunk = &CaptureChunk{Direction: direction, Offset: *count, Data: chunk, Frame: frame, Type: typ, At: at, End: end && n == len(data)}
		if direction == "request" {
			e.RequestBytes += uint64(n)
		} else {
			e.ResponseBytes += uint64(n)
		}
		if !s.queue(e) {
			return
		}
		*count += uint64(n)
		s.event.FrameCount = e.FrameCount
		_, _ = s.hashes[direction].Write(chunk)
		data = data[n:]
		emit = false
		if len(data) == 0 {
			break
		}
	}
}
func (s *CaptureStream) Write(direction string, data []byte) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.failed || s.hashes[direction] == nil {
		return
	}
	for len(data) > 0 {
		room := CaptureChunkBytes - len(s.bodies[direction])
		n := min(room, len(data))
		s.bodies[direction] = append(s.bodies[direction], data[:n]...)
		data = data[n:]
		if len(s.bodies[direction]) == CaptureChunkBytes {
			s.write(direction, s.bodies[direction], 0, "", "", false)
			s.bodies[direction] = nil
		}
		if s.failed {
			return
		}
	}
	if s.timer == nil && len(s.bodies[direction]) > 0 {
		s.timer = time.AfterFunc(captureLiveRevisionInterval, func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.timer = nil
			if !s.closed {
				s.flushBodies()
				s.flushEvents()
			}
		})
	}
}
func (s *CaptureStream) flushBodies() {
	for _, direction := range []string{"request", "response"} {
		if len(s.bodies[direction]) > 0 {
			s.write(direction, s.bodies[direction], 0, "", "", false)
			s.bodies[direction] = nil
		}
	}
}
func (s *CaptureStream) Flush() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.flushBodies()
	s.flushEvents()
}
func (s *CaptureStream) Message(direction string, kind int, data []byte) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.failed {
		return
	}
	s.flushBodies()
	frame := s.event.FrameCount + 1
	typ := "text"
	if kind == 2 {
		typ = "binary"
	}
	s.write(direction, data, frame, typ, time.Now().UTC().Format(time.RFC3339Nano), true)
}

func (s *CaptureStream) finalize() bool {
	if s.finalRecord == nil {
		return !s.failed
	}
	if !s.flushEvents() {
		return false
	}
	terminal := s.event
	terminal.CaptureRecord = *s.finalRecord
	terminal.Event = "end"
	terminal.RequestSHA256 = hex.EncodeToString(s.hashes["request"].Sum(nil))
	terminal.ResponseSHA256 = hex.EncodeToString(s.hashes["response"].Sum(nil))
	if s.partialReason != "" && terminal.State == "complete" {
		terminal.State, terminal.Reason = "partial", s.partialReason
	}
	if s.failed {
		terminal.State, terminal.Reason = "partial", "capture-storage-write-failed"
	}
	if err := s.spool.appendBatch([]CaptureEvent{terminal}, s.generation); err != nil {
		s.failed = true
		return false
	}
	s.finalRecord = nil
	return true
}
func (s *CaptureStream) finish(r CaptureRecord) {
	if s.closed {
		return
	}
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	s.flushBodies()
	s.finalRecord = &r
	s.closed = true
	s.finalize()
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
}
func (s *CaptureStream) retryFinalization() (bool, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	retried := s.finalRecord != nil
	return s.finalize(), retried
}
func (s *CaptureStream) Finish(r CaptureRecord) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finish(r)
}
func (s *CaptureStream) stop(reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.event.CaptureRecord
	r.State, r.Reason = "partial", reason
	s.finish(r)
}
func (s *CaptureStream) abandon() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	s.pending = nil
	s.pendingBytes = 0
	s.bodies = nil
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
}
