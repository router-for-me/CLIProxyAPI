package pluginhost

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestAuthProviderDiscovery(t *testing.T) {
	host := newHostWithRecords(
		capabilityRecord{
			id:       "high",
			priority: 20,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				AuthProvider: fakeAuthProvider{identifier: " High-Provider "},
			}},
		},
		capabilityRecord{
			id:       "low",
			priority: 10,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				AuthProvider: fakeAuthProvider{identifier: "low-provider"},
			}},
		},
		capabilityRecord{
			id: "missing-auth-provider",
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				ModelRegistrar: staticModelRegistrar("provider", "model"),
			}},
		},
	)

	identifiers := host.AuthProviderIdentifiers()
	if len(identifiers) != 2 || identifiers[0] != "high-provider" || identifiers[1] != "low-provider" {
		t.Fatalf("AuthProviderIdentifiers() = %#v, want sorted normalized providers", identifiers)
	}
	if !host.HasAuthProvider(" HIGH-PROVIDER ") {
		t.Fatal("HasAuthProvider(high-provider) = false, want true")
	}
	if host.HasAuthProvider("missing-provider") {
		t.Fatal("HasAuthProvider(missing-provider) = true, want false")
	}
}

func TestParseAuthDefaultsProviderFromRequest(t *testing.T) {
	host := newHostWithRecords(capabilityRecord{
		id: "auth-plugin",
		plugin: pluginapi.Plugin{
			Capabilities: pluginapi.Capabilities{
				AuthProvider: fakeAuthProvider{
					identifier: "plugin-provider",
					parseAuth: func(ctx context.Context, req pluginapi.AuthParseRequest) (pluginapi.AuthParseResponse, error) {
						return pluginapi.AuthParseResponse{
							Handled: true,
							Auth: pluginapi.AuthData{
								ID: "auth-1",
							},
						}, nil
					},
				},
			},
		},
	})

	auth, handled, errParse := host.ParseAuth(context.Background(), pluginapi.AuthParseRequest{Provider: "plugin-provider"})
	if errParse != nil {
		t.Fatalf("ParseAuth() error = %v", errParse)
	}
	if !handled || auth == nil {
		t.Fatalf("ParseAuth() handled=%t auth=%#v, want parsed auth", handled, auth)
	}
	if auth.Provider != "plugin-provider" || auth.Metadata["type"] != "plugin-provider" {
		t.Fatalf("ParseAuth() auth = %#v, want plugin-provider defaults", auth)
	}
}

func TestParseAuthDefaultsProviderFromAuthProviderIdentifier(t *testing.T) {
	seenProvider := ""
	host := newHostWithRecords(capabilityRecord{
		id: "auth-plugin",
		plugin: pluginapi.Plugin{
			Capabilities: pluginapi.Capabilities{
				AuthProvider: fakeAuthProvider{
					identifier: "Plugin-Provider",
					parseAuth: func(ctx context.Context, req pluginapi.AuthParseRequest) (pluginapi.AuthParseResponse, error) {
						seenProvider = req.Provider
						return pluginapi.AuthParseResponse{
							Handled: true,
							Auth: pluginapi.AuthData{
								ID: "auth-1",
							},
						}, nil
					},
				},
			},
		},
	})

	auth, handled, errParse := host.ParseAuth(context.Background(), pluginapi.AuthParseRequest{})
	if errParse != nil {
		t.Fatalf("ParseAuth() error = %v", errParse)
	}
	if !handled || auth == nil {
		t.Fatalf("ParseAuth() handled=%t auth=%#v, want parsed auth", handled, auth)
	}
	if seenProvider != "plugin-provider" {
		t.Fatalf("plugin parse request provider = %q, want plugin-provider", seenProvider)
	}
	if auth.Provider != "plugin-provider" || auth.Metadata["type"] != "plugin-provider" {
		t.Fatalf("ParseAuth() auth = %#v, want identifier provider fallback", auth)
	}
}

func TestParseAuthsExpandsMultiplePluginAuths(t *testing.T) {
	host := newHostWithRecords(capabilityRecord{
		id: "geminicli",
		plugin: pluginapi.Plugin{
			Capabilities: pluginapi.Capabilities{
				AuthProvider: fakeAuthProvider{
					identifier: "gemini-cli",
					parseAuth: func(ctx context.Context, req pluginapi.AuthParseRequest) (pluginapi.AuthParseResponse, error) {
						return pluginapi.AuthParseResponse{
							Handled: true,
							Auths: []pluginapi.AuthData{
								{
									Provider:    "gemini-cli",
									ID:          "user.json",
									FileName:    "user.json",
									StorageJSON: []byte(`{"type":"gemini-cli"}`),
								},
								{
									Provider:    "gemini-cli",
									ID:          "user-project-a.json",
									FileName:    "user-project-a.json",
									StorageJSON: []byte(`{"type":"gemini-cli","project_id":"project-a"}`),
									Metadata:    map[string]any{"project_id": "project-a"},
								},
							},
						}, nil
					},
				},
			},
		},
	})
	host.runtimeConfig = &config.Config{AuthDir: t.TempDir()}

	auths, handled, errParse := host.ParseAuths(context.Background(), pluginapi.AuthParseRequest{Provider: "gemini-cli"})
	if errParse != nil {
		t.Fatalf("ParseAuths() error = %v", errParse)
	}
	if !handled || len(auths) != 2 {
		t.Fatalf("ParseAuths() handled=%t len=%d, want two auths", handled, len(auths))
	}
	if auths[1].Provider != "gemini-cli" || auths[1].Metadata["project_id"] != "project-a" {
		t.Fatalf("second auth = %#v, want project-a virtual auth", auths[1])
	}
}

func TestStartLoginPassesProviderBaseURLHostAndHTTPClient(t *testing.T) {
	authDir := t.TempDir()
	expiresAt := time.Now().Add(time.Minute).UTC()
	called := false
	host := newHostWithRecords(capabilityRecord{
		id: "auth-plugin",
		plugin: pluginapi.Plugin{
			Capabilities: pluginapi.Capabilities{
				AuthProvider: fakeAuthProvider{
					identifier: "plugin-provider",
					startLogin: func(ctx context.Context, req pluginapi.AuthLoginStartRequest) (pluginapi.AuthLoginStartResponse, error) {
						called = true
						if req.Provider != "plugin-provider" || req.BaseURL != "http://localhost:8080/login" {
							t.Fatalf("StartLogin request = %#v, want provider/baseURL", req)
						}
						if req.Host.AuthDir != authDir || req.Host.ProxyURL != "http://proxy.local" || !req.Host.ForceModelPrefix {
							t.Fatalf("StartLogin host = %#v, want configured summary", req.Host)
						}
						if req.HTTPClient == nil {
							t.Fatal("StartLogin HTTPClient = nil, want host HTTP bridge")
						}
						return pluginapi.AuthLoginStartResponse{
							Provider:  req.Provider,
							URL:       "http://provider/login",
							State:     "state-1",
							ExpiresAt: expiresAt,
						}, nil
					},
				},
			},
		},
	})
	host.runtimeConfig = &config.Config{
		SDKConfig: config.SDKConfig{
			ProxyURL:         "http://proxy.local",
			ForceModelPrefix: true,
		},
		AuthDir: authDir,
	}

	resp, handled, errStart := host.StartLogin(context.Background(), " Plugin-Provider ", "http://localhost:8080/login")
	if errStart != nil {
		t.Fatalf("StartLogin() error = %v", errStart)
	}
	if !handled || !called {
		t.Fatalf("StartLogin() handled=%t called=%t, want handled call", handled, called)
	}
	if resp.Provider != "plugin-provider" || resp.URL != "http://provider/login" || resp.State != "state-1" || !resp.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("StartLogin() response = %#v, want plugin response", resp)
	}
}

func TestStartLoginPassesMetadataAndClonesMap(t *testing.T) {
	called := false
	var receivedMetadata map[string]any
	host := newHostWithRecords(capabilityRecord{
		id: "auth-plugin",
		plugin: pluginapi.Plugin{
			Capabilities: pluginapi.Capabilities{
				AuthProvider: fakeAuthProvider{
					identifier: "plugin-provider",
					startLogin: func(ctx context.Context, req pluginapi.AuthLoginStartRequest) (pluginapi.AuthLoginStartResponse, error) {
						called = true
						receivedMetadata = req.Metadata
						return pluginapi.AuthLoginStartResponse{
							Provider: req.Provider,
							State:    "state-1",
						}, nil
					},
				},
			},
		},
	})

	meta := map[string]any{"region": "us-east-1", "nested": "val"}
	resp, handled, errStart := host.StartLogin(context.Background(), "plugin-provider", "http://localhost:8080/login", meta)
	if errStart != nil {
		t.Fatalf("StartLogin() error = %v", errStart)
	}
	if !handled || !called {
		t.Fatalf("StartLogin() handled=%t called=%t, want handled call", handled, called)
	}
	if resp.State != "state-1" {
		t.Fatalf("StartLogin() response = %#v, want state-1", resp)
	}
	if receivedMetadata == nil || receivedMetadata["region"] != "us-east-1" {
		t.Fatalf("receivedMetadata = %#v, want region=us-east-1", receivedMetadata)
	}

	// Verify metadata cloning: mutating original meta map must not mutate received map
	meta["region"] = "mutated"
	if receivedMetadata["region"] != "us-east-1" {
		t.Fatalf("receivedMetadata was mutated when caller map changed: %#v", receivedMetadata)
	}

	// Verify calling StartLogin without metadata sets req.Metadata to nil
	called = false
	receivedMetadata = nil
	_, _, errNoMeta := host.StartLogin(context.Background(), "plugin-provider", "http://localhost:8080/login")
	if errNoMeta != nil {
		t.Fatalf("StartLogin() without metadata error = %v", errNoMeta)
	}
	if !called {
		t.Fatal("StartLogin() without metadata was not called")
	}
	if receivedMetadata != nil {
		t.Fatalf("receivedMetadata = %#v, want nil for empty metadata", receivedMetadata)
	}
}

func TestPollLoginPassesProviderStateHostAndHTTPClient(t *testing.T) {
	authDir := t.TempDir()
	called := false
	host := newHostWithRecords(capabilityRecord{
		id: "auth-plugin",
		plugin: pluginapi.Plugin{
			Capabilities: pluginapi.Capabilities{
				AuthProvider: fakeAuthProvider{
					identifier: "plugin-provider",
					pollLogin: func(ctx context.Context, req pluginapi.AuthLoginPollRequest) (pluginapi.AuthLoginPollResponse, error) {
						called = true
						if req.Provider != "plugin-provider" || req.State != "state-1" {
							t.Fatalf("PollLogin request = %#v, want provider/state", req)
						}
						if req.Host.AuthDir != authDir || req.Host.ProxyURL != "http://proxy.local" || !req.Host.ForceModelPrefix {
							t.Fatalf("PollLogin host = %#v, want configured summary", req.Host)
						}
						if req.HTTPClient == nil {
							t.Fatal("PollLogin HTTPClient = nil, want host HTTP bridge")
						}
						return pluginapi.AuthLoginPollResponse{
							Status:  pluginapi.AuthLoginStatusSuccess,
							Message: "done",
							Auth: pluginapi.AuthData{
								Provider: "plugin-provider",
								ID:       "auth-1",
							},
						}, nil
					},
				},
			},
		},
	})
	host.runtimeConfig = &config.Config{
		SDKConfig: config.SDKConfig{
			ProxyURL:         "http://proxy.local",
			ForceModelPrefix: true,
		},
		AuthDir: authDir,
	}

	resp, handled, errPoll := host.PollLogin(context.Background(), " Plugin-Provider ", " state-1 ")
	if errPoll != nil {
		t.Fatalf("PollLogin() error = %v", errPoll)
	}
	if !handled || !called {
		t.Fatalf("PollLogin() handled=%t called=%t, want handled call", handled, called)
	}
	if resp.Status != pluginapi.AuthLoginStatusSuccess || resp.Message != "done" || resp.Auth.ID != "auth-1" {
		t.Fatalf("PollLogin() response = %#v, want plugin response", resp)
	}
}

func TestRefreshAuthPreservesAuthIndex(t *testing.T) {
	host := newHostWithRecords(capabilityRecord{
		id: "auth-plugin",
		plugin: pluginapi.Plugin{
			Capabilities: pluginapi.Capabilities{
				AuthProvider: fakeAuthProvider{
					identifier: "plugin-provider",
					refreshAuth: func(ctx context.Context, req pluginapi.AuthRefreshRequest) (pluginapi.AuthRefreshResponse, error) {
						if req.AuthID != "auth-1" || req.AuthProvider != "plugin-provider" {
							t.Fatalf("RefreshAuth request = %#v, want auth id/provider", req)
						}
						return pluginapi.AuthRefreshResponse{
							Auth: pluginapi.AuthData{
								Metadata: map[string]any{"access_token": "new-token"},
							},
						}, nil
					},
				},
			},
		},
	})

	auth := host.AuthDataToCoreAuth(pluginapi.AuthData{
		Provider: "plugin-provider",
		ID:       "auth-1",
		Metadata: map[string]any{"access_token": "old-token"},
	}, "", "")
	if auth == nil {
		t.Fatal("AuthDataToCoreAuth() = nil, want auth")
	}
	auth.Index = "home-index-1"

	refreshed, handled, errRefresh := host.RefreshAuth(context.Background(), auth)
	if errRefresh != nil {
		t.Fatalf("RefreshAuth() error = %v", errRefresh)
	}
	if !handled || refreshed == nil {
		t.Fatalf("RefreshAuth() handled=%t auth=%#v, want refreshed auth", handled, refreshed)
	}
	if refreshed.Index != "home-index-1" {
		t.Fatalf("RefreshAuth() index = %q, want home-index-1", refreshed.Index)
	}
	if got := refreshed.Metadata["access_token"]; got != "new-token" {
		t.Fatalf("RefreshAuth() access_token = %q, want new-token", got)
	}
}

func TestRefreshAuthPreservesFilePriorityWhenPluginReturnsPartialAuth(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "plugin.json")
	host := newHostWithRecords(capabilityRecord{
		id: "auth-plugin",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			AuthProvider: fakeAuthProvider{
				identifier: "plugin-provider",
				refreshAuth: func(context.Context, pluginapi.AuthRefreshRequest) (pluginapi.AuthRefreshResponse, error) {
					return pluginapi.AuthRefreshResponse{Auth: pluginapi.AuthData{
						Metadata:   map[string]any{"access_token": "new-token", "priority": float64(0)},
						Attributes: map[string]string{"plugin_attr": "new-value", "priority": "0"},
					}}, nil
				},
			},
		}},
	})
	auth := &coreauth.Auth{
		ID:       "auth-1",
		Provider: "plugin-provider",
		Attributes: map[string]string{
			coreauth.AttributeSourceBackend: coreauth.AuthSourceFile,
			coreauth.AttributePath:          filePath,
			coreauth.AttributeSource:        filePath,
			coreauth.AttributeFilePriority:  "true",
			"priority":                      "1",
		},
		Metadata: map[string]any{"priority": float64(1), "access_token": "old-token"},
	}
	refreshed, handled, errRefresh := host.RefreshAuth(context.Background(), auth)
	if errRefresh != nil || !handled || refreshed == nil {
		t.Fatalf("RefreshAuth() auth = %#v, handled = %t, error = %v", refreshed, handled, errRefresh)
	}
	if got := refreshed.Attributes["priority"]; got != "1" {
		t.Errorf("refreshed priority attribute = %q, want 1", got)
	}
	if got := refreshed.Metadata["priority"]; got != float64(1) {
		t.Errorf("refreshed priority metadata = %v, want 1", got)
	}
	if _, errSave := sdkauth.NewFileTokenStore().Save(context.Background(), refreshed); errSave != nil {
		t.Fatalf("Save() refreshed auth: %v", errSave)
	}
	payload, errRead := os.ReadFile(filePath)
	if errRead != nil {
		t.Fatalf("read saved auth file: %v", errRead)
	}
	var saved map[string]any
	if errUnmarshal := json.Unmarshal(payload, &saved); errUnmarshal != nil {
		t.Fatalf("decode saved auth file: %v", errUnmarshal)
	}
	if got := saved["priority"]; got != float64(1) {
		t.Errorf("saved priority = %v, want 1", got)
	}
}

func TestRefreshAuthAllowsPluginPriorityWithoutFilePriority(t *testing.T) {
	host := newHostWithRecords(capabilityRecord{
		id: "auth-plugin",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			AuthProvider: fakeAuthProvider{
				identifier: "plugin-provider",
				refreshAuth: func(context.Context, pluginapi.AuthRefreshRequest) (pluginapi.AuthRefreshResponse, error) {
					return pluginapi.AuthRefreshResponse{Auth: pluginapi.AuthData{
						Metadata:   map[string]any{"priority": float64(2)},
						Attributes: map[string]string{"priority": "2", coreauth.AttributeFilePriority: "true"},
					}}, nil
				},
			},
		}},
	})
	auth := &coreauth.Auth{
		ID:       "auth-1",
		Provider: "plugin-provider",
		Attributes: map[string]string{
			coreauth.AttributeSourceBackend: coreauth.AuthSourceFile,
			"priority":                      "1",
		},
		Metadata: map[string]any{"priority": float64(1)},
	}
	refreshed, handled, errRefresh := host.RefreshAuth(context.Background(), auth)
	if errRefresh != nil || !handled || refreshed == nil {
		t.Fatalf("RefreshAuth() auth = %#v, handled = %t, error = %v", refreshed, handled, errRefresh)
	}
	if refreshed.Attributes["priority"] != "2" || refreshed.Metadata["priority"] != float64(2) {
		t.Fatalf("refreshed priority = %q/%v, want 2/2", refreshed.Attributes["priority"], refreshed.Metadata["priority"])
	}
	if _, inherited := refreshed.Attributes[coreauth.AttributeFilePriority]; inherited {
		t.Fatal("plugin-supplied file priority marker survived refresh without file priority")
	}
}

func TestPluginTokenStorageRemovesDeletedPriority(t *testing.T) {
	storage := &pluginTokenStorage{
		provider: "plugin-provider",
		rawJSON:  []byte(`{"type":"plugin-provider","priority":1,"access_token":"old"}`),
		meta:     map[string]any{"priority": float64(1), "access_token": "old"},
	}
	storage.SetMetadata(map[string]any{"access_token": "new"})
	var saved map[string]any
	if errUnmarshal := json.Unmarshal(storage.RawJSON(), &saved); errUnmarshal != nil {
		t.Fatalf("decode storage JSON: %v", errUnmarshal)
	}
	if _, exists := saved["priority"]; exists {
		t.Fatalf("deleted priority reappeared in storage JSON: %#v", saved)
	}
}

func TestHostAuthDataToCoreAuthRejectsMissingProviderAndUsesAuthDir(t *testing.T) {
	authDir := t.TempDir()
	host := New()
	host.runtimeConfig = &config.Config{AuthDir: authDir}
	path := filepath.Join(authDir, "nested", "auth.json")

	if auth := host.AuthDataToCoreAuth(pluginapi.AuthData{ID: "auth-1"}, path, "auth.json"); auth != nil {
		t.Fatalf("AuthDataToCoreAuth() = %#v, want nil for missing provider", auth)
	}
	auth := host.AuthDataToCoreAuth(pluginapi.AuthData{Provider: "Plugin-Provider"}, path, "")
	if auth == nil {
		t.Fatal("AuthDataToCoreAuth() = nil, want auth")
	}
	if auth.Provider != "plugin-provider" || auth.ID != "nested/auth.json" {
		t.Fatalf("AuthDataToCoreAuth() auth = %#v, want normalized provider and relative ID", auth)
	}
	if auth.Metadata["type"] != "plugin-provider" || auth.Attributes["path"] != path || auth.Attributes["source"] != path {
		t.Fatalf("AuthDataToCoreAuth() metadata=%#v attributes=%#v, want path/source/type", auth.Metadata, auth.Attributes)
	}
}

func TestPluginTokenStorageMergesRawMetadataAndProviderType(t *testing.T) {
	storage := &pluginTokenStorage{
		provider: "plugin-provider",
		rawJSON:  []byte(`{"old":"value","type":"old-provider"}`),
	}
	storage.SetMetadata(map[string]any{
		"new": "value",
		"old": "override",
	})

	raw := storage.RawJSON()
	var decoded map[string]any
	if errUnmarshal := json.Unmarshal(raw, &decoded); errUnmarshal != nil {
		t.Fatalf("RawJSON() decode error = %v", errUnmarshal)
	}
	if decoded["old"] != "override" || decoded["new"] != "value" || decoded["type"] != "plugin-provider" {
		t.Fatalf("RawJSON() decoded = %#v, want merged metadata and provider type", decoded)
	}

	path := filepath.Join(t.TempDir(), "auth.json")
	if errSave := storage.SaveTokenToFile(path); errSave != nil {
		t.Fatalf("SaveTokenToFile() error = %v", errSave)
	}
	saved, errReadFile := os.ReadFile(path)
	if errReadFile != nil {
		t.Fatalf("ReadFile(saved token) error = %v", errReadFile)
	}
	decoded = nil
	if errUnmarshal := json.Unmarshal(saved, &decoded); errUnmarshal != nil {
		t.Fatalf("saved token decode error = %v", errUnmarshal)
	}
	if decoded["old"] != "override" || decoded["new"] != "value" || decoded["type"] != "plugin-provider" {
		t.Fatalf("saved token decoded = %#v, want merged metadata and provider type", decoded)
	}
}

func TestPluginTokenStorageNormalizesCredentialMetadataKeys(t *testing.T) {
	tests := []struct {
		name     string
		rawJSON  []byte
		metadata map[string]any
		want     map[string]any
	}{
		{
			name:    "legacy raw keys",
			rawJSON: []byte(`{"request-retry":2,"disable-cooling":true,"provider-specific-key":"preserved"}`),
			want: map[string]any{
				"request_retry":         float64(2),
				"disable_cooling":       true,
				"provider-specific-key": "preserved",
				"type":                  "plugin-provider",
			},
		},
		{
			name:    "canonical metadata wins",
			rawJSON: []byte(`{"request-retry":2,"disable-cooling":true}`),
			metadata: map[string]any{
				"request_retry":   0,
				"disable_cooling": false,
			},
			want: map[string]any{
				"request_retry":   float64(0),
				"disable_cooling": false,
				"type":            "plugin-provider",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			storage := &pluginTokenStorage{
				provider: "plugin-provider",
				rawJSON:  test.rawJSON,
			}
			storage.SetMetadata(test.metadata)

			outputs := map[string][]byte{
				"RawJSON": storage.RawJSON(),
			}
			path := filepath.Join(t.TempDir(), "auth.json")
			if errSave := storage.SaveTokenToFile(path); errSave != nil {
				t.Fatalf("SaveTokenToFile() error = %v", errSave)
			}
			saved, errReadFile := os.ReadFile(path)
			if errReadFile != nil {
				t.Fatalf("ReadFile(saved token) error = %v", errReadFile)
			}
			outputs["SaveTokenToFile"] = saved

			for outputName, payload := range outputs {
				var decoded map[string]any
				if errUnmarshal := json.Unmarshal(payload, &decoded); errUnmarshal != nil {
					t.Fatalf("%s decode error = %v", outputName, errUnmarshal)
				}
				if !reflect.DeepEqual(decoded, test.want) {
					t.Errorf("%s decoded = %#v, want %#v", outputName, decoded, test.want)
				}
				if _, exists := decoded["request-retry"]; exists {
					t.Errorf("%s retained request-retry: %#v", outputName, decoded)
				}
				if _, exists := decoded["disable-cooling"]; exists {
					t.Errorf("%s retained disable-cooling: %#v", outputName, decoded)
				}
			}
		})
	}
}

func TestPluginTokenStorageSkipsUnchangedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	if errWriteFile := os.WriteFile(path, []byte(`{"disabled":false,"token":"secret","type":"plugin-provider"}`), 0o600); errWriteFile != nil {
		t.Fatalf("WriteFile() error = %v", errWriteFile)
	}
	before, errStatBefore := os.Stat(path)
	if errStatBefore != nil {
		t.Fatalf("Stat(before) error = %v", errStatBefore)
	}
	storage := &pluginTokenStorage{
		provider: "plugin-provider",
		rawJSON:  []byte(`{"token":"secret"}`),
	}
	storage.SetMetadata(map[string]any{"disabled": false})

	if errSave := storage.SaveTokenToFile(path); errSave != nil {
		t.Fatalf("SaveTokenToFile() error = %v", errSave)
	}
	after, errStatAfter := os.Stat(path)
	if errStatAfter != nil {
		t.Fatalf("Stat(after) error = %v", errStatAfter)
	}
	if !os.SameFile(before, after) {
		t.Fatal("SaveTokenToFile() replaced unchanged auth file, want write skipped")
	}
}

func TestPluginTokenStorageRejectsEmptyPayload(t *testing.T) {
	storage := &pluginTokenStorage{}
	if raw := storage.RawJSON(); raw != nil {
		t.Fatalf("RawJSON() = %q, want nil for empty payload", raw)
	}
	if errSave := storage.SaveTokenToFile(filepath.Join(t.TempDir(), "auth.json")); errSave == nil {
		t.Fatal("SaveTokenToFile() error = nil, want empty payload error")
	}
}

func TestRefreshAuth_MergesAttributesAndPreservesPath_Issue6119(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "plugin.json")
	host := newHostWithRecords(capabilityRecord{
		id: "auth-plugin",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			AuthProvider: fakeAuthProvider{
				identifier: "plugin-provider",
				refreshAuth: func(context.Context, pluginapi.AuthRefreshRequest) (pluginapi.AuthRefreshResponse, error) {
					return pluginapi.AuthRefreshResponse{Auth: pluginapi.AuthData{
						Metadata:   map[string]any{"access_token": "new-token"},
						Attributes: map[string]string{"priority": "1"},
					}}, nil
				},
			},
		}},
	})

	testCases := []struct {
		name          string
		sourceBackend string
	}{
		{name: "file_backend", sourceBackend: coreauth.AuthSourceFile},
		{name: "postgres_backend", sourceBackend: "postgres"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			auth := &coreauth.Auth{
				ID:       "auth-1",
				Provider: "plugin-provider",
				FileName: "plugin.json",
				Attributes: map[string]string{
					coreauth.AttributeSourceBackend: tc.sourceBackend,
					coreauth.AttributePath:          filePath,
					coreauth.AttributeSource:        filePath,
					"custom_env":                    "production",
				},
				Metadata: map[string]any{"access_token": "old-token"},
			}

			refreshed, handled, errRefresh := host.RefreshAuth(context.Background(), auth)
			if errRefresh != nil || !handled || refreshed == nil {
				t.Fatalf("RefreshAuth() auth = %#v, handled = %t, error = %v", refreshed, handled, errRefresh)
			}

			if got := refreshed.Attributes[coreauth.AttributePath]; got != filePath {
				t.Errorf("refreshed path attribute = %q, want %q", got, filePath)
			}
			if got := refreshed.Attributes[coreauth.AttributeSource]; got != filePath {
				t.Errorf("refreshed source attribute = %q, want %q", got, filePath)
			}
			if got := refreshed.Attributes[coreauth.AttributeSourceBackend]; got != tc.sourceBackend {
				t.Errorf("refreshed source_backend attribute = %q, want %q", got, tc.sourceBackend)
			}
			if got := refreshed.Attributes["custom_env"]; got != "production" {
				t.Errorf("refreshed custom_env attribute = %q, want production", got)
			}
			if got := refreshed.Attributes["priority"]; got != "1" {
				t.Errorf("refreshed priority attribute = %q, want 1", got)
			}
		})
	}
}

func TestAuthProviderMultiPortalRegistrationAndRouting(t *testing.T) {
	t.Run("multi_identifier_provider_with_shared_executor", func(t *testing.T) {
		var startedProviders []string
		var polledProviders []string
		var refreshedProviders []string
		host := newHostWithRecords(capabilityRecord{
			id:       "omc-zcode-oauth",
			priority: 10,
			plugin: pluginapi.Plugin{
				Capabilities: pluginapi.Capabilities{
					AuthProvider: fakeAuthProvider{
						identifier:  " ZCode-ZAI ",
						identifiers: []string{" ZCode-ZAI ", "zcode-bigmodel", "zcode-zai"},
						parseAuth: func(_ context.Context, req pluginapi.AuthParseRequest) (pluginapi.AuthParseResponse, error) {
							return pluginapi.AuthParseResponse{
								Handled: true,
								Auth: pluginapi.AuthData{
									Provider: "zcode",
									ID:       "zcode-auth-1",
								},
							}, nil
						},
						startLogin: func(_ context.Context, req pluginapi.AuthLoginStartRequest) (pluginapi.AuthLoginStartResponse, error) {
							startedProviders = append(startedProviders, req.Provider)
							return pluginapi.AuthLoginStartResponse{
								Provider: req.Provider,
								URL:      "https://portal.example/" + req.Provider,
								State:    "state-" + req.Provider,
							}, nil
						},
						pollLogin: func(_ context.Context, req pluginapi.AuthLoginPollRequest) (pluginapi.AuthLoginPollResponse, error) {
							polledProviders = append(polledProviders, req.Provider)
							return pluginapi.AuthLoginPollResponse{
								Status: pluginapi.AuthLoginStatusSuccess,
								Auth: pluginapi.AuthData{
									Provider: "zcode",
									ID:       "zcode-auth-" + req.Provider,
								},
							}, nil
						},
						refreshAuth: func(_ context.Context, req pluginapi.AuthRefreshRequest) (pluginapi.AuthRefreshResponse, error) {
							refreshedProviders = append(refreshedProviders, req.AuthProvider)
							return pluginapi.AuthRefreshResponse{
								Auth: pluginapi.AuthData{
									Provider: req.AuthProvider,
									ID:       req.AuthID,
									Metadata: map[string]any{"access_token": "refreshed"},
								},
							}, nil
						},
					},
					Executor:           &fakeExecutor{identifier: "zcode"},
					ExecutorModelScope: pluginapi.ExecutorModelScopeOAuth,
					ModelProvider: modelProviderFunc{
						modelsForAuth: func(_ context.Context, req pluginapi.AuthModelRequest) (pluginapi.ModelResponse, error) {
							return pluginapi.ModelResponse{
								Provider: req.AuthProvider,
								Models:   []pluginapi.ModelInfo{{ID: "glm-5"}},
							}, nil
						},
					},
				},
			},
		})

		infos := host.RegisteredPlugins()
		if len(infos) != 1 {
			t.Fatalf("RegisteredPlugins() len = %d, want 1", len(infos))
		}
		if !infos[0].SupportsOAuth || infos[0].OAuthProvider != "zcode-zai" {
			t.Fatalf("RegisteredPlugins()[0] = %#v, want SupportsOAuth=true OAuthProvider=zcode-zai", infos[0])
		}
		wantProviders := []string{"zcode-zai", "zcode-bigmodel"}
		if !reflect.DeepEqual(infos[0].OAuthProviders, wantProviders) {
			t.Fatalf("RegisteredPlugins()[0].OAuthProviders = %#v, want %#v", infos[0].OAuthProviders, wantProviders)
		}
		if got := host.AuthProviderIdentifiers(); !reflect.DeepEqual(got, wantProviders) {
			t.Fatalf("AuthProviderIdentifiers() = %#v, want %#v", got, wantProviders)
		}
		for _, portal := range wantProviders {
			if !host.HasAuthProvider(portal) {
				t.Fatalf("HasAuthProvider(%q) = false, want true", portal)
			}
			startResp, handled, errStart := host.StartLogin(context.Background(), portal, "http://127.0.0.1:8317/v8/management/oauth/callback")
			if errStart != nil || !handled || startResp.Provider != portal {
				t.Fatalf("StartLogin(%q) = (%#v, %v, %v), want handled response", portal, startResp, handled, errStart)
			}
			pollResp, handled, errPoll := host.PollLogin(context.Background(), portal, startResp.State)
			if errPoll != nil || !handled || pollResp.Status != pluginapi.AuthLoginStatusSuccess {
				t.Fatalf("PollLogin(%q) = (%#v, %v, %v), want success", portal, pollResp, handled, errPoll)
			}
		}
		if !reflect.DeepEqual(startedProviders, wantProviders) || !reflect.DeepEqual(polledProviders, wantProviders) {
			t.Fatalf("started=%#v polled=%#v, want %#v", startedProviders, polledProviders, wantProviders)
		}
		if !host.SameAuthProvider("zcode", "zcode-bigmodel") || !host.SameAuthProvider("zcode-zai", "zcode-bigmodel") {
			t.Fatal("SameAuthProvider() = false, want true for providers owned by the same plugin")
		}
		for _, credProvider := range []string{"zcode", "zcode-zai", "zcode-bigmodel"} {
			if !host.HasAuthModelProvider(credProvider) {
				t.Fatalf("HasAuthModelProvider(%q) = false, want true", credProvider)
			}
			refreshed, handled, errRefresh := host.RefreshAuth(context.Background(), &coreauth.Auth{
				ID:       "auth-" + credProvider,
				Provider: credProvider,
			})
			if errRefresh != nil || !handled || refreshed == nil {
				t.Fatalf("RefreshAuth(%q) = (%#v, %v, %v), want handled refresh", credProvider, refreshed, handled, errRefresh)
			}
		}
	})

	t.Run("plural_auth_providers_capability", func(t *testing.T) {
		var calledPortal string
		host := newHostWithRecords(capabilityRecord{
			id:       "multi-cap-plugin",
			priority: 10,
			plugin: pluginapi.Plugin{
				Capabilities: pluginapi.Capabilities{
					AuthProviders: []pluginapi.AuthProvider{
						fakeAuthProvider{
							identifier: "portal-one",
							startLogin: func(_ context.Context, req pluginapi.AuthLoginStartRequest) (pluginapi.AuthLoginStartResponse, error) {
								calledPortal = "one:" + req.Provider
								return pluginapi.AuthLoginStartResponse{Provider: req.Provider, URL: "https://one.example", State: "state-1"}, nil
							},
						},
						fakeAuthProvider{
							identifier: "portal-two",
							startLogin: func(_ context.Context, req pluginapi.AuthLoginStartRequest) (pluginapi.AuthLoginStartResponse, error) {
								calledPortal = "two:" + req.Provider
								return pluginapi.AuthLoginStartResponse{Provider: req.Provider, URL: "https://two.example", State: "state-2"}, nil
							},
						},
					},
				},
			},
		})

		infos := host.RegisteredPlugins()
		if len(infos) != 1 || !infos[0].SupportsOAuth || infos[0].OAuthProvider != "portal-one" {
			t.Fatalf("RegisteredPlugins() = %#v, want portal-one primary", infos)
		}
		if !reflect.DeepEqual(infos[0].OAuthProviders, []string{"portal-one", "portal-two"}) {
			t.Fatalf("OAuthProviders = %#v, want [portal-one portal-two]", infos[0].OAuthProviders)
		}
		_, handled, errStart := host.StartLogin(context.Background(), "portal-two", "http://127.0.0.1:8317/callback")
		if errStart != nil || !handled || calledPortal != "two:portal-two" {
			t.Fatalf("StartLogin(portal-two) handled=%v err=%v calledPortal=%q, want two:portal-two", handled, errStart, calledPortal)
		}
	})

	t.Run("plural_auth_providers_interleaved_shared_credential_refresh_and_parse", func(t *testing.T) {
		refreshedBy := ""
		parsedBy := ""
		host := newHostWithRecords(capabilityRecord{
			id:       "multi-handler-plugin",
			priority: 10,
			plugin: pluginapi.Plugin{
				Capabilities: pluginapi.Capabilities{
					AuthProviders: []pluginapi.AuthProvider{
						fakeAuthProvider{
							identifier: "portal-one",
							pollLogin: func(_ context.Context, req pluginapi.AuthLoginPollRequest) (pluginapi.AuthLoginPollResponse, error) {
								return pluginapi.AuthLoginPollResponse{
									Status: pluginapi.AuthLoginStatusSuccess,
									Auth: pluginapi.AuthData{
										Provider: "vendor-shared",
										ID:       "cred-one",
									},
								}, nil
							},
							parseAuth: func(_ context.Context, req pluginapi.AuthParseRequest) (pluginapi.AuthParseResponse, error) {
								parsedBy = "one"
								return pluginapi.AuthParseResponse{
									Handled: true,
									Auth:    pluginapi.AuthData{Provider: "vendor-shared", ID: "cred-one"},
								}, nil
							},
							refreshAuth: func(_ context.Context, req pluginapi.AuthRefreshRequest) (pluginapi.AuthRefreshResponse, error) {
								refreshedBy = "one"
								return pluginapi.AuthRefreshResponse{Auth: pluginapi.AuthData{Provider: req.AuthProvider, ID: req.AuthID}}, nil
							},
						},
						fakeAuthProvider{
							identifier: "portal-two",
							pollLogin: func(_ context.Context, req pluginapi.AuthLoginPollRequest) (pluginapi.AuthLoginPollResponse, error) {
								return pluginapi.AuthLoginPollResponse{
									Status: pluginapi.AuthLoginStatusSuccess,
									Auth: pluginapi.AuthData{
										Provider: "vendor-shared",
										ID:       "cred-two",
									},
								}, nil
							},
							parseAuth: func(_ context.Context, req pluginapi.AuthParseRequest) (pluginapi.AuthParseResponse, error) {
								parsedBy = "two"
								return pluginapi.AuthParseResponse{
									Handled: true,
									Auth:    pluginapi.AuthData{Provider: "vendor-shared", ID: "cred-two"},
								}, nil
							},
							refreshAuth: func(_ context.Context, req pluginapi.AuthRefreshRequest) (pluginapi.AuthRefreshResponse, error) {
								refreshedBy = "two"
								return pluginapi.AuthRefreshResponse{Auth: pluginapi.AuthData{Provider: req.AuthProvider, ID: req.AuthID}}, nil
							},
						},
					},
				},
			},
		})

		// Log in via portal-one first, then portal-two; portal-two must NOT overwrite portal-one's credential handler.
		pollOne, handled, errPoll := host.PollLogin(context.Background(), "portal-one", "state-1")
		if errPoll != nil || !handled || pollOne.Status != pluginapi.AuthLoginStatusSuccess {
			t.Fatalf("PollLogin(portal-one) = (%#v, %v, %v), want success", pollOne, handled, errPoll)
		}
		pollTwo, handled, errPoll := host.PollLogin(context.Background(), "portal-two", "state-2")
		if errPoll != nil || !handled || pollTwo.Status != pluginapi.AuthLoginStatusSuccess {
			t.Fatalf("PollLogin(portal-two) = (%#v, %v, %v), want success", pollTwo, handled, errPoll)
		}
		credOne := host.AuthDataToCoreAuth(pollOne.Auth, "", "cred-one.json")
		credTwo := host.AuthDataToCoreAuth(pollTwo.Auth, "", "cred-two.json")

		_, handled, errRefresh := host.RefreshAuth(context.Background(), credOne)
		if errRefresh != nil || !handled || refreshedBy != "one" {
			t.Fatalf("RefreshAuth(credOne) handled=%v err=%v refreshedBy=%q, want one", handled, errRefresh, refreshedBy)
		}
		_, handled, errRefresh = host.RefreshAuth(context.Background(), credTwo)
		if errRefresh != nil || !handled || refreshedBy != "two" {
			t.Fatalf("RefreshAuth(credTwo) handled=%v err=%v refreshedBy=%q, want two", handled, errRefresh, refreshedBy)
		}
		parsedOne, handled, errParse := host.ParseAuth(context.Background(), pluginapi.AuthParseRequest{
			Provider: "vendor-shared",
			RawJSON:  []byte(`{"type":"vendor-shared","oauth_provider":"portal-one"}`),
		})
		if errParse != nil || !handled || parsedBy != "one" || parsedOne == nil {
			t.Fatalf("ParseAuth(portal-one) handled=%v err=%v parsedBy=%q auth=%#v, want one", handled, errParse, parsedBy, parsedOne)
		}
		parsedTwo, handled, errParse := host.ParseAuth(context.Background(), pluginapi.AuthParseRequest{
			Provider: "vendor-shared",
			RawJSON:  []byte(`{"type":"vendor-shared","oauth_provider":"portal-two"}`),
		})
		if errParse != nil || !handled || parsedBy != "two" || parsedTwo == nil {
			t.Fatalf("ParseAuth(portal-two) handled=%v err=%v parsedBy=%q auth=%#v, want two", handled, errParse, parsedBy, parsedTwo)
		}
		refreshedBy = ""
		_, handled, errRefresh = host.RefreshAuth(context.Background(), parsedOne)
		if errRefresh != nil || !handled || refreshedBy != "one" {
			t.Fatalf("RefreshAuth(parsedOne) handled=%v err=%v refreshedBy=%q, want one", handled, errRefresh, refreshedBy)
		}
		refreshedBy = ""
		_, handled, errRefresh = host.RefreshAuth(context.Background(), parsedTwo)
		if errRefresh != nil || !handled || refreshedBy != "two" {
			t.Fatalf("RefreshAuth(parsedTwo) handled=%v err=%v refreshedBy=%q, want two", handled, errRefresh, refreshedBy)
		}
	})

	t.Run("no_prefix_hijack_or_unrelated_parse_broadcast", func(t *testing.T) {
		unrelatedParseCalled := false
		host := newHostWithRecords(capabilityRecord{
			id:       "vendor-cn-plugin",
			priority: 10,
			plugin: pluginapi.Plugin{
				Capabilities: pluginapi.Capabilities{
					AuthProvider: fakeAuthProvider{
						identifier: "vendor-cn",
						parseAuth: func(_ context.Context, req pluginapi.AuthParseRequest) (pluginapi.AuthParseResponse, error) {
							unrelatedParseCalled = true
							return pluginapi.AuthParseResponse{Handled: true, Auth: pluginapi.AuthData{Provider: req.Provider, ID: "hijacked"}}, nil
						},
					},
				},
			},
		})

		// "vendor-cn" must NOT implicitly claim "vendor" via prefix guessing
		if host.HasAuthProvider("vendor") {
			t.Fatal("HasAuthProvider(vendor) = true for vendor-cn plugin, want false")
		}
		if host.SameAuthProvider("vendor", "vendor-cn") {
			t.Fatal("SameAuthProvider(vendor, vendor-cn) = true without ownership declaration, want false")
		}
		// ParseAuth with an explicit unrelated provider ("claude" or "vendor") must NOT broadcast to vendor-cn
		_, handled, errParse := host.ParseAuth(context.Background(), pluginapi.AuthParseRequest{
			Provider: "claude",
			RawJSON:  []byte(`{"type":"claude","access_token":"tok"}`),
		})
		if errParse != nil || handled || unrelatedParseCalled {
			t.Fatalf("ParseAuth(claude) = (handled=%v, err=%v), unrelatedParseCalled=%v, want not called", handled, errParse, unrelatedParseCalled)
		}
	})

	t.Run("cold_start_parse_and_refresh_without_executor", func(t *testing.T) {
		// Simulate a fresh Host after restart where an auth-only plugin has portal identifiers
		// ("zai", "bigmodel") and either persisted oauth_provider metadata in RawJSON or declared CredentialProviders.
		refreshed := false
		host := newHostWithRecords(capabilityRecord{
			id:       "omc-zcode-oauth",
			priority: 10,
			plugin: pluginapi.Plugin{
				Capabilities: pluginapi.Capabilities{
					AuthProvider: fakeAuthProvider{
						identifier:  "zai",
						identifiers: []string{"zai", "bigmodel"},
						parseAuth: func(_ context.Context, req pluginapi.AuthParseRequest) (pluginapi.AuthParseResponse, error) {
							if req.Provider != "zcode" {
								return pluginapi.AuthParseResponse{Handled: false}, nil
							}
							return pluginapi.AuthParseResponse{
								Handled: true,
								Auth: pluginapi.AuthData{
									Provider: "zcode",
									ID:       "zcode-cold.json",
									Metadata: map[string]any{"oauth_provider": "bigmodel"},
								},
							}, nil
						},
						refreshAuth: func(_ context.Context, req pluginapi.AuthRefreshRequest) (pluginapi.AuthRefreshResponse, error) {
							refreshed = true
							return pluginapi.AuthRefreshResponse{
								Auth: pluginapi.AuthData{
									Provider: req.AuthProvider,
									ID:       req.AuthID,
								},
							}, nil
						},
					},
				},
			},
		})

		auth, handled, errParse := host.ParseAuth(context.Background(), pluginapi.AuthParseRequest{
			Provider: "zcode",
			FileName: "zcode-cold.json",
			RawJSON:  []byte(`{"type":"zcode","oauth_provider":"bigmodel","token":"abc"}`),
		})
		if errParse != nil || !handled || auth == nil || auth.Provider != "zcode" {
			t.Fatalf("cold-start ParseAuth(zcode) = (%#v, %v, %v), want handled zcode auth", auth, handled, errParse)
		}
		authNoRawHint, handled, errParse := host.ParseAuth(context.Background(), pluginapi.AuthParseRequest{
			Provider: "zcode",
			FileName: "zcode-cold-nohint.json",
			RawJSON:  []byte(`{"type":"zcode","token":"abc"}`),
		})
		if errParse != nil || !handled || authNoRawHint == nil {
			t.Fatalf("cold-start ParseAuth without raw hint = (%#v, %v, %v), want handled", authNoRawHint, handled, errParse)
		}
		if authNoRawHint.Metadata["oauth_provider"] != "bigmodel" || authNoRawHint.Attributes["oauth_provider"] != "bigmodel" {
			t.Fatalf("cold-start ParseAuth without raw hint attribution = (meta=%#v, attr=%q), want both bigmodel", authNoRawHint.Metadata["oauth_provider"], authNoRawHint.Attributes["oauth_provider"])
		}
		if !host.SameAuthProvider("zcode", "zai") || !host.SameAuthProvider("zcode", "bigmodel") {
			t.Fatal("SameAuthProvider after cold-start ParseAuth = false, want true")
		}
		_, handled, errRefresh := host.RefreshAuth(context.Background(), auth)
		if errRefresh != nil || !handled || !refreshed {
			t.Fatalf("RefreshAuth(zcode) after cold-start ParseAuth = (%v, %v), refreshed=%v", handled, errRefresh, refreshed)
		}
	})
}
