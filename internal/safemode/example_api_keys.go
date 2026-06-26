package safemode

import (
	"context"
	"crypto/tls"
	"fmt"
	"html"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

var exampleAPIKeys = map[string]struct{}{
	"your-api-key-1": {},
	"your-api-key-2": {},
	"your-api-key-3": {},
}

// ExampleAPIKeys returns configured top-level API keys that still use template values.
func ExampleAPIKeys(keys []string) []string {
	if len(keys) == 0 {
		return nil
	}

	matches := make([]string, 0, len(keys))
	seen := make(map[string]struct{}, len(exampleAPIKeys))
	for _, key := range keys {
		trimmed := strings.TrimSpace(key)
		if _, ok := exampleAPIKeys[trimmed]; !ok {
			continue
		}
		if _, exists := seen[trimmed]; exists {
			continue
		}
		seen[trimmed] = struct{}{}
		matches = append(matches, trimmed)
	}
	if len(matches) == 0 {
		return nil
	}
	return matches
}

// HasExampleAPIKeys reports whether any configured top-level API key is a template value.
func HasExampleAPIKeys(keys []string) bool {
	return len(ExampleAPIKeys(keys)) > 0
}

// WarningServerURL returns a local-friendly URL for the warning-only server.
func WarningServerURL(cfg *config.Config) string {
	scheme := "http"
	host := "127.0.0.1"
	port := 0
	if cfg != nil {
		port = cfg.Port
		if cfg.TLS.Enable {
			scheme = "https"
		}
		if trimmed := strings.TrimSpace(cfg.Host); trimmed != "" {
			host = trimmed
		}
	}
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	return fmt.Sprintf("%s://%s:%d/", scheme, host, port)
}

// NewExampleAPIKeyWarningHandler serves a setup warning page and leaves all other routes unregistered.
func NewExampleAPIKeyWarningHandler(configPath string, keys []string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL == nil || (r.URL.Path != "/" && r.URL.Path != "/management.html") {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		_, _ = fmt.Fprint(w, warningPageHTML(configPath, keys))
	})
	return mux
}

// StartExampleAPIKeyWarningServer starts the warning-only HTTP(S) server and blocks until it stops.
func StartExampleAPIKeyWarningServer(ctx context.Context, cfg *config.Config, configPath string, keys []string) error {
	if cfg == nil {
		cfg = &config.Config{}
	}
	if ctx == nil {
		ctx = context.Background()
	}

	var tlsConfig *tls.Config
	if cfg.TLS.Enable {
		certPath := strings.TrimSpace(cfg.TLS.Cert)
		keyPath := strings.TrimSpace(cfg.TLS.Key)
		if certPath == "" || keyPath == "" {
			return fmt.Errorf("failed to start HTTPS warning server: tls.cert or tls.key is empty")
		}
		certPair, errLoad := tls.LoadX509KeyPair(certPath, keyPath)
		if errLoad != nil {
			return fmt.Errorf("failed to start HTTPS warning server: %w", errLoad)
		}
		tlsConfig = &tls.Config{
			Certificates: []tls.Certificate{certPair},
			MinVersion:   tls.VersionTLS12,
		}
	}

	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	listener, errListen := net.Listen("tcp", addr)
	if errListen != nil {
		return fmt.Errorf("failed to start warning server: %w", errListen)
	}
	if tlsConfig != nil {
		listener = tls.NewListener(listener, tlsConfig)
	}

	server := &http.Server{
		Addr:    addr,
		Handler: NewExampleAPIKeyWarningHandler(configPath, keys),
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- server.Serve(listener)
	}()

	select {
	case errServe := <-errCh:
		if errServe == nil || errServe == http.ErrServerClosed {
			return nil
		}
		return errServe
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		errShutdown := server.Shutdown(shutdownCtx)
		errServe := <-errCh
		if errShutdown != nil {
			return errShutdown
		}
		if errServe != nil && errServe != http.ErrServerClosed {
			return errServe
		}
		return ctx.Err()
	}
}

func warningPageHTML(configPath string, keys []string) string {
	var b strings.Builder
	b.WriteString(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Example API key detected</title><style>body{margin:0;font-family:Arial,sans-serif;background:#f6f8fa;color:#1f2328}.wrap{max-width:760px;margin:12vh auto;padding:0 24px}.panel{background:#fff;border:1px solid #d0d7de;border-radius:8px;padding:28px;box-shadow:0 8px 24px rgba(140,149,159,.2)}h1{margin:0 0 12px;font-size:28px;line-height:1.25}p{font-size:16px;line-height:1.55}code{background:#f6f8fa;border:1px solid #d0d7de;border-radius:4px;padding:2px 5px}.keys{margin:16px 0;padding-left:22px}.path{word-break:break-all}</style></head><body><main class="wrap"><section class="panel"><h1>Example API key detected</h1><p>The normal API server was not started because the top-level <code>api-keys</code> configuration still contains template values.</p>`)
	if len(keys) > 0 {
		b.WriteString(`<p>Replace these values before using the proxy:</p><ul class="keys">`)
		for _, key := range keys {
			b.WriteString(`<li><code>`)
			b.WriteString(html.EscapeString(key))
			b.WriteString(`</code></li>`)
		}
		b.WriteString(`</ul>`)
	}
	if strings.TrimSpace(configPath) != "" {
		b.WriteString(`<p>Edit <code class="path">`)
		b.WriteString(html.EscapeString(configPath))
		b.WriteString(`</code>, set strong random API keys, then restart CLIProxyAPI.</p>`)
	} else {
		b.WriteString(`<p>Edit your config file, set strong random API keys, then restart CLIProxyAPI.</p>`)
	}
	b.WriteString(`</section></main></body></html>`)
	return b.String()
}
