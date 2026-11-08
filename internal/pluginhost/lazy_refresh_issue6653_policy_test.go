package pluginhost

import (
	"context"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"testing"
	"testing/synctest"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestIssue6653PolicyValidationAndClone(t *testing.T) {
	for _, tc := range []struct {
		mode   string
		margin int64
		valid  bool
	}{
		{"lazy", 0, true}, {"lazy", 60, true}, {"lazy", 3600, true}, {"scheduled", 0, true}, {"lazy", -1, false}, {"lazy", 3601, false}, {"", 60, false}, {"unknown", 0, false},
	} {
		policy := &pluginapi.AuthRefreshPolicy{Mode: tc.mode, ExpiryMarginSeconds: tc.margin}
		provider := fakeAuthProvider{identifier: "qa-policy", parseAuth: func(context.Context, pluginapi.AuthParseRequest) (pluginapi.AuthParseResponse, error) {
			return pluginapi.AuthParseResponse{Handled: true, Auth: pluginapi.AuthData{ID: "qa", Provider: "qa-policy", RefreshPolicy: policy}}, nil
		}}
		host := newHostWithRecords(capabilityRecord{id: "qa-policy", plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{AuthProvider: provider}}})
		a, _, err := host.ParseAuth(context.Background(), pluginapi.AuthParseRequest{Provider: "qa-policy"})
		if tc.valid {
			if err != nil || a == nil || a.RefreshPolicy == nil {
				t.Fatalf("valid policy %v parse=%v err=%v", policy, a, err)
			}
			clone := a.Clone()
			clone.RefreshPolicy.ExpiryMarginSeconds = 123
			if a.RefreshPolicy.ExpiryMarginSeconds != tc.margin {
				t.Fatal("clone shared mutable policy")
			}
			policy.ExpiryMarginSeconds = 321
			if a.RefreshPolicy.ExpiryMarginSeconds != tc.margin {
				t.Fatal("mapping shared plugin-owned policy")
			}
		} else if err == nil {
			t.Fatalf("invalid policy accepted: mode=%q margin=%d auth=%v", tc.mode, tc.margin, a)
		}
	}
}

func TestIssue6653PolicyRestoreAndExplicitScheduled(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		data := pluginapi.AuthData{ID: "qa", Provider: "qa-policy", StorageJSON: []byte(`{"host_refresh_policy":{"mode":"lazy","expiry_margin_seconds":60}}`)}
		if explicit {
			data.RefreshPolicy = &pluginapi.AuthRefreshPolicy{Mode: "scheduled"}
		}
		host := New()
		a := host.AuthDataToCoreAuth(data, "", "")
		if a == nil || a.RefreshPolicy == nil {
			t.Fatal("persisted policy not restored")
		}
		if a.UsesLazyRefresh() == explicit {
			t.Fatalf("explicit scheduled=%v lazy=%v", explicit, a.UsesLazyRefresh())
		}
	}
	for _, raw := range []string{`{"host_refresh_policy":{"mode":"lazy","expiry_margin_seconds":-1}}`, `{"host_refresh_policy":"wrong"}`, `{"host_refresh_policy":{"mode":"unknown"}}`} {
		if a := New().AuthDataToCoreAuth(pluginapi.AuthData{Provider: "qa-policy", StorageJSON: []byte(raw)}, "", ""); a != nil {
			t.Fatal("invalid persisted policy admitted")
		}
	}
}

func TestIssue6653NativeStructuredGrantCodeIsTerminal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		provider := fakeAuthProvider{identifier: "plugin-provider", refreshAuth: func(context.Context, pluginapi.AuthRefreshRequest) (pluginapi.AuthRefreshResponse, error) {
			calls++
			return pluginapi.AuthRefreshResponse{}, rpcError{Code: "invalid_grant", message: "qa refresh denied", statusCode: 400}
		}}
		p := pluginapi.Plugin{Capabilities: pluginapi.Capabilities{AuthProvider: provider, Executor: &fakeExecutor{identifier: "plugin-provider"}}}
		record := capabilityRecord{id: "qa-native-code", plugin: p}
		host := newHostWithRecords(record)
		m := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
		host.SetAuthManager(m)
		adapter := newExecutorAdapterForRecordForTest(host, record, p.Capabilities.Executor, nil, nil)
		m.RegisterExecutor(adapter)
		a := host.AuthDataToCoreAuth(pluginapi.AuthData{ID: "qa-code", Provider: "plugin-provider", RefreshPolicy: &pluginapi.AuthRefreshPolicy{Mode: "lazy"}, Metadata: map[string]any{"access_token": "qa-access", "refresh_token": "qa-refresh", "expires_at": time.Now().Add(-time.Hour).Format(time.RFC3339Nano)}}, "", "")
		a, err := m.Register(context.Background(), a)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := m.EnsureAuthFresh(context.Background(), a); err == nil {
			t.Fatal("native coded invalid_grant accepted")
		}
		current, _ := m.GetByID(a.ID)
		if !current.RequiresSignIn || current.RejectedRefreshToken == "" {
			t.Errorf("native RPC invalid_grant treated as transient: requires_sign_in=%v rejected_grant=%v refresh_calls=%d", current.RequiresSignIn, current.RejectedRefreshToken != "", calls)
		}
		time.Sleep(time.Hour)
		if _, err := m.EnsureAuthFresh(context.Background(), a); err == nil {
			t.Fatal("rejected native grant recovered without new credentials")
		}
		if calls != 1 {
			t.Fatalf("native structured invalid_grant reused after backoff: calls=%d want 1", calls)
		}
	})
}
