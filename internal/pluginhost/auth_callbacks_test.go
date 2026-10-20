package pluginhost

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type memoryAuthStorage struct {
	payload []byte
}

func (s *memoryAuthStorage) RawJSON() []byte {
	if s == nil {
		return nil
	}
	return append([]byte(nil), s.payload...)
}
func (s *memoryAuthStorage) SaveTokenToFile(authFilePath string) error {
	if s == nil || len(s.payload) == 0 {
		return fmt.Errorf("memory auth storage payload is empty")
	}
	return os.WriteFile(authFilePath, s.payload, 0o600)
}

func TestHostAuthListCallbackUsesAuthManager(t *testing.T) {
	authDir := t.TempDir()
	path := filepath.Join(authDir, "demo-a.json")
	if errWrite := os.WriteFile(path, []byte(`{"type":"demo","email":"a@example.com","api_key":"k1"}`), 0o600); errWrite != nil {
		t.Fatalf("write auth file: %v", errWrite)
	}

	auth := &coreauth.Auth{
		ID:       "demo-a.json",
		Provider: "demo",
		FileName: "demo-a.json",
		Label:    "a@example.com",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"path":   path,
			"source": path,
		},
		Metadata: map[string]any{
			"type":    "demo",
			"email":   "a@example.com",
			"api_key": "k1",
		},
		Storage: &memoryAuthStorage{payload: []byte(`{"type":"demo","email":"a@example.com","api_key":"k1"}`)},
	}
	auth.EnsureIndex()

	host := New()
	host.runtimeConfig = &config.Config{AuthDir: authDir}
	host.SetAuthManager(coreauth.NewManager(nil, nil, nil))
	if _, errRegister := host.currentAuthManager().Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	rawResp, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostAuthList, nil)
	if errCall != nil {
		t.Fatalf("callFromPlugin() error = %v", errCall)
	}
	resp, errDecode := decodeRPCEnvelope[rpcHostAuthListResponse](rawResp)
	if errDecode != nil {
		t.Fatalf("decode response: %v", errDecode)
	}
	if len(resp.Files) != 1 {
		t.Fatalf("files = %#v, want one entry", resp.Files)
	}
	entry := resp.Files[0]
	if entry.AuthIndex != auth.Index || entry.Name != "demo-a.json" || entry.Email != "a@example.com" {
		t.Fatalf("entry = %#v, want auth index and file metadata", entry)
	}
}

func TestHostAuthGetCallbackReturnsPhysicalJSONByAuthIndex(t *testing.T) {
	authDir := t.TempDir()
	path := filepath.Join(authDir, "demo-b.json")
	if errWrite := os.WriteFile(path, []byte(`{"type":"demo","email":"b@example.com","api_key":"k2"}`), 0o600); errWrite != nil {
		t.Fatalf("write auth file: %v", errWrite)
	}

	auth := &coreauth.Auth{
		ID:       "demo-b.json",
		Provider: "demo",
		FileName: "demo-b.json",
		Label:    "b@example.com",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"path":   path,
			"source": path,
		},
		Metadata: map[string]any{
			"type":    "demo",
			"email":   "b@example.com",
			"api_key": "k2",
		},
		Storage: &memoryAuthStorage{payload: []byte(`{"type":"demo","email":"b@example.com","api_key":"changed"}`)},
	}
	auth.EnsureIndex()

	host := New()
	host.SetAuthManager(coreauth.NewManager(nil, nil, nil))
	if _, errRegister := host.currentAuthManager().Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	req, errMarshal := json.Marshal(pluginapi.HostAuthGetRequest{AuthIndex: auth.Index})
	if errMarshal != nil {
		t.Fatalf("marshal request: %v", errMarshal)
	}
	rawResp, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostAuthGet, req)
	if errCall != nil {
		t.Fatalf("callFromPlugin() error = %v", errCall)
	}
	resp, errDecode := decodeRPCEnvelope[rpcHostAuthGetResponse](rawResp)
	if errDecode != nil {
		t.Fatalf("decode response: %v", errDecode)
	}
	if resp.AuthIndex != auth.Index || resp.Name != "demo-b.json" {
		t.Fatalf("response = %#v, want auth index and name", resp)
	}
	var decoded map[string]any
	if errUnmarshal := json.Unmarshal(resp.JSON, &decoded); errUnmarshal != nil {
		t.Fatalf("unmarshal auth json: %v", errUnmarshal)
	}
	if decoded["email"] != "b@example.com" || decoded["api_key"] != "k2" {
		t.Fatalf("decoded json = %#v, want credential payload", decoded)
	}
}

func TestHostAuthListCallbackFallsBackToDisk(t *testing.T) {
	authDir := t.TempDir()
	path := filepath.Join(authDir, "claude-a.json")
	if errWrite := os.WriteFile(path, []byte(`{"type":"claude","email":"c@example.com"}`), 0o600); errWrite != nil {
		t.Fatalf("write auth file: %v", errWrite)
	}

	host := New()
	host.runtimeConfig = &config.Config{AuthDir: authDir}

	rawResp, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostAuthList, nil)
	if errCall != nil {
		t.Fatalf("callFromPlugin() error = %v", errCall)
	}
	resp, errDecode := decodeRPCEnvelope[rpcHostAuthListResponse](rawResp)
	if errDecode != nil {
		t.Fatalf("decode response: %v", errDecode)
	}
	if len(resp.Files) != 1 {
		t.Fatalf("files = %#v, want one disk entry", resp.Files)
	}
	entry := resp.Files[0]
	if entry.Name != "claude-a.json" || entry.Type != "claude" || entry.Email != "c@example.com" {
		t.Fatalf("entry = %#v, want disk metadata", entry)
	}
	if entry.ModTime.IsZero() {
		t.Fatalf("entry modtime is zero: %#v", entry)
	}
	_ = time.Now()
}

func TestHostAuthGetRuntimeCallbackReturnsRuntimeInfo(t *testing.T) {
	auth := &coreauth.Auth{
		ID:       "demo-runtime.json",
		Provider: "demo",
		FileName: "demo-runtime.json",
		Label:    "runtime@example.com",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"runtime_only": "true",
		},
		Metadata: map[string]any{
			"type":    "demo",
			"email":   "runtime@example.com",
			"api_key": "runtime-key",
		},
		Storage: &memoryAuthStorage{payload: []byte(`{"type":"demo","email":"runtime@example.com","api_key":"runtime-key"}`)},
	}
	auth.EnsureIndex()

	host := New()
	host.SetAuthManager(coreauth.NewManager(nil, nil, nil))
	if _, errRegister := host.currentAuthManager().Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	req, errMarshal := json.Marshal(pluginapi.HostAuthGetRequest{AuthIndex: auth.Index})
	if errMarshal != nil {
		t.Fatalf("marshal request: %v", errMarshal)
	}
	rawResp, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostAuthGetRuntime, req)
	if errCall != nil {
		t.Fatalf("callFromPlugin() error = %v", errCall)
	}
	resp, errDecode := decodeRPCEnvelope[pluginapi.HostAuthGetRuntimeResponse](rawResp)
	if errDecode != nil {
		t.Fatalf("decode response: %v", errDecode)
	}
	if resp.Auth.AuthIndex != auth.Index || resp.Auth.RuntimeOnly != true || resp.Auth.Email != "runtime@example.com" {
		t.Fatalf("response = %#v, want runtime auth entry", resp.Auth)
	}
}

func TestHostAuthGetRuntimeCallbackReturnsBaseURL(t *testing.T) {
	auth := &coreauth.Auth{
		ID:       "demo-base-url.json",
		Provider: "demo",
		FileName: "demo-base-url.json",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"runtime_only": "true",
			"base_url":     "https://api.custom.example.com/v1",
		},
	}
	auth.EnsureIndex()

	authMeta := &coreauth.Auth{
		ID:       "demo-meta-base-url.json",
		Provider: "demo",
		FileName: "demo-meta-base-url.json",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"runtime_only": "true",
		},
		Metadata: map[string]any{
			"base_url": "https://meta.custom.example.com/v1",
		},
	}
	authMeta.EnsureIndex()

	host := New()
	host.SetAuthManager(coreauth.NewManager(nil, nil, nil))
	if _, errRegister := host.currentAuthManager().Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	if _, errRegister := host.currentAuthManager().Register(context.Background(), authMeta); errRegister != nil {
		t.Fatalf("register authMeta: %v", errRegister)
	}

	req, errMarshal := json.Marshal(pluginapi.HostAuthGetRequest{AuthIndex: auth.Index})
	if errMarshal != nil {
		t.Fatalf("marshal request: %v", errMarshal)
	}
	rawResp, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostAuthGetRuntime, req)
	if errCall != nil {
		t.Fatalf("callFromPlugin() error = %v", errCall)
	}
	resp, errDecode := decodeRPCEnvelope[pluginapi.HostAuthGetRuntimeResponse](rawResp)
	if errDecode != nil {
		t.Fatalf("decode response: %v", errDecode)
	}
	if resp.Auth.BaseURL != "https://api.custom.example.com/v1" {
		t.Fatalf("resp.Auth.BaseURL = %q, want %q", resp.Auth.BaseURL, "https://api.custom.example.com/v1")
	}

	reqMeta, errMarshalMeta := json.Marshal(pluginapi.HostAuthGetRequest{AuthIndex: authMeta.Index})
	if errMarshalMeta != nil {
		t.Fatalf("marshal request meta: %v", errMarshalMeta)
	}
	rawRespMeta, errCallMeta := host.callFromPlugin(context.Background(), pluginabi.MethodHostAuthGetRuntime, reqMeta)
	if errCallMeta != nil {
		t.Fatalf("callFromPlugin() meta error = %v", errCallMeta)
	}
	respMeta, errDecodeMeta := decodeRPCEnvelope[pluginapi.HostAuthGetRuntimeResponse](rawRespMeta)
	if errDecodeMeta != nil {
		t.Fatalf("decode response meta: %v", errDecodeMeta)
	}
	if respMeta.Auth.BaseURL != "https://meta.custom.example.com/v1" {
		t.Fatalf("respMeta.Auth.BaseURL = %q, want %q", respMeta.Auth.BaseURL, "https://meta.custom.example.com/v1")
	}

	authBoth := &coreauth.Auth{
		ID:       "demo-both-base-url.json",
		Provider: "demo",
		FileName: "demo-both-base-url.json",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"runtime_only": "true",
			"base_url":     "https://attr.custom.example.com/v1",
		},
		Metadata: map[string]any{
			"base_url": "https://meta.custom.example.com/v1",
		},
	}
	authBoth.EnsureIndex()
	if _, errRegister := host.currentAuthManager().Register(context.Background(), authBoth); errRegister != nil {
		t.Fatalf("register authBoth: %v", errRegister)
	}

	reqBoth, errMarshalBoth := json.Marshal(pluginapi.HostAuthGetRequest{AuthIndex: authBoth.Index})
	if errMarshalBoth != nil {
		t.Fatalf("marshal request both: %v", errMarshalBoth)
	}
	rawRespBoth, errCallBoth := host.callFromPlugin(context.Background(), pluginabi.MethodHostAuthGetRuntime, reqBoth)
	if errCallBoth != nil {
		t.Fatalf("callFromPlugin() both error = %v", errCallBoth)
	}
	respBoth, errDecodeBoth := decodeRPCEnvelope[pluginapi.HostAuthGetRuntimeResponse](rawRespBoth)
	if errDecodeBoth != nil {
		t.Fatalf("decode response both: %v", errDecodeBoth)
	}
	if respBoth.Auth.BaseURL != "https://attr.custom.example.com/v1" {
		t.Fatalf("respBoth.Auth.BaseURL = %q, want %q", respBoth.Auth.BaseURL, "https://attr.custom.example.com/v1")
	}
}

func TestListAuthFilesFromDiskReadsBaseURL(t *testing.T) {
	authDir := t.TempDir()
	filePath := filepath.Join(authDir, "test-auth.json")
	fileData := []byte(`{"type":"openai","base_url":"https://disk-proxy.example.com/v1","email":"user@example.com"}`)
	if errWrite := os.WriteFile(filePath, fileData, 0o600); errWrite != nil {
		t.Fatalf("write file: %v", errWrite)
	}

	host := New()
	host.runtimeConfig = &config.Config{AuthDir: authDir}
	entries, errList := host.listAuthFilesFromDisk()
	if errList != nil {
		t.Fatalf("listAuthFilesFromDisk error: %v", errList)
	}
	if len(entries) != 1 {
		t.Fatalf("entries count = %d, want 1", len(entries))
	}
	if entries[0].BaseURL != "https://disk-proxy.example.com/v1" {
		t.Fatalf("entry BaseURL = %q, want https://disk-proxy.example.com/v1", entries[0].BaseURL)
	}
}

func TestHostAuthSaveCallbackRejectsInvalidWeightBeforePersistence(t *testing.T) {
	for _, rawWeight := range []string{`1.5`, `1000001`, `9223372036854775808`, `"invalid"`} {
		t.Run(rawWeight, func(t *testing.T) {
			authDir := t.TempDir()
			host := New()
			host.runtimeConfig = &config.Config{AuthDir: authDir}
			host.SetAuthManager(coreauth.NewManager(nil, nil, nil))

			req, errMarshal := json.Marshal(pluginapi.HostAuthSaveRequest{
				Name: "invalid.json",
				JSON: json.RawMessage(`{"type":"demo","weight":` + rawWeight + `}`),
			})
			if errMarshal != nil {
				t.Fatalf("marshal request: %v", errMarshal)
			}
			if _, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostAuthSave, req); errCall == nil {
				t.Fatal("host.auth.save accepted an invalid weight")
			}
			if _, errStat := os.Stat(filepath.Join(authDir, "invalid.json")); !os.IsNotExist(errStat) {
				t.Fatalf("invalid auth file was persisted: %v", errStat)
			}
			if auths := host.currentAuthManager().List(); len(auths) != 0 {
				t.Fatalf("invalid auth was registered: %#v", auths)
			}
		})
	}
}

func TestHostAuthSaveCallbackWritesPhysicalFile(t *testing.T) {
	authDir := t.TempDir()
	host := New()
	host.runtimeConfig = &config.Config{AuthDir: authDir}
	host.SetAuthManager(coreauth.NewManager(nil, nil, nil))

	req, errMarshal := json.Marshal(pluginapi.HostAuthSaveRequest{
		Name: "saved.json",
		JSON: json.RawMessage(`{"type":"demo","email":"saved@example.com","api_key":"saved-key"}`),
	})
	if errMarshal != nil {
		t.Fatalf("marshal request: %v", errMarshal)
	}
	rawResp, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostAuthSave, req)
	if errCall != nil {
		t.Fatalf("callFromPlugin() error = %v", errCall)
	}
	resp, errDecode := decodeRPCEnvelope[pluginapi.HostAuthSaveResponse](rawResp)
	if errDecode != nil {
		t.Fatalf("decode response: %v", errDecode)
	}
	if resp.Name != "saved.json" {
		t.Fatalf("response = %#v, want saved file name", resp)
	}
	data, errRead := os.ReadFile(resp.Path)
	if errRead != nil {
		t.Fatalf("read saved file: %v", errRead)
	}
	if string(data) != `{"type":"demo","email":"saved@example.com","api_key":"saved-key"}` {
		t.Fatalf("saved file = %q, want credential json", string(data))
	}
	auths := host.currentAuthManager().List()
	if len(auths) != 1 || auths[0].FileName != "saved.json" {
		t.Fatalf("auths = %#v, want one registered auth", auths)
	}
}

func TestHostAuthSaveCallbackPreservesDisabledState(t *testing.T) {
	authDir := t.TempDir()
	host := New()
	host.runtimeConfig = &config.Config{AuthDir: authDir}
	host.SetAuthManager(coreauth.NewManager(sdkauth.NewFileTokenStore(), nil, nil))

	reqDisabled, errMarshal := json.Marshal(pluginapi.HostAuthSaveRequest{
		Name: "test-auth.json",
		JSON: json.RawMessage(`{"type":"demo","email":"disabled@example.com","api_key":"test-key","disabled":true}`),
	})
	if errMarshal != nil {
		t.Fatalf("marshal request: %v", errMarshal)
	}
	rawResp, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostAuthSave, reqDisabled)
	if errCall != nil {
		t.Fatalf("callFromPlugin() error = %v", errCall)
	}
	resp, errDecode := decodeRPCEnvelope[pluginapi.HostAuthSaveResponse](rawResp)
	if errDecode != nil {
		t.Fatalf("decode response: %v", errDecode)
	}

	auths := host.currentAuthManager().List()
	if len(auths) != 1 {
		t.Fatalf("auths len = %d, want 1", len(auths))
	}
	if !auths[0].Disabled {
		t.Fatalf("auth.Disabled = %v, want true", auths[0].Disabled)
	}
	if auths[0].Status != coreauth.StatusDisabled {
		t.Fatalf("auth.Status = %v, want %v", auths[0].Status, coreauth.StatusDisabled)
	}

	data, errRead := os.ReadFile(resp.Path)
	if errRead != nil {
		t.Fatalf("read saved file: %v", errRead)
	}
	var fileMeta map[string]any
	if errUnmarshal := json.Unmarshal(data, &fileMeta); errUnmarshal != nil {
		t.Fatalf("unmarshal saved file: %v", errUnmarshal)
	}
	if disabled, ok := fileMeta["disabled"].(bool); !ok || !disabled {
		t.Fatalf("saved file metadata disabled = %v, want true", fileMeta["disabled"])
	}

	reqEnabled, errMarshalEnabled := json.Marshal(pluginapi.HostAuthSaveRequest{
		Name: "test-auth.json",
		JSON: json.RawMessage(`{"type":"demo","email":"disabled@example.com","api_key":"test-key","disabled":false}`),
	})
	if errMarshalEnabled != nil {
		t.Fatalf("marshal request enabled: %v", errMarshalEnabled)
	}
	rawRespEnabled, errCallEnabled := host.callFromPlugin(context.Background(), pluginabi.MethodHostAuthSave, reqEnabled)
	if errCallEnabled != nil {
		t.Fatalf("callFromPlugin() error = %v", errCallEnabled)
	}
	if _, errDecodeEnabled := decodeRPCEnvelope[pluginapi.HostAuthSaveResponse](rawRespEnabled); errDecodeEnabled != nil {
		t.Fatalf("decode response enabled: %v", errDecodeEnabled)
	}
	auths = host.currentAuthManager().List()
	if len(auths) != 1 {
		t.Fatalf("auths len = %d, want 1", len(auths))
	}
	if auths[0].Disabled {
		t.Fatalf("auth.Disabled after re-enable = %v, want false", auths[0].Disabled)
	}
	if auths[0].Status != coreauth.StatusActive {
		t.Fatalf("auth.Status after re-enable = %v, want %v", auths[0].Status, coreauth.StatusActive)
	}

	dataEnabled, errReadEnabled := os.ReadFile(resp.Path)
	if errReadEnabled != nil {
		t.Fatalf("read saved file: %v", errReadEnabled)
	}
	var fileMetaEnabled map[string]any
	if errUnmarshalEnabled := json.Unmarshal(dataEnabled, &fileMetaEnabled); errUnmarshalEnabled != nil {
		t.Fatalf("unmarshal saved file: %v", errUnmarshalEnabled)
	}
	if disabled, ok := fileMetaEnabled["disabled"].(bool); ok && disabled {
		t.Fatalf("saved file metadata disabled after re-enable = %v, want false", fileMetaEnabled["disabled"])
	}

	reqUpdateDisabled, errMarshalUpdateDisabled := json.Marshal(pluginapi.HostAuthSaveRequest{
		Name: "test-auth.json",
		JSON: json.RawMessage(`{"type":"demo","email":"disabled@example.com","api_key":"test-key","disabled":true}`),
	})
	if errMarshalUpdateDisabled != nil {
		t.Fatalf("marshal update disabled request: %v", errMarshalUpdateDisabled)
	}
	rawRespUpdateDisabled, errCallUpdateDisabled := host.callFromPlugin(context.Background(), pluginabi.MethodHostAuthSave, reqUpdateDisabled)
	if errCallUpdateDisabled != nil {
		t.Fatalf("callFromPlugin() update disabled error = %v", errCallUpdateDisabled)
	}
	if _, errDecodeUpdateDisabled := decodeRPCEnvelope[pluginapi.HostAuthSaveResponse](rawRespUpdateDisabled); errDecodeUpdateDisabled != nil {
		t.Fatalf("decode update disabled response: %v", errDecodeUpdateDisabled)
	}
	auths = host.currentAuthManager().List()
	if len(auths) != 1 {
		t.Fatalf("auths len = %d, want 1", len(auths))
	}
	if !auths[0].Disabled {
		t.Fatalf("auth.Disabled after update to disabled = %v, want true", auths[0].Disabled)
	}
	if auths[0].Status != coreauth.StatusDisabled {
		t.Fatalf("auth.Status after update to disabled = %v, want %v", auths[0].Status, coreauth.StatusDisabled)
	}

	dataUpdateDisabled, errReadUpdateDisabled := os.ReadFile(resp.Path)
	if errReadUpdateDisabled != nil {
		t.Fatalf("read update disabled file: %v", errReadUpdateDisabled)
	}
	var fileMetaUpdateDisabled map[string]any
	if errUnmarshalUpdateDisabled := json.Unmarshal(dataUpdateDisabled, &fileMetaUpdateDisabled); errUnmarshalUpdateDisabled != nil {
		t.Fatalf("unmarshal update disabled file: %v", errUnmarshalUpdateDisabled)
	}
	if disabled, ok := fileMetaUpdateDisabled["disabled"].(bool); !ok || !disabled {
		t.Fatalf("saved file metadata disabled after update to disabled = %v, want true", fileMetaUpdateDisabled["disabled"])
	}
}

func TestHostListAuthFilesFromDiskParsesDisabled(t *testing.T) {
	authDir := t.TempDir()
	host := New()
	host.runtimeConfig = &config.Config{AuthDir: authDir}

	disabledFile := filepath.Join(authDir, "disabled.json")
	if errWrite := os.WriteFile(disabledFile, []byte(`{"type":"demo","disabled":true}`), 0o600); errWrite != nil {
		t.Fatalf("write disabled file: %v", errWrite)
	}
	activeFile := filepath.Join(authDir, "active.json")
	if errWrite := os.WriteFile(activeFile, []byte(`{"type":"demo","disabled":false}`), 0o600); errWrite != nil {
		t.Fatalf("write active file: %v", errWrite)
	}

	files, errList := host.listAuthFilesFromDisk()
	if errList != nil {
		t.Fatalf("listAuthFilesFromDisk() error = %v", errList)
	}
	if len(files) != 2 {
		t.Fatalf("listAuthFilesFromDisk() len = %d, want 2", len(files))
	}
	for _, f := range files {
		if f.Name == "disabled.json" {
			if !f.Disabled {
				t.Fatalf("disabled.json entry.Disabled = %v, want true", f.Disabled)
			}
			if f.Status != string(coreauth.StatusDisabled) {
				t.Fatalf("disabled.json entry.Status = %v, want %v", f.Status, coreauth.StatusDisabled)
			}
		}
		if f.Name == "active.json" {
			if f.Disabled {
				t.Fatalf("active.json entry.Disabled = %v, want false", f.Disabled)
			}
			if f.Status != string(coreauth.StatusActive) {
				t.Fatalf("active.json entry.Status = %v, want %v", f.Status, coreauth.StatusActive)
			}
		}
	}
}

func TestHostAuthSaveCallbackDoesNotDuplicatePluginAuthWithNonJSONID(t *testing.T) {
	authDir := t.TempDir()
	const (
		authID   = "demo-72a36b69-d20f-444f-a57d-f3cd0944530b"
		fileName = authID + ".json"
	)
	initialPath := filepath.Join(authDir, fileName)
	initialPayload := []byte(`{"type":"demo","email":"user@example.com","access_token":"tok-1"}`)
	if errWrite := os.WriteFile(initialPath, initialPayload, 0o600); errWrite != nil {
		t.Fatalf("write initial auth file: %v", errWrite)
	}

	host := newHostWithRecords(capabilityRecord{
		id: "demo-plugin",
		plugin: pluginapi.Plugin{
			Capabilities: pluginapi.Capabilities{
				AuthProvider: fakeAuthProvider{
					identifier: "demo",
					parseAuth: func(_ context.Context, req pluginapi.AuthParseRequest) (pluginapi.AuthParseResponse, error) {
						var meta map[string]any
						if errUnmarshal := json.Unmarshal(req.RawJSON, &meta); errUnmarshal != nil {
							return pluginapi.AuthParseResponse{}, errUnmarshal
						}
						email, _ := meta["email"].(string)
						return pluginapi.AuthParseResponse{
							Handled: true,
							Auth: pluginapi.AuthData{
								Provider:    "demo",
								ID:          authID,
								FileName:    filepath.Base(req.FileName),
								Label:       email,
								StorageJSON: req.RawJSON,
								Metadata:    meta,
							},
						}, nil
					},
				},
			},
		},
	})
	host.runtimeConfig = &config.Config{AuthDir: authDir}

	sdkauth.RegisterPluginAuthParser(host)
	t.Cleanup(func() {
		sdkauth.RegisterPluginAuthParser(nil)
	})

	store := sdkauth.NewFileTokenStore()
	store.SetBaseDir(authDir)
	manager := coreauth.NewManager(store, nil, nil)
	if errLoad := manager.Load(context.Background()); errLoad != nil {
		t.Fatalf("manager.Load() error = %v", errLoad)
	}
	host.SetAuthManager(manager)

	initialAuths := manager.List()
	if len(initialAuths) != 1 || initialAuths[0].ID != authID || initialAuths[0].FileName != fileName {
		t.Fatalf("initial manager.List() = %#v, want single auth with ID=%q FileName=%q", initialAuths, authID, fileName)
	}

	reqSave, errMarshal := json.Marshal(pluginapi.HostAuthSaveRequest{
		Name: fileName,
		JSON: json.RawMessage(`{"type":"demo","email":"user@example.com","access_token":"tok-2"}`),
	})
	if errMarshal != nil {
		t.Fatalf("marshal save request: %v", errMarshal)
	}
	rawSaveResp, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostAuthSave, reqSave)
	if errCall != nil {
		t.Fatalf("callFromPlugin(host.auth.save) error = %v", errCall)
	}
	if _, errDecode := decodeRPCEnvelope[pluginapi.HostAuthSaveResponse](rawSaveResp); errDecode != nil {
		t.Fatalf("decode save response: %v", errDecode)
	}

	authsAfterSave := manager.List()
	if len(authsAfterSave) != 1 {
		ids := make([]string, 0, len(authsAfterSave))
		for _, a := range authsAfterSave {
			if a != nil {
				ids = append(ids, a.ID+"(name="+a.FileName+")")
			}
		}
		t.Fatalf("manager.List() after host.auth.save len = %d (%v), want 1", len(authsAfterSave), ids)
	}
	if authsAfterSave[0].ID != authID || authsAfterSave[0].FileName != fileName {
		t.Fatalf("auth after save = %#v, want ID=%q FileName=%q", authsAfterSave[0], authID, fileName)
	}
	if gotToken := authsAfterSave[0].Metadata["access_token"]; gotToken != "tok-2" {
		t.Fatalf("auth Metadata[access_token] = %v, want tok-2", gotToken)
	}

	rawListResp, errListCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostAuthList, nil)
	if errListCall != nil {
		t.Fatalf("callFromPlugin(host.auth.list) error = %v", errListCall)
	}
	listResp, errDecodeList := decodeRPCEnvelope[rpcHostAuthListResponse](rawListResp)
	if errDecodeList != nil {
		t.Fatalf("decode list response: %v", errDecodeList)
	}
	if len(listResp.Files) != 1 || listResp.Files[0].ID != authID || listResp.Files[0].Name != fileName {
		t.Fatalf("host.auth.list files = %#v, want single entry with ID=%q Name=%q", listResp.Files, authID, fileName)
	}
}

func TestHostAuthSaveCallbackReusesExistingSameFileAuthIDWithoutAuthProvider(t *testing.T) {
	authDir := t.TempDir()
	const (
		authID   = "demo-existing-72a36b69"
		fileName = authID + ".json"
	)
	filePath := filepath.Join(authDir, fileName)
	if errWrite := os.WriteFile(filePath, []byte(`{"type":"demo","email":"user@example.com","access_token":"tok-1"}`), 0o600); errWrite != nil {
		t.Fatalf("write initial auth file: %v", errWrite)
	}

	manager := coreauth.NewManager(nil, nil, nil)
	existing := &coreauth.Auth{
		ID:       authID,
		Provider: "demo",
		FileName: fileName,
		Label:    "user@example.com",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"path":   filePath,
			"source": filePath,
		},
		Metadata: map[string]any{
			"type":         "demo",
			"email":        "user@example.com",
			"access_token": "tok-1",
		},
	}
	if _, errRegister := manager.Register(context.Background(), existing); errRegister != nil {
		t.Fatalf("register existing auth: %v", errRegister)
	}

	host := New()
	host.runtimeConfig = &config.Config{AuthDir: authDir}
	host.SetAuthManager(manager)

	reqSave, errMarshal := json.Marshal(pluginapi.HostAuthSaveRequest{
		Name: fileName,
		JSON: json.RawMessage(`{"type":"demo","email":"user@example.com","access_token":"tok-2"}`),
	})
	if errMarshal != nil {
		t.Fatalf("marshal save request: %v", errMarshal)
	}
	if _, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostAuthSave, reqSave); errCall != nil {
		t.Fatalf("callFromPlugin(host.auth.save) error = %v", errCall)
	}

	auths := manager.List()
	if len(auths) != 1 || auths[0].ID != authID || auths[0].FileName != fileName {
		t.Fatalf("manager.List() = %#v, want single auth with ID=%q FileName=%q", auths, authID, fileName)
	}
	if gotToken := auths[0].Metadata["access_token"]; gotToken != "tok-2" {
		t.Fatalf("auth Metadata[access_token] = %v, want tok-2", gotToken)
	}
}

func TestHostAuthSaveCallbackPreservesPluginDisabledChildWhenFileDisabledFalse(t *testing.T) {
	authDir := t.TempDir()
	const fileName = "multi-source.json"

	host := newHostWithRecords(capabilityRecord{
		id: "multi-plugin",
		plugin: pluginapi.Plugin{
			Capabilities: pluginapi.Capabilities{
				AuthProvider: fakeAuthProvider{
					identifier: "demo",
					parseAuth: func(_ context.Context, _ pluginapi.AuthParseRequest) (pluginapi.AuthParseResponse, error) {
						return pluginapi.AuthParseResponse{
							Handled: true,
							Auths: []pluginapi.AuthData{
								{
									Provider: "demo",
									ID:       "child-active",
									FileName: fileName,
									Disabled: false,
								},
								{
									Provider: "demo",
									ID:       "child-disabled",
									FileName: fileName,
									Disabled: true,
								},
							},
						}, nil
					},
				},
			},
		},
	})
	host.runtimeConfig = &config.Config{AuthDir: authDir}
	manager := coreauth.NewManager(nil, nil, nil)
	host.SetAuthManager(manager)

	reqSave, errMarshal := json.Marshal(pluginapi.HostAuthSaveRequest{
		Name: fileName,
		JSON: json.RawMessage(`{"type":"demo","disabled":false}`),
	})
	if errMarshal != nil {
		t.Fatalf("marshal save request: %v", errMarshal)
	}
	if _, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostAuthSave, reqSave); errCall != nil {
		t.Fatalf("callFromPlugin(host.auth.save) error = %v", errCall)
	}

	activeAuth, okActive := manager.GetByID("child-active")
	if !okActive || activeAuth == nil || activeAuth.Disabled || activeAuth.Status != coreauth.StatusActive {
		t.Fatalf("child-active = %#v, want enabled active auth", activeAuth)
	}
	disabledAuth, okDisabled := manager.GetByID("child-disabled")
	if !okDisabled || disabledAuth == nil || !disabledAuth.Disabled || disabledAuth.Status != coreauth.StatusDisabled {
		t.Fatalf("child-disabled = %#v, want plugin-disabled state preserved", disabledAuth)
	}
}

func TestHostAuthSaveCallbackUnregistersModelsForRemovedStaleSameFileAuth(t *testing.T) {
	authDir := t.TempDir()
	const (
		canonicalID = "demo-canonical-72a36b69"
		staleID     = canonicalID + ".json"
		fileName    = staleID
	)
	filePath := filepath.Join(authDir, fileName)
	if errWrite := os.WriteFile(filePath, []byte(`{"type":"demo","access_token":"tok-1"}`), 0o600); errWrite != nil {
		t.Fatalf("write initial auth file: %v", errWrite)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(staleID, "demo", []*registry.ModelInfo{{ID: "demo-model-1"}})
	t.Cleanup(func() {
		reg.UnregisterClient(staleID)
		reg.UnregisterClient(canonicalID)
	})
	if len(reg.GetModelsForClient(staleID)) == 0 {
		t.Fatal("expected initial models registered for stale client ID")
	}

	manager := coreauth.NewManager(nil, nil, nil)
	for _, id := range []string{canonicalID, staleID} {
		auth := &coreauth.Auth{
			ID:       id,
			Provider: "demo",
			FileName: fileName,
			Status:   coreauth.StatusActive,
			Attributes: map[string]string{
				"path":   filePath,
				"source": filePath,
			},
			Metadata: map[string]any{
				"type":         "demo",
				"access_token": "tok-1",
			},
		}
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register auth %s: %v", id, errRegister)
		}
	}

	host := New()
	host.runtimeConfig = &config.Config{AuthDir: authDir}
	host.SetAuthManager(manager)

	reqSave, errMarshal := json.Marshal(pluginapi.HostAuthSaveRequest{
		Name: fileName,
		JSON: json.RawMessage(`{"type":"demo","access_token":"tok-2"}`),
	})
	if errMarshal != nil {
		t.Fatalf("marshal save request: %v", errMarshal)
	}
	if _, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostAuthSave, reqSave); errCall != nil {
		t.Fatalf("callFromPlugin(host.auth.save) error = %v", errCall)
	}

	if _, exists := manager.GetByID(staleID); exists {
		t.Fatalf("expected stale auth %q to be removed from manager", staleID)
	}
	if models := reg.GetModelsForClient(staleID); len(models) != 0 {
		t.Fatalf("GetModelsForClient(%q) = %#v, want empty slice after stale auth removal", staleID, models)
	}
}

func TestHostAuthSaveRelativeAuthDirPreservesIdentity(t *testing.T) {
	for _, relative := range []bool{false, true} {
		name := "absolute"
		if relative {
			name = "relative"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			work := filepath.Join(root, "core")
			authDir := filepath.Join(root, "oauth")
			for _, dir := range []string{work, authDir} {
				if errMkdir := os.MkdirAll(dir, 0o700); errMkdir != nil {
					t.Fatal(errMkdir)
				}
			}
			t.Chdir(work)
			if relative {
				authDir = filepath.Join("..", "oauth")
			}
			const fileName = "demo-credential.json"
			payload := []byte(`{"type":"demo","access_token":"synthetic-not-a-real-token"}`)
			path := filepath.Join(authDir, fileName)
			if errWrite := os.WriteFile(path, payload, 0o600); errWrite != nil {
				t.Fatal(errWrite)
			}
			host := newHostWithRecords(capabilityRecord{
				id: "demo-plugin",
				plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
					AuthProvider: fakeAuthProvider{
						identifier: "demo",
						parseAuth: func(_ context.Context, req pluginapi.AuthParseRequest) (pluginapi.AuthParseResponse, error) {
							return pluginapi.AuthParseResponse{
								Handled: true,
								Auth: pluginapi.AuthData{
									Provider:    "demo",
									FileName:    req.FileName,
									StorageJSON: req.RawJSON,
								},
							}, nil
						},
					},
				}},
			})
			host.runtimeConfig = &config.Config{AuthDir: authDir}
			ctx := context.Background()
			initial, handled, errParse := host.ParseAuth(ctx, pluginapi.AuthParseRequest{
				Provider: "demo",
				Path:     path,
				FileName: fileName,
				RawJSON:  payload,
			})
			if errParse != nil || !handled || initial == nil || initial.ID != fileName {
				t.Fatalf("initial parse: auth=%#v handled=%v err=%v", initial, handled, errParse)
			}
			manager := coreauth.NewManager(nil, nil, nil)
			host.SetAuthManager(manager)
			if _, errRegister := manager.Register(coreauth.WithSkipPersist(ctx), initial); errRegister != nil {
				t.Fatal(errRegister)
			}
			reg := registry.GetGlobalRegistry()
			reg.RegisterClient(initial.ID, "demo", []*registry.ModelInfo{{ID: "demo-model"}})
			t.Cleanup(func() {
				reg.UnregisterClient(initial.ID)
				for _, auth := range manager.List() {
					reg.UnregisterClient(auth.ID)
				}
			})
			request, errMarshal := json.Marshal(pluginapi.HostAuthSaveRequest{Name: fileName, JSON: payload})
			if errMarshal != nil {
				t.Fatal(errMarshal)
			}
			if _, errCall := host.callFromPlugin(ctx, pluginabi.MethodHostAuthSave, request); errCall != nil {
				t.Fatal(errCall)
			}
			auths := manager.List()
			if len(auths) != 1 {
				t.Fatalf("got %d auth records, want 1", len(auths))
			}
			if auths[0].ID != initial.ID {
				t.Errorf("Core changed auth ID: before=%q after=%q", initial.ID, auths[0].ID)
			}
			if models := reg.GetModelsForClient(initial.ID); len(models) != 1 {
				t.Errorf("Core removed registered models: got %d, want 1", len(models))
			}
		})
	}
}
