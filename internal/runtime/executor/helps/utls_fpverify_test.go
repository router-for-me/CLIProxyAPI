package helps

import (
	"io"
	"net/http"
	"os"
	"testing"

	"github.com/tidwall/gjson"
	"golang.org/x/net/proxy"
)

// fingerprintReporterURL returns the observed TLS/HTTP fingerprint as JSON.
// tls.peet.ws/api/all reports ja3, ja3_hash, ja4, and the negotiated HTTP version.
const fingerprintReporterURL = "https://tls.peet.ws/api/all"

// TestFingerprintAgainstReporter is an evidence-grade check: it drives each uTLS
// profile against a public JA3/JA4 reporter and asserts the SERVER-OBSERVED
// fingerprint matches what we intend to impersonate. Gated behind FP_VERIFY=1
// (makes a real outbound request); never runs in the offline suite. This is the
// ground-truth verification of the anti-fingerprinting work — run it after any
// uTLS/profile change or utls library bump.
func TestFingerprintAgainstReporter(t *testing.T) {
	if os.Getenv("FP_VERIFY") != "1" {
		t.Skip("set FP_VERIFY=1 to run the live fingerprint verification")
	}

	fetch := func(rt http.RoundTripper) (gjson.Result, error) {
		req, err := http.NewRequest(http.MethodGet, fingerprintReporterURL, nil)
		if err != nil {
			return gjson.Result{}, err
		}
		req.Header.Set("User-Agent", "claude-cli/2.1.63 (external, cli)")
		req.Header.Set("Accept", "application/json")
		resp, err := rt.RoundTrip(req)
		if err != nil {
			return gjson.Result{}, err
		}
		defer func() { _ = resp.Body.Close() }()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return gjson.Result{}, err
		}
		return gjson.ParseBytes(data), nil
	}

	t.Run("node_h1_profile", func(t *testing.T) {
		rt := newUtlsH1RoundTripper(proxy.Direct, nodeH1Profile, claudeHeaderOrder)
		res, err := fetch(rt)
		if err != nil {
			t.Skipf("reporter unreachable: %v", err)
		}
		httpVer := res.Get("http_version").String()
		ja3Hash := res.Get("tls.ja3_hash").String()
		ja4 := res.Get("tls.ja4").String()
		t.Logf("node/h1 observed: http_version=%s ja3_hash=%s ja4=%s", httpVer, ja3Hash, ja4)

		if httpVer != "HTTP/1.1" {
			t.Errorf("http_version = %q, want HTTP/1.1 (Node/undici uses h1)", httpVer)
		}
		// JA4 encodes protocol+cipher/ext counts+ALPN; the "h1" segment must hold.
		if len(ja4) < 10 || ja4[:10] != "t13d1714h1" {
			t.Errorf("ja4 = %q, want prefix t13d1714h1 (Node.js 24 fingerprint)", ja4)
		}
		if ja3Hash != "44f88fca027f27bab4bb08d4af15f23e" {
			t.Errorf("ja3_hash = %q, want 44f88fca027f27bab4bb08d4af15f23e (captured Claude Code)", ja3Hash)
		}
	})

	t.Run("chrome_h2_profile", func(t *testing.T) {
		rt := newUtlsRoundTripper(proxy.Direct, chromeH2Profile)
		res, err := fetch(rt)
		if err != nil {
			t.Skipf("reporter unreachable: %v", err)
		}
		httpVer := res.Get("http_version").String()
		ja4 := res.Get("tls.ja4").String()
		t.Logf("chrome/h2 observed: http_version=%s ja3_hash=%s ja4=%s", httpVer, res.Get("tls.ja3_hash").String(), ja4)

		if httpVer != "h2" {
			t.Errorf("http_version = %q, want h2 (Chrome uses HTTP/2)", httpVer)
		}
		// Chrome-family JA4 over TLS 1.3 with h2 ALPN (utls HelloChrome_Auto
		// currently yields t13d1516h2_...; the exact cipher/ext counts can shift
		// across utls versions, so assert the structural TLS1.3+h2 shape).
		// JA4_a layout: t + 13 + d + <2-digit ciphers> + <2-digit exts> + <2-char ALPN>,
		// so the ALPN marker sits at [8:10] (e.g. t13d1516h2 -> "h2").
		if len(ja4) < 10 || ja4[:4] != "t13d" || ja4[8:10] != "h2" {
			t.Errorf("ja4 = %q, want a TLS1.3 + h2 Chrome-family fingerprint (t13d..h2..)", ja4)
		}
	})
}
