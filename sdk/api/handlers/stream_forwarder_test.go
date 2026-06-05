package handlers

import (
	"errors"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

type countingFlusher struct {
	count atomic.Int32
}

func (f *countingFlusher) Flush() {
	f.count.Add(1)
}

func newForwardStreamTestContext() *gin.Context {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest("GET", "/stream", nil)
	return c
}

func TestForwardStreamCoalescesChunkFlushesWhenConfigured(t *testing.T) {
	t.Parallel()

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{
		Streaming: sdkconfig.StreamingConfig{FlushIntervalMilliseconds: 50},
	}, nil)
	c := newForwardStreamTestContext()
	flusher := &countingFlusher{}
	data := make(chan []byte, 2)
	data <- []byte("a")
	data <- []byte("b")
	close(data)
	errs := make(chan *interfaces.ErrorMessage)
	close(errs)

	handler.ForwardStream(c, flusher, func(error) {}, data, errs, StreamForwardOptions{
		WriteChunk: func([]byte) {},
	})

	if got := flusher.count.Load(); got != 1 {
		t.Fatalf("flush count = %d, want one final flush", got)
	}
}

func TestForwardStreamFlushesEveryChunkByDefault(t *testing.T) {
	t.Parallel()

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	c := newForwardStreamTestContext()
	flusher := &countingFlusher{}
	data := make(chan []byte, 2)
	data <- []byte("a")
	data <- []byte("b")
	close(data)
	errs := make(chan *interfaces.ErrorMessage)
	close(errs)

	var cancelErr error
	handler.ForwardStream(c, flusher, func(err error) { cancelErr = err }, data, errs, StreamForwardOptions{
		WriteChunk: func([]byte) {},
	})

	if got := flusher.count.Load(); got != 3 {
		t.Fatalf("flush count = %d, want two chunk flushes plus final flush", got)
	}
	if cancelErr != nil {
		t.Fatalf("cancel error = %v, want nil", cancelErr)
	}
}

func TestForwardStreamTerminalErrorFlushesImmediatelyWhenCoalescing(t *testing.T) {
	t.Parallel()

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{
		Streaming: sdkconfig.StreamingConfig{FlushIntervalMilliseconds: 50},
	}, nil)
	c := newForwardStreamTestContext()
	flusher := &countingFlusher{}
	data := make(chan []byte)
	close(data)
	errs := make(chan *interfaces.ErrorMessage, 1)
	errs <- &interfaces.ErrorMessage{Error: errors.New("upstream failed")}
	close(errs)

	handler.ForwardStream(c, flusher, func(error) {}, data, errs, StreamForwardOptions{
		WriteChunk:         func([]byte) {},
		WriteTerminalError: func(*interfaces.ErrorMessage) {},
	})

	if got := flusher.count.Load(); got != 1 {
		t.Fatalf("flush count = %d, want immediate terminal-error flush", got)
	}
}
