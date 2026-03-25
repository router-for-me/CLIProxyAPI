package proto

import (
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

// H2Stream provides bidirectional HTTP/2 streaming for the Connect protocol.
// Go's net/http does not support full-duplex HTTP/2, so we use the low-level framer.
type H2Stream struct {
	framer   *http2.Framer
	conn     net.Conn
	streamID uint32
	mu       sync.Mutex
	id       string // unique identifier for debugging
	frameNum int64 // sequential frame counter for debugging

	dataCh chan []byte
	doneCh chan struct{}
	err    error
}

// ID returns the unique identifier for this stream (for logging).
func (s *H2Stream) ID() string { return s.id }

// FrameNum returns the current frame number for debugging.
func (s *H2Stream) FrameNum() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.frameNum
}

// DialH2Stream establishes a TLS+HTTP/2 connection and opens a new stream.
func DialH2Stream(host string, headers map[string]string) (*H2Stream, error) {
	tlsConn, err := tls.Dial("tcp", host+":443", &tls.Config{
		NextProtos: []string{"h2"},
	})
	if err != nil {
		return nil, fmt.Errorf("h2: TLS dial failed: %w", err)
	}
	if tlsConn.ConnectionState().NegotiatedProtocol != "h2" {
		tlsConn.Close()
		return nil, fmt.Errorf("h2: server did not negotiate h2")
	}

	framer := http2.NewFramer(tlsConn, tlsConn)

	// Client connection preface
	if _, err := tlsConn.Write([]byte(http2.ClientPreface)); err != nil {
		tlsConn.Close()
		return nil, fmt.Errorf("h2: preface write failed: %w", err)
	}

	// Send initial SETTINGS (with large initial window)
	if err := framer.WriteSettings(
		http2.Setting{ID: http2.SettingInitialWindowSize, Val: 4 * 1024 * 1024},
		http2.Setting{ID: http2.SettingMaxConcurrentStreams, Val: 100},
	); err != nil {
		tlsConn.Close()
		return nil, fmt.Errorf("h2: settings write failed: %w", err)
	}

	// Connection-level window update (default is 65535, bump it up)
	if err := framer.WriteWindowUpdate(0, 3*1024*1024); err != nil {
		tlsConn.Close()
		return nil, fmt.Errorf("h2: window update failed: %w", err)
	}

	// Read and handle initial server frames (SETTINGS, WINDOW_UPDATE)
	for i := 0; i < 5; i++ {
		f, err := framer.ReadFrame()
		if err != nil {
			tlsConn.Close()
			return nil, fmt.Errorf("h2: initial frame read failed: %w", err)
		}
		switch sf := f.(type) {
		case *http2.SettingsFrame:
			if !sf.IsAck() {
				framer.WriteSettingsAck()
			} else {
				goto handshakeDone
			}
		case *http2.WindowUpdateFrame:
			// ignore
		default:
			// unexpected but continue
		}
	}
handshakeDone:

	// Build HEADERS
	streamID := uint32(1)
	var hdrBuf []byte
	enc := hpack.NewEncoder(&sliceWriter{buf: &hdrBuf})
	enc.WriteField(hpack.HeaderField{Name: ":method", Value: "POST"})
	enc.WriteField(hpack.HeaderField{Name: ":scheme", Value: "https"})
	enc.WriteField(hpack.HeaderField{Name: ":authority", Value: host})
	if p, ok := headers[":path"]; ok {
		enc.WriteField(hpack.HeaderField{Name: ":path", Value: p})
	}
	for k, v := range headers {
		if len(k) > 0 && k[0] == ':' {
			continue
		}
		enc.WriteField(hpack.HeaderField{Name: k, Value: v})
	}

	if err := framer.WriteHeaders(http2.HeadersFrameParam{
		StreamID:      streamID,
		BlockFragment: hdrBuf,
		EndStream:     false,
		EndHeaders:    true,
	}); err != nil {
		tlsConn.Close()
		return nil, fmt.Errorf("h2: headers write failed: %w", err)
	}

	s := &H2Stream{
		framer:   framer,
		conn:     tlsConn,
		streamID: streamID,
		dataCh:   make(chan []byte, 256),
		doneCh:   make(chan struct{}),
		id:       fmt.Sprintf("%d-%s", streamID, time.Now().Format("150405.000")),
		frameNum: 0,
	}
	go s.readLoop()
	return s, nil
}

// Write sends a DATA frame on the stream.
func (s *H2Stream) Write(data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	const maxFrame = 16384
	for len(data) > 0 {
		chunk := data
		if len(chunk) > maxFrame {
			chunk = data[:maxFrame]
		}
		data = data[len(chunk):]
		if err := s.framer.WriteData(s.streamID, false, chunk); err != nil {
			return err
		}
	}
	// Try to flush the underlying connection if it supports it
	if flusher, ok := s.conn.(interface{ Flush() error }); ok {
		flusher.Flush()
	}
	return nil
}

// Data returns the channel of received data chunks.
func (s *H2Stream) Data() <-chan []byte { return s.dataCh }

// Done returns a channel closed when the stream ends.
func (s *H2Stream) Done() <-chan struct{} { return s.doneCh }

// Close tears down the connection.
func (s *H2Stream) Close() {
	s.conn.Close()
}

func (s *H2Stream) readLoop() {
	defer close(s.doneCh)
	defer close(s.dataCh)
	log.Debugf("h2stream[%s]: readLoop started for streamID=%d", s.id, s.streamID)

	for {
		f, err := s.framer.ReadFrame()
		if err != nil {
			if err != io.EOF {
				s.err = err
				log.Debugf("h2stream[%s]: readLoop error: %v", s.id, err)
			} else {
				log.Debugf("h2stream[%s]: readLoop EOF", s.id)
			}
			return
		}

		// Increment frame counter for debugging
		s.mu.Lock()
		s.frameNum++
		frameNum := s.frameNum
		s.mu.Unlock()

		switch frame := f.(type) {
		case *http2.DataFrame:
			log.Debugf("h2stream[%s]: frame#%d received DATA frame streamID=%d, len=%d, endStream=%v", s.id, frameNum, frame.StreamID, len(frame.Data()), frame.StreamEnded())
			if frame.StreamID == s.streamID && len(frame.Data()) > 0 {
				cp := make([]byte, len(frame.Data()))
				copy(cp, frame.Data())
				// Log first 20 bytes for debugging
				previewLen := len(cp)
				if previewLen > 20 {
					previewLen = 20
				}
				log.Debugf("h2stream[%s]: frame#%d sending to dataCh: len=%d, dataCh len=%d/%d, first bytes: %x (%q)", s.id, frameNum, len(cp), len(s.dataCh), cap(s.dataCh), cp[:previewLen], string(cp[:previewLen]))
				s.dataCh <- cp

				// Flow control: send WINDOW_UPDATE
				s.mu.Lock()
				s.framer.WriteWindowUpdate(0, uint32(len(cp)))
				s.framer.WriteWindowUpdate(s.streamID, uint32(len(cp)))
				s.mu.Unlock()
			}
			if frame.StreamEnded() {
				log.Debugf("h2stream[%s]: frame#%d DATA frame has END_STREAM flag, stream ending", s.id, frameNum)
				return
			}

		case *http2.HeadersFrame:
			// Decode HPACK headers for debugging
			decoder := hpack.NewDecoder(4096, func(hf hpack.HeaderField) {
				log.Debugf("h2stream[%s]: frame#%d   header: %s = %q", s.id, frameNum, hf.Name, hf.Value)
				// Check for error status
				if hf.Name == "grpc-status" || hf.Name == ":status" && hf.Value != "200" {
					log.Warnf("h2stream[%s]: frame#%d received error status header: %s = %q", s.id, frameNum, hf.Name, hf.Value)
				}
			})
			decoder.Write(frame.HeaderBlockFragment())
			log.Debugf("h2stream[%s]: frame#%d received HEADERS frame streamID=%d, endStream=%v", s.id, frameNum, frame.StreamID, frame.StreamEnded())
			if frame.StreamEnded() {
				log.Debugf("h2stream[%s]: frame#%d HEADERS frame has END_STREAM flag, stream ending", s.id, frameNum)
				return
			}

		case *http2.RSTStreamFrame:
			s.err = fmt.Errorf("h2: RST_STREAM code=%d", frame.ErrCode)
			log.Debugf("h2stream[%s]: frame#%d received RST_STREAM code=%d", s.id, frameNum, frame.ErrCode)
			return

		case *http2.GoAwayFrame:
			s.err = fmt.Errorf("h2: GOAWAY code=%d", frame.ErrCode)
			log.Debugf("h2stream[%s]: received GOAWAY code=%d", s.id, frame.ErrCode)
			return

		case *http2.PingFrame:
			log.Debugf("h2stream[%s]: received PING frame, isAck=%v", s.id, frame.IsAck())
			if !frame.IsAck() {
				s.mu.Lock()
				s.framer.WritePing(true, frame.Data)
				s.mu.Unlock()
			}

		case *http2.SettingsFrame:
			log.Debugf("h2stream[%s]: received SETTINGS frame, isAck=%v, numSettings=%d", s.id, frame.IsAck(), frame.NumSettings())
			if !frame.IsAck() {
				s.mu.Lock()
				s.framer.WriteSettingsAck()
				s.mu.Unlock()
			}

		case *http2.WindowUpdateFrame:
			log.Debugf("h2stream[%s]: received WINDOW_UPDATE frame", s.id)
		}
	}
}

type sliceWriter struct{ buf *[]byte }

func (w *sliceWriter) Write(p []byte) (int, error) {
	*w.buf = append(*w.buf, p...)
	return len(p), nil
}
