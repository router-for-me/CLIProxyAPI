package helps

import (
	"io"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
)

const (
	MaxErrorResponseBodyBytes        = 1 << 20
	MaxNonStreamResponseBodyBytes    = util.DefaultResponseBodyLimit
	errorResponseBodyTruncatedMarker = "\n...[error response body truncated]...\n"
)

var ErrResponseBodyTooLarge = util.ErrResponseBodyTooLarge

// ReadErrorResponseBody reads enough upstream error body for diagnostics without
// allowing malformed providers or proxies to allocate unbounded memory.
func ReadErrorResponseBody(reader io.Reader) ([]byte, error) {
	if reader == nil {
		return nil, nil
	}
	data, err := io.ReadAll(io.LimitReader(reader, MaxErrorResponseBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) <= MaxErrorResponseBodyBytes {
		return data, nil
	}
	data = data[:MaxErrorResponseBodyBytes]
	data = append(data, errorResponseBodyTruncatedMarker...)
	return data, nil
}

// ReadNonStreamResponseBody reads non-stream upstream responses with a hard cap.
// Successful non-stream responses are translated as one payload, so an abnormal
// upstream body must not be allowed to grow memory without bound.
func ReadNonStreamResponseBody(reader io.Reader) ([]byte, error) {
	return ReadResponseBodyLimited(reader, MaxNonStreamResponseBodyBytes)
}

func ReadResponseBodyLimited(reader io.Reader, maxBytes int64) ([]byte, error) {
	return util.ReadResponseBodyLimited(reader, maxBytes)
}
