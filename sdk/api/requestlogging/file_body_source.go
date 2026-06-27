package requestlogging

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	WebsocketTimelineSourceContextKey = "websocket_timeline_source"
	APIWebsocketSourceContextKey      = "api_websocket_source"
)

type FileBodySource struct {
	dir     string
	prefix  string
	mu      sync.Mutex
	counter int
	paths   []string
}

type nopWriteCloser struct{}

func (nopWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (nopWriteCloser) Close() error                { return nil }

func NewFileBodySource(dir, prefix string) (*FileBodySource, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		dir = os.TempDir()
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		prefix = "body"
	}
	return &FileBodySource{dir: dir, prefix: prefix}, nil
}

func (s *FileBodySource) CreatePart(name string) (io.WriteCloser, error) {
	if s == nil {
		return nopWriteCloser{}, nil
	}
	s.mu.Lock()
	s.counter++
	counter := s.counter
	s.mu.Unlock()
	name = strings.TrimSpace(name)
	if name == "" {
		name = "part"
	}
	path := filepath.Join(s.dir, fmt.Sprintf("%s-%03d-%s.log", s.prefix, counter, name))
	s.mu.Lock()
	s.paths = append(s.paths, path)
	s.mu.Unlock()
	return os.Create(path)
}

func (s *FileBodySource) HasPayload() bool {
	if s == nil {
		return false
	}
	data, err := s.Bytes()
	return err == nil && len(bytes.TrimSpace(data)) > 0
}

func (s *FileBodySource) Bytes() ([]byte, error) {
	if s == nil {
		return nil, nil
	}
	s.mu.Lock()
	paths := append([]string(nil), s.paths...)
	s.mu.Unlock()
	var out []byte
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		out = append(out, data...)
	}
	return out, nil
}

func (s *FileBodySource) Cleanup() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	paths := append([]string(nil), s.paths...)
	s.paths = nil
	s.mu.Unlock()
	var firstErr error
	for _, path := range paths {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
