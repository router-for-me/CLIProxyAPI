package management

import (
	"bytes"
	"encoding/json"
	"html"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

type pluginQuotaMarshalProbe struct {
	called *bool
}

func (probe pluginQuotaMarshalProbe) MarshalJSON() ([]byte, error) {
	*probe.called = true
	return []byte(`{"expanded":"payload"}`), nil
}

func pluginQuotaAuth(metadata map[string]any) *coreauth.Auth {
	return &coreauth.Auth{
		ID:          "plugin-auth-1",
		Provider:    "codex",
		Attributes:  map[string]string{"runtime_only": "true"},
		Metadata:    metadata,
		Unavailable: false,
		Quota: coreauth.QuotaState{
			ObservedAt: time.Date(2026, time.August, 28, 9, 0, 0, 0, time.UTC),
			Signals:    map[string]string{"X-Codex-Plan-Type": "pro"},
		},
	}
}

func TestPluginQuotaPreservesFutureFieldsAndSanitizesStrings(t *testing.T) {
	raw := map[string]any{
		"schema":  "plugin.example.quota.future",
		"version": 99,
		"future": map[string]any{
			"<key>":   "<script>alert('quota')</script>",
			"enabled": true,
			"ratio":   -4.5,
			"items":   []any{"safe & sound", map[string]any{"label": "<b>daily</b>"}},
		},
	}

	quota, ok := pluginQuotaMetadata(raw)
	if !ok {
		t.Fatal("future plugin quota object was rejected")
	}
	if quota["schema"] != "plugin.example.quota.future" || quota["version"] != json.Number("99") {
		t.Fatalf("schema fields changed: %#v", quota)
	}
	future := quota["future"].(map[string]any)
	if future["<key>"] != html.EscapeString("<script>alert('quota')</script>") {
		t.Fatalf("nested string was not escaped or key was changed: %#v", future)
	}
	items := future["items"].([]any)
	if items[0] != html.EscapeString("safe & sound") || items[1].(map[string]any)["label"] != html.EscapeString("<b>daily</b>") {
		t.Fatalf("nested collection strings were not escaped: %#v", items)
	}
	if future["enabled"] != true || future["ratio"] != json.Number("-4.5") {
		t.Fatalf("non-string future values changed: %#v", future)
	}
}

func TestPluginQuotaOutputIsDetached(t *testing.T) {
	nested := map[string]any{"label": "before"}
	items := []any{"first"}
	raw := map[string]any{"nested": nested, "items": items}

	quota, ok := pluginQuotaMetadata(raw)
	if !ok {
		t.Fatal("plugin quota object was rejected")
	}
	nested["label"] = "after"
	items[0] = "changed"
	raw["new"] = "added"

	if quota["nested"].(map[string]any)["label"] != "before" || quota["items"].([]any)[0] != "first" {
		t.Fatalf("output aliases plugin-owned data: %#v", quota)
	}
	if _, exists := quota["new"]; exists {
		t.Fatalf("output observed later source mutation: %#v", quota)
	}
}

func TestPluginQuotaRejectsInvalidShapesAndOversize(t *testing.T) {
	tests := map[string]any{
		"empty object": map[string]any{},
		"array":        []any{map[string]any{"value": 1}},
		"string":       "quota",
		"null":         nil,
		"non-JSON":     make(chan int),
		"oversize":     map[string]any{"padding": strings.Repeat("x", 16<<10)},
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			if quota, ok := pluginQuotaMetadata(raw); ok || quota != nil {
				t.Fatalf("invalid plugin quota accepted: %#v", quota)
			}
		})
	}
}

func TestPluginQuotaPreflightRejectsLargeAndUnsafeTrees(t *testing.T) {
	deep := map[string]any{}
	cursor := deep
	for range 20 {
		next := map[string]any{}
		cursor["next"] = next
		cursor = next
	}
	cursor["value"] = true

	cycle := map[string]any{}
	cycle["self"] = cycle
	called := false
	tests := map[string]any{
		"large string":     map[string]any{"value": strings.Repeat("x", 9<<10)},
		"large key":        map[string]any{strings.Repeat("k", 9<<10): true},
		"large container":  map[string]any{"items": make([]any, 2049)},
		"excessive depth":  deep,
		"cycle":            cycle,
		"byte blob":        map[string]any{"blob": []byte("quota")},
		"struct":           map[string]any{"value": struct{ Value string }{Value: "quota"}},
		"custom marshaler": map[string]any{"value": pluginQuotaMarshalProbe{called: &called}},
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			if quota, ok := pluginQuotaMetadata(raw); ok || quota != nil {
				t.Fatalf("unsafe plugin quota accepted: %#v", quota)
			}
		})
	}
	if called {
		t.Fatal("plugin quota preflight executed a custom marshaler")
	}
}

func TestPluginQuotaSiblingCredentialsStayPrivate(t *testing.T) {
	entry := (&Handler{}).buildAuthFileEntry(pluginQuotaAuth(map[string]any{
		"access_token": "sibling-secret",
		"plugin_quota": map[string]any{"remaining": 42},
	}))
	quota, ok := entry["plugin_quota"].(map[string]any)
	if !ok || quota["remaining"] != json.Number("42") {
		t.Fatalf("public quota missing: %#v", entry)
	}
	if _, exists := entry["metadata"]; exists {
		t.Fatalf("generic metadata exposed: %#v", entry)
	}
}

func TestPluginQuotaRejectsCredentialShapedKeysAtAnyDepth(t *testing.T) {
	keys := []string{
		"token", "Access-Token", "refresh.token", "ID TOKEN", "oauth_token", "bearer_token",
		"session_token", "portal-token", "client_secret", "secret", "secret-key", "Password",
		"passwd", "credential", "credentials", "creds", "Authorization", "Cookie",
		"session-cookie", "api-key", "APIKey", "access_key", "private.key",
	}
	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			raw := map[string]any{"safe": []any{map[string]any{key: "do-not-expose"}}}
			if quota, ok := pluginQuotaMetadata(raw); ok || quota != nil {
				t.Fatalf("credential-shaped key %q accepted: %#v", key, quota)
			}
		})
	}
}

func TestPluginQuotaRejectsNonASCIIKeys(t *testing.T) {
	keys := []string{
		"ａｃｃｅｓｓ＿ｔｏｋｅｎ",
		"ſecret",
		"acceѕѕ_token",
		"access\u200btoken",
		"quota_über",
	}
	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			if quota, ok := pluginQuotaMetadata(map[string]any{key: "value"}); ok || quota != nil {
				t.Fatalf("non-ASCII key %q accepted: %#v", key, quota)
			}
		})
	}
}

func TestPluginQuotaCredentialPolicyKeepsFalsePositiveControls(t *testing.T) {
	raw := map[string]any{"secretary_count": 2, "tokenization_rate": 3, "token_budget": 4}
	quota, ok := pluginQuotaMetadata(raw)
	if !ok {
		t.Fatal("false-positive controls were rejected")
	}
	for key, want := range map[string]json.Number{
		"secretary_count": "2", "tokenization_rate": "3", "token_budget": "4",
	} {
		if quota[key] != want {
			t.Fatalf("%s = %#v, want %s", key, quota[key], want)
		}
	}
}

func TestPluginQuotaAllowsTokenCountKeys(t *testing.T) {
	raw := map[string]any{
		"tokens":        11,
		"latest_tokens": 12,
		"period-tokens": 13,
		"daily":         []any{map[string]any{"tokens": 14}},
	}
	quota, ok := pluginQuotaMetadata(raw)
	if !ok {
		t.Fatal("quota token counts were rejected")
	}
	if quota["tokens"] != json.Number("11") || quota["latest_tokens"] != json.Number("12") || quota["period-tokens"] != json.Number("13") {
		t.Fatalf("quota token counts changed: %#v", quota)
	}
	if quota["daily"].([]any)[0].(map[string]any)["tokens"] != json.Number("14") {
		t.Fatalf("daily token count changed: %#v", quota)
	}
}

func TestPluginQuotaPreservesExactLargeJSONNumber(t *testing.T) {
	const exact = "9007199254740993"
	quota, ok := pluginQuotaMetadata(map[string]any{
		"future_json_number": json.Number(exact),
		"future_int64":       int64(9_007_199_254_740_993),
	})
	if !ok {
		t.Fatal("exact future integer was rejected")
	}
	if quota["future_json_number"] != json.Number(exact) || quota["future_int64"] != json.Number(exact) {
		t.Fatalf("exact integers changed: %#v", quota)
	}
}

func TestRejectedPluginQuotaDoesNotChangeHostStateAndLogsIDOnly(t *testing.T) {
	originalLevel := log.GetLevel()
	originalOutput := log.StandardLogger().Out
	defer log.SetLevel(originalLevel)
	defer log.SetOutput(originalOutput)

	var output bytes.Buffer
	log.SetLevel(log.DebugLevel)
	log.SetOutput(&output)

	auth := pluginQuotaAuth(map[string]any{
		"plugin_quota": map[string]any{"nested": map[string]any{"refresh_token": "payload-secret"}},
	})
	entry := (&Handler{}).buildAuthFileEntry(auth)
	if _, exists := entry["plugin_quota"]; exists {
		t.Fatalf("rejected plugin quota exposed: %#v", entry)
	}
	if entry["disabled"] != false || entry["unavailable"] != false {
		t.Fatalf("host availability changed: %#v", entry)
	}
	quota, ok := entry["quota"].(gin.H)
	if !ok || quota["signals"].(map[string]string)["X-Codex-Plan-Type"] != "pro" {
		t.Fatalf("built-in quota changed: %#v", entry["quota"])
	}
	message := output.String()
	if strings.Count(message, "rejected plugin quota metadata") != 1 || !strings.Contains(message, "plugin-auth-1") {
		t.Fatalf("rejection diagnostic missing message or auth ID: %q", message)
	}
	if strings.Contains(message, "payload-secret") || strings.Contains(message, "refresh_token") {
		t.Fatalf("rejection diagnostic leaked payload data: %q", message)
	}
}

func TestAbsentPluginQuotaDoesNothing(t *testing.T) {
	originalLevel := log.GetLevel()
	originalOutput := log.StandardLogger().Out
	defer log.SetLevel(originalLevel)
	defer log.SetOutput(originalOutput)

	var output bytes.Buffer
	log.SetLevel(log.DebugLevel)
	log.SetOutput(&output)
	entry := (&Handler{}).buildAuthFileEntry(pluginQuotaAuth(map[string]any{"access_token": "sibling-secret"}))
	if _, exists := entry["plugin_quota"]; exists || output.Len() != 0 {
		t.Fatalf("absent plugin quota changed output or logged: entry=%#v log=%q", entry, output.String())
	}
}
