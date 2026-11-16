package helps

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/httptransport"
)

type observingTransport struct {
	http.RoundTripper
}

func TestClientDecoratorsPreserveProtectedAndProxyTransports(t *testing.T) {
	for _, factory := range []string{"plain", "devin", "utls"} {
		for _, proxy := range []string{"", "http://127.0.0.1:12345"} {
			t.Run(factory+"_"+proxy, func(t *testing.T) {
				calls := 0
				var selected http.RoundTripper
				ctx := httptransport.WithDecorator(context.Background(), func(next http.RoundTripper) http.RoundTripper {
					calls++
					selected = next
					return &observingTransport{RoundTripper: next}
				})
				auth := &coreauth.Auth{ProxyURL: proxy}
				cfg := &config.Config{}

				var c *http.Client
				var plain *http.Client
				switch factory {
				case "utls":
					c = NewUtlsHTTPClient(ctx, cfg, auth, 0)
					plain = NewUtlsHTTPClient(context.Background(), cfg, auth, 0)
					rt, ok := selected.(*fallbackRoundTripper)
					if !ok || rt.anthropic == nil || rt.chrome == nil || rt.fallback == nil {
						t.Fatalf("lost TLS profiles: %#v", selected)
					}
					plainRT, ok := plain.Transport.(*fallbackRoundTripper)
					if !ok || plainRT.anthropic != rt.anthropic {
						t.Fatal("Anthropic cached transport identity changed under decoration")
					}
				case "devin":
					c = NewDevinHTTPClient(ctx, cfg, auth, 0)
					plain = NewDevinHTTPClient(context.Background(), cfg, auth, 0)
					if plain.Transport != selected {
						t.Fatal("Devin pooled transport identity changed under decoration")
					}
				default:
					c = NewProxyAwareHTTPClient(ctx, cfg, auth, 0)
					if proxy == "" {
						if selected != http.DefaultTransport {
							t.Fatalf("expected DefaultTransport for direct client, got %T", selected)
						}
					} else {
						tr, ok := selected.(*http.Transport)
						if !ok || tr.Proxy == nil {
							t.Fatalf("expected proxied *http.Transport, got %#v", selected)
						}
						probeReq, errProbe := http.NewRequest(http.MethodGet, "https://upstream.example/v1", nil)
						if errProbe != nil {
							t.Fatalf("probe request: %v", errProbe)
						}
						resolvedProxy, errProxy := tr.Proxy(probeReq)
						if errProxy != nil || resolvedProxy == nil || resolvedProxy.String() != proxy {
							t.Fatalf("resolved proxy = %v (err=%v), want %s", resolvedProxy, errProxy, proxy)
						}
					}
				}
				if calls != 1 {
					t.Fatalf("decorator calls = %d, want 1", calls)
				}
				obs, ok := c.Transport.(*observingTransport)
				if !ok || obs.RoundTripper != selected {
					t.Fatal("decoration replaced transport or failed to wrap selected transport")
				}
			})
		}
	}

	t.Run("request_proxy_override_with_decorator", func(t *testing.T) {
		var selected http.RoundTripper
		ctx := coreexecutor.WithRequestProxyURL(context.Background(), "http://127.0.0.1:18081")
		ctx = httptransport.WithDecorator(ctx, func(next http.RoundTripper) http.RoundTripper {
			selected = next
			return &observingTransport{RoundTripper: next}
		})
		c := NewProxyAwareHTTPClient(
			ctx,
			&config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://127.0.0.1:18080"}},
			&coreauth.Auth{ProxyURL: "http://127.0.0.1:18082"},
			0,
		)
		obs, ok := c.Transport.(*observingTransport)
		if !ok || obs.RoundTripper != selected {
			t.Fatalf("expected wrapped transport, got %T", c.Transport)
		}
		tr, ok := selected.(*http.Transport)
		if !ok || tr.Proxy == nil {
			t.Fatalf("selected transport = %#v, want proxied *http.Transport", selected)
		}
		probeReq, errProbe := http.NewRequest(http.MethodGet, "https://upstream.example/v1", nil)
		if errProbe != nil {
			t.Fatalf("probe request: %v", errProbe)
		}
		resolvedProxy, errProxy := tr.Proxy(probeReq)
		if errProxy != nil || resolvedProxy == nil || resolvedProxy.String() != "http://127.0.0.1:18081" {
			t.Fatalf("resolved proxy = %v (err=%v), want http://127.0.0.1:18081", resolvedProxy, errProxy)
		}
	})
}

type lifecycleReadCloser struct {
	io.ReadCloser
	bytesRead *int64
	closed    *atomic.Bool
}

func (l *lifecycleReadCloser) Read(p []byte) (int, error) {
	n, errRead := l.ReadCloser.Read(p)
	atomic.AddInt64(l.bytesRead, int64(n))
	return n, errRead
}

func (l *lifecycleReadCloser) Close() error {
	l.closed.Store(true)
	return l.ReadCloser.Close()
}

func TestClientDecoratorContextIsolationAndStreamLifecycle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: " + string(body) + "\n\n"))
	}))
	defer srv.Close()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			var capturedBody []byte
			var bytesRead int64
			var closed atomic.Bool

			ctx := httptransport.WithDecorator(context.Background(), func(next http.RoundTripper) http.RoundTripper {
				return roundTripFunc(func(req *http.Request) (*http.Response, error) {
					if req.Body != nil {
						data, errReadAll := io.ReadAll(req.Body)
						if errReadAll != nil {
							return nil, errReadAll
						}
						capturedBody = append([]byte(nil), data...)
						req.Body = io.NopCloser(bytes.NewReader(data))
					}
					resp, errRT := next.RoundTrip(req)
					if errRT != nil || resp == nil || resp.Body == nil {
						return resp, errRT
					}
					resp.Body = &lifecycleReadCloser{
						ReadCloser: resp.Body,
						bytesRead:  &bytesRead,
						closed:     &closed,
					}
					return resp, nil
				})
			})

			client := NewProxyAwareHTTPClient(ctx, &config.Config{}, nil, 0)
			req, errReq := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL, bytes.NewBufferString(`{"model":"gpt-5","thinking":"high"}`))
			if errReq != nil {
				t.Errorf("NewRequestWithContext: %v", errReq)
				return
			}
			resp, errDo := client.Do(req)
			if errDo != nil {
				t.Errorf("Do: %v", errDo)
				return
			}
			_, _ = io.ReadAll(resp.Body)
			if errClose := resp.Body.Close(); errClose != nil {
				t.Errorf("Close: %v", errClose)
				return
			}
			if string(capturedBody) != `{"model":"gpt-5","thinking":"high"}` {
				t.Errorf("capturedBody = %q", string(capturedBody))
			}
			if atomic.LoadInt64(&bytesRead) == 0 || !closed.Load() {
				t.Errorf("stream lifecycle not observed: bytesRead=%d closed=%v", atomic.LoadInt64(&bytesRead), closed.Load())
			}
		}()

		go func() {
			defer wg.Done()
			client := NewProxyAwareHTTPClient(context.Background(), &config.Config{}, nil, 0)
			if client.Transport != nil {
				t.Errorf("undecorated default client Transport = %T, want nil", client.Transport)
			}
		}()
	}
	wg.Wait()
}
