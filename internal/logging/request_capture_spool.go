package logging

import (
	"crypto/cipher"
	"encoding/hex"
	"os"
	"path/filepath"
	"sync"

	bolt "go.etcd.io/bbolt"
)

// CaptureRecord describes the downstream exchange; legacy text fields remain readable in old archives.
type CaptureRecord struct {
	ID               string           `json:"id"`
	TraceID          string           `json:"traceId"`
	Epoch            string           `json:"epoch"`
	Sequence         uint64           `json:"sequence"`
	StartedAt        string           `json:"startedAt"`
	Method           string           `json:"method"`
	Path             string           `json:"path"`
	Status           int              `json:"status"`
	State            string           `json:"state"`
	Reason           string           `json:"reason"`
	Request          string           `json:"request"`
	Response         string           `json:"response"`
	RequestType      string           `json:"requestType"`
	ResponseType     string           `json:"responseType"`
	RequestEncoding  string           `json:"requestEncoding"`
	ResponseEncoding string           `json:"responseEncoding"`
	Format           string           `json:"format"`
	RequestDecoded   bool             `json:"requestDecoded"`
	ResponseDecoded  bool             `json:"responseDecoded"`
	Frames           []CaptureFrame   `json:"frames,omitempty"`
	RequestCut       *CaptureCut      `json:"requestCut"`
	ResponseCut      *CaptureCut      `json:"responseCut"`
	RawRequest       []byte           `json:"-"`
	RawResponse      []byte           `json:"-"`
	Messages         *CaptureMessages `json:"-"`
}

// This bound applies only to legacy text previews, never journal storage.
const CaptureBodyLimit = 8 << 20

// Chunks bound journal, management-page and archive-worker memory independently of body size.
const CaptureChunkBytes = 64 << 10
const CapturePageRecords = 4

type CaptureChunk struct {
	Direction string `json:"direction"`
	Offset    uint64 `json:"offset"`
	Data      []byte `json:"data"`
	Frame     uint64 `json:"frame"`
	Type      string `json:"type"`
	At        string `json:"at"`
	End       bool   `json:"end"`
}
type CaptureEvent struct {
	CaptureRecord
	Event          string        `json:"event"`
	Chunk          *CaptureChunk `json:"chunk,omitempty"`
	RequestBytes   uint64        `json:"requestBytes"`
	ResponseBytes  uint64        `json:"responseBytes"`
	FrameCount     uint64        `json:"frameCount"`
	RequestSHA256  string        `json:"requestSha256"`
	ResponseSHA256 string        `json:"responseSha256"`
}
type CapturePage struct {
	Version int            `json:"version"`
	Epoch   string         `json:"epoch"`
	Dropped uint64         `json:"dropped"`
	Error   string         `json:"error,omitempty"`
	Records []CaptureEvent `json:"records"`
}

// CaptureSpool synchronously commits encrypted chunks. It never evicts unacknowledged events.
type captureJournal interface {
	Update(func(*bolt.Tx) error) error
	View(func(*bolt.Tx) error) error
	Close() error
}
type CaptureSpool struct {
	mu         sync.Mutex
	policyMu   sync.Mutex
	streams    map[string]*CaptureStream
	dir        string
	key        []byte
	db         captureJournal
	aead       cipher.AEAD
	epoch      string
	enabled    bool
	generation uint64
	error      string
}

func NewCaptureSpool() *CaptureSpool {
	key, _ := hex.DecodeString(os.Getenv("SPILLWAY_CAPTURE_KEY"))
	_ = os.Unsetenv("SPILLWAY_CAPTURE_KEY")
	return NewDurableCaptureSpool(os.Getenv("SPILLWAY_CAPTURE_DIR"), key)
}
func NewDurableCaptureSpool(dir string, key []byte) *CaptureSpool {
	return &CaptureSpool{dir: dir, key: append([]byte(nil), key...), streams: make(map[string]*CaptureStream)}
}

var DesktopCaptures = NewCaptureSpool()

// Recording stays enabled until explicitly disabled; consumer stalls cannot silently stop capture.
func (s *CaptureSpool) Lease(enabled bool) error {
	s.policyMu.Lock()
	defer s.policyMu.Unlock()
	s.mu.Lock()
	if enabled {
		err := s.open()
		if err != nil {
			s.mu.Unlock()
			return err
		}
		s.enabled = true
		s.mu.Unlock()
		s.retryFinalizations()
		return nil
	}
	s.enabled = false
	if s.db == nil {
		if _, err := os.Stat(filepath.Join(s.dir, "captures.db")); err == nil {
			if errOpen := s.open(); errOpen != nil {
				s.mu.Unlock()
				return errOpen
			}
		}
	}
	streams := make([]*CaptureStream, 0, len(s.streams))
	for _, stream := range s.streams {
		streams = append(streams, stream)
	}
	s.mu.Unlock()
	for _, stream := range streams {
		stream.stop("capture-disabled")
	}
	return nil
}
func (s *CaptureSpool) Close() error {
	s.policyMu.Lock()
	defer s.policyMu.Unlock()
	s.mu.Lock()
	s.enabled = false
	streams := make([]*CaptureStream, 0, len(s.streams))
	for _, stream := range s.streams {
		streams = append(streams, stream)
	}
	s.mu.Unlock()
	for _, stream := range streams {
		stream.abandon()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	s.streams = make(map[string]*CaptureStream)
	return err
}
