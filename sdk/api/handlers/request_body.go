package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
)

const requestBodyLimit = 16 << 20

// RequestBodyTooLargeCode is the stable API error code for an oversized request body.
const RequestBodyTooLargeCode = "request_body_too_large"

// RequestBodyTooLargeError indicates that the request body exceeded the configured limit.
type RequestBodyTooLargeError struct {
	Limit int
}

func (e *RequestBodyTooLargeError) Error() string {
	if e == nil {
		return RequestBodyTooLargeCode
	}
	return fmt.Sprintf("request body exceeds %d-byte limit", e.Limit)
}

// WriteRequestBodyError writes the stable client error for an oversized request body.
func WriteRequestBodyError(c *gin.Context, err error) bool {
	var tooLargeErr *RequestBodyTooLargeError
	if c == nil || !errors.As(err, &tooLargeErr) || tooLargeErr == nil {
		return false
	}
	c.JSON(http.StatusRequestEntityTooLarge, ErrorResponse{
		Error: ErrorDetail{
			Message: tooLargeErr.Error(),
			Type:    "invalid_request_error",
			Code:    RequestBodyTooLargeCode,
		},
	})
	return true
}

// ReadRequestBody reads the incoming request body and decodes supported
// Content-Encoding values before handlers inspect JSON fields.
func ReadRequestBody(c *gin.Context) ([]byte, error) {
	if c.Request.Body == nil {
		return nil, errors.New("cannot read nil body")
	}
	raw, err := readRequestBodyWithLimit(c.Request.Body, requestBodyLimit)
	if err != nil {
		return nil, err
	}

	encoding := ""
	if c != nil && c.Request != nil {
		encoding = strings.TrimSpace(c.Request.Header.Get("Content-Encoding"))
	}
	if encoding == "" || strings.EqualFold(encoding, "identity") {
		return raw, nil
	}

	decoded, err := decodeRequestBody(raw, encoding)
	if err != nil {
		if json.Valid(raw) {
			return raw, nil
		}
		return nil, err
	}
	return decoded, nil
}

func readRequestBodyWithLimit(body io.Reader, limit int) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, &RequestBodyTooLargeError{Limit: limit}
	}
	return data, nil
}

func decodeRequestBody(raw []byte, encoding string) ([]byte, error) {
	parts := strings.Split(encoding, ",")
	body := raw
	for i := len(parts) - 1; i >= 0; i-- {
		enc := strings.ToLower(strings.TrimSpace(parts[i]))
		switch enc {
		case "", "identity":
			continue
		case "zstd":
			decoded, err := decodeZstdRequestBody(body)
			if err != nil {
				return nil, err
			}
			body = decoded
		default:
			return nil, fmt.Errorf("unsupported request content encoding: %s", enc)
		}
	}
	return body, nil
}

func decodeZstdRequestBody(raw []byte) ([]byte, error) {
	decoder, err := zstd.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("failed to create zstd request decoder: %w", err)
	}
	defer decoder.Close()

	decoded, err := io.ReadAll(decoder)
	if err != nil {
		return nil, fmt.Errorf("failed to decode zstd request body: %w", err)
	}
	return decoded, nil
}
