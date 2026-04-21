package misc

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
)

var customRootCAsCache struct {
	mu   sync.Mutex
	key  string
	pool *x509.CertPool
	err  error
}

// CustomRootCAsFromEnv loads extra root certificates from CODEX_CA_CERTIFICATE and SSL_CERT_FILE.
// CODEX_CA_CERTIFICATE accepts either a PEM string or a filesystem path.
func CustomRootCAsFromEnv() (*x509.CertPool, error) {
	codeXCA := strings.TrimSpace(os.Getenv("CODEX_CA_CERTIFICATE"))
	sslCertFile := strings.TrimSpace(os.Getenv("SSL_CERT_FILE"))
	cacheKey := codeXCA + "\x00" + sslCertFile

	customRootCAsCache.mu.Lock()
	defer customRootCAsCache.mu.Unlock()
	if customRootCAsCache.key == cacheKey {
		return customRootCAsCache.pool, customRootCAsCache.err
	}

	pool, err := loadCustomRootCAs(codeXCA, sslCertFile)
	customRootCAsCache.key = cacheKey
	customRootCAsCache.pool = pool
	customRootCAsCache.err = err
	return pool, err
}

func loadCustomRootCAs(values ...string) (*x509.CertPool, error) {
	hasSource := false
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			hasSource = true
			break
		}
	}
	if !hasSource {
		return nil, nil
	}

	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}

	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		pemData, errRead := readCustomCertPEM(value)
		if errRead != nil {
			return nil, errRead
		}
		if ok := pool.AppendCertsFromPEM(pemData); !ok {
			return nil, fmt.Errorf("failed to append custom certificate from %q", summarizeCertSource(value))
		}
	}
	return pool, nil
}

func readCustomCertPEM(value string) ([]byte, error) {
	if strings.Contains(value, "-----BEGIN CERTIFICATE-----") {
		return []byte(value), nil
	}
	data, err := os.ReadFile(value)
	if err != nil {
		return nil, fmt.Errorf("read custom certificate %q: %w", value, err)
	}
	return data, nil
}

func summarizeCertSource(value string) string {
	if strings.Contains(value, "-----BEGIN CERTIFICATE-----") {
		return "inline PEM"
	}
	return value
}

// CustomTLSConfigFromEnv returns a tls.Config carrying custom root CAs when configured.
func CustomTLSConfigFromEnv() (*tls.Config, error) {
	pool, err := CustomRootCAsFromEnv()
	if err != nil || pool == nil {
		return nil, err
	}
	return &tls.Config{RootCAs: pool}, nil
}

// RoundTripperWithCustomRootCAs clones the transport with custom root CAs when possible.
func RoundTripperWithCustomRootCAs(roundTripper http.RoundTripper, pool *x509.CertPool) http.RoundTripper {
	if pool == nil || roundTripper == nil {
		return roundTripper
	}
	transport, ok := roundTripper.(*http.Transport)
	if !ok || transport == nil {
		return roundTripper
	}
	clone := transport.Clone()
	if clone.TLSClientConfig == nil {
		clone.TLSClientConfig = &tls.Config{}
	} else {
		clone.TLSClientConfig = clone.TLSClientConfig.Clone()
	}
	clone.TLSClientConfig.RootCAs = pool
	return clone
}
