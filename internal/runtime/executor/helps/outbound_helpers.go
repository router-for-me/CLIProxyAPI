package helps

import (
	"context"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/outbound"
)

// DoProviderRequest applies final provider headers and sends req through client.
// The canonical provider normally comes from the auth manager's execution context.
func DoProviderRequest(ctx context.Context, provider string, client *http.Client, req *http.Request) (*http.Response, error) {
	return outbound.Do(ctx, provider, client, req)
}
