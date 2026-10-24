package middleware

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/httpwire"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/httpcompression"
)

func TestResponseCompressionRepeatedAcceptEncoding(t *testing.T) {
	router := gin.New()
	router.Use(ResponseCompression())
	router.GET("/", func(c *gin.Context) {
		c.Header("ETag", `"original"`)
		c.JSON(200, gin.H{"text": "negotiated response"})
	})
	server := httptest.NewServer(router)
	defer server.Close()
	request, err := http.NewRequest(http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Add("Accept-Encoding", "gzip;q=0, identity;q=0")
	request.Header.Add("Accept-Encoding", "br")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Encoding") != "br" {
		t.Fatalf("status=%d encoding=%q", response.StatusCode, response.Header.Get("Content-Encoding"))
	}
	if response.Header.Get("ETag") != `W/"original"` {
		t.Fatalf("compressed representation retained strong original ETag: %q", response.Header.Get("ETag"))
	}
	decoded, err := httpcompression.Decode(response.Body, "br")
	if err != nil {
		t.Fatal(err)
	}
	defer decoded.Close()
	body, err := io.ReadAll(decoded)
	if err != nil || string(body) != `{"text":"negotiated response"}` {
		t.Fatalf("body=%q error=%v", body, err)
	}
}

func TestResponseCompressionDoesNotSendExcludedIdentity(t *testing.T) {
	for _, mode := range []string{"request-no-transform", "response-no-transform", "range", "preencoded"} {
		t.Run(mode, func(t *testing.T) {
			router := gin.New()
			router.Use(ResponseCompression())
			router.GET("/", func(c *gin.Context) {
				switch mode {
				case "response-no-transform":
					c.Header("Cache-Control", "no-transform")
				case "range":
					c.Header("Content-Range", "bytes 0-3/4")
				case "preencoded":
					c.Header("Content-Encoding", "identity")
				}
				c.String(200, "body")
			})
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			request.Header.Set("Accept-Encoding", "gzip, identity;q=0")
			if mode == "request-no-transform" {
				request.Header.Set("Cache-Control", "no-transform")
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusNotAcceptable || response.Body.Len() != 0 || response.Header().Get("Content-Encoding") != "" {
				t.Fatalf("status=%d encoding=%q body=%q", response.Code, response.Header().Get("Content-Encoding"), response.Body.String())
			}
		})
	}
}

type compressionDisconnectedWriter struct {
	*httptest.ResponseRecorder
	err error
}

func (w *compressionDisconnectedWriter) FlushError() error { return w.err }

func TestCompressedSSEPreservesFlushFailure(t *testing.T) {
	disconnected := errors.New("downstream disconnected")
	for _, encoding := range []string{"identity", "gzip", "br", "zstd"} {
		t.Run(encoding, func(t *testing.T) {
			router := gin.New()
			router.Use(ResponseCompression())
			var flushErr error
			router.GET("/", func(c *gin.Context) {
				c.Header("Content-Type", "text/event-stream")
				if _, errWrite := c.Writer.WriteString("data: delivered\n\n"); errWrite != nil {
					t.Error(errWrite)
					return
				}
				flushErr = httpwire.FlushResponse(c.Writer)
			})
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			request.Header.Set("Accept-Encoding", encoding)
			router.ServeHTTP(&compressionDisconnectedWriter{ResponseRecorder: httptest.NewRecorder(), err: disconnected}, request)
			if !errors.Is(flushErr, disconnected) {
				t.Fatalf("transport flush failure hidden: %v", flushErr)
			}
		})
	}
}
