package management

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestProvisionAgentIdentityAndExportCodexAuthFile(t *testing.T) {
	h, manager, registrar := newAgentIdentityTestHandler(t)

	recorder := performAgentIdentityProvision(t, h, `{"auth_index":"idx-codex"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("provision status = %d, want %d body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	for _, secret := range []string{"access-secret", "refresh-secret"} {
		if strings.Contains(recorder.Body.String(), secret) {
			t.Fatalf("provision response leaked %q: %s", secret, recorder.Body.String())
		}
	}

	updated, ok := manager.GetByID("codex-auth")
	if !ok {
		t.Fatal("updated auth was not found")
	}
	if got := updated.Metadata["access_token"]; got != "access-secret" {
		t.Fatalf("access_token = %#v, want original token", got)
	}
	if got := updated.Metadata["refresh_token"]; got != "refresh-secret" {
		t.Fatalf("refresh_token = %#v, want original token", got)
	}
	if got := updated.Metadata["agent_runtime_id"]; got != "runtime-1" {
		t.Fatalf("agent_runtime_id = %#v, want runtime-1", got)
	}
	if got := updated.Metadata["task_id"]; got != "task-1" {
		t.Fatalf("task_id = %#v, want task-1", got)
	}
	if got := updated.Metadata["agent_identity_account_id"]; got != "account-1" {
		t.Fatalf("agent_identity_account_id = %#v, want account-1", got)
	}
	privateKey, _ := updated.Metadata["agent_private_key"].(string)
	if privateKey == "" {
		t.Fatal("agent_private_key was not persisted")
	}
	if strings.Contains(recorder.Body.String(), privateKey) {
		t.Fatal("provision response leaked agent_private_key")
	}

	agentCalls, taskCalls := registrar.calls()
	if agentCalls != 1 || taskCalls != 1 {
		t.Fatalf("registrar calls = agent:%d task:%d, want 1 each", agentCalls, taskCalls)
	}
	delete(updated.Metadata, "last_refresh")
	if _, err := manager.Update(context.Background(), updated); err != nil {
		t.Fatalf("remove last_refresh from legacy credential: %v", err)
	}

	reusedRecorder := performAgentIdentityProvision(t, h, `{"auth_index":"idx-codex"}`)
	if reusedRecorder.Code != http.StatusOK {
		t.Fatalf("reused provision status = %d, want %d body=%s", reusedRecorder.Code, http.StatusOK, reusedRecorder.Body.String())
	}
	var reusedPayload map[string]any
	if err := json.Unmarshal(reusedRecorder.Body.Bytes(), &reusedPayload); err != nil {
		t.Fatalf("decode reused response: %v", err)
	}
	if reusedPayload["reused"] != true {
		t.Fatalf("reused = %#v, want true", reusedPayload["reused"])
	}
	agentCalls, taskCalls = registrar.calls()
	if agentCalls != 1 || taskCalls != 1 {
		t.Fatalf("reused request called registrar: agent:%d task:%d", agentCalls, taskCalls)
	}
	reusedAuth, _ := manager.GetByID("codex-auth")
	if lastRefresh, _ := reusedAuth.Metadata["last_refresh"].(string); lastRefresh == "" {
		t.Fatal("reused legacy credential did not receive last_refresh")
	}

	exportRecorder := httptest.NewRecorder()
	exportContext, _ := gin.CreateTestContext(exportRecorder)
	exportContext.Request = httptest.NewRequest(http.MethodGet, "/v0/management/auth-files/agent-identity/export?auth_index=idx-codex", nil)
	h.ExportAgentIdentityAuth(exportContext)

	if exportRecorder.Code != http.StatusOK {
		t.Fatalf("export status = %d, want %d body=%s", exportRecorder.Code, http.StatusOK, exportRecorder.Body.String())
	}
	if got := exportRecorder.Header().Get("Cache-Control"); got != "no-store, private" {
		t.Fatalf("Cache-Control = %q, want no-store, private", got)
	}
	if got := exportRecorder.Header().Get("Content-Disposition"); got != `attachment; filename="auth.json"` {
		t.Fatalf("Content-Disposition = %q", got)
	}
	if got := exportRecorder.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := exportRecorder.Header().Get("Pragma"); got != "no-cache" {
		t.Fatalf("Pragma = %q, want no-cache", got)
	}
	if got := exportRecorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}

	var authFile codex.CodexAuthFile
	if err := json.Unmarshal(exportRecorder.Body.Bytes(), &authFile); err != nil {
		t.Fatalf("decode exported auth.json: %v", err)
	}
	if authFile.AuthMode != codex.CodexAuthModeChatGPT {
		t.Fatalf("auth_mode = %q, want %q", authFile.AuthMode, codex.CodexAuthModeChatGPT)
	}
	if authFile.Tokens.AccessToken != "access-secret" || authFile.Tokens.RefreshToken != "refresh-secret" {
		t.Fatalf("exported OAuth tokens were not preserved: %+v", authFile.Tokens)
	}
	if authFile.Tokens.IDToken != testAgentIdentityIDToken(t) {
		t.Fatal("exported id_token was not preserved byte-for-byte")
	}
	if authFile.AgentIdentity.AgentRuntimeID != "runtime-1" || authFile.AgentIdentity.TaskID != "task-1" {
		t.Fatalf("unexpected exported Agent Identity: %+v", authFile.AgentIdentity)
	}
	if authFile.AgentIdentity.AgentPrivateKey != privateKey {
		t.Fatal("exported Agent Identity private key does not match persisted key")
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(exportRecorder.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw exported auth.json: %v", err)
	}
	if _, exists := raw["type"]; exists {
		t.Fatal("exported auth.json contains internal type metadata")
	}
	if apiKey, exists := raw["OPENAI_API_KEY"]; !exists || string(apiKey) != "null" {
		t.Fatalf("OPENAI_API_KEY = %s, want explicit null", apiKey)
	}
}

func TestProvisionAgentIdentityTaskFailureCanResume(t *testing.T) {
	h, manager, registrar := newAgentIdentityTestHandler(t)
	registrar.setTaskError(errors.New("task service unavailable"))

	failed := performAgentIdentityProvision(t, h, `{"auth_index":"idx-codex"}`)
	if failed.Code != http.StatusBadGateway {
		t.Fatalf("failed task status = %d, want %d body=%s", failed.Code, http.StatusBadGateway, failed.Body.String())
	}
	if strings.Contains(failed.Body.String(), "task service unavailable") {
		t.Fatalf("response exposed upstream error detail: %s", failed.Body.String())
	}
	updated, ok := manager.GetByID("codex-auth")
	if !ok {
		t.Fatal("updated auth was not found")
	}
	if updated.Metadata["agent_runtime_id"] != "runtime-1" {
		t.Fatalf("runtime ID was not retained after task failure: %#v", updated.Metadata["agent_runtime_id"])
	}
	if updated.Metadata["agent_identity_state"] != string(codex.ManagedAgentIdentityStateNeedsTask) {
		t.Fatalf("state = %#v, want needs_task", updated.Metadata["agent_identity_state"])
	}
	if privateKey, _ := updated.Metadata["agent_private_key"].(string); privateKey == "" {
		t.Fatal("private key was not retained after task failure")
	}

	registrar.setTaskError(nil)
	resumed := performAgentIdentityProvision(t, h, `{"auth_index":"idx-codex"}`)
	if resumed.Code != http.StatusOK {
		t.Fatalf("resumed status = %d, want %d body=%s", resumed.Code, http.StatusOK, resumed.Body.String())
	}
	agentCalls, taskCalls := registrar.calls()
	if agentCalls != 1 || taskCalls != 2 {
		t.Fatalf("resume calls = agent:%d task:%d, want agent:1 task:2", agentCalls, taskCalls)
	}
}

func TestProvisionAgentIdentityBlankTaskIDCanResume(t *testing.T) {
	h, manager, registrar := newAgentIdentityTestHandler(t)
	registrar.setTaskID(" \t")

	recorder := performAgentIdentityProvision(t, h, `{"auth_index":"idx-codex"}`)
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d body=%s", recorder.Code, http.StatusBadGateway, recorder.Body.String())
	}
	updated, _ := manager.GetByID("codex-auth")
	if got := updated.Metadata["agent_identity_state"]; got != string(codex.ManagedAgentIdentityStateNeedsTask) {
		t.Fatalf("agent_identity_state = %#v, want needs_task", got)
	}
	if got := updated.Metadata["task_id"]; got != "" {
		t.Fatalf("task_id = %#v, want empty", got)
	}

	registrar.setTaskID(" task-2 ")
	resumed := performAgentIdentityProvision(t, h, `{"auth_index":"idx-codex"}`)
	if resumed.Code != http.StatusOK {
		t.Fatalf("resumed status = %d, want %d body=%s", resumed.Code, http.StatusOK, resumed.Body.String())
	}
	updated, _ = manager.GetByID("codex-auth")
	if got := updated.Metadata["task_id"]; got != " task-2 " {
		t.Fatalf("task_id = %#v, want original non-empty value", got)
	}
	agentCalls, taskCalls := registrar.calls()
	if agentCalls != 1 || taskCalls != 2 {
		t.Fatalf("resume calls = agent:%d task:%d, want agent:1 task:2", agentCalls, taskCalls)
	}
}

func TestProvisionAgentIdentityBlankRuntimeIDCanResume(t *testing.T) {
	h, manager, registrar := newAgentIdentityTestHandler(t)
	registrar.setAgentRuntimeID(" \t")

	failed := performAgentIdentityProvision(t, h, `{"auth_index":"idx-codex"}`)
	if failed.Code != http.StatusBadGateway {
		t.Fatalf("blank runtime status = %d, want %d body=%s", failed.Code, http.StatusBadGateway, failed.Body.String())
	}
	if strings.Contains(failed.Body.String(), "empty runtime ID") {
		t.Fatalf("response exposed runtime validation detail: %s", failed.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(failed.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode failure response: %v", err)
	}
	if payload["has_agent_identity"] != false || payload["has_task"] != false {
		t.Fatalf("unexpected failure response: %s", failed.Body.String())
	}

	pending, ok := manager.GetByID("codex-auth")
	if !ok {
		t.Fatal("pending auth was not found")
	}
	pendingKey, _ := pending.Metadata["agent_private_key"].(string)
	if pendingKey == "" {
		t.Fatal("blank runtime failure did not retain the generated key")
	}
	if got := pending.Metadata["agent_runtime_id"]; got != "" {
		t.Fatalf("agent_runtime_id = %#v, want empty", got)
	}
	if got := pending.Metadata["agent_identity_state"]; got != string(codex.ManagedAgentIdentityStateError) {
		t.Fatalf("agent_identity_state = %#v, want error", got)
	}

	registrar.setAgentRuntimeID("runtime-2")
	resumed := performAgentIdentityProvision(t, h, `{"auth_index":"idx-codex"}`)
	if resumed.Code != http.StatusOK {
		t.Fatalf("resumed status = %d, want %d body=%s", resumed.Code, http.StatusOK, resumed.Body.String())
	}
	keys := registrar.registrationKeys()
	if len(keys) != 2 || keys[0] != pendingKey || keys[1] != pendingKey {
		t.Fatal("registration retry did not reuse pending key")
	}
	agentCalls, taskCalls := registrar.calls()
	if agentCalls != 2 || taskCalls != 1 {
		t.Fatalf("resume calls = agent:%d task:%d, want agent:2 task:1", agentCalls, taskCalls)
	}
}

func TestForceProvisionPersistsKeyForRegistrationRetry(t *testing.T) {
	h, manager, registrar := newAgentIdentityTestHandler(t)
	initial := performAgentIdentityProvision(t, h, `{"auth_index":"idx-codex"}`)
	if initial.Code != http.StatusOK {
		t.Fatalf("initial provision status = %d, want %d body=%s", initial.Code, http.StatusOK, initial.Body.String())
	}
	initialAuth, _ := manager.GetByID("codex-auth")
	initialKey, _ := initialAuth.Metadata["agent_private_key"].(string)

	registrar.setAgentError(errors.New("registration timeout"))
	failed := performAgentIdentityProvision(t, h, `{"auth_index":"idx-codex","force":true}`)
	if failed.Code != http.StatusBadGateway {
		t.Fatalf("forced provision status = %d, want %d body=%s", failed.Code, http.StatusBadGateway, failed.Body.String())
	}
	pendingAuth, _ := manager.GetByID("codex-auth")
	pendingKey, _ := pendingAuth.Metadata["agent_private_key"].(string)
	if pendingKey == "" || pendingKey == initialKey {
		t.Fatal("force provision did not persist newly generated pending key")
	}
	if pendingAuth.Metadata["agent_runtime_id"] != "" {
		t.Fatalf("pending runtime ID = %#v, want empty", pendingAuth.Metadata["agent_runtime_id"])
	}

	registrar.setAgentError(nil)
	resumed := performAgentIdentityProvision(t, h, `{"auth_index":"idx-codex"}`)
	if resumed.Code != http.StatusOK {
		t.Fatalf("resumed provision status = %d, want %d body=%s", resumed.Code, http.StatusOK, resumed.Body.String())
	}
	keys := registrar.registrationKeys()
	if len(keys) != 3 {
		t.Fatalf("registration key count = %d, want 3", len(keys))
	}
	if keys[1] != pendingKey || keys[2] != pendingKey {
		t.Fatal("retry did not reuse the force provision pending key")
	}
}

func TestProvisionAgentIdentityRequiresCompleteOAuthBundle(t *testing.T) {
	h, manager, registrar := newAgentIdentityTestHandler(t)
	auth, ok := manager.GetByID("codex-auth")
	if !ok {
		t.Fatal("auth was not found")
	}
	auth.Metadata["refresh_token"] = ""
	if _, err := manager.Update(context.Background(), auth); err != nil {
		t.Fatalf("update auth: %v", err)
	}

	recorder := performAgentIdentityProvision(t, h, `{"auth_index":"idx-codex"}`)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d body=%s", recorder.Code, http.StatusUnprocessableEntity, recorder.Body.String())
	}
	agentCalls, taskCalls := registrar.calls()
	if agentCalls != 0 || taskCalls != 0 {
		t.Fatalf("invalid credential called registrar: agent:%d task:%d", agentCalls, taskCalls)
	}
}

func TestAgentIdentityCredentialErrorsDoNotExposeMetadataValues(t *testing.T) {
	h, manager, registrar := newAgentIdentityTestHandler(t)
	auth, ok := manager.GetByID("codex-auth")
	if !ok {
		t.Fatal("auth was not found")
	}
	auth.Metadata["access_token"] = "access-token-sentinel"
	auth.Metadata["agent_private_key"] = "private-key-sentinel"
	auth.Metadata["agent_identity_state"] = "private-key-sentinel"
	if _, err := manager.Update(context.Background(), auth); err != nil {
		t.Fatalf("update auth: %v", err)
	}

	provisionRecorder := performAgentIdentityProvision(t, h, `{"auth_index":"idx-codex"}`)
	if provisionRecorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("provision status = %d, want %d body=%s", provisionRecorder.Code, http.StatusUnprocessableEntity, provisionRecorder.Body.String())
	}

	exportRecorder := httptest.NewRecorder()
	exportContext, _ := gin.CreateTestContext(exportRecorder)
	exportContext.Request = httptest.NewRequest(http.MethodGet, "/v0/management/auth-files/agent-identity/export?auth_index=idx-codex", nil)
	h.ExportAgentIdentityAuth(exportContext)
	if exportRecorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("export status = %d, want %d body=%s", exportRecorder.Code, http.StatusUnprocessableEntity, exportRecorder.Body.String())
	}

	for operation, body := range map[string]string{
		"provision": provisionRecorder.Body.String(),
		"export":    exportRecorder.Body.String(),
	} {
		if !strings.Contains(body, `"error":"invalid Codex credential"`) {
			t.Fatalf("%s response = %s, want generic credential error", operation, body)
		}
		for _, secret := range []string{"access-token-sentinel", "private-key-sentinel", "refresh-secret"} {
			if strings.Contains(body, secret) {
				t.Fatalf("%s response leaked %q: %s", operation, secret, body)
			}
		}
	}

	agentCalls, taskCalls := registrar.calls()
	if agentCalls != 0 || taskCalls != 0 {
		t.Fatalf("invalid credential called registrar: agent:%d task:%d", agentCalls, taskCalls)
	}
}

func TestProvisionAgentIdentityCanSkipTaskRegistration(t *testing.T) {
	h, manager, registrar := newAgentIdentityTestHandler(t)

	recorder := performAgentIdentityProvision(t, h, `{"auth_index":"idx-codex","register_task":false}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload["status"] != string(codex.ManagedAgentIdentityStateNeedsTask) || payload["has_task"] != false {
		t.Fatalf("unexpected response: %s", recorder.Body.String())
	}
	agentCalls, taskCalls := registrar.calls()
	if agentCalls != 1 || taskCalls != 0 {
		t.Fatalf("registrar calls = agent:%d task:%d, want agent:1 task:0", agentCalls, taskCalls)
	}
	updated, _ := manager.GetByID("codex-auth")
	if updated.Metadata["agent_identity_state"] != string(codex.ManagedAgentIdentityStateNeedsTask) {
		t.Fatalf("state = %#v, want needs_task", updated.Metadata["agent_identity_state"])
	}

	exportRecorder := httptest.NewRecorder()
	exportContext, _ := gin.CreateTestContext(exportRecorder)
	exportContext.Request = httptest.NewRequest(http.MethodGet, "/v0/management/auth-files/agent-identity/export?auth_index=idx-codex", nil)
	h.ExportAgentIdentityAuth(exportContext)
	if exportRecorder.Code != http.StatusOK {
		t.Fatalf("export status = %d, want %d body=%s", exportRecorder.Code, http.StatusOK, exportRecorder.Body.String())
	}
	var document struct {
		AgentIdentity map[string]any `json:"agent_identity"`
	}
	if err := json.Unmarshal(exportRecorder.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode exported auth.json: %v", err)
	}
	if _, exists := document.AgentIdentity["task_id"]; exists {
		t.Fatal("exported needs_task auth.json contains task_id")
	}
}

func TestProvisionAgentIdentityBackfillsLegacyAccountBinding(t *testing.T) {
	h, manager, registrar := newAgentIdentityTestHandler(t)
	auth, _ := manager.GetByID("codex-auth")
	keyMaterial, errKey := codex.GenerateAgentKeyMaterial()
	if errKey != nil {
		t.Fatalf("GenerateAgentKeyMaterial() error = %v", errKey)
	}
	auth.Metadata["agent_runtime_id"] = "runtime-legacy"
	auth.Metadata["agent_private_key"] = keyMaterial.PrivateKeyPKCS8Base64
	auth.Metadata["chatgpt_user_id"] = "user-1"
	auth.Metadata["task_id"] = "task-legacy"
	auth.Metadata["agent_identity_state"] = string(codex.ManagedAgentIdentityStateReady)
	delete(auth.Metadata, "agent_identity_account_id")
	if _, errUpdate := manager.Update(context.Background(), auth); errUpdate != nil {
		t.Fatalf("Update() error = %v", errUpdate)
	}

	recorder := performAgentIdentityProvision(t, h, `{"auth_index":"idx-codex"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	updated, _ := manager.GetByID("codex-auth")
	if got := updated.Metadata["agent_identity_account_id"]; got != "account-1" {
		t.Fatalf("agent_identity_account_id = %#v, want account-1", got)
	}
	agentCalls, taskCalls := registrar.calls()
	if agentCalls != 0 || taskCalls != 0 {
		t.Fatalf("legacy binding backfill called registrar: agent:%d task:%d", agentCalls, taskCalls)
	}
}

func TestProvisionAgentIdentityPersistsFlatCredentialToFile(t *testing.T) {
	authDir := t.TempDir()
	filePath := filepath.Join(authDir, "codex-auth.json")
	store := sdkauth.NewFileTokenStore()
	store.SetBaseDir(authDir)
	manager := coreauth.NewManager(store, nil, nil)
	auth := &coreauth.Auth{
		ID:       "codex-auth",
		Index:    "idx-codex",
		FileName: "codex-auth.json",
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			coreauth.AttributePath:   filePath,
			coreauth.AttributeSource: filePath,
		},
		Metadata: map[string]any{
			"type":          "codex",
			"id_token":      testAgentIdentityIDToken(t),
			"access_token":  "access-secret",
			"refresh_token": "refresh-secret",
			"account_id":    "account-1",
			"last_refresh":  "2026-07-22T10:00:00Z",
		},
	}
	if _, err := manager.Register(coreauth.WithSkipPersist(context.Background()), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	registrar := &fakeAgentIdentityRegistrar{agentRuntimeID: "runtime-1", taskID: "task-1"}
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	h.agentIdentityRegistrar = func(*coreauth.Auth) agentIdentityRegistrar { return registrar }

	recorder := performAgentIdentityProvision(t, h, `{"auth_index":"idx-codex"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("read persisted credential: %v", err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(data, &metadata); err != nil {
		t.Fatalf("decode persisted credential: %v", err)
	}
	for key, want := range map[string]string{
		"access_token":              "access-secret",
		"refresh_token":             "refresh-secret",
		"agent_identity_account_id": "account-1",
		"agent_runtime_id":          "runtime-1",
		"task_id":                   "task-1",
	} {
		if got := metadata[key]; got != want {
			t.Fatalf("persisted %s = %#v, want %q", key, got, want)
		}
	}
	if privateKey, _ := metadata["agent_private_key"].(string); privateKey == "" {
		t.Fatal("persisted credential is missing agent_private_key")
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filePath)
		if err != nil {
			t.Fatalf("stat persisted credential: %v", err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("credential mode = %o, want 600", got)
		}
	}
}

func TestProvisionAgentIdentitySerializesSameAuthIndex(t *testing.T) {
	h, _, registrar := newAgentIdentityTestHandler(t)

	const requestCount = 12
	statuses := make(chan int, requestCount)
	var waitGroup sync.WaitGroup
	for range requestCount {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			recorder := performAgentIdentityProvision(t, h, `{"auth_index":"idx-codex"}`)
			statuses <- recorder.Code
		}()
	}
	waitGroup.Wait()
	close(statuses)
	for status := range statuses {
		if status != http.StatusOK {
			t.Errorf("concurrent provision status = %d, want %d", status, http.StatusOK)
		}
	}
	agentCalls, taskCalls := registrar.calls()
	if agentCalls != 1 || taskCalls != 1 {
		t.Fatalf("concurrent registrar calls = agent:%d task:%d, want 1 each", agentCalls, taskCalls)
	}
}

func TestProvisionAgentIdentityRequiresDurableStore(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	registerAuthForLookupTest(t, manager, newAgentIdentityTestAuth(t))
	registrar := &fakeAgentIdentityRegistrar{agentRuntimeID: "runtime-1", taskID: "task-1"}
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
	h.agentIdentityRegistrar = func(*coreauth.Auth) agentIdentityRegistrar { return registrar }

	recorder := performAgentIdentityProvision(t, h, `{"auth_index":"idx-codex"}`)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d body=%s", recorder.Code, http.StatusServiceUnavailable, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "durable auth store unavailable") {
		t.Fatalf("unexpected response body: %s", recorder.Body.String())
	}
	agentCalls, taskCalls := registrar.calls()
	if agentCalls != 0 || taskCalls != 0 {
		t.Fatalf("registrar calls = agent:%d task:%d, want zero", agentCalls, taskCalls)
	}
}

func TestProvisionAgentIdentityRejectsChangedAccountBindingWithoutForce(t *testing.T) {
	h, manager, registrar := newAgentIdentityTestHandler(t)
	initial := performAgentIdentityProvision(t, h, `{"auth_index":"idx-codex"}`)
	if initial.Code != http.StatusOK {
		t.Fatalf("initial status = %d, want %d body=%s", initial.Code, http.StatusOK, initial.Body.String())
	}

	auth, _ := manager.GetByID("codex-auth")
	auth.Metadata["account_id"] = "account-2"
	if _, errUpdate := manager.Update(context.Background(), auth); errUpdate != nil {
		t.Fatalf("Update() error = %v", errUpdate)
	}

	conflict := performAgentIdentityProvision(t, h, `{"auth_index":"idx-codex"}`)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("changed binding status = %d, want %d body=%s", conflict.Code, http.StatusConflict, conflict.Body.String())
	}
	if !strings.Contains(conflict.Body.String(), "different ChatGPT account") {
		t.Fatalf("unexpected binding conflict response: %s", conflict.Body.String())
	}
	agentCalls, taskCalls := registrar.calls()
	if agentCalls != 1 || taskCalls != 1 {
		t.Fatalf("binding conflict called registrar: agent:%d task:%d", agentCalls, taskCalls)
	}

	forced := performAgentIdentityProvision(t, h, `{"auth_index":"idx-codex","force":true}`)
	if forced.Code != http.StatusOK {
		t.Fatalf("forced status = %d, want %d body=%s", forced.Code, http.StatusOK, forced.Body.String())
	}
	rotated, _ := manager.GetByID("codex-auth")
	if got := rotated.Metadata["account_id"]; got != "account-2" {
		t.Fatalf("OAuth account_id = %#v, want account-2", got)
	}
	if got := rotated.Metadata["agent_identity_account_id"]; got != "account-2" {
		t.Fatalf("Agent Identity account binding = %#v, want account-2", got)
	}
}

func TestProvisionAgentIdentityPostPersistHookCanReenterManager(t *testing.T) {
	h, manager, _ := newAgentIdentityTestHandler(t)
	hookCalled := make(chan struct{})
	h.SetPostAuthPersistHook(func(ctx context.Context, auth *coreauth.Auth) error {
		_, errMerge := manager.MergeMetadataByIndex(ctx, auth.Index, map[string]any{"post_hook_marker": "set"})
		close(hookCalled)
		return errMerge
	})

	type provisionResponse struct {
		recorder *httptest.ResponseRecorder
	}
	done := make(chan provisionResponse, 1)
	go func() {
		done <- provisionResponse{recorder: performAgentIdentityProvision(t, h, `{"auth_index":"idx-codex"}`)}
	}()
	select {
	case <-hookCalled:
	case <-time.After(5 * time.Second):
		t.Fatal("post-auth persist hook deadlocked while re-entering manager")
	}
	select {
	case response := <-done:
		if response.recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d body=%s", response.recorder.Code, http.StatusOK, response.recorder.Body.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("provision request did not finish after post-auth persist hook")
	}
	updated, _ := manager.GetByID("codex-auth")
	if got := updated.Metadata["post_hook_marker"]; got != "set" {
		t.Fatalf("post_hook_marker = %#v, want set", got)
	}
}

func TestProvisionAgentIdentityReportsPostPersistHookFailureAfterSuccess(t *testing.T) {
	h, _, _ := newAgentIdentityTestHandler(t)
	h.SetPostAuthPersistHook(func(context.Context, *coreauth.Auth) error {
		return errors.New("hook unavailable")
	})

	recorder := performAgentIdentityProvision(t, h, `{"auth_index":"idx-codex"}`)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d body=%s", recorder.Code, http.StatusInternalServerError, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "hook unavailable") {
		t.Fatalf("response leaked post-persist hook detail: %s", recorder.Body.String())
	}
}

func TestProvisionAgentIdentityPreservesUpstreamErrorWhenPostPersistHookFails(t *testing.T) {
	h, _, registrar := newAgentIdentityTestHandler(t)
	registrar.setTaskError(errors.New("task unavailable"))
	h.SetPostAuthPersistHook(func(context.Context, *coreauth.Auth) error {
		return errors.New("hook unavailable")
	})

	recorder := performAgentIdentityProvision(t, h, `{"auth_index":"idx-codex"}`)
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want primary upstream status %d body=%s", recorder.Code, http.StatusBadGateway, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "task unavailable") || strings.Contains(recorder.Body.String(), "hook unavailable") {
		t.Fatalf("response leaked internal error detail: %s", recorder.Body.String())
	}
}

type failNthAgentIdentityStore struct {
	store  *memoryAuthStore
	mu     sync.Mutex
	calls  int
	failAt int
}

func (s *failNthAgentIdentityStore) List(ctx context.Context) ([]*coreauth.Auth, error) {
	return s.store.List(ctx)
}

func (s *failNthAgentIdentityStore) Save(ctx context.Context, auth *coreauth.Auth) (string, error) {
	s.mu.Lock()
	s.calls++
	call := s.calls
	s.mu.Unlock()
	if call == s.failAt {
		return "", errors.New("injected save failure")
	}
	return s.store.Save(ctx, auth)
}

func (s *failNthAgentIdentityStore) Delete(ctx context.Context, id string) error {
	return s.store.Delete(ctx, id)
}

func TestProvisionAgentIdentityDoesNotPublishFailedFinalMerge(t *testing.T) {
	// Registration persists the initial OAuth auth, followed by pending key,
	// registered runtime, and ready task snapshots.
	store := &failNthAgentIdentityStore{store: &memoryAuthStore{}, failAt: 4}
	manager := coreauth.NewManager(store, nil, nil)
	registerAuthForLookupTest(t, manager, newAgentIdentityTestAuth(t))
	registrar := &fakeAgentIdentityRegistrar{agentRuntimeID: "runtime-1", taskID: "task-1"}
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
	h.agentIdentityRegistrar = func(*coreauth.Auth) agentIdentityRegistrar { return registrar }

	recorder := performAgentIdentityProvision(t, h, `{"auth_index":"idx-codex"}`)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d body=%s", recorder.Code, http.StatusInternalServerError, recorder.Body.String())
	}
	updated, _ := manager.GetByID("codex-auth")
	if got := updated.Metadata["agent_runtime_id"]; got != "runtime-1" {
		t.Fatalf("agent_runtime_id = %#v, want durable runtime-1", got)
	}
	if got := updated.Metadata["agent_identity_state"]; got != string(codex.ManagedAgentIdentityStateNeedsTask) {
		t.Fatalf("agent_identity_state = %#v, want needs_task", got)
	}
	if got := updated.Metadata["task_id"]; got != "" {
		t.Fatalf("task_id = %#v after failed Save, want empty", got)
	}
}

type fakeAgentIdentityRegistrar struct {
	mu             sync.Mutex
	agentCalls     int
	taskCalls      int
	agentRuntimeID string
	taskID         string
	agentErr       error
	taskErr        error
	registeredKeys []string
}

func (registrar *fakeAgentIdentityRegistrar) RegisterAgent(_ context.Context, registration codex.AgentRegistration) (string, error) {
	registrar.mu.Lock()
	defer registrar.mu.Unlock()
	registrar.agentCalls++
	registrar.registeredKeys = append(registrar.registeredKeys, registration.KeyMaterial.PrivateKeyPKCS8Base64)
	if registration.AccessToken != "access-secret" {
		return "", errors.New("unexpected access token")
	}
	if registration.KeyMaterial.PrivateKeyPKCS8Base64 == "" || registration.KeyMaterial.PublicKeySSH == "" {
		return "", errors.New("missing key material")
	}
	return registrar.agentRuntimeID, registrar.agentErr
}

func (registrar *fakeAgentIdentityRegistrar) RegisterTask(_ context.Context, key codex.AgentIdentityKey) (string, error) {
	registrar.mu.Lock()
	defer registrar.mu.Unlock()
	registrar.taskCalls++
	if key.AgentRuntimeID != registrar.agentRuntimeID || key.PrivateKeyPKCS8Base64 == "" {
		return "", errors.New("unexpected Agent Identity key")
	}
	return registrar.taskID, registrar.taskErr
}

func (registrar *fakeAgentIdentityRegistrar) calls() (int, int) {
	registrar.mu.Lock()
	defer registrar.mu.Unlock()
	return registrar.agentCalls, registrar.taskCalls
}

func (registrar *fakeAgentIdentityRegistrar) setTaskError(err error) {
	registrar.mu.Lock()
	registrar.taskErr = err
	registrar.mu.Unlock()
}

func (registrar *fakeAgentIdentityRegistrar) setTaskID(taskID string) {
	registrar.mu.Lock()
	registrar.taskID = taskID
	registrar.mu.Unlock()
}

func (registrar *fakeAgentIdentityRegistrar) setAgentError(err error) {
	registrar.mu.Lock()
	registrar.agentErr = err
	registrar.mu.Unlock()
}

func (registrar *fakeAgentIdentityRegistrar) setAgentRuntimeID(runtimeID string) {
	registrar.mu.Lock()
	registrar.agentRuntimeID = runtimeID
	registrar.mu.Unlock()
}

func (registrar *fakeAgentIdentityRegistrar) registrationKeys() []string {
	registrar.mu.Lock()
	defer registrar.mu.Unlock()
	return append([]string(nil), registrar.registeredKeys...)
}

func newAgentIdentityTestHandler(t *testing.T) (*Handler, *coreauth.Manager, *fakeAgentIdentityRegistrar) {
	t.Helper()
	store := &memoryAuthStore{}
	manager := coreauth.NewManager(store, nil, nil)
	registerAuthForLookupTest(t, manager, newAgentIdentityTestAuth(t))
	registrar := &fakeAgentIdentityRegistrar{agentRuntimeID: "runtime-1", taskID: "task-1"}
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
	h.agentIdentityRegistrar = func(*coreauth.Auth) agentIdentityRegistrar { return registrar }
	return h, manager, registrar
}

func newAgentIdentityTestAuth(t *testing.T) *coreauth.Auth {
	t.Helper()
	return &coreauth.Auth{
		ID:       "codex-auth",
		Index:    "idx-codex",
		FileName: "codex-auth.json",
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{
			"type":          "codex",
			"id_token":      testAgentIdentityIDToken(t),
			"access_token":  "access-secret",
			"refresh_token": "refresh-secret",
			"account_id":    "account-1",
			"last_refresh":  "2026-07-22T10:00:00Z",
			"unrelated":     "preserve-me",
		},
	}
}

func performAgentIdentityProvision(t *testing.T, h *Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	request := httptest.NewRequest(http.MethodPost, "/v0/management/auth-files/agent-identity/provision", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	ctx.Request = request
	h.ProvisionAgentIdentity(ctx)
	return recorder
}

func testAgentIdentityIDToken(t *testing.T) string {
	t.Helper()
	header, err := json.Marshal(map[string]any{"alg": "none", "typ": "JWT"})
	if err != nil {
		t.Fatalf("marshal JWT header: %v", err)
	}
	payload, err := json.Marshal(map[string]any{
		"email": "owner@example.com",
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id":         "account-1",
			"chatgpt_user_id":            "user-1",
			"chatgpt_plan_type":          "pro",
			"chatgpt_account_is_fedramp": false,
		},
	})
	if err != nil {
		t.Fatalf("marshal JWT payload: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}
