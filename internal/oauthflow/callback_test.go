package oauthflow

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestValidateResponse(t *testing.T) {
	cases := []struct {
		name, query string
		ok          bool
	}{
		{"code", "state=expected&code=one", true}, {"denied", "state=expected&error=access_denied", true},
		{"missing state", "code=one", false}, {"wrong state", "state=other&code=one", false},
		{"duplicate state", "state=expected&state=expected&code=one", false},
		{"duplicate code", "state=expected&code=one&code=two", false},
		{"both", "state=expected&code=one&error=denied", false},
		{"neither", "state=expected", false}, {"whitespace", "state=expected&code=+one", false},
		{"control", "state=expected&code=x%00y", false}, {"extra", "state=expected&code=one&scope=abc", true},
		{"long", "state=expected&code=" + strings.Repeat("x", 8193), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			q, e := url.ParseQuery(c.query)
			if e != nil {
				t.Fatal(e)
			}
			_, e = ValidateResponse(q, "expected")
			if (e == nil) != c.ok {
				t.Fatalf("accepted=%v, want %v: %v", e == nil, c.ok, e)
			}
		})
	}
}
func TestRedirectAndManualBinding(t *testing.T) {
	valid := []string{"http://127.0.0.1:12500/auth/callback", "http://[::1]:12500/auth/callback", "http://localhost:12500/auth/callback", "https://example.test/oauth"}
	for _, s := range valid {
		if err := ValidateRedirectURI(s); err != nil {
			t.Errorf("%s: %v", s, err)
		}
	}
	invalid := []string{"", "http://localhost:0/auth/callback", "http://localhost/auth/callback", "http://example.test:12500/auth/callback", "http://user@localhost:12500/cb", "http://localhost:12500/cb?x=1", "http://localhost:12500/cb#f", "http://[::1%25eth0]:12500/cb", "https://example.test:0/cb", "file:///cb"}
	for _, s := range invalid {
		if ValidateRedirectURI(s) == nil {
			t.Errorf("accepted %q", s)
		}
	}
	base := "http://127.0.0.1:12500/auth/callback"
	if _, err := ParseCallbackURL(base+"?code=c&state=s", base, "s"); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{base + "?code=c&state=bad", strings.Replace(base, "12500", "12501", 1) + "?code=c&state=s", base + "/extra?code=c&state=s", base + "?code=c&state=s#frag", base + "?state=s&code=c&code=d"} {
		if _, err := ParseCallbackURL(s, base, "s"); err == nil {
			t.Errorf("accepted mismatched callback %q", s)
		}
	}
}
func callbackClient(t *testing.T) *http.Client {
	t.Helper()
	tr := &http.Transport{Proxy: nil}
	t.Cleanup(tr.CloseIdleConnections)
	return &http.Client{Transport: tr, Timeout: 3 * time.Second}
}
func call(t *testing.T, c *http.Client, method, uri, host string) (int, string, http.Header) {
	t.Helper()
	r, err := http.NewRequest(method, uri, nil)
	if err != nil {
		t.Fatal(err)
	}
	if host != "" {
		r.Host = host
	}
	resp, err := c.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(b), resp.Header
}
func TestCallbackRejectsWithoutConsumingAndRejectsReplay(t *testing.T) {
	cb, err := StartCallback("expected", 0, "/auth/callback")
	if err != nil {
		t.Fatal(err)
	}
	defer cb.Close()
	client := callbackClient(t)
	for _, tc := range []struct {
		method, suffix, host string
		want                 int
	}{{"GET", "?code=one&state=wrong", "", 400}, {"GET", "?code=one&state=expected&state=expected", "", 400}, {"POST", "?code=one&state=expected", "", 405}, {"GET", "?code=one&state=expected", "attacker.test", 400}, {"GET", "/other?code=one&state=expected", "", 404}} {
		status, _, _ := call(t, client, tc.method, cb.URI+tc.suffix, tc.host)
		if status != tc.want {
			t.Fatalf("got %d want %d", status, tc.want)
		}
		select {
		case <-cb.Results:
			t.Fatal("invalid request consumed session")
		default:
		}
	}
	status, body, headers := call(t, client, "GET", cb.URI+"?code=one&state=expected", "")
	if status != 200 {
		t.Fatal(status)
	}
	if strings.Contains(strings.ToLower(body), "login successful") {
		t.Fatal("premature success")
	}
	if headers.Get("Cache-Control") != "no-store" || headers.Get("Referrer-Policy") != "no-referrer" {
		t.Fatal("missing privacy headers")
	}
	select {
	case r := <-cb.Results:
		if r.Code != "one" {
			t.Fatal(r)
		}
	case <-time.After(time.Second):
		t.Fatal("missing result")
	}
	status, _, _ = call(t, client, "GET", cb.URI+"?code=two&state=expected", "")
	if status != 409 {
		t.Fatal(status)
	}
}
func TestCallbackConcurrentExactlyOnce(t *testing.T) {
	cb, err := StartCallback("state", 0, "/cb")
	if err != nil {
		t.Fatal(err)
	}
	defer cb.Close()
	client := callbackClient(t)
	var wg sync.WaitGroup
	var accepted atomic.Int32
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := client.Get(cb.URI + "?code=code&state=state")
			if err != nil {
				t.Error(err)
				return
			}
			defer resp.Body.Close()
			_, _ = io.Copy(io.Discard, resp.Body)
			if resp.StatusCode == 200 {
				accepted.Add(1)
			} else if resp.StatusCode != 409 {
				t.Errorf("status %d", resp.StatusCode)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() != 1 {
		t.Fatalf("accepted=%d", accepted.Load())
	}
	select {
	case <-cb.Results:
	default:
		t.Fatal("missing result")
	}
	select {
	case <-cb.Results:
		t.Fatal("duplicate result")
	default:
	}
}
func TestCallbackIPv6Fallback(t *testing.T) {
	probe, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skip("IPv6 unavailable")
	}
	probe.Close()
	cb, err := startCallback("s", 0, "/cb", func(network, address string) (net.Listener, error) {
		if network == "tcp4" {
			return nil, errors.New("IPv4 unavailable")
		}
		return net.Listen(network, address)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cb.Close()
	if !strings.HasPrefix(cb.URI, "http://[::1]:") {
		t.Fatal(cb.URI)
	}
	status, _, _ := call(t, callbackClient(t), "GET", cb.URI+"?state=s&code=c", "")
	if status != 200 {
		t.Fatal(status)
	}
}
func TestCallbackPortHeldAndReleased(t *testing.T) {
	cb, err := StartCallback("s", 0, "/cb")
	if err != nil {
		t.Fatal(err)
	}
	defer cb.Close()
	u, _ := url.Parse(cb.URI)
	network := "tcp4"
	if strings.Contains(u.Host, "[") {
		network = "tcp6"
	}
	if l, e := net.Listen(network, u.Host); e == nil {
		l.Close()
		t.Fatal("port was not reserved")
	}
	cb.Close()
	l, e := net.Listen(network, u.Host)
	if e != nil {
		t.Fatal(e)
	}
	l.Close()
	cb.Close()
}
func TestCallbackImmediateCloseAndInvalidConfig(t *testing.T) {
	for i := 0; i < 30; i++ {
		cb, err := StartCallback("s", 0, "/cb")
		if err != nil {
			t.Fatal(err)
		}
		cb.Close()
	}
	for _, p := range []int{-1, 65536} {
		if cb, e := StartCallback("s", p, "/cb"); e == nil {
			cb.Close()
			t.Fatal("invalid port accepted")
		}
	}
	if cb, e := StartCallback("", 0, "/cb"); e == nil {
		cb.Close()
		t.Fatal("empty state accepted")
	}
}
func TestCallbackSeparateSessions(t *testing.T) {
	a, e := StartCallback("a", 0, "/cb")
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	b, e := StartCallback("b", 0, "/cb")
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	c := callbackClient(t)
	status, _, _ := call(t, c, "GET", a.URI+"?state=b&code=code", "")
	if status != 400 {
		t.Fatal(status)
	}
	status, _, _ = call(t, c, "GET", b.URI+"?state=b&code=code", "")
	if status != 200 {
		t.Fatal(status)
	}
	select {
	case <-a.Results:
		t.Fatal("cross-session callback")
	default:
	}
}
func FuzzCallbackValidation(f *testing.F) {
	for _, s := range []string{"state=s&code=c", "state=s&error=denied", "state=s&code=c&code=d", "state=x&code=y", "%zz"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if len(s) > 20<<10 {
			return
		}
		q, e := url.ParseQuery(s)
		if e != nil {
			return
		}
		r, e := ValidateResponse(q, "s")
		if e == nil {
			if r.State != "s" || (r.Code == "") == (r.Error == "") {
				t.Fatal("invalid accepted response")
			}
		}
	})
}
