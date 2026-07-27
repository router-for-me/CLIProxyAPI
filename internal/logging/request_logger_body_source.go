package logging

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	log "github.com/sirupsen/logrus"
)

// FileBodySource stores large log sections as ordered temp-file parts.
type FileBodySource struct {
	mu      sync.Mutex
	dir     string
	paths   []string
	cleaned bool
}

// NewFileBodySourceInDir creates a temp-backed source under baseDir.
func NewFileBodySourceInDir(baseDir string, prefix string) (*FileBodySource, error) {
	prefix = sanitizeTempPrefix(prefix)
	baseDir = strings.TrimSpace(baseDir)
	if baseDir == "" {
		return nil, fmt.Errorf("base directory is required")
	}
	if errMkdir := os.MkdirAll(baseDir, 0755); errMkdir != nil {
		return nil, errMkdir
	}
	dir, errCreate := os.MkdirTemp(baseDir, "request-log-parts-"+prefix+"-*")
	if errCreate != nil {
		return nil, errCreate
	}
	return &FileBodySource{dir: dir}, nil
}

func sanitizeTempPrefix(prefix string) string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return "log"
	}
	var builder strings.Builder
	for _, r := range prefix {
		switch {
		case r >= 'a' && r <= 'z':
			builder.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			builder.WriteRune(r)
		case r >= '0' && r <= '9':
			builder.WriteRune(r)
		case r == '-' || r == '_':
			builder.WriteRune(r)
		default:
			builder.WriteByte('-')
		}
	}
	out := strings.Trim(builder.String(), "-_")
	if out == "" {
		return "log"
	}
	return out
}

// CreatePart creates one ordered detail log part.
func (s *FileBodySource) CreatePart(prefix string) (*os.File, error) {
	if s == nil {
		return nil, fmt.Errorf("file body source is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cleaned {
		return nil, fmt.Errorf("file body source has been cleaned")
	}
	prefix = sanitizeTempPrefix(prefix)
	if errMkdir := os.MkdirAll(s.dir, 0755); errMkdir != nil {
		return nil, errMkdir
	}
	file, errCreate := os.CreateTemp(s.dir, prefix+"-*.tmp")
	if errCreate != nil {
		return nil, errCreate
	}
	s.paths = append(s.paths, file.Name())
	return file, nil
}

// AppendPart appends one complete ordered part to the source.
func (s *FileBodySource) AppendPart(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil
	}
	file, errCreate := s.CreatePart("part")
	if errCreate != nil {
		return errCreate
	}
	writeErr := writeLogPart(file, data, false)
	if errClose := file.Close(); errClose != nil {
		if writeErr == nil {
			writeErr = errClose
		}
	}
	return writeErr
}

// AppendBytes appends raw bytes to a single ordered part.
func (s *FileBodySource) AppendBytes(data []byte) error {
	if s == nil {
		return fmt.Errorf("file body source is nil")
	}
	if len(data) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cleaned {
		return fmt.Errorf("file body source has been cleaned")
	}
	if errMkdir := os.MkdirAll(s.dir, 0755); errMkdir != nil {
		return errMkdir
	}

	var file *os.File
	var errOpen error
	if len(s.paths) == 0 {
		file, errOpen = os.CreateTemp(s.dir, "part-*.tmp")
		if errOpen == nil {
			s.paths = append(s.paths, file.Name())
		}
	} else {
		file, errOpen = os.OpenFile(s.paths[len(s.paths)-1], os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	}
	if errOpen != nil {
		return errOpen
	}

	_, writeErr := file.Write(data)
	if errClose := file.Close(); errClose != nil {
		if writeErr == nil {
			writeErr = errClose
		}
	}
	return writeErr
}

// HasPayload reports whether any detail parts were recorded.
func (s *FileBodySource) HasPayload() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.paths) > 0 && !s.cleaned
}

// Paths returns a copy of the ordered part paths.
func (s *FileBodySource) Paths() []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.paths))
	copy(out, s.paths)
	return out
}

// WriteTo merges all ordered parts into w.
func (s *FileBodySource) WriteTo(w io.Writer) error {
	if s == nil || w == nil {
		return nil
	}
	paths := s.Paths()
	wrote := false
	for _, path := range paths {
		file, errOpen := os.Open(path)
		if errOpen != nil {
			if os.IsNotExist(errOpen) {
				continue
			}
			return errOpen
		}
		if wrote {
			if _, errWrite := io.WriteString(w, "\n"); errWrite != nil {
				if errClose := file.Close(); errClose != nil {
					log.WithError(errClose).Warn("failed to close log part file")
				}
				return errWrite
			}
		}
		_, errCopy := io.Copy(w, file)
		if errClose := file.Close(); errClose != nil {
			log.WithError(errClose).Warn("failed to close log part file")
			if errCopy == nil {
				errCopy = errClose
			}
		}
		if errCopy != nil {
			return errCopy
		}
		wrote = true
	}
	return nil
}

// Bytes merges all ordered parts into memory.
func (s *FileBodySource) Bytes() ([]byte, error) {
	var buf bytes.Buffer
	if errWrite := s.WriteTo(&buf); errWrite != nil {
		return nil, errWrite
	}
	return buf.Bytes(), nil
}

// Cleanup removes all temp detail parts and their directory.
func (s *FileBodySource) Cleanup() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.cleaned {
		s.mu.Unlock()
		return nil
	}
	paths := make([]string, len(s.paths))
	copy(paths, s.paths)
	dir := s.dir
	s.paths = nil
	s.cleaned = true
	s.mu.Unlock()

	var firstErr error
	for _, path := range paths {
		if errRemove := os.Remove(path); errRemove != nil && !os.IsNotExist(errRemove) && firstErr == nil {
			firstErr = errRemove
		}
	}
	if dir != "" {
		if errRemove := os.RemoveAll(dir); errRemove != nil && firstErr == nil {
			firstErr = errRemove
		}
	}
	return firstErr
}

func cleanupFileBodySources(sources ...*FileBodySource) {
	for _, source := range sources {
		if source == nil {
			continue
		}
		if errCleanup := source.Cleanup(); errCleanup != nil {
			log.WithError(errCleanup).Warn("failed to clean up log part files")
		}
	}
}
