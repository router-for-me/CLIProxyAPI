package management

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

type runtimeShutdownFlushBarrier struct {
	*httptest.ResponseRecorder
	started chan struct{}
	release chan struct{}
}

func (w *runtimeShutdownFlushBarrier) Flush() {
	close(w.started)
	<-w.release
	w.ResponseRecorder.Flush()
}

func TestPostRuntimeShutdownWaitsForResponseFlush(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, peer := range []string{"127.0.0.1:45678", "[::1]:45678"} {
		t.Run(peer, func(t *testing.T) {
			writer := &runtimeShutdownFlushBarrier{
				ResponseRecorder: httptest.NewRecorder(),
				started:          make(chan struct{}),
				release:          make(chan struct{}),
			}
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(writer.release) }) }
			t.Cleanup(release)
			shutdownCalled := make(chan struct{})
			handlerDone := make(chan struct{})
			handler := NewHandlerWithoutConfigFilePath(nil, nil)
			handler.SetRuntimeControl(RuntimeInfo{}, func() { close(shutdownCalled) })
			ctx, _ := gin.CreateTestContext(writer)
			ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/runtime-shutdown", nil)
			ctx.Request.RemoteAddr = peer
			go func() {
				defer close(handlerDone)
				handler.PostRuntimeShutdown(ctx)
			}()
			select {
			case <-writer.started:
			case <-shutdownCalled:
				t.Fatal("shutdown ran before the response was flushed")
			case <-time.After(time.Second):
				t.Fatal("response flush was not reached")
			}
			select {
			case <-shutdownCalled:
				t.Fatal("shutdown ran while the response flush was blocked")
			default:
			}
			release()
			select {
			case <-shutdownCalled:
			case <-time.After(time.Second):
				t.Fatal("shutdown did not run after the response flush")
			}
			<-handlerDone
			if writer.Code != http.StatusAccepted || !writer.Flushed || writer.Header().Get("Content-Length") != "0" || writer.Body.Len() != 0 {
				t.Fatalf("acknowledgement was not completed: status=%d flushed=%v length=%q body=%q",
					writer.Code, writer.Flushed, writer.Header().Get("Content-Length"), writer.Body.String())
			}
		})
	}
}

func TestPostRuntimeShutdownAcknowledgesBeforeImmediateConnectionClose(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := NewHandlerWithoutConfigFilePath(nil, nil)
	shutdownDone := make(chan struct{})
	engine := gin.New()
	engine.POST("/v0/management/runtime-shutdown", func(ctx *gin.Context) {
		handler.PostRuntimeShutdown(ctx)
		// Prevent implicit net/http end-of-handler flushing from hiding the race.
		<-shutdownDone
	})
	server := httptest.NewServer(engine)
	t.Cleanup(server.Close)
	handler.SetRuntimeControl(RuntimeInfo{}, func() {
		server.CloseClientConnections()
		close(shutdownDone)
	})
	client := server.Client()
	client.Timeout = 2 * time.Second
	response, errPost := client.Post(server.URL+"/v0/management/runtime-shutdown", "application/json", nil)
	if errPost != nil {
		t.Fatalf("shutdown acknowledgement was lost: %v", errPost)
	}
	defer func() {
		if errClose := response.Body.Close(); errClose != nil {
			t.Errorf("close acknowledgement body: %v", errClose)
		}
	}()
	body, errRead := io.ReadAll(response.Body)
	if errRead != nil {
		t.Fatalf("shutdown acknowledgement was truncated: %v", errRead)
	}
	if response.StatusCode != http.StatusAccepted || response.ContentLength != 0 || len(body) != 0 {
		t.Fatalf("unexpected acknowledgement: status=%d length=%d body=%q",
			response.StatusCode, response.ContentLength, body)
	}
	select {
	case <-shutdownDone:
	case <-time.After(time.Second):
		t.Fatal("shutdown callback did not finish")
	}
}
