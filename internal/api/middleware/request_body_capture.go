package middleware

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"

	"github.com/gin-gonic/gin"
)

const maxLoggedRequestBodyBytes int64 = 1 << 20 // 1 MiB

type capturedRequestBody struct {
	info          *RequestInfo
	limit         int64
	declaredBytes int64
	buffer        bytes.Buffer
	hasher        hash.Hash
	observedBytes int64
	truncated     bool
	complete      bool
}

func newCapturedRequestBody(c *gin.Context, info *RequestInfo, limit int64) *capturedRequestBody {
	if c == nil || c.Request == nil || c.Request.Body == nil || info == nil {
		return nil
	}

	body := c.Request.Body
	capture := &capturedRequestBody{
		info:          info,
		limit:         limit,
		declaredBytes: c.Request.ContentLength,
		hasher:        sha256.New(),
	}
	c.Request.Body = &capturedRequestReadCloser{
		reader:  io.TeeReader(body, capture),
		closer:  body,
		capture: capture,
	}
	return capture
}

func (c *capturedRequestBody) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}

	c.observedBytes += int64(len(p))
	if _, err := c.hasher.Write(p); err != nil {
		return 0, err
	}

	c.capturePreview(p)
	c.truncated = c.observedBytes > int64(c.buffer.Len())
	return len(p), nil
}

func (c *capturedRequestBody) capturePreview(chunk []byte) {
	remaining := c.limit - int64(c.buffer.Len())
	if remaining <= 0 {
		return
	}

	writeLen := len(chunk)
	if int64(writeLen) > remaining {
		writeLen = int(remaining)
	}
	_, _ = c.buffer.Write(chunk[:writeLen])
	c.info.Body = c.buffer.Bytes()
}

func (c *capturedRequestBody) markComplete() {
	c.complete = true
}

func (c *capturedRequestBody) markClosed() {
	if c.complete {
		return
	}
	if c.declaredBytes == 0 {
		c.complete = true
		return
	}
	if c.declaredBytes > 0 && c.observedBytes == c.declaredBytes {
		c.complete = true
	}
}

func (c *capturedRequestBody) applyToContext(ctx *gin.Context) {
	if ctx == nil {
		return
	}
	c.markClosed()
	if body := c.logBody(); len(body) > 0 {
		ctx.Set(requestBodyOverrideContextKey, body)
	}
}

func (c *capturedRequestBody) logBody() []byte {
	if c == nil {
		return nil
	}
	if c.complete && !c.truncated {
		return nil
	}
	if c.observedBytes == 0 && c.declaredBytes < 0 {
		return nil
	}
	return []byte(c.summary())
}

func (c *capturedRequestBody) summary() string {
	summary := fmt.Sprintf(
		"[request body omitted] captured_bytes=%d observed_bytes=%d complete=%t truncated=%t",
		c.buffer.Len(),
		c.observedBytes,
		c.complete,
		c.truncated,
	)
	if c.declaredBytes >= 0 {
		summary += fmt.Sprintf(" declared_bytes=%d", c.declaredBytes)
	}
	return summary + " observed_sha256=" + hex.EncodeToString(c.hasher.Sum(nil))
}

type capturedRequestReadCloser struct {
	reader  io.Reader
	closer  io.Closer
	capture *capturedRequestBody
}

func (r *capturedRequestReadCloser) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if err == io.EOF && r.capture != nil {
		r.capture.markComplete()
	}
	return n, err
}

func (r *capturedRequestReadCloser) Close() error {
	if r.capture != nil {
		r.capture.markClosed()
	}
	return r.closer.Close()
}
