package helps

import (
	"io"
)

const (
	MaxErrorResponseBodyBytes        = 1 << 20
	errorResponseBodyTruncatedMarker = "\n...[error response body truncated]...\n"
)

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
