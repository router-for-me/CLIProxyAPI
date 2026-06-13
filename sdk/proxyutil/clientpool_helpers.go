package proxyutil

import (
	"io"
	"net/http"
	"time"
)

// httpTimeoutDuration converts seconds to a time.Duration.
// Returns 0 if seconds <= 0.
func httpTimeoutDuration(seconds int) time.Duration {
	if seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

// DrainAndClose reads and discards any remaining data in resp.Body, then
// closes it. This is essential for HTTP/1.1 connection reuse: a connection
// cannot return to the Transport's idle pool while the response body is
// still open with unread data.
//
// Call this instead of resp.Body.Close() when the response body does not
// need to be fully consumed (e.g., on error paths, after reading only
// headers, or after json.NewDecoder.Decode which may leave trailing data).
//
// It is safe to call with a nil response or a nil body.
func DrainAndClose(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	// Limit the amount we drain to 1 MB to prevent a malicious or
	// buggy server from causing unbounded memory/CPU consumption.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
}
