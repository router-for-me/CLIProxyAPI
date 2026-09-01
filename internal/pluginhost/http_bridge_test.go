package pluginhost

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestHostHTTPClientMarksUpstreamAttempt(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(server.Close)

	client := New().newHTTPClient(nil)
	for _, test := range []struct {
		name string
		do   func(context.Context) error
	}{
		{
			name: "buffered",
			do: func(ctx context.Context) error {
				_, errDo := client.Do(ctx, pluginapi.HTTPRequest{URL: server.URL})
				return errDo
			},
		},
		{
			name: "stream",
			do: func(ctx context.Context) error {
				response, errDo := client.DoStream(ctx, pluginapi.HTTPRequest{URL: server.URL})
				if errDo != nil {
					return errDo
				}
				for range response.Chunks {
				}
				return nil
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := cliproxyexecutor.WithUpstreamAttemptTracker(context.Background())
			if errDo := test.do(ctx); errDo != nil {
				t.Fatalf("host HTTP request error = %v", errDo)
			}
			if !cliproxyexecutor.UpstreamAttempted(ctx) {
				t.Fatal("host HTTP request did not mark an upstream attempt")
			}
		})
	}
}
