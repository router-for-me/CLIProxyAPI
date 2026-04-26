package handlers

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

type countingFlusher struct {
	count int
}

func (f *countingFlusher) Flush() {
	f.count++
}

func newStreamForwardTestContext(t *testing.T) (*gin.Context, context.CancelFunc) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	reqCtx, cancel := context.WithCancel(req.Context())
	ctx.Request = req.WithContext(reqCtx)
	return ctx, cancel
}

func TestForwardStreamFlushesEveryChunkByDefault(t *testing.T) {
	ctx, cancelRequest := newStreamForwardTestContext(t)
	defer cancelRequest()

	data := make(chan []byte, 2)
	errs := make(chan *interfaces.ErrorMessage)
	data <- []byte("a")
	data <- []byte("b")
	close(data)
	close(errs)

	flusher := &countingFlusher{}
	handler := &BaseAPIHandler{Cfg: &config.SDKConfig{}}
	handler.ForwardStream(ctx, flusher, func(error) {}, data, errs, StreamForwardOptions{
		WriteChunk: func([]byte) bool { return true },
	})

	if flusher.count != 3 {
		t.Fatalf("flush count = %d, want 3", flusher.count)
	}
}

func TestForwardStreamFlushesTerminalErrorImmediately(t *testing.T) {
	ctx, cancelRequest := newStreamForwardTestContext(t)
	defer cancelRequest()

	data := make(chan []byte)
	errs := make(chan *interfaces.ErrorMessage, 1)
	errs <- &interfaces.ErrorMessage{Error: context.Canceled}
	close(data)
	close(errs)

	flusher := &countingFlusher{}
	wroteError := false
	handler := &BaseAPIHandler{Cfg: &config.SDKConfig{}}
	handler.ForwardStream(ctx, flusher, func(error) {}, data, errs, StreamForwardOptions{
		WriteTerminalError: func(*interfaces.ErrorMessage) { wroteError = true },
	})

	if !wroteError {
		t.Fatal("terminal error writer was not called")
	}
	if flusher.count != 1 {
		t.Fatalf("flush count = %d, want 1", flusher.count)
	}
}

func TestForwardStreamFlushesKeepAlive(t *testing.T) {
	ctx, cancelRequest := newStreamForwardTestContext(t)
	defer cancelRequest()

	data := make(chan []byte)
	errs := make(chan *interfaces.ErrorMessage)
	defer close(data)
	defer close(errs)

	flusher := &countingFlusher{}
	handler := &BaseAPIHandler{Cfg: &config.SDKConfig{}}
	interval := time.Millisecond
	wroteKeepAlive := make(chan struct{})
	done := make(chan struct{})
	go func() {
		handler.ForwardStream(ctx, flusher, func(error) {}, data, errs, StreamForwardOptions{
			KeepAliveInterval: &interval,
			WriteKeepAlive: func() {
				close(wroteKeepAlive)
				cancelRequest()
			},
		})
		close(done)
	}()

	select {
	case <-wroteKeepAlive:
	case <-time.After(time.Second):
		t.Fatal("keep-alive was not written")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("ForwardStream did not stop after request cancellation")
	}
	if flusher.count == 0 {
		t.Fatal("keep-alive did not flush")
	}
}
