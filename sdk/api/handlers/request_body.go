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
	log "github.com/sirupsen/logrus"
)

const requestBodyLimit = 16 << 20

// RequestBodyTooLargeCode is the stable API error code for an oversized request body.
const RequestBodyTooLargeCode = "request_body_too_large"

// RequestBodyTooLargeError indicates that the request body exceeded the configured limit.
type RequestBodyTooLargeError struct {
	Limit           int
	Route           string
	ContentEncoding string
	RawBytes        int64
	DecodedBytes    int64
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
	fields := log.Fields{
		"route":            tooLargeErr.Route,
		"content_encoding": tooLargeErr.ContentEncoding,
		"raw_bytes":        tooLargeErr.RawBytes,
		"limit_bytes":      tooLargeErr.Limit,
	}
	if tooLargeErr.DecodedBytes > 0 {
		fields["decoded_bytes"] = tooLargeErr.DecodedBytes
	}
	log.WithFields(fields).Warn("request body rejected: size limit exceeded")
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
	if c == nil || c.Request == nil || c.Request.Body == nil {
		return nil, errors.New("cannot read nil body")
	}
	raw, err := readRequestBodyWithLimit(c.Request.Body, requestBodyLimit)
	if err != nil {
		return nil, withRequestBodyMetadata(err, c, "", 0)
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
			var tooLargeErr *RequestBodyTooLargeError
			if errors.As(err, &tooLargeErr) {
				return nil, withRequestBodyMetadata(err, c, encoding, int64(len(raw)))
			}
			return raw, nil
		}
		return nil, withRequestBodyMetadata(err, c, encoding, int64(len(raw)))
	}
	return decoded, nil
}

func readRequestBodyWithLimit(body io.Reader, limit int) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, &RequestBodyTooLargeError{Limit: limit, RawBytes: int64(len(data))}
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
				var tooLargeErr *RequestBodyTooLargeError
				if errors.As(err, &tooLargeErr) && tooLargeErr != nil {
					tooLargeErr.RawBytes = int64(len(raw))
				}
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

	decoded, err := io.ReadAll(io.LimitReader(decoder, int64(requestBodyLimit)+1))
	if err != nil {
		return nil, fmt.Errorf("failed to decode zstd request body: %w", err)
	}
	if len(decoded) > requestBodyLimit {
		return nil, &RequestBodyTooLargeError{
			Limit:        requestBodyLimit,
			RawBytes:     int64(len(raw)),
			DecodedBytes: int64(len(decoded)),
		}
	}
	return decoded, nil
}

func withRequestBodyMetadata(err error, c *gin.Context, encoding string, rawBytes int64) error {
	var tooLargeErr *RequestBodyTooLargeError
	if !errors.As(err, &tooLargeErr) || tooLargeErr == nil {
		return err
	}
	if tooLargeErr.RawBytes == 0 {
		tooLargeErr.RawBytes = rawBytes
	}
	if c != nil && c.Request != nil {
		if c.Request.URL != nil {
			tooLargeErr.Route = boundedRequestMetadata(c.Request.URL.Path, 256)
		}
		if encoding == "" {
			encoding = c.Request.Header.Get("Content-Encoding")
		}
	}
	tooLargeErr.ContentEncoding = boundedRequestMetadata(encoding, 128)
	return err
}

func boundedRequestMetadata(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
