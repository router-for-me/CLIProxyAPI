package executor

import (
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/httpcompression"
	"net/http"
)

// compressionTestClient exposes the selected pool underneath HTTP content decoding.
func compressionTestClient(client *http.Client) *http.Client {
	copyClient := *client
	if wrapped, ok := copyClient.Transport.(httpcompression.Transport); ok {
		copyClient.Transport = wrapped.Base
	}
	return &copyClient
}
