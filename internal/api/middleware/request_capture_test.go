package middleware

import (
	"bytes"
	"crypto/rand"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func captureTestRouter(t *testing.T, handler gin.HandlerFunc) (*gin.Engine, *logging.CaptureSpool) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	spool := logging.NewDurableCaptureSpool(t.TempDir(), key)
	if err := spool.Lease(true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = spool.Close() })
	router := gin.New()
	router.Use(func(c *gin.Context) { logging.SetGinRequestID(c, "full-inbound-uuid"); c.Next() })
	router.Use(RequestCaptureMiddleware(spool))
	router.POST("/v1/responses", handler)
	return router, spool
}
func TestCaptureMiddlewareCompleteWireBodies(t *testing.T) {
	router, spool := captureTestRouter(t, func(c *gin.Context) {
		CaptureAuthenticatedRequestBody(c)
		body, _ := io.ReadAll(c.Request.Body)
		c.Data(200, "application/octet-stream", body)
	})
	payload := bytes.Repeat([]byte{0, 0xff, 'x', '\n'}, (logging.CaptureBodyLimit+logging.CaptureChunkBytes)/4)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses?api_key=excluded", bytes.NewReader(payload))
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)
	if !bytes.Equal(res.Body.Bytes(), payload) {
		t.Fatal("response changed")
	}
	var request, response []byte
	var final logging.CaptureEvent
	var cursor uint64
	for {
		page := spool.Page(cursor)
		if len(page.Records) == 0 {
			break
		}
		for _, e := range page.Records {
			if e.Path != "/v1/responses" {
				t.Fatal("query captured")
			}
			if e.Chunk != nil {
				if e.Chunk.Direction == "request" {
					request = append(request, e.Chunk.Data...)
				} else {
					response = append(response, e.Chunk.Data...)
				}
			}
			cursor = e.Sequence
			final = e
		}
	}
	if !bytes.Equal(request, payload) || !bytes.Equal(response, payload) || final.State != "complete" {
		t.Fatal("wire bytes lost")
	}
}
func TestCaptureMiddlewareDoesNotPersistUnauthenticatedBodies(t *testing.T) {
	router, spool := captureTestRouter(t, func(c *gin.Context) { _, _ = io.ReadAll(c.Request.Body); c.String(401, "authentication rejected") })
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString("private body")))
	if len(spool.Page(0).Records) != 0 {
		t.Fatal("unauthenticated content retained")
	}
}
func TestCaptureMiddlewareMarksUnreadBodyAndWriteString(t *testing.T) {
	router, spool := captureTestRouter(t, func(c *gin.Context) { CaptureAuthenticatedRequestBody(c); _, _ = c.Writer.WriteString("hello") })
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString("not consumed")))
	page := spool.Page(0)
	if len(page.Records) != 3 || page.Records[2].Reason != "request-not-fully-consumed" || string(page.Records[1].Chunk.Data) != "hello" {
		t.Fatal("incorrect partial capture")
	}
}
func TestCaptureMiddlewareFailedWebsocketUpgradeEndsAsHTTP(t *testing.T) {
	router, spool := captureTestRouter(t, func(c *gin.Context) { CaptureAuthenticatedRequestBody(c); c.String(400, "bad upgrade") })
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	req.Header.Set("Upgrade", "websocket")
	router.ServeHTTP(httptest.NewRecorder(), req)
	events := spool.Page(0).Records
	if len(events) != 3 || events[2].Format != "http" || events[2].FrameCount != 0 || events[2].State != "complete" {
		t.Fatal("failed upgrade was not an HTTP capture")
	}
}
func TestCaptureMiddlewareRetainsBodiesWithFullRequestLoggingEnabled(t *testing.T) {
	for _, readBody := range []bool{true, false} {
		key := make([]byte, 32)
		_, _ = rand.Read(key)
		spool := logging.NewDurableCaptureSpool(t.TempDir(), key)
		if err := spool.Lease(true); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = spool.Close() })
		router := gin.New()
		router.Use(func(c *gin.Context) { logging.SetGinRequestID(c, "full-logged-trace"); c.Next() })
		router.Use(RequestCaptureMiddleware(spool), RequestLoggingMiddleware(&spyRequestLogger{}))
		router.POST("/v1/messages", func(c *gin.Context) {
			CaptureAuthenticatedRequestBody(c)
			if readBody {
				_, _ = io.ReadAll(c.Request.Body)
			}
			c.String(200, "ok")
		})
		payload := []byte("request consumed once by the authenticated file logger")
		router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(payload)))
		var request []byte
		var final logging.CaptureEvent
		var cursor uint64
		for {
			page := spool.Page(cursor)
			if len(page.Records) == 0 {
				break
			}
			for _, e := range page.Records {
				if e.Chunk != nil && e.Chunk.Direction == "request" {
					request = append(request, e.Chunk.Data...)
				}
				cursor = e.Sequence
				final = e
			}
		}
		if !bytes.Equal(request, payload) || final.State != "complete" {
			t.Fatal("full request logging bypassed or duplicated body capture")
		}
	}
}
