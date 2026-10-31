//go:build cgo && (linux || darwin || freebsd)

package pluginhost

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestNativeAuthRefreshQuotaDemand(t *testing.T) {
	native := nativeAuthRefreshBuildNativeHost(t)
	for _, operation := range []string{"fetch", "reset", "failure"} {
		t.Run(operation, func(t *testing.T) {
			upstream := &nativeAuthRefreshLoopback{}
			provider := httptest.NewServer(upstream)
			defer provider.Close()
			credential := nativeAuthRefreshCredential(operation+".json", "expired", provider.URL)
			credential["test_refresh_policy"] = "on-demand"
			if operation == "failure" {
				credential["refresh_mode"] = "transient"
			}
			auth := nativeAuthRefreshParse(t, native.host, native.cfg.AuthDir, credential)
			_ = nativeAuthRefreshManager(t, native.cfg, native.host, auth)
			before := nativeAuthRefreshNativeCounters(t, native.client)
			stale := map[string]any{"access_token": "stale", "_cliproxy_refresh_policy": "on-demand"}
			var err error
			var handled bool
			if operation == "reset" {
				_, handled, err = native.host.ResetQuota(context.Background(), pluginapi.QuotaResetRequest{Provider: auth.Provider, AuthID: auth.ID, AuthIndex: auth.Index, Metadata: stale})
			} else {
				_, handled, err = native.host.FetchQuota(context.Background(), pluginapi.QuotaFetchRequest{Provider: auth.Provider, AuthID: auth.ID, AuthIndex: auth.Index, Metadata: stale})
			}
			if !handled {
				t.Fatal("native quota not handled")
			}
			after := nativeAuthRefreshNativeCounters(t, native.client)
			seen := upstream.snapshot()
			if after.Refresh-before.Refresh != 1 || after.ConsistencyErrors != 0 {
				t.Fatalf("native quota refresh=%+v", after)
			}
			if operation == "failure" {
				if err == nil || !strings.Contains(err.Error(), "synthetic refresh unavailable") || len(seen) != 1 {
					t.Fatalf("quota failure=%v calls=%d", err, len(seen))
				}
				return
			}
			path := "/v1/quota"
			if operation == "reset" {
				path += "/reset"
			}
			if err != nil || len(seen) != 2 || seen[0].Path != "/oauth/token" || seen[1].Path != path || seen[1].Authorization != "Bearer synthetic-access-rotated" {
				t.Fatalf("native quota err=%v requests=%+v", err, seen)
			}
		})
	}
}
