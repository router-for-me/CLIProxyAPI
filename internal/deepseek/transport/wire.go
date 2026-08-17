package transport

import (
	"compress/flate"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
)

// accountIDKey is the context key used to carry the CLIP auth ID so the cookie
// jar can scope cookies per account. The executor stamps the request context
// with WithAccountID before dispatching through the wireDoer.
type accountIDKey struct{}

// WithAccountID returns a copy of ctx carrying the DeepSeek account identifier
// (the CLIP auth ID) so the wire-layer cookie jar can scope cookies per account.
func WithAccountID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, accountIDKey{}, id)
}

func accountIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(accountIDKey{}).(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

// AccountIDFromContext returns the account ID stamped on ctx by WithAccountID.
// Exported so the client package can read it for pow-cache scoping.
func AccountIDFromContext(ctx context.Context) string {
	return accountIDFromContext(ctx)
}

// wireDoer decorates a transport with the two behaviours that have to apply to
// every upstream request regardless of which endpoint issued it:
//
//   - replaying per-account cookies, so the session looks like a browser that
//     accepted what the server set rather than one that never stores anything;
//   - decompressing responses, which the caller must now do itself because we
//     advertise Chrome's full Accept-Encoding instead of letting the HTTP stack
//     inject a gzip-only header it would transparently unwrap.
type wireDoer struct {
	inner Doer
	jar   *CookieJar
}

// NewWireDoer wraps inner with per-account cookie replay and streaming response
// decompression. jar may be nil, in which case only decompression is applied.
func NewWireDoer(inner Doer, jar *CookieJar) Doer {
	if inner == nil {
		return nil
	}
	return wireDoer{inner: inner, jar: jar}
}

func (d wireDoer) Do(req *http.Request) (*http.Response, error) {
	accountID := accountIDFromContext(req.Context())
	if d.jar != nil {
		d.jar.apply(accountID, req)
	}

	resp, err := d.inner.Do(req)
	if err != nil {
		return nil, err
	}

	if d.jar != nil {
		d.jar.capture(accountID, resp)
	}
	if err := decompressResponse(resp); err != nil {
		_ = resp.Body.Close()
		return nil, err
	}
	return resp, nil
}

// CookieJar keeps cookies per account, in memory only.
//
// A client that presents a Chrome User-Agent, Origin and sec-fetch-site:
// same-origin but never sends a single Cookie is contradicting itself at the
// application layer. Replaying what the server sets removes that tell without
// changing any request semantics: nothing is invented, only echoed back.
type CookieJar struct {
	mu sync.RWMutex
	m  map[string]map[string]string // accountID -> cookie name -> value
}

// NewCookieJar creates an empty in-memory cookie jar keyed by account ID.
func NewCookieJar() *CookieJar {
	return &CookieJar{m: map[string]map[string]string{}}
}

func (j *CookieJar) apply(accountID string, req *http.Request) {
	if j == nil || req == nil || accountID == "" {
		return
	}
	// Never overwrite a Cookie header a caller set deliberately.
	if req.Header.Get("Cookie") != "" {
		return
	}

	j.mu.RLock()
	jar := j.m[accountID]
	names := make([]string, 0, len(jar))
	for name := range jar {
		names = append(names, name)
	}
	pairs := make([]string, 0, len(jar))
	sort.Strings(names) // stable ordering; a browser does not shuffle its cookies
	for _, name := range names {
		pairs = append(pairs, name+"="+jar[name])
	}
	j.mu.RUnlock()

	if len(pairs) == 0 {
		return
	}
	req.Header.Set("Cookie", strings.Join(pairs, "; "))
}

func (j *CookieJar) capture(accountID string, resp *http.Response) {
	if j == nil || resp == nil || accountID == "" {
		return
	}
	cookies := resp.Cookies()
	if len(cookies) == 0 {
		return
	}

	j.mu.Lock()
	defer j.mu.Unlock()
	jar := j.m[accountID]
	if jar == nil {
		jar = map[string]string{}
		j.m[accountID] = jar
	}
	for _, c := range cookies {
		if c.Name == "" {
			continue
		}
		if expired(c) {
			delete(jar, c.Name)
			continue
		}
		jar[c.Name] = c.Value
	}
}

func expired(c *http.Cookie) bool {
	if c.MaxAge < 0 {
		return true
	}
	if c.MaxAge == 0 && !c.Expires.IsZero() && c.Expires.Before(time.Now()) {
		return true
	}
	return false
}

// Forget drops an account's cookies, e.g. after re-login so a stale session
// cookie is not replayed alongside a fresh token.
func (j *CookieJar) Forget(accountID string) {
	if j == nil || accountID == "" {
		return
	}
	j.mu.Lock()
	delete(j.m, accountID)
	j.mu.Unlock()
}

// decompressResponse replaces resp.Body with a streaming decompressing reader
// and clears the now-meaningless Content-Encoding/Content-Length metadata.
//
// The readers are all incremental, so Server-Sent Events keep streaming rather
// than buffering until the response completes.
func decompressResponse(resp *http.Response) error {
	if resp == nil || resp.Body == nil {
		return nil
	}
	encoding := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding")))
	switch encoding {
	case "", "identity":
		return nil
	}

	decoded, err := DecompressReader(resp.Body, encoding)
	if err != nil {
		return err
	}
	if decoded == nil {
		return nil // unknown encoding: leave the body untouched
	}

	resp.Body = decoded
	resp.Header.Del("Content-Encoding")
	resp.Header.Del("Content-Length")
	resp.ContentLength = -1
	resp.Uncompressed = true
	return nil
}

// DecompressReader returns a reader that decodes encoding, or nil when the
// encoding is not one we handle. Exported so the client package can use it
// as a defensive fallback for responses that bypassed the wire layer.
func DecompressReader(body io.ReadCloser, encoding string) (io.ReadCloser, error) {
	switch encoding {
	case "gzip":
		gz, err := gzip.NewReader(body)
		if err != nil {
			return nil, err
		}
		return chainCloser{Reader: gz, closers: []io.Closer{gz, body}}, nil
	case "deflate":
		fl := flate.NewReader(body)
		return chainCloser{Reader: fl, closers: []io.Closer{fl, body}}, nil
	case "br":
		return chainCloser{Reader: brotli.NewReader(body), closers: []io.Closer{body}}, nil
	case "zstd":
		zr, err := zstd.NewReader(body)
		if err != nil {
			return nil, err
		}
		return chainCloser{Reader: zr.IOReadCloser(), closers: []io.Closer{zr.IOReadCloser(), body}}, nil
	default:
		return nil, nil
	}
}

// chainCloser closes the decompressor and the underlying body together.
type chainCloser struct {
	io.Reader
	closers []io.Closer
}

func (c chainCloser) Close() error {
	var firstErr error
	for _, closer := range c.closers {
		if err := closer.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
