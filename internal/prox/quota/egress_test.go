package quota

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAccountProxyEgress(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Host != "api.anthropic.com" || r.Header.Get("Authorization") != "Bearer SECRET_TOKEN" {
			t.Error("proxy did not receive usage request")
		}
		w.Write([]byte(validClaude))
	}))
	defer proxy.Close()
	f := NewHTTPFetcher()
	defer f.Close()
	f.ClaudeURL = "http://api.anthropic.com/api/oauth/usage"
	cred := testCredential()
	cred.ProxyURL = proxy.URL
	if _, err := f.Fetch(context.Background(), cred); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			a, err := f.clientFor(proxy.URL)
			if err != nil {
				t.Error(err)
				return
			}
			b, _ := f.clientFor(proxy.URL)
			if a.Transport != b.Transport {
				t.Error("transport was not cached")
			}
		})
	}
	wg.Wait()
	if len(f.transports) != 1 {
		t.Fatal("unexpected cache size")
	}
}

func TestProxySchemesAndSanitizedErrors(t *testing.T) {
	f := NewHTTPFetcher()
	defer f.Close()
	for _, scheme := range []string{"http", "https", "socks5", "socks5h"} {
		if _, err := f.clientFor(scheme + "://user:password@127.0.0.1:1234"); err != nil {
			t.Fatal(err)
		}
	}
	for _, raw := range []string{"http://secret-user:secret-pass@%zz", "ftp://secret-user:secret-pass@proxy.test", "missing-host"} {
		cred := testCredential()
		cred.ProxyURL = raw
		_, err := f.Fetch(context.Background(), cred)
		qerr, ok := IsError(err)
		if !ok || qerr.Code != "invalid_proxy_url" {
			t.Fatalf("unexpected error: %v", err)
		}
		if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "@") {
			t.Fatal("userinfo leaked")
		}
	}
	cred := testCredential()
	cred.ProxyURL = "http://secret-user:secret-pass@127.0.0.1:1"
	if _, err := f.Fetch(context.Background(), cred); err == nil || err.Error() != "quota_transport_failed" {
		t.Fatalf("unexpected transport error: %v", err)
	}
}

// ProxyFromEnvironment caches process environment, so each case gets a fresh process.
func TestEnvironmentEgress(t *testing.T) {
	if mode := os.Getenv("QR_EGRESS_CASE"); mode != "" {
		f := NewHTTPFetcher()
		defer f.Close()
		req, _ := http.NewRequest("GET", "https://quota.example/usage", nil)
		proxy, err := f.Client.Transport.(*http.Transport).Proxy(req)
		if err != nil {
			t.Fatal(err)
		}
		if mode == "proxy" && (proxy == nil || proxy.String() != "http://environment.test:8080") {
			t.Fatal("environment proxy missing")
		}
		if mode == "direct" && proxy != nil {
			t.Fatal("expected direct egress")
		}
		c, err := f.clientFor("http://account.test:8080")
		if err != nil {
			t.Fatal(err)
		}
		proxy, err = c.Transport.(*http.Transport).Proxy(req)
		if err != nil || proxy.Host != "account.test:8080" {
			t.Fatal("account proxy did not take precedence")
		}
		return
	}
	for _, mode := range []string{"proxy", "direct"} {
		cmd := exec.Command(os.Args[0], "-test.run=^TestEnvironmentEgress$")
		for _, entry := range os.Environ() {
			key := strings.ToUpper(strings.SplitN(entry, "=", 2)[0])
			if key == "HTTP_PROXY" || key == "HTTPS_PROXY" || key == "NO_PROXY" || key == "ALL_PROXY" || key == "QR_EGRESS_CASE" {
				continue
			}
			cmd.Env = append(cmd.Env, entry)
		}
		cmd.Env = append(cmd.Env, "QR_EGRESS_CASE="+mode)
		if mode == "proxy" {
			cmd.Env = append(cmd.Env, "HTTPS_PROXY=http://environment.test:8080")
		}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v: %s", mode, err, out)
		}
	}
}

// A SOCKS proxy that accepts TCP but never answers the handshake must not
// leave the dial blocked past the poll's context.
func TestStalledSOCKSHandshakeHonorsContext(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()
	f := NewHTTPFetcher()
	defer f.Close()
	client, err := f.clientFor("socks5://" + ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := client.Transport.(*http.Transport).DialContext(ctx, "tcp", "api.anthropic.com:443")
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("stalled proxy dial succeeded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SOCKS dial ignored its context")
	}
}
