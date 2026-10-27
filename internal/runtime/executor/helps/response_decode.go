package helps

import (
	"bufio"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
	log "github.com/sirupsen/logrus"
)

type compositeReadCloser struct {
	io.Reader
	closers []func() error
}

func (c *compositeReadCloser) Close() error {
	var firstErr error
	for i := range c.closers {
		if c.closers[i] == nil {
			continue
		}
		if errClose := c.closers[i](); errClose != nil && firstErr == nil {
			firstErr = errClose
		}
	}
	return firstErr
}

// peekableBody wraps a bufio.Reader around the original ReadCloser so that
// magic bytes can be inspected without consuming them from the stream.
type peekableBody struct {
	*bufio.Reader
	closer io.Closer
}

func (p *peekableBody) Close() error {
	return p.closer.Close()
}

// DecodeResponseBody decodes declared encodings in reverse order, or sniffs gzip/zstd without a header.
// Closing the returned body also closes all decoders and the original body, including on setup errors.
func DecodeResponseBody(body io.ReadCloser, header http.Header) (io.ReadCloser, error) {
	contentEncoding := strings.Join(header.Values("Content-Encoding"), ",")
	if body == nil {
		return nil, fmt.Errorf("response body is nil")
	}
	if contentEncoding == "" {
		// No Content-Encoding header.  Attempt best-effort magic-byte detection to
		// handle misbehaving upstreams that compress without setting the header.
		// Only gzip (1f 8b) and zstd (28 b5 2f fd) have reliable magic sequences;
		// br and deflate have none and are left as-is.
		// The bufio wrapper preserves unread bytes so callers always see the full
		// stream regardless of whether decompression was applied.
		// Detection waits for the first Read: a streaming upstream may send headers long
		// before its first byte, and peeking here would block the caller until then.
		return &sniffedResponseBody{body: &peekableBody{Reader: bufio.NewReader(body), closer: body}}, nil
	}
	encodings := strings.Split(contentEncoding, ",")
	reader := io.Reader(body)
	decoderClosers := make([]func() error, 0, len(encodings))
	cleanup := func() {
		for i := len(decoderClosers) - 1; i >= 0; i-- {
			if errClose := decoderClosers[i](); errClose != nil {
				log.Errorf("response decoder: close decoder error: %v", errClose)
			}
		}
		if errClose := body.Close(); errClose != nil {
			log.Errorf("response decoder: close body error: %v", errClose)
		}
	}
	for index := len(encodings) - 1; index >= 0; index-- {
		encoding := strings.TrimSpace(strings.ToLower(encodings[index]))
		switch encoding {
		case "", "identity":
			continue
		case "gzip":
			gzipReader, errGzip := gzip.NewReader(reader)
			if errGzip != nil {
				cleanup()
				return nil, fmt.Errorf("failed to create gzip reader: %w", errGzip)
			}
			reader = gzipReader
			decoderClosers = append(decoderClosers, gzipReader.Close)
		case "deflate":
			deflateReader, errDeflate := newResponseDeflateReader(reader)
			if errDeflate != nil {
				cleanup()
				return nil, errDeflate
			}
			reader = deflateReader
			decoderClosers = append(decoderClosers, deflateReader.Close)
		case "br":
			reader = brotli.NewReader(reader)
		case "zstd":
			decoder, errZstd := newResponseZstdReader(reader)
			if errZstd != nil {
				cleanup()
				return nil, fmt.Errorf("failed to create zstd reader: %w", errZstd)
			}
			reader = decoder
			decoderClosers = append(decoderClosers, func() error {
				decoder.Close()
				return nil
			})
		default:
			cleanup()
			return nil, fmt.Errorf("unsupported content encoding %q", encoding)
		}
	}
	if len(decoderClosers) == 0 && reader == body {
		return body, nil
	}
	closers := make([]func() error, 0, len(decoderClosers)+1)
	for index := len(decoderClosers) - 1; index >= 0; index-- {
		closers = append(closers, decoderClosers[index])
	}
	closers = append(closers, body.Close)
	return &compositeReadCloser{Reader: reader, closers: closers}, nil
}

func newResponseDeflateReader(reader io.Reader) (io.ReadCloser, error) {
	buffered := bufio.NewReader(reader)
	header, errPeek := buffered.Peek(2)
	if errPeek == nil && isZlibHeader(header) {
		zlibReader, errZlib := zlib.NewReader(buffered)
		if errZlib != nil {
			return nil, fmt.Errorf("failed to create zlib deflate reader: %w", errZlib)
		}
		return zlibReader, nil
	}
	return flate.NewReader(buffered), nil
}

func isZlibHeader(header []byte) bool {
	if len(header) < 2 {
		return false
	}
	cmf, flg := header[0], header[1]
	return cmf&0x0f == 8 && cmf>>4 <= 7 && (uint16(cmf)<<8|uint16(flg))%31 == 0
}

// newResponseZstdReader decodes one response synchronously. The default decoder hands blocks to up
// to four asynchronous block decoders, which costs allocations and handoff latency and gains
// nothing on bodies that are read as they arrive.
func newResponseZstdReader(reader io.Reader) (*zstd.Decoder, error) {
	return zstd.NewReader(reader, zstd.WithDecoderConcurrency(1))
}

// sniffedResponseBody decodes a body that arrived without Content-Encoding but starts with
// gzip or zstd magic bytes. Detection runs on the first Read and waits for more than one byte
// only when the first byte can start a gzip or zstd frame.
type sniffedResponseBody struct {
	body    *peekableBody
	once    sync.Once
	mu      sync.Mutex
	reader  io.Reader
	decoder func() error
	err     error
}

func (s *sniffedResponseBody) detect() {
	reader := io.Reader(s.body)
	var closeDecoder func() error
	defer func() {
		s.mu.Lock()
		s.reader, s.decoder = reader, closeDecoder
		s.mu.Unlock()
	}()
	first, _ := s.body.Peek(1)
	if len(first) == 0 || (first[0] != 0x1f && first[0] != 0x28) {
		return
	}
	magic, errPeek := s.body.Peek(4)
	if errPeek != nil && !(errPeek == io.EOF && len(magic) >= 2) {
		return
	}
	switch {
	case len(magic) >= 2 && magic[0] == 0x1f && magic[1] == 0x8b:
		gzipReader, errGzip := gzip.NewReader(s.body)
		if errGzip != nil {
			s.err = fmt.Errorf("magic-byte gzip: failed to create reader: %w", errGzip)
			return
		}
		reader, closeDecoder = gzipReader, gzipReader.Close
	case len(magic) >= 4 && magic[0] == 0x28 && magic[1] == 0xb5 && magic[2] == 0x2f && magic[3] == 0xfd:
		decoder, errZstd := newResponseZstdReader(s.body)
		if errZstd != nil {
			s.err = fmt.Errorf("magic-byte zstd: failed to create reader: %w", errZstd)
			return
		}
		reader, closeDecoder = decoder, func() error { decoder.Close(); return nil }
	}
}

func (s *sniffedResponseBody) Read(p []byte) (int, error) {
	s.once.Do(s.detect)
	if s.err != nil {
		return 0, s.err
	}
	return s.reader.Read(p)
}

func (s *sniffedResponseBody) Close() error {
	s.mu.Lock()
	closeDecoder := s.decoder
	s.mu.Unlock()
	var errDecoder error
	if closeDecoder != nil {
		errDecoder = closeDecoder()
	}
	if errClose := s.body.Close(); errClose != nil {
		return errClose
	}
	return errDecoder
}
