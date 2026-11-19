package middleware

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
)

type captureReader struct {
	io.ReadCloser
	write func([]byte)
	eof   bool
	read  int64
}

func (r *captureReader) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	r.write(p[:n])
	r.read += int64(n)
	if err == io.EOF {
		r.eof = true
	}
	return n, err
}

type captureWriter struct {
	gin.ResponseWriter
	write       func([]byte)
	hijacked    bool
	writeFailed bool
}

func (w *captureWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *captureWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	w.write(p[:n])
	if err != nil || n != len(p) {
		w.writeFailed = true
	}
	return n, err
}
func (w *captureWriter) WriteString(p string) (int, error) {
	n, err := w.ResponseWriter.WriteString(p)
	for offset := 0; offset < n; offset += logging.CaptureChunkBytes {
		w.write([]byte(p[offset:min(n, offset+logging.CaptureChunkBytes)]))
	}
	if err != nil || n != len(p) {
		w.writeFailed = true
	}
	return n, err
}
func (w *captureWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := w.ResponseWriter.Hijack()
	w.hijacked = err == nil
	return conn, rw, err
}

const archiveAuthenticatedKey = "CPA_ARCHIVE_AUTHENTICATED"
const captureStartContextKey = "CPA_CAPTURE_START"

func startAuthenticatedCapture(c *gin.Context) {
	if start, ok := c.Get(captureStartContextKey); ok {
		if begin, valid := start.(func() *logging.CaptureStream); valid {
			begin()
		}
	}
}

type captureSink interface {
	StartCapture(logging.CaptureRecord) *logging.CaptureStream
}

// Body wrappers are installed only after authentication and capture opt-in; unauthenticated transport remains untouched.
func RequestCaptureMiddleware(spool captureSink) gin.HandlerFunc {
	return func(c *gin.Context) {
		trace := logging.GetGinRequestID(c)
		if trace == "" || !shouldLogRequest(c.Request.URL.Path) {
			c.Next()
			return
		}
		r := logging.CaptureRecord{ID: uuid.NewString(), TraceID: trace, StartedAt: time.Now().UTC().Format(time.RFC3339Nano), Method: boundedCaptureField(c.Request.Method, 16), Path: boundedCaptureField(c.Request.URL.Path, 2048), State: "complete", Reason: "http-finalized", RequestType: boundedCaptureField(c.GetHeader("Content-Type"), 256), RequestEncoding: boundedCaptureField(c.GetHeader("Content-Encoding"), 64), Format: "http"}
		websocket := strings.EqualFold(c.GetHeader("Upgrade"), "websocket")
		if websocket {
			r.Format = "websocket"
		}
		var mu sync.Mutex
		var stream *logging.CaptureStream
		var reader *captureReader
		var writer *captureWriter
		started := false
		ensure := func() *logging.CaptureStream {
			mu.Lock()
			defer mu.Unlock()
			if !started && c.GetBool(archiveAuthenticatedKey) {
				started = true
				stream = spool.StartCapture(r)
				if stream != nil {
					if c.Request.Body != nil {
						reader = &captureReader{ReadCloser: c.Request.Body, write: func(p []byte) { stream.Write("request", p) }}
						c.Request.Body = reader
					}
				}
			}
			return stream
		}
		writer = &captureWriter{ResponseWriter: c.Writer, write: func(p []byte) { ensure().Write("response", p) }}
		c.Writer = writer
		c.Set(captureStartContextKey, ensure)
		var messages *logging.CaptureMessages
		if websocket {
			messages = logging.NewStreamingCaptureMessages(ensure)
			c.Set(logging.CaptureMessagesContextKey, messages)
		}
		defer func() {
			panicValue := recover()
			if writer == nil {
				if panicValue != nil {
					panic(panicValue)
				}
				return
			}
			r.Status = writer.Status()
			r.ResponseType = boundedCaptureField(writer.Header().Get("Content-Type"), 256)
			r.ResponseEncoding = boundedCaptureField(writer.Header().Get("Content-Encoding"), 64)
			cleanWebsocketClose := false
			if websocket {
				if writer.hijacked {
					r.Status, r.Reason = http.StatusSwitchingProtocols, "websocket-messages-finalized"
					_, partial, closed, reason := messages.Snapshot()
					cleanWebsocketClose = closed && !partial
					if partial {
						r.State, r.Reason = "partial", reason
					}
				} else {
					r.Format = "http"
				}
			}
			if !writer.hijacked && reader != nil && !reader.eof && (c.Request.ContentLength < 0 || reader.read < c.Request.ContentLength) {
				r.State, r.Reason = "partial", "request-not-fully-consumed"
			}
			// A recorded WebSocket close handshake can precede HTTP context cancellation.
			if !cleanWebsocketClose && c.Request.Context().Err() != nil {
				r.State, r.Reason = "partial", "connection-cancelled"
			}
			if writer.writeFailed {
				r.State, r.Reason = "partial", "response-write-failed"
			}
			if panicValue != nil {
				r.State, r.Reason = "partial", "handler-panic"
			}
			ensure().Finish(r)
			if panicValue != nil {
				panic(panicValue)
			}
		}()
		c.Next()
	}
}

var _ http.Hijacker = (*captureWriter)(nil)

func boundedCaptureField(value string, limit int) string {
	if len(value) > limit {
		value = value[:limit]
	}
	return strings.ToValidUTF8(value, "�")
}
