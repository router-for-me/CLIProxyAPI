package pluginhost

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
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

func TestHostAuthSaveCallbackSyncsSelectionWeightAttribute(t *testing.T) {
	tests := []struct {
		name string
		json string
	}{
		{name: "underscore", json: `{"type":"codex","email":"saved@example.com","selection_weight":0}`},
		{name: "hyphen", json: `{"type":"codex","email":"saved@example.com","selection-weight":2}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			authDir := t.TempDir()
			host := New()
			host.runtimeConfig = &config.Config{AuthDir: authDir}
			host.SetAuthManager(coreauth.NewManager(nil, nil, nil))

			req, errMarshal := json.Marshal(pluginapi.HostAuthSaveRequest{
				Name: "saved.json",
				JSON: json.RawMessage(tt.json),
			})
			if errMarshal != nil {
				t.Fatalf("marshal request: %v", errMarshal)
			}
			rawResp, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostAuthSave, req)
			if errCall != nil {
				t.Fatalf("callFromPlugin() error = %v", errCall)
			}
			if _, errDecode := decodeRPCEnvelope[pluginapi.HostAuthSaveResponse](rawResp); errDecode != nil {
				t.Fatalf("decode response: %v", errDecode)
			}
			auths := host.currentAuthManager().List()
			if len(auths) != 1 {
				t.Fatalf("auths = %#v, want one registered auth", auths)
			}
			want := "0"
			if tt.name == "hyphen" {
				want = "2"
			}
			if got := auths[0].Attributes["selection_weight"]; got != want {
				t.Fatalf("selection_weight attribute = %q, want %q", got, want)
			}
		})
	}
}

func TestBuildAuthFromFileDataSelectionWeightAttribute(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "weighted.json")
	auth, err := (&Host{}).buildAuthFromFileData(authPath, []byte(`{"type":"codex","selection-weight":0}`))
	if err != nil {
		t.Fatalf("buildAuthFromFileData() error = %v", err)
	}
	if auth == nil {
		t.Fatalf("buildAuthFromFileData() = nil")
	}
	if got := auth.Attributes["selection_weight"]; got != "0" {
		t.Fatalf("Attributes selection_weight = %q, want 0", got)
	}
}

func TestBuildAuthFromFileDataSelectionWeightRejectsFractional(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "weighted.json")
	auth, err := (&Host{}).buildAuthFromFileData(authPath, []byte(`{"type":"codex","selection-weight":1.5}`))
	if err != nil {
		t.Fatalf("buildAuthFromFileData() error = %v", err)
	}
	if auth == nil {
		t.Fatalf("buildAuthFromFileData() = nil")
	}
	if got, ok := auth.Attributes["selection_weight"]; ok {
		t.Fatalf("Attributes selection_weight = %q, want absent", got)
	}
}

func TestBuildHostAuthFileEntrySelectionWeight(t *testing.T) {
	authDir := t.TempDir()
	authPath := filepath.Join(authDir, "codex.json")
	if err := os.WriteFile(authPath, []byte(`{"type":"codex"}`), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}

	auth := &coreauth.Auth{
		ID:       "codex.json",
		FileName: "codex.json",
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"path":             authPath,
			"selection_weight": "0",
		},
	}

	entry := (&Host{}).buildHostAuthFileEntry(auth)
	if entry == nil {
		t.Fatalf("buildHostAuthFileEntry() = nil")
	}
	if entry.SelectionWeight != 0 {
		t.Fatalf("SelectionWeight = %d, want 0", entry.SelectionWeight)
	}
	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal entry: %v", err)
	}
	var encoded map[string]any
	if err := json.Unmarshal(data, &encoded); err != nil {
		t.Fatalf("unmarshal entry: %v", err)
	}
	if got, ok := encoded["selection_weight"].(float64); !ok || got != 0 {
		t.Fatalf("encoded selection_weight = %#v, want 0", encoded["selection_weight"])
	}
}
