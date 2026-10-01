package claudemaster

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestProcessCertificateRenewsAfterWeeklyBoundaryWithoutChangingCA(t *testing.T) {
	initial := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	var clock atomic.Int64
	clock.Store(initial.UnixNano())
	now := func() time.Time { return time.Unix(0, clock.Load()) }
	certs, err := newProcessCertificateWithClock(now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(certs.dir) })
	old, err := certs.getCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	oldDER := append([]byte(nil), old.Certificate[0]...)
	ca, err := x509.ParseCertificate(old.Certificate[1])
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	proxy, err := StartProxy(ProxyOptions{GetCertificate: certs.getCertificate, Inference: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "native-response")
	})})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proxy.Close() })
	if len(proxy.tlsConfig.Certificates) != 0 || proxy.tlsConfig.GetCertificate == nil {
		t.Fatal("proxy did not install its renewal provider for all handshakes")
	}
	proxyURL, err := url.Parse(proxy.URL())
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), DisableKeepAlives: true, TLSClientConfig: &tls.Config{RootCAs: roots, Time: now, MinVersion: tls.VersionTLS12}}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport}
	for _, days := range []int{0, 8, 16} {
		clock.Store(initial.Add(time.Duration(days) * 24 * time.Hour).UnixNano())
		resp, err := client.Post("https://"+masterAPIHost+"/v1/messages", "application/json", bytes.NewBufferString(`{}`))
		if err != nil {
			t.Fatalf("native TLS failed after %d days: %v", days, err)
		}
		body, errRead := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if errRead != nil || string(body) != "native-response" {
			t.Fatal("renewal changed the inference response")
		}
		if _, err := resp.TLS.PeerCertificates[0].Verify(x509.VerifyOptions{Roots: roots, DNSName: masterAPIHost, CurrentTime: now()}); err != nil {
			t.Fatal(err)
		}
	}
	current, err := certs.getCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(oldDER, current.Certificate[0]) || !bytes.Equal(oldDER, old.Certificate[0]) || !bytes.Equal(old.Certificate[1], current.Certificate[1]) {
		t.Fatal("leaf did not renew immutably under the same process CA")
	}
}

func TestProcessCertificateConcurrentRenewalUsesImmutableSnapshots(t *testing.T) {
	initial := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	var clock atomic.Int64
	clock.Store(initial.UnixNano())
	certs, err := newProcessCertificateWithClock(func() time.Time { return time.Unix(0, clock.Load()) })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(certs.dir) })
	previous, err := certs.getCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	serial := previous.Leaf.SerialNumber.String()
	clock.Store(initial.Add(8 * 24 * time.Hour).UnixNano())
	var wg sync.WaitGroup
	results := make(chan *tls.Certificate, 32)
	for i := 0; i < cap(results); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			certificate, err := certs.getCertificate(nil)
			if err != nil {
				t.Error(err)
			}
			results <- certificate
		}()
	}
	wg.Wait()
	close(results)
	var renewed string
	for certificate := range results {
		if certificate == nil {
			continue
		}
		got := certificate.Leaf.SerialNumber.String()
		if got == serial || renewed != "" && got != renewed {
			t.Fatal("concurrent handshakes did not share one renewed leaf")
		}
		renewed = got
	}
	if previous.Leaf.SerialNumber.String() != serial {
		t.Fatal("renewal mutated an in-flight handshake's certificate")
	}
}
