// Package egresstest holds the counting target used by the proxy-egress tests.
package egresstest

import (
	"errors"
	"net"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/proxyutil"
)

// BadProxy is a set-but-unusable proxy value; it may never reach the target. The full
// list of unusable values is tested once in sdk/proxyutil.
const BadProxy = "ftp://127.0.0.1:1"

// Target is a listener standing for the upstream: it counts connections and drops them.
func Target(t *testing.T) (addr string, count *atomic.Int64) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	count = new(atomic.Int64)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			count.Add(1)
			_ = c.Close()
		}
	}()
	return ln.Addr().String(), count
}

// ClientRT adapts an http.Client to a RoundTripper.
type ClientRT struct{ C *http.Client }

func (r ClientRT) RoundTrip(req *http.Request) (*http.Response, error) { return r.C.Do(req) }

// Get sends one request through rt, which must fail, and checks the target saw nothing.
func Get(t *testing.T, rt http.RoundTripper, scheme, addr string, count *atomic.Int64) {
	t.Helper()
	if rt == nil { // a nil Transport is http.DefaultTransport: direct or the environment
		rt = http.DefaultTransport
	}
	req, _ := http.NewRequest(http.MethodGet, scheme+"://"+addr+"/", nil)
	resp, err := rt.RoundTrip(req)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if !errors.Is(err, proxyutil.ErrUnusable) {
		t.Errorf("error = %v, want proxyutil.ErrUnusable", err)
	}
	NoLeak(t, count)
}

// NoLeak fails when the target accepted a connection.
func NoLeak(t *testing.T, count *atomic.Int64) {
	t.Helper()
	if n := count.Load(); n != 0 {
		t.Errorf("target got %d connections through an unusable proxy", n)
	}
}
