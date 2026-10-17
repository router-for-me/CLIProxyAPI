package helps

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/httpcompression"
)

// compressionTestClient exposes the selected transport for fingerprint and pool assertions.
// Content decoding is exercised separately by socket tests using the unmodified client.
func compressionTestClient(client *http.Client) *http.Client {
	copyClient := *client
	if wrapped, ok := copyClient.Transport.(httpcompression.Transport); ok {
		copyClient.Transport = wrapped.Base
	}
	return &copyClient
}

func TestSharedClientsDecodeCompressionSocket(t *testing.T) {
	for name, factory := range map[string]func(context.Context, *config.Config, *cliproxyauth.Auth, time.Duration) *http.Client{"standard": NewProxyAwareHTTPClient, "fingerprint": NewUtlsHTTPClient, "devin": NewDevinHTTPClient} {
		for _, encoding := range []string{"gzip", "br", "zstd"} {
			t.Run(name+"/"+encoding, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("Accept-Encoding") != "identity" {
						t.Error("explicit identity override changed")
					}
					w.Header().Set("Content-Encoding", encoding)
					writer, _ := httpcompression.Encode(w, encoding)
					_, _ = writer.Write([]byte("data: decoded\n\n"))
					_ = writer.Close()
				}))
				defer server.Close()
				client := factory(context.Background(), &config.Config{}, nil, 0)
				defer client.CloseIdleConnections()
				request, _ := http.NewRequest("POST", server.URL, nil)
				request.Header.Set("Accept-Encoding", "identity")
				response, err := client.Do(request)
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				body, err := io.ReadAll(response.Body)
				if err != nil || string(body) != "data: decoded\n\n" {
					t.Fatalf("body=%q err=%v", body, err)
				}
			})
		}
	}
}
