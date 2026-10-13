package httpwire

import (
	"bufio"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

type flushErrorWriter struct {
	*httptest.ResponseRecorder
	flushes       int
	legacyFlushes int
}

func (w *flushErrorWriter) Flush() {
	w.legacyFlushes++
	w.ResponseRecorder.Flush()
}

func (w *flushErrorWriter) FlushError() error {
	w.flushes++
	return io.ErrClosedPipe
}

type flushMiddlewareWriter struct {
	http.ResponseWriter
	flushes int
}

func (w *flushMiddlewareWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *flushMiddlewareWriter) FlushError() error {
	w.flushes++
	w.Header().Set("X-Middleware-Flush", "preserved")
	return FlushResponse(w.ResponseWriter)
}

func TestFlushResponsePreservesMiddlewareBeforeUnwrap(t *testing.T) {
	transport := &flushErrorWriter{ResponseRecorder: httptest.NewRecorder()}
	c, _ := gin.CreateTestContext(transport)
	c.Writer.WriteHeader(http.StatusAccepted)
	wrapper := &flushMiddlewareWriter{ResponseWriter: c.Writer}
	if errFlush := FlushResponse(wrapper); !errors.Is(errFlush, io.ErrClosedPipe) {
		t.Fatalf("flush error = %v", errFlush)
	}
	if wrapper.flushes != 1 || transport.flushes != 1 || transport.legacyFlushes != 0 {
		t.Fatalf("flush counts = %d/%d/%d, want 1/1/0", wrapper.flushes, transport.flushes, transport.legacyFlushes)
	}
	if c.Writer.Status() != http.StatusAccepted || transport.Code != http.StatusAccepted || c.Writer.Size() != 0 {
		t.Fatalf("Gin accounting: status=%d, transport=%d, size=%d", c.Writer.Status(), transport.Code, c.Writer.Size())
	}
	if !c.Writer.Written() || transport.Result().Header.Get("X-Middleware-Flush") != "preserved" {
		t.Fatal("flush bypassed middleware or Gin header accounting")
	}
}

type bufferedFlushWriter struct {
	http.ResponseWriter
	buffer   *bufio.Writer
	flushes  int
	unwraps  int
	flushErr error
}

func (w *bufferedFlushWriter) Write(p []byte) (int, error) { return w.buffer.Write(p) }
func (w *bufferedFlushWriter) Unwrap() http.ResponseWriter {
	w.unwraps++
	return w.ResponseWriter
}
func (w *bufferedFlushWriter) Flush() {
	w.flushes++
	w.flushErr = w.buffer.Flush()
	w.ResponseWriter.(http.Flusher).Flush()
}

// Embedding Gin's interface must not make middleware look like its raw writer.
type bufferedGinFlushWriter struct {
	gin.ResponseWriter
	buffered *bufferedFlushWriter
}

func (w *bufferedGinFlushWriter) Write(p []byte) (int, error) { return w.buffered.Write(p) }
func (w *bufferedGinFlushWriter) Flush()                      { w.buffered.Flush() }
func (w *bufferedGinFlushWriter) Unwrap() http.ResponseWriter {
	return w.buffered.Unwrap()
}

type countedFlushWriter struct {
	*httptest.ResponseRecorder
	flushes int
}

func (w *countedFlushWriter) Flush() {
	w.flushes++
	w.ResponseRecorder.Flush()
}

func TestFlushResponsePreservesBufferedLegacyWrapper(t *testing.T) {
	for _, shape := range []string{"http", "gin", "under_gin"} {
		t.Run(shape, func(t *testing.T) {
			transport := &countedFlushWriter{ResponseRecorder: httptest.NewRecorder()}
			c, _ := gin.CreateTestContext(transport)
			inner := http.ResponseWriter(transport)
			if shape == "gin" {
				inner = c.Writer
			}
			buffered := &bufferedFlushWriter{ResponseWriter: inner, buffer: bufio.NewWriter(inner)}
			var writer http.ResponseWriter = buffered
			if shape == "gin" {
				writer = &bufferedGinFlushWriter{ResponseWriter: c.Writer, buffered: buffered}
			} else if shape == "under_gin" {
				c, _ = gin.CreateTestContext(buffered)
				writer = c.Writer
			}
			wrapper := &flushMiddlewareWriter{ResponseWriter: writer}
			const body = "data: buffered response\n\n"
			if _, errWrite := wrapper.Write([]byte(body)); errWrite != nil {
				t.Fatal(errWrite)
			}
			if transport.Body.Len() != 0 || buffered.buffer.Buffered() != len(body) {
				t.Fatal("test write did not stay buffered")
			}
			if errFlush := FlushResponse(wrapper); errFlush != nil {
				t.Fatal(errFlush)
			}
			if buffered.flushErr != nil || buffered.buffer.Buffered() != 0 || transport.Body.String() != body {
				t.Errorf("buffer not drained: buffered=%d, body=%q, error=%v", buffered.buffer.Buffered(), transport.Body.String(), buffered.flushErr)
			}
			if wrapper.flushes != 1 || buffered.flushes != 1 || buffered.unwraps != 0 || transport.flushes != 1 {
				t.Errorf("calls: middleware=%d, buffer flush=%d, buffer unwrap=%d, transport=%d; want 1/1/0/1", wrapper.flushes, buffered.flushes, buffered.unwraps, transport.flushes)
			}
			if !transport.Flushed {
				t.Error("transport was not flushed")
			}
		})
	}
}

func TestFlushResponseSupportsLegacyFlusher(t *testing.T) {
	writer := httptest.NewRecorder()
	if errFlush := FlushResponse(writer); errFlush != nil {
		t.Fatal(errFlush)
	}
	if !writer.Flushed {
		t.Fatal("legacy flusher was not called")
	}
}
