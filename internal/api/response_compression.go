package api

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/httpwire"
	log "github.com/sirupsen/logrus"
)

func (s *Server) responseCompressionMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		responseCompressionMiddleware(s.getConfig())(c)
	}
}

func responseCompressionMiddleware(cfg *config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		if cfg == nil || !cfg.EffectiveResponseCompression().IsEnabled() ||
			strings.EqualFold(strings.TrimSpace(c.GetHeader("Upgrade")), "websocket") {
			c.Next()
			return
		}
		original := c.Writer
		commitWriter := &responseCompressionCommitWriter{ResponseWriter: original}
		var writer *responseCompressionWriter
		c.Writer = commitWriter
		defer func() {
			if recovered := recover(); recovered != nil {
				// Discard uncommitted data so outer recovery can send a clean error.
				if writer != nil && original.Written() {
					writer.finish()
				}
				c.Writer = original
				panic(recovered)
			}
			if writer != nil {
				writer.finish()
			}
			// Commit even bodyless responses through the hook before restoring Gin's writer.
			commitWriter.WriteHeaderNow()
			c.Writer = original
		}()
		encoding, identityAllowed := negotiateResponseCompression(strings.Join(c.Request.Header.Values("Accept-Encoding"), ","))
		if encoding == "" || c.Request.Method == http.MethodHead {
			if !identityAllowed {
				log.Debug("response compression: no acceptable encoding; passing through for compatibility")
			}
			c.Next()
			return
		}
		minBytes := cfg.EffectiveResponseCompression().EffectiveMinBytes()
		if !identityAllowed {
			minBytes = 0
		}
		writer = &responseCompressionWriter{
			ResponseWriter: commitWriter,
			encoding:       encoding,
			minBytes:       minBytes,
		}
		c.Writer = writer
		c.Next()
	}
}

// negotiateResponseCompression ranks quality first, using server preference for ties.
// The second result reports whether identity is allowed, including wildcard exclusions.
func negotiateResponseCompression(header string) (string, bool) {
	qualities := make(map[string]float64)
	for _, token := range strings.Split(header, ",") {
		parts := strings.Split(token, ";")
		name := strings.ToLower(strings.TrimSpace(parts[0]))
		quality := 1.0
		for _, parameter := range parts[1:] {
			key, value, ok := strings.Cut(parameter, "=")
			if !ok || !strings.EqualFold(strings.TrimSpace(key), "q") {
				continue
			}
			parsed, errParseFloat := strconv.ParseFloat(strings.TrimSpace(value), 64)
			if errParseFloat != nil || !(parsed >= 0 && parsed <= 1) {
				quality = 0
			} else {
				quality = parsed
			}
		}
		qualities[name] = quality
	}
	identityQuality, explicitIdentity := qualities["identity"]
	wildcardQuality, explicitWildcard := qualities["*"]
	identityAllowed := identityQuality > 0 || (!explicitIdentity && (!explicitWildcard || wildcardQuality > 0))
	selected := ""
	highestQuality := 0.0
	for _, encoding := range []string{"zstd", "gzip"} {
		quality, explicit := qualities[encoding]
		if !explicit {
			quality = wildcardQuality
		}
		if quality > highestQuality {
			selected = encoding
			highestQuality = quality
		}
	}
	return selected, identityAllowed
}

// responseCompressionCommitWriter merges Vary at the first transport write without buffering.
// Gin's WriteHeader only stages the status; Hijacker and other interfaces remain embedded.
type responseCompressionCommitWriter struct {
	gin.ResponseWriter
	committed bool
}

func (w *responseCompressionCommitWriter) Unwrap() http.ResponseWriter {
	if w == nil {
		return nil
	}
	return w.ResponseWriter
}

func (w *responseCompressionCommitWriter) mergeVary() {
	if !w.committed {
		appendCompressionVary(w.Header())
		w.committed = true
	}
}

func (w *responseCompressionCommitWriter) Write(data []byte) (int, error) {
	w.mergeVary()
	return w.ResponseWriter.Write(data)
}

func (w *responseCompressionCommitWriter) WriteString(data string) (int, error) {
	w.mergeVary()
	return w.ResponseWriter.WriteString(data)
}

func (w *responseCompressionCommitWriter) WriteHeaderNow() {
	w.mergeVary()
	w.ResponseWriter.WriteHeaderNow()
}

func (w *responseCompressionCommitWriter) Flush() {
	if errFlush := w.FlushError(); errFlush != nil {
		log.Debugf("response compression commit flush failed: %v", errFlush)
	}
}

func (w *responseCompressionCommitWriter) FlushError() error {
	w.mergeVary()
	return httpwire.FlushResponse(w.ResponseWriter)
}

type responseCompressor interface {
	io.WriteCloser
	Flush() error
	Reset(io.Writer)
}

type responseCompressorPool interface {
	Get() any
	Put(any)
}

// responseCompressorPools reuse encoders across responses. The default zstd encoder keeps an
// 8 MiB window and its buffers (about 18 MB per stream); a 1 MiB window with lower encoder
// memory loses little ratio on API responses, which are far smaller than the window.
var responseCompressorPools = map[string]responseCompressorPool{
	"zstd": &sync.Pool{New: func() any {
		encoder, errNewWriter := zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1), zstd.WithWindowSize(1<<20), zstd.WithLowerEncoderMem(true))
		if errNewWriter != nil {
			log.Errorf("create response compression encoder: %v", errNewWriter)
			return nil
		}
		return encoder
	}},
	"gzip": &sync.Pool{New: func() any { return gzip.NewWriter(io.Discard) }},
}

// newResponseCompressor takes a pooled encoder writing to dst, or nil when none can be created.
func newResponseCompressor(encoding string, dst io.Writer) responseCompressor {
	pool := responseCompressorPools[encoding]
	if pool == nil {
		return nil
	}
	compressor, _ := pool.Get().(responseCompressor)
	if compressor == nil {
		return nil
	}
	compressor.Reset(dst)
	return compressor
}

// responseCompressionWriter keeps headers uncommitted until the encoding is known.
// Gin's remaining interfaces, including Hijacker and Pusher, are embedded unchanged.
type responseCompressionWriter struct {
	gin.ResponseWriter
	encoding   string
	minBytes   int
	buffer     bytes.Buffer
	compressor responseCompressor
	decided    bool
	writeErr   error
	written    bool
	size       int
	status     int
}

func (w *responseCompressionWriter) Unwrap() http.ResponseWriter {
	if w == nil {
		return nil
	}
	return w.ResponseWriter
}

func (w *responseCompressionWriter) Written() bool {
	return w.written || w.ResponseWriter.Written()
}

func (w *responseCompressionWriter) Size() int {
	if !w.Written() {
		return -1
	}
	return w.size
}

func (w *responseCompressionWriter) Status() int {
	if w.status == 0 {
		return w.ResponseWriter.Status()
	}
	return w.status
}

func (w *responseCompressionWriter) WriteHeader(code int) {
	if w.Written() {
		return
	}
	w.ResponseWriter.WriteHeader(code)
	w.status = w.ResponseWriter.Status()
}

func (w *responseCompressionWriter) Write(data []byte) (n int, err error) {
	if w.writeErr != nil {
		return 0, w.writeErr
	}
	w.written = true
	defer func() { w.size += n }()
	if !w.decided {
		if w.Header().Get("Content-Type") == "" && len(data) > 0 {
			w.Header().Set("Content-Type", http.DetectContentType(data))
		}
		w.decide(false, w.buffer.Len()+len(data))
	}
	if !w.decided {
		return w.buffer.Write(data)
	}
	if errDrain := w.drain(); errDrain != nil {
		return 0, errDrain
	}
	if w.compressor != nil {
		n, w.writeErr = w.compressor.Write(data)
	} else {
		n, w.writeErr = w.ResponseWriter.Write(data)
	}
	return n, w.writeErr
}

// WriteString must not bypass the compression path through the embedded writer.
func (w *responseCompressionWriter) WriteString(data string) (int, error) {
	return w.Write([]byte(data))
}

func (w *responseCompressionWriter) WriteHeaderNow() {
	w.written = true
	w.decide(true, w.buffer.Len())
	if errDrain := w.drain(); errDrain != nil {
		log.Debugf("response compression header write failed: %v", errDrain)
	}
	w.ResponseWriter.WriteHeaderNow()
}

func (w *responseCompressionWriter) Flush() {
	if errFlush := w.FlushError(); errFlush != nil {
		log.Debugf("response compression flush failed: %v", errFlush)
	}
}

// FlushError preserves transport errors for httpwire and streaming cancellation.
func (w *responseCompressionWriter) FlushError() error {
	if w.writeErr != nil {
		return w.writeErr
	}
	w.written = true
	w.decide(true, w.buffer.Len())
	if errDrain := w.drain(); errDrain != nil {
		return errDrain
	}
	if w.compressor != nil {
		if errFlush := w.compressor.Flush(); errFlush != nil {
			w.writeErr = errFlush
			return errFlush
		}
	}
	w.writeErr = httpwire.FlushResponse(w.ResponseWriter)
	return w.writeErr
}

func (w *responseCompressionWriter) decide(force bool, size int) {
	if w.decided {
		return
	}
	status := w.Status()
	mediaType, _, _ := strings.Cut(w.Header().Get("Content-Type"), ";")
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	compressible := strings.HasPrefix(mediaType, "text/") || mediaType == "application/json" ||
		(strings.HasPrefix(mediaType, "application/") && strings.HasSuffix(mediaType, "+json")) ||
		mediaType == "application/javascript" || mediaType == "image/svg+xml"
	if w.ResponseWriter.Written() || status < 200 || status == http.StatusNoContent ||
		status == http.StatusNotModified || status == http.StatusPartialContent ||
		w.Header().Get("Content-Encoding") != "" || !compressible {
		w.decided = true
		return
	}
	streaming := mediaType == "text/event-stream"
	if !streaming {
		if contentLength, errParseInt := strconv.ParseInt(w.Header().Get("Content-Length"), 10, 64); errParseInt == nil && contentLength >= 0 && contentLength < int64(w.minBytes) {
			w.decided = true
			return
		}
		if size < w.minBytes {
			w.decided = force
			return
		}
	}
	compressor := newResponseCompressor(w.encoding, w.ResponseWriter)
	if compressor == nil {
		w.decided = true
		return
	}
	w.compressor = compressor
	w.decided = true
	w.Header().Del("Content-Length")
	w.Header().Set("Content-Encoding", w.encoding)
	// Gin otherwise commits headers lazily when the encoder emits its first bytes.
	w.ResponseWriter.WriteHeaderNow()
}

func appendCompressionVary(header http.Header) {
	seen := make(map[string]bool)
	var tokens []string
	for _, value := range header.Values("Vary") {
		for _, token := range strings.Split(value, ",") {
			token = strings.TrimSpace(token)
			key := strings.ToLower(token)
			if token != "" && !seen[key] {
				tokens = append(tokens, token)
				seen[key] = true
			}
		}
	}
	if !seen["accept-encoding"] {
		tokens = append(tokens, "Accept-Encoding")
	}
	header.Set("Vary", strings.Join(tokens, ", "))
}

func (w *responseCompressionWriter) drain() error {
	if w.writeErr != nil || w.buffer.Len() == 0 {
		return w.writeErr
	}
	var target io.Writer = w.ResponseWriter
	if w.compressor != nil {
		target = w.compressor
	}
	_, w.writeErr = target.Write(w.buffer.Bytes())
	w.buffer.Reset()
	return w.writeErr
}

func (w *responseCompressionWriter) finish() {
	w.decide(true, w.buffer.Len())
	if errDrain := w.drain(); errDrain != nil {
		log.Debugf("response compression final write failed: %v", errDrain)
	}
	if w.compressor != nil {
		if errClose := w.compressor.Close(); errClose != nil {
			if w.writeErr != nil || errors.Is(errClose, context.Canceled) || errors.Is(errClose, net.ErrClosed) ||
				errors.Is(errClose, io.ErrClosedPipe) || errors.Is(errClose, syscall.EPIPE) || errors.Is(errClose, syscall.ECONNRESET) {
				log.Debugf("response compression client disconnected: %v", errClose)
			} else {
				log.Errorf("close response compression encoder: %v", errClose)
			}
		}
		// Drop the response writer reference before pooling the encoder, and fail later writes
		// instead of sending them uncompressed under Content-Encoding.
		w.compressor.Reset(io.Discard)
		responseCompressorPools[w.encoding].Put(w.compressor)
		w.compressor = nil
		if w.writeErr == nil {
			w.writeErr = io.ErrClosedPipe
		}
	}
}
