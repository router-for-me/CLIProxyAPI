package handlers

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/httpcompression"
)

func TestCompressionSocketRequests(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/", func(c *gin.Context) {
		c.Set(MaxRequestBodyBytesContextKey, int64(1024))
		b, e := ReadRequestBody(c)
		if e != nil {
			c.String(400, "%v", e)
			return
		}
		c.Data(200, "application/json", b)
	})
	s := httptest.NewServer(r)
	defer s.Close()
	for _, enc := range []string{"identity", "gzip", "br", "zstd"} {
		t.Run(enc, func(t *testing.T) {
			payload := []byte(`{"ok":true}`)
			var b bytes.Buffer
			if enc == "identity" {
				b.Write(payload)
			} else {
				z, e := httpcompression.Encode(&b, enc)
				if e != nil {
					t.Fatal(e)
				}
				_, _ = z.Write(payload)
				_ = z.Close()
			}
			q, _ := http.NewRequest("POST", s.URL, &b)
			q.Header.Set("Content-Encoding", enc)
			p, e := http.DefaultClient.Do(q)
			if e != nil {
				t.Fatal(e)
			}
			defer p.Body.Close()
			out, _ := io.ReadAll(p.Body)
			if p.StatusCode != 200 || !bytes.Equal(out, payload) {
				t.Fatalf("status=%d body=%s", p.StatusCode, out)
			}
		})
	}
}
func TestCompressionDecodedLimit(t *testing.T) {
	for _, enc := range []string{"gzip", "br", "zstd"} {
		var b bytes.Buffer
		z, _ := httpcompression.Encode(&b, enc)
		_, _ = z.Write(bytes.Repeat([]byte("x"), 4096))
		_ = z.Close()
		if _, err := decodeRequestBody(b.Bytes(), enc, 64); err == nil {
			t.Fatalf("%s accepted oversized decoded body", enc)
		}
	}
}
