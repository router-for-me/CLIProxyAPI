package util

import (
	"errors"
	"fmt"
	"io"
)

const DefaultResponseBodyLimit = 32 << 20

var ErrResponseBodyTooLarge = errors.New("response body too large")

func ReadResponseBodyLimited(reader io.Reader, maxBytes int64) ([]byte, error) {
	if reader == nil {
		return nil, nil
	}
	if maxBytes <= 0 {
		maxBytes = DefaultResponseBodyLimit
	}
	limited := io.LimitReader(reader, maxBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("%w: limit=%d", ErrResponseBodyTooLarge, maxBytes)
	}
	return data, nil
}

func ReadResponseBody(reader io.Reader) ([]byte, error) {
	return ReadResponseBodyLimited(reader, DefaultResponseBodyLimit)
}
