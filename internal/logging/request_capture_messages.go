package logging

import (
	"io"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

const CaptureMessagesContextKey = "CPA_CAPTURE_MESSAGES"
const CaptureFrameLimit = CaptureBodyLimit
const CaptureFrameCount = 128

// The app polls idle captures every three seconds; faster live revisions are not visible.
const captureLiveRevisionInterval = 3 * time.Second

type CaptureFrame struct {
	Index     int         `json:"index"`
	Direction string      `json:"direction"`
	At        string      `json:"at"`
	Type      string      `json:"type"`
	Payload   string      `json:"payload"`
	Truncated bool        `json:"truncated"`
	Cut       *CaptureCut `json:"cut,omitempty"`
}
type rawCaptureFrame struct {
	index     int
	direction string
	at        time.Time
	kind      int
	payload   []byte
	cut       *CaptureCut
}

// CaptureMessages only copies bounded application messages on the socket path.
// JSON/text normalization and serialization happen in the spool worker.
type CaptureMessages struct {
	stream     func() *CaptureStream
	mu         sync.Mutex
	frames     []rawCaptureFrame
	bytes      map[string]int
	seen       int
	partial    bool
	closed     bool
	reason     string
	notify     func()
	notifyMu   sync.Mutex
	lastNotify time.Time
	timer      *time.Timer
}

func NewCaptureMessages(notify func()) *CaptureMessages {
	return &CaptureMessages{bytes: map[string]int{}, notify: notify}
}

// Streaming captures persist application bytes instead of retaining diagnostic snapshots.
func NewStreamingCaptureMessages(stream func() *CaptureStream) *CaptureMessages {
	return &CaptureMessages{stream: stream}
}
func (m *CaptureMessages) Writer(direction string, kind int) io.WriteCloser {
	if m == nil || m.stream == nil {
		return discardCaptureWriter{}
	}
	return m.stream().MessageWriter(direction, kind)
}
func CaptureMessagesFromGin(c *gin.Context) *CaptureMessages {
	if c == nil || !c.GetBool("CPA_ARCHIVE_AUTHENTICATED") {
		return nil
	}
	v, _ := c.Get(CaptureMessagesContextKey)
	m, _ := v.(*CaptureMessages)
	return m
}
func (m *CaptureMessages) Append(direction string, kind int, payload []byte) {
	if m == nil || (direction != "request" && direction != "response") || (kind != 1 && kind != 2) {
		return
	}
	if m.stream != nil {
		m.stream().Message(direction, kind, payload)
		return
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.seen++
	var buffer CaptureBuffer
	_, _ = buffer.Write(payload)
	frame := rawCaptureFrame{m.seen, direction, time.Now().UTC(), kind, buffer.Bytes(), buffer.Cut()}
	m.frames = append(m.frames, frame)
	m.bytes[direction] += len(frame.payload)
	if frame.cut != nil {
		m.partial = true
		m.reason = "websocket-frame-or-byte-limit"
	}
	if m.bytes[direction] > CaptureBodyLimit {
		m.partial = true
		m.reason = "websocket-frame-or-byte-limit"
		for i := range m.frames {
			f := &m.frames[i]
			if f.direction != direction || len(f.payload) <= CaptureBodyLimit/2 {
				continue
			}
			retained := CaptureBodyLimit / 2
			omitted := int64(len(f.payload) - retained)
			if f.cut != nil && f.cut.Omitted != nil {
				omitted += *f.cut.Omitted
			}
			f.payload = append(bytesCloneCapture(f.payload[:retained/2]), f.payload[len(f.payload)-retained/2:]...)
			f.cut = &CaptureCut{Offset: retained / 2, Omitted: &omitted}
		}
		m.bytes[direction] = 0
		for _, f := range m.frames {
			if f.direction == direction {
				m.bytes[direction] += len(f.payload)
			}
		}
	}
	// Evict the middle, preserving the earliest half and a rolling newest half.
	for len(m.frames) > CaptureFrameCount || m.bytes[direction] > CaptureBodyLimit {
		candidates := make([]int, 0, len(m.frames))
		for i, f := range m.frames {
			if len(m.frames) > CaptureFrameCount || f.direction == direction {
				candidates = append(candidates, i)
			}
		}
		victim := candidates[0]
		budget := 0
		for _, i := range candidates {
			if len(m.frames) > CaptureFrameCount {
				if budget >= CaptureFrameCount/2 {
					victim = i
					break
				}
				budget++
			} else {
				if budget+len(m.frames[i].payload) > CaptureBodyLimit/2 {
					victim = i
					break
				}
				budget += len(m.frames[i].payload)
			}
		}
		// A single large newest message shares the budget with the retained prefix.
		if victim == len(m.frames)-1 && len(candidates) == 1 {
			break
		}
		if victim == len(m.frames)-1 {
			victim = candidates[len(candidates)-2]
		}
		m.bytes[m.frames[victim].direction] -= len(m.frames[victim].payload)
		m.frames = append(m.frames[:victim], m.frames[victim+1:]...)
		m.partial = true
		m.reason = "websocket-frame-or-byte-limit"
	}
	m.mu.Unlock()
	m.scheduleNotify()
}
func (m *CaptureMessages) scheduleNotify() {
	m.notifyMu.Lock()
	defer m.notifyMu.Unlock()
	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()
	if closed || m.notify == nil || m.timer != nil {
		return
	}
	if m.lastNotify.IsZero() || time.Since(m.lastNotify) >= captureLiveRevisionInterval {
		m.lastNotify = time.Now()
		m.notify()
		return
	}
	m.timer = time.AfterFunc(captureLiveRevisionInterval-time.Since(m.lastNotify), m.flushNotify)
}
func (m *CaptureMessages) flushNotify() {
	m.notifyMu.Lock()
	defer m.notifyMu.Unlock()
	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()
	m.timer = nil
	if !closed {
		m.lastNotify = time.Now()
		m.notify()
	}
}
func bytesCloneCapture(b []byte) []byte { return append([]byte(nil), b...) }
func (m *CaptureMessages) Close(partial bool) {
	if m == nil {
		return
	}
	m.notifyMu.Lock()
	defer m.notifyMu.Unlock()
	if m.timer != nil {
		m.timer.Stop()
		m.timer = nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	if partial {
		m.partial = true
		m.reason = "websocket-disconnected"
	}
}
func (m *CaptureMessages) WriteError() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.partial = true
	m.reason = "websocket-write-failed"
}
func (m *CaptureMessages) Snapshot() ([]CaptureFrame, bool, bool, string) {
	m.mu.Lock()
	raw := append([]rawCaptureFrame(nil), m.frames...)
	partial, closed, reason := m.partial, m.closed, m.reason
	m.mu.Unlock()
	frames := make([]CaptureFrame, 0, len(raw))
	for _, f := range raw {
		kind := "text"
		if f.kind == 2 {
			kind = "binary"
		}
		if !utf8.Valid(f.payload) {
			partial = true
			reason = "websocket-invalid-utf8"
		}
		text, cut := captureText(f.payload, f.cut)
		frames = append(frames, CaptureFrame{f.index, f.direction, f.at.Format(time.RFC3339Nano), kind, strings.ToValidUTF8(text, "�"), cut != nil, cut})
	}
	return frames, partial, closed, reason
}
