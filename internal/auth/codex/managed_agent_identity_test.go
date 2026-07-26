package codex

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestManagedAgentIdentityCredentialsCodexAuthFile(t *testing.T) {
	keyMaterial := deterministicAgentKeyMaterial(t, 0x42)
	credentials := ManagedAgentIdentityCredentials{
		IDToken:                 "id-token-with-original-bytes",
		AccessToken:             "access-token-with-original-bytes",
		RefreshToken:            "refresh-token-with-original-bytes",
		AccountID:               "account-123",
		LastRefresh:             "2026-07-22T20:34:56+08:00",
		AgentRuntimeID:          "runtime-123",
		AgentPrivateKey:         keyMaterial.PrivateKeyPKCS8Base64,
		ChatGPTUserID:           "user-123",
		AgentAccountID:          "account-123",
		AgentChatGPTUserID:      "user-123",
		Email:                   "owner@example.com",
		PlanType:                "pro",
		ChatGPTAccountIsFedRAMP: true,
		TaskID:                  "  task-123\n",
		State:                   ManagedAgentIdentityStateReady,
	}

	authFile, err := credentials.CodexAuthFile()
	if err != nil {
		t.Fatalf("CodexAuthFile() error = %v", err)
	}
	if authFile.AuthMode != CodexAuthModeChatGPT {
		t.Fatalf("AuthMode = %q, want %q", authFile.AuthMode, CodexAuthModeChatGPT)
	}
	if authFile.OpenAIAPIKey != nil {
		t.Fatal("OpenAIAPIKey must be nil for managed ChatGPT auth")
	}
	if authFile.LastRefresh != "2026-07-22T12:34:56Z" {
		t.Fatalf("LastRefresh = %q, want UTC timestamp", authFile.LastRefresh)
	}
	if authFile.Tokens.IDToken != credentials.IDToken ||
		authFile.Tokens.AccessToken != credentials.AccessToken ||
		authFile.Tokens.RefreshToken != credentials.RefreshToken {
		t.Fatal("OAuth token bytes changed during export")
	}
	if authFile.AgentIdentity.TaskID != credentials.TaskID {
		t.Fatalf("TaskID = %q, want %q", authFile.AgentIdentity.TaskID, credentials.TaskID)
	}

	data, err := credentials.MarshalCodexAuthFile()
	if err != nil {
		t.Fatalf("MarshalCodexAuthFile() error = %v", err)
	}
	if !bytes.HasSuffix(data, []byte("\n")) {
		t.Fatal("auth.json must end with a newline")
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatalf("unmarshal exported auth.json: %v", err)
	}
	wantRootKeys := []string{"OPENAI_API_KEY", "agent_identity", "auth_mode", "last_refresh", "tokens"}
	if got := sortedMapKeys(document); !reflect.DeepEqual(got, wantRootKeys) {
		t.Fatalf("root keys = %v, want %v", got, wantRootKeys)
	}
	if document["OPENAI_API_KEY"] != nil {
		t.Fatalf("OPENAI_API_KEY = %#v, want null", document["OPENAI_API_KEY"])
	}
	tokens, ok := document["tokens"].(map[string]any)
	if !ok {
		t.Fatalf("tokens has type %T", document["tokens"])
	}
	wantTokenKeys := []string{"access_token", "account_id", "id_token", "refresh_token"}
	if got := sortedMapKeys(tokens); !reflect.DeepEqual(got, wantTokenKeys) {
		t.Fatalf("token keys = %v, want %v", got, wantTokenKeys)
	}
	agentIdentity, ok := document["agent_identity"].(map[string]any)
	if !ok {
		t.Fatalf("agent_identity has type %T", document["agent_identity"])
	}
	if _, exists := agentIdentity["agent_identity_state"]; exists {
		t.Fatal("internal Agent Identity state leaked into auth.json")
	}
	wantAgentKeys := []string{
		"account_id", "agent_private_key", "agent_runtime_id", "chatgpt_account_is_fedramp",
		"chatgpt_user_id", "email", "plan_type", "task_id",
	}
	if got := sortedMapKeys(agentIdentity); !reflect.DeepEqual(got, wantAgentKeys) {
		t.Fatalf("Agent Identity keys = %v, want %v", got, wantAgentKeys)
	}
}

func TestManagedAgentIdentityCredentialsCodexAuthFileOmitsMissingTask(t *testing.T) {
	credentials := validManagedAgentIdentityCredentials(t)
	credentials.TaskID = ""
	credentials.State = ManagedAgentIdentityStateNeedsTask

	data, err := credentials.MarshalCodexAuthFile()
	if err != nil {
		t.Fatalf("MarshalCodexAuthFile() error = %v", err)
	}
	var document struct {
		AgentIdentity map[string]any `json:"agent_identity"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatalf("unmarshal exported auth.json: %v", err)
	}
	if _, exists := document.AgentIdentity["task_id"]; exists {
		t.Fatal("empty task_id must be omitted")
	}
}

func TestManagedAgentIdentityCredentialsCodexAuthFileRejectsIncompleteData(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ManagedAgentIdentityCredentials)
		want   string
	}{
		{name: "missing token", mutate: func(value *ManagedAgentIdentityCredentials) { value.RefreshToken = "" }, want: "refresh_token"},
		{name: "invalid private key", mutate: func(value *ManagedAgentIdentityCredentials) { value.AgentPrivateKey = "not-base64" }, want: "agent_private_key"},
		{name: "invalid timestamp", mutate: func(value *ManagedAgentIdentityCredentials) { value.LastRefresh = "yesterday" }, want: "last_refresh"},
		{name: "ready without task", mutate: func(value *ManagedAgentIdentityCredentials) { value.TaskID = "" }, want: "missing task_id"},
		{name: "ready with whitespace task", mutate: func(value *ManagedAgentIdentityCredentials) { value.TaskID = " \t\n" }, want: "missing task_id"},
		{name: "account binding mismatch", mutate: func(value *ManagedAgentIdentityCredentials) { value.AgentAccountID = "other-account" }, want: "account binding"},
		{name: "user binding mismatch", mutate: func(value *ManagedAgentIdentityCredentials) { value.AgentChatGPTUserID = "other-user" }, want: "user binding"},
		{name: "provisioning", mutate: func(value *ManagedAgentIdentityCredentials) { value.State = ManagedAgentIdentityStateProvisioning }, want: "not exportable"},
		{name: "unknown state", mutate: func(value *ManagedAgentIdentityCredentials) { value.State = "mystery" }, want: "unknown state"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			credentials := validManagedAgentIdentityCredentials(t)
			test.mutate(&credentials)
			_, err := credentials.CodexAuthFile()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("CodexAuthFile() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestManagedAgentIdentityCredentialsFromMetadata(t *testing.T) {
	idToken := testJWT(t, map[string]any{
		"email": "jwt-owner@example.com",
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id":         "account-123",
			"chatgpt_user_id":            "user-123",
			"chatgpt_plan_type":          "business",
			"chatgpt_account_is_fedramp": true,
		},
	})
	metadata := map[string]any{
		"id_token":                   idToken,
		"access_token":               "access-token",
		"refresh_token":              "refresh-token",
		"account_id":                 "account-123",
		"last_refresh":               "2026-07-22T20:34:56+08:00",
		"agent_runtime_id":           "runtime-123",
		"agent_private_key":          "private-key-placeholder",
		"chatgpt_user_id":            "user-123",
		"email":                      "stale@example.com",
		"plan_type":                  "stale-plan",
		"chatgpt_account_is_fedramp": false,
		"task_id":                    "task-123",
		"agent_identity_state":       "ready",
	}

	credentials, err := ManagedAgentIdentityCredentialsFromMetadata(metadata)
	if err != nil {
		t.Fatalf("ManagedAgentIdentityCredentialsFromMetadata() error = %v", err)
	}
	if credentials.IDToken != idToken || credentials.AccessToken != "access-token" || credentials.RefreshToken != "refresh-token" {
		t.Fatal("normalizer did not preserve OAuth token bytes")
	}
	if credentials.AccountID != "account-123" || credentials.ChatGPTUserID != "user-123" {
		t.Fatalf("normalized binding = %q/%q", credentials.AccountID, credentials.ChatGPTUserID)
	}
	if credentials.AgentAccountID != "account-123" || credentials.AgentChatGPTUserID != "user-123" {
		t.Fatalf("Agent Identity binding = %q/%q", credentials.AgentAccountID, credentials.AgentChatGPTUserID)
	}
	if credentials.Email != "jwt-owner@example.com" || credentials.PlanType != "business" {
		t.Fatalf("normalized profile = %q/%q", credentials.Email, credentials.PlanType)
	}
	if !credentials.ChatGPTAccountIsFedRAMP {
		t.Fatal("FedRAMP claim was not normalized")
	}
	if credentials.LastRefresh != "2026-07-22T12:34:56Z" {
		t.Fatalf("LastRefresh = %q", credentials.LastRefresh)
	}
	if credentials.AgentRuntimeID != "runtime-123" || credentials.AgentPrivateKey != "private-key-placeholder" || credentials.TaskID != "task-123" {
		t.Fatal("existing flat Agent Identity fields were not preserved")
	}
	if credentials.State != ManagedAgentIdentityStateReady {
		t.Fatalf("State = %q", credentials.State)
	}
}

func TestManagedAgentIdentityCredentialsFromMetadataUsesClaimFallbacks(t *testing.T) {
	idToken := testJWT(t, map[string]any{
		"https://api.openai.com/profile": map[string]any{"email": "profile@example.com"},
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "account-123",
			"user_id":            "fallback-user",
		},
	})
	credentials, err := ManagedAgentIdentityCredentialsFromMetadata(map[string]any{
		"id_token":      idToken,
		"access_token":  "access-token",
		"refresh_token": "refresh-token",
	})
	if err != nil {
		t.Fatalf("ManagedAgentIdentityCredentialsFromMetadata() error = %v", err)
	}
	if credentials.Email != "profile@example.com" || credentials.ChatGPTUserID != "fallback-user" {
		t.Fatalf("fallback claims = email %q, user %q", credentials.Email, credentials.ChatGPTUserID)
	}
	if credentials.LastRefresh != "" {
		t.Fatalf("missing LastRefresh = %q, want empty", credentials.LastRefresh)
	}
}

func TestManagedAgentIdentityCredentialsFromMetadataUsesSelectedAccount(t *testing.T) {
	idToken := testJWT(t, map[string]any{
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "jwt-account",
			"chatgpt_user_id":    "jwt-user",
		},
	})
	credentials, err := ManagedAgentIdentityCredentialsFromMetadata(map[string]any{
		"id_token":      idToken,
		"access_token":  "access-token",
		"refresh_token": "refresh-token",
		"account_id":    "selected-workspace",
	})
	if err != nil {
		t.Fatalf("ManagedAgentIdentityCredentialsFromMetadata() error = %v", err)
	}
	if credentials.AccountID != "selected-workspace" {
		t.Fatalf("AccountID = %q, want selected-workspace", credentials.AccountID)
	}
	if credentials.AgentAccountID != "selected-workspace" {
		t.Fatalf("AgentAccountID = %q, want selected-workspace", credentials.AgentAccountID)
	}
}

func TestManagedAgentIdentityCredentialsFromMetadataPreservesRegisteredBinding(t *testing.T) {
	idToken := testJWT(t, map[string]any{
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "default-account",
			"chatgpt_user_id":    "current-user",
		},
	})
	credentials, err := ManagedAgentIdentityCredentialsFromMetadata(map[string]any{
		"id_token":                  idToken,
		"access_token":              "access-token",
		"refresh_token":             "refresh-token",
		"account_id":                "selected-account",
		"agent_runtime_id":          "runtime-123",
		"agent_private_key":         "private-key-placeholder",
		"agent_identity_account_id": "registered-account",
		"chatgpt_user_id":           "registered-user",
	})
	if err != nil {
		t.Fatalf("ManagedAgentIdentityCredentialsFromMetadata() error = %v", err)
	}
	if credentials.AccountID != "selected-account" || credentials.ChatGPTUserID != "current-user" {
		t.Fatalf("current binding = %q/%q", credentials.AccountID, credentials.ChatGPTUserID)
	}
	if credentials.AgentAccountID != "registered-account" || credentials.AgentChatGPTUserID != "registered-user" {
		t.Fatalf("registered binding = %q/%q", credentials.AgentAccountID, credentials.AgentChatGPTUserID)
	}
}

func TestManagedAgentIdentityCredentialsFromMetadataInitializesBindingWithoutMaterial(t *testing.T) {
	idToken := testJWT(t, map[string]any{
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "current-account",
			"chatgpt_user_id":    "current-user",
		},
	})
	credentials, err := ManagedAgentIdentityCredentialsFromMetadata(map[string]any{
		"id_token":                  idToken,
		"access_token":              "access-token",
		"refresh_token":             "refresh-token",
		"agent_identity_account_id": "stale-account",
		"chatgpt_user_id":           "stale-user",
	})
	if err != nil {
		t.Fatalf("ManagedAgentIdentityCredentialsFromMetadata() error = %v", err)
	}
	if credentials.AgentAccountID != "current-account" || credentials.AgentChatGPTUserID != "current-user" {
		t.Fatalf("initialized binding = %q/%q", credentials.AgentAccountID, credentials.AgentChatGPTUserID)
	}
}

func TestManagedAgentIdentityCredentialsFromMetadataPreservesUserBindingConflict(t *testing.T) {
	idToken := testJWT(t, map[string]any{
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "jwt-account",
			"chatgpt_user_id":    "jwt-user",
		},
	})
	credentials, err := ManagedAgentIdentityCredentialsFromMetadata(map[string]any{
		"id_token":          idToken,
		"access_token":      "access-token",
		"refresh_token":     "refresh-token",
		"agent_runtime_id":  "runtime-123",
		"agent_private_key": "private-key-placeholder",
		"chatgpt_user_id":   "other-user",
	})
	if err != nil {
		t.Fatalf("ManagedAgentIdentityCredentialsFromMetadata() error = %v", err)
	}
	if credentials.ChatGPTUserID != "jwt-user" || credentials.AgentChatGPTUserID != "other-user" {
		t.Fatalf("current/agent users = %q/%q", credentials.ChatGPTUserID, credentials.AgentChatGPTUserID)
	}
}

func TestManagedAgentIdentityCredentialsFromMetadataNormalizesPlanType(t *testing.T) {
	tests := []struct {
		name string
		plan string
		want string
	}{
		{name: "missing", want: "unknown"},
		{name: "health care", plan: "hc", want: "enterprise"},
		{name: "education", plan: "education", want: "edu"},
		{name: "known", plan: "pro", want: "pro"},
		{name: "unknown", plan: "future-plan", want: "unknown"},
		{name: "case-insensitive alias", plan: "HC", want: "enterprise"},
		{name: "case-insensitive known", plan: "Pro", want: "pro"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			idToken := testJWT(t, map[string]any{
				"https://api.openai.com/auth": map[string]any{
					"chatgpt_account_id": "account-123",
					"chatgpt_user_id":    "user-123",
					"chatgpt_plan_type":  test.plan,
				},
			})
			credentials, err := ManagedAgentIdentityCredentialsFromMetadata(map[string]any{
				"id_token":      idToken,
				"access_token":  "access-token",
				"refresh_token": "refresh-token",
			})
			if err != nil {
				t.Fatalf("ManagedAgentIdentityCredentialsFromMetadata() error = %v", err)
			}
			if credentials.PlanType != test.want {
				t.Fatalf("PlanType = %q, want %q", credentials.PlanType, test.want)
			}
		})
	}
}

func TestManagedAgentIdentityCredentialsFromMetadataRequiresCompleteTokenBundle(t *testing.T) {
	idToken := testJWT(t, map[string]any{
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "account-123",
			"chatgpt_user_id":    "user-123",
		},
	})
	tests := []string{"id_token", "access_token", "refresh_token"}
	for _, missing := range tests {
		t.Run(missing, func(t *testing.T) {
			metadata := map[string]any{
				"id_token":      idToken,
				"access_token":  "access-token",
				"refresh_token": "refresh-token",
			}
			delete(metadata, missing)
			_, err := ManagedAgentIdentityCredentialsFromMetadata(metadata)
			if err == nil || !strings.Contains(err.Error(), missing) {
				t.Fatalf("error = %v, want missing %s", err, missing)
			}
		})
	}
}

func TestManagedAgentIdentityCredentialsFromMetadataRejectsUnknownState(t *testing.T) {
	idToken := testJWT(t, map[string]any{
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "account-123",
			"chatgpt_user_id":    "user-123",
		},
	})
	_, err := ManagedAgentIdentityCredentialsFromMetadata(map[string]any{
		"id_token":             idToken,
		"access_token":         "access-token",
		"refresh_token":        "refresh-token",
		"agent_identity_state": "mystery",
	})
	if err == nil || !strings.Contains(err.Error(), "unknown state") {
		t.Fatalf("error = %v, want unknown state", err)
	}
}

func validManagedAgentIdentityCredentials(t *testing.T) ManagedAgentIdentityCredentials {
	t.Helper()
	return ManagedAgentIdentityCredentials{
		IDToken:            "id-token",
		AccessToken:        "access-token",
		RefreshToken:       "refresh-token",
		AccountID:          "account-123",
		LastRefresh:        "2026-07-22T12:34:56Z",
		AgentRuntimeID:     "runtime-123",
		AgentPrivateKey:    deterministicAgentKeyMaterial(t, 0x24).PrivateKeyPKCS8Base64,
		ChatGPTUserID:      "user-123",
		AgentAccountID:     "account-123",
		AgentChatGPTUserID: "user-123",
		Email:              "owner@example.com",
		PlanType:           "pro",
		TaskID:             "task-123",
		State:              ManagedAgentIdentityStateReady,
	}
}

func deterministicAgentKeyMaterial(t *testing.T, value byte) AgentKeyMaterial {
	t.Helper()
	material, err := generateAgentKeyMaterial(bytes.NewReader(bytes.Repeat([]byte{value}, agentIdentityKeySeedBytes)))
	if err != nil {
		t.Fatalf("generateAgentKeyMaterial() error = %v", err)
	}
	return material
}

func testJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	header, err := json.Marshal(map[string]string{"alg": "none", "typ": "JWT"})
	if err != nil {
		t.Fatalf("marshal JWT header: %v", err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal JWT payload: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(header) + "." +
		base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}

func sortedMapKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func TestNormalizeMetadataTimestampAcceptsTime(t *testing.T) {
	got, err := normalizeMetadataTimestamp(map[string]any{
		"last_refresh": time.Date(2026, 7, 22, 20, 34, 56, 0, time.FixedZone("HKT", 8*60*60)),
	}, "last_refresh")
	if err != nil {
		t.Fatalf("normalizeMetadataTimestamp() error = %v", err)
	}
	if got != "2026-07-22T12:34:56Z" {
		t.Fatalf("timestamp = %q", got)
	}
}

func TestNormalizeMetadataTimestampPreservesFractionalSeconds(t *testing.T) {
	got, err := normalizeMetadataTimestamp(map[string]any{
		"last_refresh": "2026-07-22T20:34:56.123456+08:00",
	}, "last_refresh")
	if err != nil {
		t.Fatalf("normalizeMetadataTimestamp() error = %v", err)
	}
	if got != "2026-07-22T12:34:56.123456Z" {
		t.Fatalf("timestamp = %q", got)
	}
}
