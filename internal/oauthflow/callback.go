// Package oauthflow implements bounded, single-use loopback OAuth callbacks.
// It has no provider credentials, persistence, or external network dependencies.
package oauthflow

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const LoginTimeout = 5 * time.Minute
const AcquisitionTimeout = 30 * time.Second
const MaxResponseBytes int64 = 1 << 20

// Result is an authorization response, not a successful token exchange.
type Result struct{ Code, Error, State string }

// ValidateResponse rejects ambiguous or unbound callbacks before they consume a session.
func ValidateResponse(q url.Values, expectedState string) (Result, error) {
	for _, key := range []string{"state", "code", "error"} {
		if len(q[key]) > 1 {
			return Result{}, errors.New("oauth callback: duplicate parameter")
		}
	}
	state := q.Get("state")
	if expectedState == "" || len(state) > 512 || subtle.ConstantTimeCompare([]byte(state), []byte(expectedState)) != 1 {
		return Result{}, errors.New("oauth callback: invalid state")
	}
	code, failure := q.Get("code"), q.Get("error")
	if (code == "") == (failure == "") || len(code) > 8192 || len(failure) > 128 || strings.TrimSpace(code) != code || strings.TrimSpace(failure) != failure {
		return Result{}, errors.New("oauth callback: invalid authorization response")
	}
	for _, value := range []string{code, failure} {
		for _, r := range value {
			if r < 0x21 || r == 0x7f {
				return Result{}, errors.New("oauth callback: invalid character")
			}
		}
	}
	return Result{Code: code, Error: failure, State: state}, nil
}

// ValidateRedirectURI accepts explicit desktop loopback redirects or HTTPS
// redirects registered by an embedding application. It does not establish registration.
func ValidateRedirectURI(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || u.Path == "" || !strings.HasPrefix(u.Path, "/") || u.RawPath != "" {
		return errors.New("oauth redirect: expected an HTTP loopback URI without userinfo, query or fragment")
	}
	host := u.Hostname()
	if host == "" {
		return errors.New("oauth redirect: missing host")
	}
	if u.Scheme == "https" {
		if u.Port() != "" {
			port, err := strconv.Atoi(u.Port())
			if err != nil || port < 1 || port > 65535 {
				return errors.New("oauth redirect: invalid port")
			}
		}
		return nil
	}
	if host != "localhost" {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return errors.New("oauth redirect: host must be loopback")
		}
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		return errors.New("oauth redirect: explicit nonzero port required")
	}
	return nil
}

// ParseCallbackURL validates a manually delivered full callback URL against the session.
func ParseCallbackURL(raw, redirectURI, expectedState string) (Result, error) {
	if len(raw) > 16<<10 {
		return Result{}, errors.New("oauth callback: URL too long")
	}
	if err := ValidateRedirectURI(redirectURI); err != nil {
		return Result{}, err
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	expected, _ := url.Parse(redirectURI)
	if err != nil || u.User != nil || u.Fragment != "" || u.Opaque != "" || u.Scheme != expected.Scheme || u.Host != expected.Host || u.EscapedPath() != expected.EscapedPath() {
		return Result{}, errors.New("oauth callback: redirect URI mismatch")
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return Result{}, errors.New("oauth callback: malformed query")
	}
	return ValidateResponse(q, expectedState)
}

// Callback owns its bound listeners for its entire lifetime; there is no port handoff.
type Callback struct {
	URI       string
	Port      int
	Results   <-chan Result
	Errors    <-chan error
	server    *http.Server
	listeners []net.Listener
	once      sync.Once
}

// Close stops serving and releases every listener, including a listener whose
// Serve goroutine has not started yet. It is safe to call more than once.
func (c *Callback) Close() {
	if c == nil {
		return
	}
	c.once.Do(func() {
		// Let an accepted callback finish writing its response before the
		// listeners go away; Close would cut the browser off mid-answer.
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = c.server.Shutdown(ctx)
		for _, ln := range c.listeners {
			_ = ln.Close()
		}
		_ = c.server.Close()
	})
}


// allowedCallbackHost accepts loopback authorities on the bound port. A browser
// reaching a forwarded container port keeps "localhost" in its Host header, so
// matching the dial address exactly would reject legitimate callbacks.
func allowedCallbackHost(authority, port string) bool {
	host, gotPort, err := net.SplitHostPort(authority)
	if err != nil || gotPort != port {
		return false
	}
	switch strings.ToLower(strings.Trim(host, "[]")) {
	case "localhost", "127.0.0.1", "::1":
		return true
	default:
		return false
	}
}

func StartCallback(state string, port int, path string) (*Callback, error) {
	return startCallback(state, port, path, net.Listen)
}

func startCallback(state string, port int, path string, listen func(string, string) (net.Listener, error)) (*Callback, error) {
	if state == "" || len(state) > 512 || port < 0 || port > 65535 || path == "" || path[0] != '/' || strings.ContainsAny(path, "?#\\") {
		return nil, errors.New("oauth callback: invalid listener configuration")
	}
	// Listen on every interface: a container publishes the callback port on a
	// non-loopback address, so a loopback-only bind leaves the forwarded browser
	// redirect unreachable. State validation below protects the flow, not the
	// bind address.
	ln, errListen := listen("tcp", net.JoinHostPort("", strconv.Itoa(port)))
	if errListen != nil {
		return nil, fmt.Errorf("oauth callback: listen: %w", errListen)
	}
	listeners := []net.Listener{ln}
	port = ln.Addr().(*net.TCPAddr).Port
	authority := net.JoinHostPort("localhost", strconv.Itoa(port))
	results := make(chan Result, 1)
	serveErrors := make(chan error, len(listeners))
	var accepted bool
	var mu sync.Mutex
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path != path || r.URL.RawPath != "" {
			http.NotFound(w, r)
			return
		}
		if !allowedCallbackHost(r.Host, strconv.Itoa(port)) {
			http.Error(w, "Invalid callback host", http.StatusBadRequest)
			return
		}
		if len(r.URL.RawQuery) > 16<<10 {
			http.Error(w, "Callback too large", http.StatusRequestURITooLong)
			return
		}
		q, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil {
			http.Error(w, "Malformed callback", http.StatusBadRequest)
			return
		}
		result, err := ValidateResponse(q, state)
		if err != nil {
			http.Error(w, "Invalid authorization response", http.StatusBadRequest)
			return
		}
		mu.Lock()
		if accepted {
			mu.Unlock()
			http.Error(w, "Authorization response already received", http.StatusConflict)
			return
		}
		accepted = true
		results <- result
		mu.Unlock()
		_, _ = w.Write([]byte("Authorization response received. Return to the application to confirm sign-in.\n"))
	})
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 5 * time.Second, MaxHeaderBytes: 16 << 10}
	cb := &Callback{URI: "http://" + authority + path, Port: port, Results: results, Errors: serveErrors, server: srv, listeners: listeners}
	for _, ln := range listeners {
		go func(ln net.Listener) {
			if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
				serveErrors <- err
			}
		}(ln)
	}
	return cb, nil
}
