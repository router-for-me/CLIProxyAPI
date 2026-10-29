package helps

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestUtlsFallbackProxyConnectionPool(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "enabled"}[enabled], func(t *testing.T) {
			ResetProxyTransports()
			t.Cleanup(ResetProxyTransports)
			var connections atomic.Int32
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
			server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
				if state == http.StateNew {
					connections.Add(1)
				}
			}
			server.Start()
			defer server.Close()
			cfg := testConfig()
			auth := &cliproxyauth.Auth{ProxyURL: "direct"}
			if enabled {
				auth.ProxyConnectionPool = poolEnabled()
			}
			for i := 0; i < 2; i++ {
				client := NewUtlsHTTPClient(context.Background(), cfg, auth, 0)
				resp, err := client.Get(server.URL)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = io.Copy(io.Discard, resp.Body); err != nil {
					t.Fatal(err)
				}
				if err = resp.Body.Close(); err != nil {
					t.Fatal(err)
				}
			}
			want := int32(2)
			if enabled {
				want = 1
			}
			if got := connections.Load(); got != want {
				t.Fatalf("connections = %d, want %d", got, want)
			}
		})
	}
}
