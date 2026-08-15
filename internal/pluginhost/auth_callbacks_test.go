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

func TestHostAuthSaveBatchCallbackWritesFiles(t *testing.T) {
	authDir := t.TempDir()
	host := New()
	host.runtimeConfig = &config.Config{AuthDir: authDir}
	host.SetAuthManager(coreauth.NewManager(nil, nil, nil))

	req, errMarshal := json.Marshal(pluginapi.HostAuthSaveBatchRequest{
		Files: []pluginapi.HostAuthSaveRequest{
			{Name: "batch-a.json", JSON: json.RawMessage(`{"type":"demo","email":"a@example.com","api_key":"ka"}`)},
			{Name: "batch-b.json", JSON: json.RawMessage(`{"type":"demo","email":"b@example.com","api_key":"kb"}`)},
		},
	})
	if errMarshal != nil {
		t.Fatalf("marshal request: %v", errMarshal)
	}
	rawResp, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostAuthSaveBatch, req)
	if errCall != nil {
		t.Fatalf("callFromPlugin() error = %v", errCall)
	}
	resp, errDecode := decodeRPCEnvelope[pluginapi.HostAuthSaveBatchResponse](rawResp)
	if errDecode != nil {
		t.Fatalf("decode response: %v", errDecode)
	}
	if len(resp.Files) != 2 || resp.Files[0].Name != "batch-a.json" || resp.Files[1].Name != "batch-b.json" {
		t.Fatalf("response = %#v, want two saved files in request order", resp)
	}
	for _, file := range resp.Files {
		if _, errStat := os.Stat(file.Path); errStat != nil {
			t.Fatalf("stat saved file %s: %v", file.Path, errStat)
		}
	}
	if auths := host.currentAuthManager().List(); len(auths) != 2 {
		t.Fatalf("auths = %#v, want two registered auths", auths)
	}
}

func TestHostAuthSaveBatchCallbackRejectsInvalidBeforeAnyWrite(t *testing.T) {
	authDir := t.TempDir()
	host := New()
	host.runtimeConfig = &config.Config{AuthDir: authDir}
	host.SetAuthManager(coreauth.NewManager(nil, nil, nil))

	req, errMarshal := json.Marshal(pluginapi.HostAuthSaveBatchRequest{
		Files: []pluginapi.HostAuthSaveRequest{
			{Name: "batch-ok.json", JSON: json.RawMessage(`{"type":"demo","email":"ok@example.com","api_key":"k"}`)},
			{Name: "batch-bad.txt", JSON: json.RawMessage(`{"type":"demo"}`)},
		},
	})
	if errMarshal != nil {
		t.Fatalf("marshal request: %v", errMarshal)
	}
	if _, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostAuthSaveBatch, req); errCall == nil {
		t.Fatal("callFromPlugin() error = nil, want validation error")
	}
	entries, errReadDir := os.ReadDir(authDir)
	if errReadDir != nil {
		t.Fatalf("read auth dir: %v", errReadDir)
	}
	if len(entries) != 0 {
		t.Fatalf("auth dir entries = %d, want zero files after rejected batch", len(entries))
	}
	if auths := host.currentAuthManager().List(); len(auths) != 0 {
		t.Fatalf("auths = %#v, want no registered auths after rejected batch", auths)
	}
}

func TestHostAuthSaveBatchCallbackRejectsDuplicateNames(t *testing.T) {
	authDir := t.TempDir()
	host := New()
	host.runtimeConfig = &config.Config{AuthDir: authDir}
	host.SetAuthManager(coreauth.NewManager(nil, nil, nil))

	req, errMarshal := json.Marshal(pluginapi.HostAuthSaveBatchRequest{
		Files: []pluginapi.HostAuthSaveRequest{
			{Name: "dup.json", JSON: json.RawMessage(`{"type":"demo","email":"a@example.com","api_key":"k1"}`)},
			{Name: "DUP.json", JSON: json.RawMessage(`{"type":"demo","email":"b@example.com","api_key":"k2"}`)},
		},
	})
	if errMarshal != nil {
		t.Fatalf("marshal request: %v", errMarshal)
	}
	if _, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostAuthSaveBatch, req); errCall == nil {
		t.Fatal("callFromPlugin() error = nil, want duplicate name error")
	}
	if entries, errReadDir := os.ReadDir(authDir); errReadDir != nil || len(entries) != 0 {
		t.Fatalf("auth dir entries = %v (err %v), want zero files after rejected batch", entries, errReadDir)
	}
}

func TestHostAuthDeleteCallbackRemovesFileAndRuntime(t *testing.T) {
	authDir := t.TempDir()
	host := New()
	host.runtimeConfig = &config.Config{AuthDir: authDir}
	host.SetAuthManager(coreauth.NewManager(nil, nil, nil))

	saveReq, errMarshal := json.Marshal(pluginapi.HostAuthSaveRequest{
		Name: "delete-me.json",
		JSON: json.RawMessage(`{"type":"demo","email":"gone@example.com","api_key":"kg"}`),
	})
	if errMarshal != nil {
		t.Fatalf("marshal save request: %v", errMarshal)
	}
	if _, errSave := host.callFromPlugin(context.Background(), pluginabi.MethodHostAuthSave, saveReq); errSave != nil {
		t.Fatalf("save auth file: %v", errSave)
	}
	if auths := host.currentAuthManager().List(); len(auths) != 1 {
		t.Fatalf("auths = %#v, want one registered auth before delete", auths)
	}

	delReq, errMarshal := json.Marshal(pluginapi.HostAuthDeleteRequest{Name: "delete-me.json"})
	if errMarshal != nil {
		t.Fatalf("marshal delete request: %v", errMarshal)
	}
	rawResp, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostAuthDelete, delReq)
	if errCall != nil {
		t.Fatalf("callFromPlugin() error = %v", errCall)
	}
	resp, errDecode := decodeRPCEnvelope[pluginapi.HostAuthDeleteResponse](rawResp)
	if errDecode != nil {
		t.Fatalf("decode response: %v", errDecode)
	}
	if resp.Name != "delete-me.json" || resp.Path == "" {
		t.Fatalf("response = %#v, want deleted file identity", resp)
	}
	if _, errStat := os.Stat(resp.Path); !os.IsNotExist(errStat) {
		t.Fatalf("stat deleted file err = %v, want not-exist", errStat)
	}
	if auths := host.currentAuthManager().List(); len(auths) != 0 {
		t.Fatalf("auths = %#v, want no registered auths after delete", auths)
	}
}

func TestHostAuthDeleteCallbackRequiresSelector(t *testing.T) {
	host := New()
	host.runtimeConfig = &config.Config{AuthDir: t.TempDir()}
	host.SetAuthManager(coreauth.NewManager(nil, nil, nil))

	req, errMarshal := json.Marshal(pluginapi.HostAuthDeleteRequest{})
	if errMarshal != nil {
		t.Fatalf("marshal request: %v", errMarshal)
	}
	if _, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostAuthDelete, req); errCall == nil {
		t.Fatal("callFromPlugin() error = nil, want missing selector error")
	}
}
