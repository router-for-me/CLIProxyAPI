package middleware

import (
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/httpwire"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/httpcompression"
)

// ResponseCompression negotiates HTTP content coding, including flushable SSE.
// WebSocket upgrades and management endpoints retain their existing wire representation.
func ResponseCompression() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method == http.MethodHead ||
			strings.HasPrefix(c.Request.URL.Path, "/v0/management") || strings.HasPrefix(c.Request.URL.Path, "/v8/management") ||
			strings.Contains(strings.ToLower(c.GetHeader("Connection")), "upgrade") || c.GetHeader("Upgrade") != "" {
			c.Next()
			return
		}
		encoding, qualities := negotiateEncoding(strings.Join(c.Request.Header.Values("Accept-Encoding"), ","))
		_, hasAcceptEncoding := c.Request.Header["Accept-Encoding"]
		w := &compressedResponseWriter{
			ResponseWriter:   c.Writer,
			encoding:         encoding,
			qualities:        qualities,
			noAcceptEncoding: !hasAcceptEncoding,
			noTransform:      strings.Contains(strings.ToLower(c.GetHeader("Cache-Control")), "no-transform"),
		}
		c.Writer = w
		c.Next()
		if w.encoder != nil {
			if err := w.encoder.Close(); err != nil {
				_ = c.Error(err)
			}
		}
	}
}

func negotiateEncoding(value string) (string, map[string]float64) {
	qualities := map[string]float64{}
	for _, part := range strings.Split(value, ",") {
		fields := strings.Split(part, ";")
		name := strings.ToLower(strings.TrimSpace(fields[0]))
		q := 1.0
		for _, p := range fields[1:] {
			kv := strings.SplitN(strings.TrimSpace(p), "=", 2)
			if len(kv) == 2 && strings.EqualFold(kv[0], "q") {
				var err error
				q, err = strconv.ParseFloat(kv[1], 64)
				if err != nil || math.IsNaN(q) || q < 0 || q > 1 {
					q = 0
				}
			}
		}
		qualities[name] = q
	}
	best := ""
	score := 0.0
	for _, enc := range []string{"zstd", "br", "gzip"} {
		q, ok := qualities[enc]
		if !ok {
			q = qualities["*"]
		}
		if q > score {
			best = enc
			score = q
		}
	}
	if q, ok := qualities["identity"]; ok && q > score {
		return "", qualities
	}
	if best == "" {
		identity, explicit := qualities["identity"]
		wildcard, wildcardSet := qualities["*"]
		if (explicit && identity == 0) || (!explicit && wildcardSet && wildcard == 0) {
			return "unacceptable", qualities
		}
	}
	return best, qualities
}

func acceptsEncoding(qualities map[string]float64, encoding string) bool {
	if q, ok := qualities[encoding]; ok {
		return q > 0
	}
	if q, ok := qualities["*"]; ok {
		return q > 0
	}
	return encoding == "identity"
}

type compressedResponseWriter struct {
	gin.ResponseWriter
	encoding         string
	qualities        map[string]float64
	noAcceptEncoding bool
	noTransform      bool
	encoder          httpcompression.Writer
	started          bool
	rejected         bool
}

func (w *compressedResponseWriter) start() {
	if w.started {
		return
	}
	w.started = true
	h := w.Header()
	h.Add("Vary", "Accept-Encoding")
	status := w.Status()
	if status < 200 || status == 204 || status == 205 || status == 304 {
		return
	}
	if existing := h.Get("Content-Encoding"); existing != "" {
		if !w.noAcceptEncoding {
			for _, coding := range strings.Split(existing, ",") {
				if !acceptsEncoding(w.qualities, strings.ToLower(strings.TrimSpace(coding))) {
					w.reject()
					break
				}
			}
		}
		return
	}
	if w.encoding == "unacceptable" {
		w.reject()
		return
	}
	if w.noTransform || h.Get("Content-Range") != "" || strings.Contains(strings.ToLower(h.Get("Cache-Control")), "no-transform") {
		if !acceptsEncoding(w.qualities, "identity") {
			w.reject()
		}
		return
	}
	if w.encoding == "" {
		return
	}
	// Go must not sniff compressed bytes as the representation's media type.
	if h.Get("Content-Type") == "" {
		h.Set("Content-Type", "application/octet-stream")
	}
	encoder, err := httpcompression.Encode(w.ResponseWriter, w.encoding)
	if err != nil {
		return
	}
	w.encoder = encoder
	h.Set("Content-Encoding", w.encoding)
	h.Del("Content-Length")
	if etag := h.Get("ETag"); etag != "" && !strings.HasPrefix(etag, "W/") {
		h.Set("ETag", "W/"+etag)
	}
}

func (w *compressedResponseWriter) reject() {
	w.rejected = true
	w.Header().Del("Content-Length")
	w.Header().Del("Content-Encoding")
	w.ResponseWriter.WriteHeader(http.StatusNotAcceptable)
}
func (w *compressedResponseWriter) WriteHeader(code int) { w.ResponseWriter.WriteHeader(code) }
func (w *compressedResponseWriter) WriteHeaderNow()      { w.start(); w.ResponseWriter.WriteHeaderNow() }
func (w *compressedResponseWriter) Write(b []byte) (int, error) {
	if !w.started && w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", http.DetectContentType(b))
	}
	w.start()
	if w.rejected {
		w.ResponseWriter.WriteHeaderNow()
		return len(b), nil
	}
	if w.encoder != nil {
		return w.encoder.Write(b)
	}
	return w.ResponseWriter.Write(b)
}
func (w *compressedResponseWriter) WriteString(s string) (int, error) { return w.Write([]byte(s)) }
func (w *compressedResponseWriter) Flush()                            { _ = w.FlushError() }

func (w *compressedResponseWriter) FlushError() error {
	w.start()
	if w.encoder != nil {
		if err := w.encoder.Flush(); err != nil {
			return err
		}
	}
	return httpwire.FlushResponse(w.ResponseWriter)
}

func (w *compressedResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
