package claudemaster

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// startNamedServer is a server whose inference handler reports which client each request came from.
func startNamedServer(t *testing.T, withOpen bool) (stateDir string, certs *processCertificate, proxy *Proxy, seen *[]string, mu *sync.Mutex) {
	t.Helper()
	stateDir = filepath.Join(canonicalTestTempDir(t), "state")
	var err error
	certs, err = loadOrCreatePersistentCertificate(stateDir, serverNames([]byte{127, 0, 0, 1}))
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	lock := &sync.Mutex{}
	open := ""
	if withOpen {
		open = "127.0.0.1:0"
	}
	proxy, err = startServerProxy(certs, "127.0.0.1:0", open, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lock.Lock()
		names = append(names, clientFromContext(r.Context()))
		lock.Unlock()
		_, _ = io.WriteString(w, "served")
	}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proxy.Close() })
	return stateDir, certs, proxy, &names, lock
}

func TestEachRequestKnowsWhichClientOpenedItsTunnel(t *testing.T) {
	logs := captureLogs(t, "info")
	stateDir, _, proxy, seen, mu := startNamedServer(t, false)
	dir := issueTestClient(t, stateDir, "dev-box-1", 30)
	transport := clientTransport(t, dir, proxy)
	for i := 0; i < 2; i++ {
		if body, err := post(t, transport); err != nil || body != "served" {
			t.Fatalf("post: %q %v", body, err)
		}
	}
	mu.Lock()
	got := strings.Join(*seen, ",")
	mu.Unlock()
	if got != "dev-box-1,dev-box-1" {
		t.Fatalf("requests carried the client names %q, want the certificate name twice", got)
	}
	out := logs.String()
	if strings.Count(out, "client connected for the first time") != 1 || !strings.Contains(out, "client=dev-box-1") || !strings.Contains(out, "listener=cert") {
		t.Fatalf("the first connection of a client is one info line naming it:\n%s", out)
	}
	if !strings.Contains(out, "proxy listening") {
		t.Fatalf("startup was not logged:\n%s", out)
	}
}

func TestAClientOnTheOpenListenerIsCalledTunnel(t *testing.T) {
	logs := captureLogs(t, "info")
	_, certs, proxy, seen, mu := startNamedServer(t, true)
	roots := x509.NewCertPool()
	roots.AddCert(certs.ca)
	transport := &http.Transport{
		Proxy:           http.ProxyURL(&url.URL{Scheme: "http", Host: proxy.OpenAddr()}),
		TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
	}
	t.Cleanup(transport.CloseIdleConnections)
	if _, err := post(t, transport); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(*seen) != 1 || (*seen)[0] != "tunnel" {
		t.Fatalf("clients = %v, want [tunnel]", *seen)
	}
	if !strings.Contains(logs.String(), "client=tunnel") || !strings.Contains(logs.String(), "listener=open") {
		t.Fatalf("the open listener's client was not logged:\n%s", logs.String())
	}
}

func TestARefusedCertificateIsOneWarningAMinuteWithTheRemoteAddress(t *testing.T) {
	logs := captureLogs(t, "info")
	reader := startMetrics(t, nil)
	stateDir, _, proxy, _, _ := startNamedServer(t, false)
	good := issueTestClient(t, stateDir, "dev-box-1", 30)
	otherState := filepath.Join(canonicalTestTempDir(t), "state")
	if _, err := loadOrCreatePersistentCertificate(otherState, nil); err != nil {
		t.Fatal(err)
	}
	foreign := issueTestClient(t, otherState, "dev-box-1", 30)
	identity, _ := LoadClientIdentity(foreign)
	pair, _ := tls.LoadX509KeyPair(identity.CertPath, identity.KeyPath)
	for i := 0; i < 3; i++ {
		transport := clientTransport(t, good, proxy)
		transport.TLSClientConfig.Certificates = []tls.Certificate{pair}
		if _, err := post(t, transport); err == nil {
			t.Fatal("a foreign certificate was served")
		}
	}
	deadline := time.After(3 * time.Second)
	for strings.Count(logs.String(), "client TLS handshake failed") < 1 {
		select {
		case <-deadline:
			t.Fatalf("a refused client left no trace:\n%s", logs.String())
		case <-time.After(20 * time.Millisecond):
		}
	}
	// The handshake counter is bumped after a line is logged (or suppressed), so three means all three
	// refusals have been through the limiter: no sleeping, nothing left to arrive late.
	for sumOf(collect(t, reader), "claude_master.proxy.tls_handshake_errors", nil) < 3 {
		select {
		case <-deadline:
			t.Fatalf("only some of the refusals were counted:\n%s", logs.String())
		case <-time.After(20 * time.Millisecond):
		}
	}
	out := logs.String()
	if got := strings.Count(out, "client TLS handshake failed"); got != 1 {
		t.Fatalf("%d handshake warnings for one remote in a minute, want 1:\n%s", got, out)
	}
	if !strings.Contains(out, "level=warning") || !strings.Contains(out, "remote=127.0.0.1") {
		t.Fatalf("the refusal is not a warning naming the remote:\n%s", out)
	}
}
