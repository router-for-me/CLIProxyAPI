package oauthflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ReadBounded detects oversized responses rather than accepting truncated JSON.
func ReadBounded(r io.Reader, limit int64) ([]byte, error) {
	if r == nil || limit < 1 || limit == int64(^uint64(0)>>1) {
		return nil, errors.New("oauth: invalid response reader or limit")
	}
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, fmt.Errorf("oauth: read response: %w", err)
	}
	if int64(len(data)) > limit {
		return nil, errors.New("oauth: response exceeds size limit")
	}
	return data, nil
}

// DecodeJSON rejects trailing objects as well as malformed and oversized JSON.
func DecodeJSON(r io.Reader, value any, limit int64) error {
	raw, err := ReadBounded(r, limit)
	if err != nil {
		return err
	}
	if err = json.Unmarshal(raw, value); err != nil {
		return errors.New("oauth: malformed JSON response")
	}
	return nil
}

// AcquisitionClient copies an injected client, so enforcing credential-acquisition
// bounds does not mutate the caller's shared inference client. Redirects are refused
// to avoid forwarding authorization codes or bearer credentials to another endpoint.
func AcquisitionClient(client *http.Client) *http.Client {
	clone := http.Client{}
	if client != nil {
		clone = *client
	}
	if clone.Timeout <= 0 || clone.Timeout > AcquisitionTimeout {
		clone.Timeout = AcquisitionTimeout
	}
	clone.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &clone
}

// ValidLifetime checks arithmetic bounds without inventing a provider lifetime.
func ValidLifetime(seconds int64) bool {
	return seconds > 0 && seconds <= int64((time.Duration(1<<63-1))/time.Second)
}
