//go:build cgo && (linux || darwin || freebsd)

package pluginhost

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestNativeAuthRefreshInferenceVerification(t *testing.T) {
	native := nativeAuthRefreshBuildNativeHost(t)
	upstream := &nativeAuthRefreshLoopback{}
	provider := httptest.NewServer(upstream)
	defer provider.Close()
	credential := nativeAuthRefreshCredential("verification.json", "expired", provider.URL)
	credential["test_refresh_policy"] = "on-demand"
	auth := nativeAuthRefreshParse(t, native.host, native.cfg.AuthDir, credential)
	manager := nativeAuthRefreshManager(t, native.cfg, native.host, auth)
	current, err := manager.VerifyInference(context.Background(), auth.ID, "native-auth-refresh-chat")
	counters := nativeAuthRefreshNativeCounters(t, native.client)
	seen := upstream.snapshot()
	if err != nil || current == nil || !current.InferenceVerified || !current.RenewalVerified || current.Metadata["access_token"] != "synthetic-access-rotated" || counters.Refresh != 1 || counters.ConsistencyErrors != 0 || len(seen) != 2 || seen[0].Path != "/oauth/token" || seen[1].Path != "/v1/chat/completions" || !seen[1].Stream || seen[1].Authorization != "Bearer synthetic-access-rotated" {
		t.Fatalf("native inference verification: err=%v counters=%+v requests=%+v", err, counters, seen)
	}
	nativeAuthRefreshWriteJSON(t, filepath.Join(native.root, "verification-receipt.json"), map[string]any{"upstream_requests": seen, "native_refresh": counters.Refresh, "inference_verified": current.InferenceVerified, "renewal_verified": current.RenewalVerified, "credential_version": current.CredentialVersion, "registration_epoch": current.RegistrationEpoch})
	t.Logf("native verification used saved rotated bearer with one physical refresh: version=%d epoch=%d requests=%d", current.CredentialVersion, current.RegistrationEpoch, len(seen))
}
