package pluginhost

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/MoeForge/moego/ojson"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestHasMultipleAuthProviders(t *testing.T) {
	host := newHostWithRecords(
		capabilityRecord{id: "multi", plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			AuthProvider: fakeAuthProvider{identifier: "vendor", identifiers: []string{"vendor", "vendor-cn"}, credentialProviders: []string{"canonical-vendor"}},
		}}},
		capabilityRecord{id: "separate", plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			AuthProviders: []pluginapi.AuthProvider{fakeAuthProvider{identifier: "portal-one"}, fakeAuthProvider{identifier: "portal-two"}},
		}}},
		capabilityRecord{id: "single", plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			AuthProvider: fakeAuthProvider{identifier: "single-vendor", identifiers: []string{"single-vendor", " SINGLE-VENDOR ", ""}},
		}}},
	)
	for _, tc := range []struct {
		provider string
		want     bool
	}{
		{provider: " Vendor ", want: true},
		{provider: "vendor-cn", want: true},
		{provider: "canonical-vendor", want: true},
		{provider: "portal-one", want: true},
		{provider: "portal-two", want: true},
		{provider: "single-vendor"},
		{provider: "missing"},
		{},
	} {
		if got := host.HasMultipleAuthProviders(tc.provider); got != tc.want {
			t.Errorf("HasMultipleAuthProviders(%q) = %t, want %t", tc.provider, got, tc.want)
		}
	}
	var nilHost *Host
	if nilHost.HasMultipleAuthProviders("vendor") {
		t.Fatal("nil host reports multiple auth providers")
	}
	record := host.authProviderRecord("vendor")
	setHostSnapshotForTest(host, false)
	if host.HasMultipleAuthProviders("vendor") || host.recordHasMultipleAuthProviders(*record) {
		t.Fatal("inactive plugin reports multiple auth providers")
	}
}

func TestParseAuthsOAuthProviderAttribution(t *testing.T) {
	for _, tc := range []struct {
		name        string
		request     string
		identifiers []string
		data        pluginapi.AuthData
		raw         []byte
		want        string
	}{
		{name: "multi-primary", request: "vendor", identifiers: []string{"vendor", "vendor-cn"}, want: "vendor"},
		{name: "multi-secondary", request: "vendor-cn", identifiers: []string{"vendor", "vendor-cn"}, want: "vendor-cn"},
		{name: "single-without-hint", request: "vendor", identifiers: []string{"vendor"}},
		{name: "attribute-hint", request: "vendor", identifiers: []string{"vendor"}, data: pluginapi.AuthData{Attributes: map[string]string{"oauth_provider": " Vendor "}}, want: "vendor"},
		{name: "metadata-hint", request: "vendor", identifiers: []string{"vendor"}, data: pluginapi.AuthData{Metadata: map[string]any{"portal": " Vendor "}}, want: "vendor"},
		{name: "storage-hint", request: "vendor", identifiers: []string{"vendor"}, data: pluginapi.AuthData{StorageJSON: []byte(`{"oauth_provider":" Vendor "}`)}, want: "vendor"},
		{name: "raw-hint", request: "vendor", identifiers: []string{"vendor"}, raw: []byte(`{"oauth_provider":" Vendor "}`), want: "vendor"},
		{
			name: "data-over-storage-over-request", request: "vendor", identifiers: []string{"vendor", "vendor-cn"},
			data: pluginapi.AuthData{Attributes: map[string]string{"portal": " Vendor "}, StorageJSON: []byte(`{"oauth_provider":"vendor-cn"}`)},
			raw:  []byte(`{"oauth_provider":"vendor-cn"}`), want: "vendor",
		},
		{
			name: "storage-over-request", request: "vendor", identifiers: []string{"vendor", "vendor-cn"},
			data: pluginapi.AuthData{StorageJSON: []byte(`{"oauth_provider":true,"portal":" Vendor-CN "}`)},
			raw:  []byte(`{"oauth_provider":"vendor"}`), want: "vendor-cn",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host := newHostWithRecords(capabilityRecord{id: "vendor-plugin", plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				AuthProvider: fakeAuthProvider{
					identifier: "vendor", identifiers: tc.identifiers,
					parseAuth: func(context.Context, pluginapi.AuthParseRequest) (pluginapi.AuthParseResponse, error) {
						data := tc.data
						data.Provider = "vendor"
						return pluginapi.AuthParseResponse{Handled: true, Auth: data}, nil
					},
				},
			}}})
			auths, handled, errParse := host.ParseAuths(context.Background(), pluginapi.AuthParseRequest{Provider: tc.request, RawJSON: tc.raw})
			if errParse != nil || !handled || len(auths) != 1 {
				t.Fatalf("ParseAuths() handled=%t count=%d error=%v", handled, len(auths), errParse)
			}
			auth := auths[0]
			if tc.want == "" {
				if _, exists := auth.Metadata["oauth_provider"]; exists {
					t.Errorf("single-entry parser added metadata: %#v", auth.Metadata)
				}
				if _, exists := auth.Attributes["oauth_provider"]; exists {
					t.Errorf("single-entry parser added attributes: %#v", auth.Attributes)
				}
			} else if auth.Metadata["oauth_provider"] != tc.want || auth.Attributes["oauth_provider"] != tc.want {
				t.Errorf("parsed attribution metadata=%#v attributes=%#v, want %q", auth.Metadata, auth.Attributes, tc.want)
			}
		})
	}
}

func TestPluginAuthDataToCoreAuthStorageOAuthProvider(t *testing.T) {
	for _, tc := range []struct {
		name string
		data pluginapi.AuthData
		want string
	}{
		{name: "raw-oauth", data: pluginapi.AuthData{StorageJSON: []byte(`{"oauth_provider":" Vendor "}`)}, want: "vendor"},
		{name: "raw-portal", data: pluginapi.AuthData{StorageJSON: []byte(`{"oauth_provider":"   ","portal":" Vendor-CN "}`)}, want: "vendor-cn"},
		{name: "invalid-hints-fall-back", data: pluginapi.AuthData{Metadata: map[string]any{"oauth_provider": 42}, Attributes: map[string]string{"portal": "   "}, StorageJSON: []byte(`{"oauth_provider":"vendor"}`)}, want: "vendor"},
		{name: "metadata-wins", data: pluginapi.AuthData{Metadata: map[string]any{"portal": "vendor-cn"}, StorageJSON: []byte(`{"oauth_provider":"vendor"}`)}, want: "vendor-cn"},
		{name: "attributes-win", data: pluginapi.AuthData{Attributes: map[string]string{"oauth_provider": "vendor-cn"}, StorageJSON: []byte(`{"oauth_provider":"vendor"}`)}, want: "vendor-cn"},
		{name: "invalid-json", data: pluginapi.AuthData{StorageJSON: []byte(`{"oauth_provider":"vendor"`)}},
		{name: "non-string", data: pluginapi.AuthData{StorageJSON: []byte(`{"oauth_provider":42,"portal":true}`)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.data.Provider = "vendor"
			auth := pluginAuthDataToCoreAuth(tc.data, "", "", "")
			hint := authDataPortalHint(&pluginapi.AuthData{Metadata: auth.Metadata, Attributes: auth.Attributes})
			if hint != tc.want {
				t.Fatalf("converted hint = %q, want %q", hint, tc.want)
			}
			if tc.want != "" {
				if auth.Metadata["oauth_provider"] != tc.want || auth.Attributes["oauth_provider"] != tc.want {
					t.Fatalf("hint not copied to both maps: metadata=%#v attributes=%#v", auth.Metadata, auth.Attributes)
				}
			}
		})
	}
}

func TestPreserveAuthOAuthProviderFromAttributes(t *testing.T) {
	for _, key := range []string{"oauth_provider", "portal"} {
		for _, metadata := range []map[string]any{nil, {"access_token": "old"}, {"oauth_provider": "   ", "portal": true}} {
			t.Run(key, func(t *testing.T) {
				auth := &coreauth.Auth{Provider: "vendor", Metadata: metadata, Attributes: map[string]string{key: " Vendor "}}
				data := pluginapi.AuthData{Provider: "vendor", Metadata: map[string]any{"access_token": "new"}}
				preserveAuthOAuthProviderMetadata(&data, auth, authDataPortalHint(&data))
				if data.Metadata["oauth_provider"] != "vendor" || data.Attributes["oauth_provider"] != "vendor" {
					t.Fatalf("preserved metadata=%#v attributes=%#v, want vendor", data.Metadata, data.Attributes)
				}
				if data.Metadata["access_token"] != "new" {
					t.Fatal("preservation replaced refreshed token")
				}
			})
		}
	}
}

func TestPollLoginRecordsEffectiveOAuthProvider(t *testing.T) {
	for _, responseKind := range []string{"auth", "auths"} {
		t.Run(responseKind, func(t *testing.T) {
			host := newHostWithRecords(capabilityRecord{id: "vendor-plugin", plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				AuthProvider: fakeAuthProvider{
					identifier: "vendor", identifiers: []string{"vendor", "vendor-cn"},
					pollLogin: func(context.Context, pluginapi.AuthLoginPollRequest) (pluginapi.AuthLoginPollResponse, error) {
						data := pluginapi.AuthData{Provider: "vendor", StorageJSON: []byte(`{"oauth_provider":"vendor-cn"}`)}
						resp := pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusSuccess}
						if responseKind == "auths" {
							resp.Auths = []pluginapi.AuthData{data}
						} else {
							resp.Auth = data
						}
						return resp, nil
					},
				},
			}}})
			_, handled, errPoll := host.PollLogin(context.Background(), "vendor", "state")
			if errPoll != nil || !handled {
				t.Fatalf("PollLogin() handled=%t error=%v", handled, errPoll)
			}
			if got, ok := host.recordedCredentialProviderPortal("vendor-plugin", "vendor"); !ok || got != "vendor-cn" {
				t.Fatalf("recorded portal = %q, present=%t, want vendor-cn", got, ok)
			}
		})
	}
}

func TestExecutorAdapterRefreshOAuthProvider(t *testing.T) {
	for _, tc := range []struct {
		name     string
		metadata map[string]any
		want     string
	}{
		{name: "existing-effective-hint", metadata: map[string]any{"access_token": "new"}, want: "vendor"},
		{name: "plugin-explicit-hint", metadata: map[string]any{"oauth_provider": "vendor-cn", "access_token": "new"}, want: "vendor-cn"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record := normalizeTestCapabilityRecord(capabilityRecord{id: "vendor-plugin", plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				AuthProvider: fakeAuthProvider{
					identifier: "vendor", identifiers: []string{"vendor", "vendor-cn"},
					refreshAuth: func(context.Context, pluginapi.AuthRefreshRequest) (pluginapi.AuthRefreshResponse, error) {
						return pluginapi.AuthRefreshResponse{Auth: pluginapi.AuthData{Metadata: tc.metadata}}, nil
					},
				},
			}}})
			host := newHostWithRecords(record)
			adapter := newExecutorAdapterForRecordForTest(host, record, &fakeExecutor{}, nil, nil)
			refreshed, errRefresh := adapter.Refresh(context.Background(), &coreauth.Auth{
				Provider: "vendor", Attributes: map[string]string{"oauth_provider": "vendor"}, Metadata: map[string]any{"portal": "vendor-cn"},
			})
			if errRefresh != nil || refreshed == nil {
				t.Fatalf("Refresh() auth=%#v error=%v", refreshed, errRefresh)
			}
			if refreshed.Metadata["oauth_provider"] != tc.want || refreshed.Attributes["oauth_provider"] != tc.want {
				t.Fatalf("refreshed attribution metadata=%#v attributes=%#v, want %q", refreshed.Metadata, refreshed.Attributes, tc.want)
			}
			storage := refreshed.Storage.(interface{ RawJSON() []byte })
			if got := ojson.GetBytes(storage.RawJSON(), "oauth_provider").String(); got != tc.want {
				t.Fatalf("refreshed storage hint = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRefreshAuthOAuthProviderSurvivesColdStart(t *testing.T) {
	for _, tc := range []struct {
		name       string
		attributes map[string]string
		metadata   map[string]any
		rawJSON    []byte
		response   pluginapi.AuthData
		want       string
	}{
		{
			name:       "attribute-oauth-over-metadata-portal",
			attributes: map[string]string{"oauth_provider": "vendor"},
			metadata:   map[string]any{"portal": "vendor-cn"},
			want:       "vendor",
		},
		{
			name:       "metadata-oauth-over-attribute-portal",
			attributes: map[string]string{"portal": "vendor-cn"},
			metadata:   map[string]any{"oauth_provider": "vendor"},
			want:       "vendor",
		},
		{
			name:       "attribute-portal-over-metadata-portal",
			attributes: map[string]string{"portal": "vendor"},
			metadata:   map[string]any{"portal": "vendor-cn"},
			want:       "vendor",
		},
		{
			name:     "metadata-portal-over-storage-oauth",
			metadata: map[string]any{"portal": "vendor"},
			rawJSON:  []byte(`{"oauth_provider":"vendor-cn"}`),
			want:     "vendor",
		},
		{
			name:    "storage-only",
			rawJSON: []byte(`{"oauth_provider":"vendor-cn"}`),
			want:    "vendor-cn",
		},
		{
			name:       "plugin-attribute-oauth-over-existing",
			attributes: map[string]string{"oauth_provider": "vendor"},
			metadata:   map[string]any{"oauth_provider": "vendor"},
			response:   pluginapi.AuthData{Attributes: map[string]string{"oauth_provider": "vendor-cn"}},
			want:       "vendor-cn",
		},
		{
			name:       "plugin-metadata-oauth-over-existing",
			attributes: map[string]string{"oauth_provider": "vendor"},
			response:   pluginapi.AuthData{Metadata: map[string]any{"oauth_provider": "vendor-cn"}},
			want:       "vendor-cn",
		},
		{
			name:       "plugin-attribute-portal-over-existing",
			attributes: map[string]string{"oauth_provider": "vendor"},
			response:   pluginapi.AuthData{Attributes: map[string]string{"portal": "vendor-cn"}},
			want:       "vendor-cn",
		},
		{
			name:       "plugin-metadata-portal-over-existing",
			attributes: map[string]string{"oauth_provider": "vendor"},
			response:   pluginapi.AuthData{Metadata: map[string]any{"portal": "vendor-cn"}},
			want:       "vendor-cn",
		},
		{
			name:       "plugin-storage-oauth-over-existing",
			attributes: map[string]string{"oauth_provider": "vendor"},
			metadata:   map[string]any{"portal": "vendor"},
			response:   pluginapi.AuthData{StorageJSON: []byte(`{"oauth_provider":"vendor-cn"}`)},
			want:       "vendor-cn",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			newHost := func() *Host {
				return newHostWithRecords(capabilityRecord{id: "vendor-plugin", plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
					AuthProvider: fakeAuthProvider{
						identifier: "vendor", identifiers: []string{"vendor", "vendor-cn"},
						refreshAuth: func(context.Context, pluginapi.AuthRefreshRequest) (pluginapi.AuthRefreshResponse, error) {
							data := tc.response
							data.Metadata = cloneAnyMap(data.Metadata)
							if data.Metadata == nil {
								data.Metadata = make(map[string]any)
							}
							data.Metadata["access_token"] = "new"
							return pluginapi.AuthRefreshResponse{Auth: data}, nil
						},
						parseAuth: func(_ context.Context, req pluginapi.AuthParseRequest) (pluginapi.AuthParseResponse, error) {
							return pluginapi.AuthParseResponse{Handled: true, Auth: pluginapi.AuthData{Provider: "vendor", StorageJSON: req.RawJSON}}, nil
						},
					},
				}}})
			}
			auth := &coreauth.Auth{Provider: "vendor", Attributes: tc.attributes, Metadata: tc.metadata}
			if len(tc.rawJSON) > 0 {
				auth.Storage = &pluginTokenStorage{provider: "vendor", rawJSON: tc.rawJSON}
			}
			refreshed, handled, errRefresh := newHost().RefreshAuth(context.Background(), auth)
			if errRefresh != nil || !handled || refreshed == nil {
				t.Fatalf("RefreshAuth() handled=%t auth=%#v error=%v", handled, refreshed, errRefresh)
			}
			if refreshed.Metadata["oauth_provider"] != tc.want || refreshed.Attributes["oauth_provider"] != tc.want {
				t.Fatalf("refreshed attribution metadata=%#v attributes=%#v, want %q", refreshed.Metadata, refreshed.Attributes, tc.want)
			}
			if refreshed.Metadata["access_token"] != "new" {
				t.Fatal("refresh lost the updated token")
			}
			raw := refreshed.Storage.(interface{ RawJSON() []byte }).RawJSON()
			if got := ojson.GetBytes(raw, "oauth_provider").String(); got != tc.want {
				t.Fatalf("refreshed storage hint = %q, want %q", got, tc.want)
			}
			path := filepath.Join(t.TempDir(), "account.json")
			if errSaveTokenToFile := refreshed.Storage.SaveTokenToFile(path); errSaveTokenToFile != nil {
				t.Fatalf("save refreshed auth: %v", errSaveTokenToFile)
			}
			saved, errRead := os.ReadFile(path)
			if errRead != nil {
				t.Fatalf("read saved auth: %v", errRead)
			}
			if !bytes.Equal(saved, raw) {
				t.Fatalf("saved payload = %s, want RawJSON() %s", saved, raw)
			}
			reloaded, handled, errParse := newHost().ParseAuth(context.Background(), pluginapi.AuthParseRequest{
				Provider: "vendor", Path: path, FileName: "account.json", RawJSON: saved,
			})
			if errParse != nil || !handled || reloaded == nil {
				t.Fatalf("cold-start ParseAuth() handled=%t auth=%#v error=%v", handled, reloaded, errParse)
			}
			if reloaded.Metadata["oauth_provider"] != tc.want || reloaded.Attributes["oauth_provider"] != tc.want {
				t.Fatalf("cold-start attribution metadata=%#v attributes=%#v, want %q", reloaded.Metadata, reloaded.Attributes, tc.want)
			}
		})
	}
}

func TestRefreshAuthPreservesAttributeOnlyOAuthProvider(t *testing.T) {
	host := newHostWithRecords(capabilityRecord{id: "vendor-plugin", plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
		AuthProvider: fakeAuthProvider{
			identifier: "vendor", identifiers: []string{"vendor", "vendor-cn"},
			refreshAuth: func(context.Context, pluginapi.AuthRefreshRequest) (pluginapi.AuthRefreshResponse, error) {
				return pluginapi.AuthRefreshResponse{Auth: pluginapi.AuthData{Provider: "vendor", Metadata: map[string]any{"access_token": "new"}}}, nil
			},
		},
	}}})
	auth, handled, errRefresh := host.RefreshAuth(context.Background(), &coreauth.Auth{
		Provider: "vendor", Attributes: map[string]string{"portal": " Vendor "},
	})
	if errRefresh != nil || !handled || auth == nil {
		t.Fatalf("RefreshAuth() handled=%t auth=%#v error=%v", handled, auth, errRefresh)
	}
	if auth.Metadata["oauth_provider"] != "vendor" || auth.Attributes["oauth_provider"] != "vendor" {
		t.Fatalf("refreshed attribution metadata=%#v attributes=%#v", auth.Metadata, auth.Attributes)
	}
	storage := auth.Storage.(interface{ RawJSON() []byte })
	if got := ojson.GetBytes(storage.RawJSON(), "oauth_provider").String(); got != "vendor" {
		t.Fatalf("refreshed storage hint = %q, want vendor", got)
	}
}
