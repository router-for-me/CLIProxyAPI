package management

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/managementauth"
)

func TestAccountUserGetPluginConfigRedactsProtectedFields(t *testing.T) {
	h := &Handler{
		cfg: &config.Config{Plugins: config.PluginsConfig{Configs: map[string]config.PluginInstanceConfig{
			"sample": pluginConfigFromYAML(t, "mode: safe\nquota:\n  limit: 5\nbilling_token: secret\nnested:\n  billing_plan: pro\nitems:\n  - name: ok\n    quota_limit: 9\n"),
		}}},
		configFilePath: writeTestConfigFile(t),
	}

	userRec, userCtx := pluginPolicyTestContext(http.MethodGet, "/v0/management/plugins/sample/config", nil, "sample", managementauth.RoleUser)
	h.GetPluginConfig(userCtx)
	if userRec.Code != http.StatusOK {
		t.Fatalf("user status = %d, want %d; body=%s", userRec.Code, http.StatusOK, userRec.Body.String())
	}
	var userBody map[string]any
	if err := json.Unmarshal(userRec.Body.Bytes(), &userBody); err != nil {
		t.Fatalf("decode user body: %v", err)
	}
	if userBody["mode"] != "safe" {
		t.Fatalf("mode = %#v, want safe", userBody["mode"])
	}
	if _, ok := userBody["quota"]; ok {
		t.Fatalf("user body exposed quota: %#v", userBody)
	}
	if _, ok := userBody["billing_token"]; ok {
		t.Fatalf("user body exposed billing_token: %#v", userBody)
	}
	if nested, _ := userBody["nested"].(map[string]any); len(nested) != 0 {
		t.Fatalf("user nested body = %#v, want protected fields redacted", nested)
	}
	items, _ := userBody["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items = %#v, want one redacted item", userBody["items"])
	}
	item, _ := items[0].(map[string]any)
	if _, ok := item["quota_limit"]; ok || item["name"] != "ok" {
		t.Fatalf("redacted item = %#v, want name only", item)
	}

	adminRec, adminCtx := pluginPolicyTestContext(http.MethodGet, "/v0/management/plugins/sample/config", nil, "sample", managementauth.RoleAdmin)
	h.GetPluginConfig(adminCtx)
	if adminRec.Code != http.StatusOK {
		t.Fatalf("admin status = %d, want %d; body=%s", adminRec.Code, http.StatusOK, adminRec.Body.String())
	}
	var adminBody map[string]any
	if err := json.Unmarshal(adminRec.Body.Bytes(), &adminBody); err != nil {
		t.Fatalf("decode admin body: %v", err)
	}
	if _, ok := adminBody["quota"]; !ok {
		t.Fatalf("admin body hid quota: %#v", adminBody)
	}
	if adminBody["billing_token"] != "secret" {
		t.Fatalf("admin billing_token = %#v, want secret", adminBody["billing_token"])
	}
}

func TestAccountUserPatchPluginConfigAllowsUnprotectedEdits(t *testing.T) {
	h := &Handler{
		cfg: &config.Config{Plugins: config.PluginsConfig{Configs: map[string]config.PluginInstanceConfig{
			"sample": pluginConfigFromYAML(t, "enabled: true\nmode: safe\nquota:\n  limit: 5\nbilling_code: old\n"),
		}}},
		configFilePath: writeTestConfigFile(t),
	}

	rec, c := pluginPolicyTestContext(http.MethodPatch, "/v0/management/plugins/sample/config", strings.NewReader(`{"mode":"fast","count":3}`), "sample", managementauth.RoleUser)
	h.PatchPluginConfig(c)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	raw := marshalPluginRaw(t, h.cfg.Plugins.Configs["sample"])
	if !strings.Contains(raw, "mode: fast") || !strings.Contains(raw, "count: 3") || !strings.Contains(raw, "quota:") || !strings.Contains(raw, "billing_code: old") {
		t.Fatalf("raw config after allowed user edit =\n%s", raw)
	}
}

func TestAccountUserPutPluginConfigPreservesOmittedProtectedFields(t *testing.T) {
	h := &Handler{
		cfg: &config.Config{Plugins: config.PluginsConfig{Configs: map[string]config.PluginInstanceConfig{
			"sample": pluginConfigFromYAML(t, "enabled: false\nold: true\nquota:\n  limit: 5\nsettings:\n  billing_plan: pro\n"),
		}}},
		configFilePath: writeTestConfigFile(t),
	}

	rec, c := pluginPolicyTestContext(http.MethodPut, "/v0/management/plugins/sample/config", strings.NewReader(`{"mode":"fast"}`), "sample", managementauth.RoleUser)
	h.PutPluginConfig(c)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	item := h.cfg.Plugins.Configs["sample"]
	if item.Enabled == nil || *item.Enabled {
		t.Fatalf("enabled = %#v, want preserved false", item.Enabled)
	}
	raw := marshalPluginRaw(t, item)
	if !strings.Contains(raw, "mode: fast") || strings.Contains(raw, "old:") || !strings.Contains(raw, "quota:") || !strings.Contains(raw, "limit: 5") || !strings.Contains(raw, "billing_plan: pro") {
		t.Fatalf("raw config after PUT omission =\n%s", raw)
	}
}

func TestAccountUserPluginConfigRejectsProtectedChanges(t *testing.T) {
	tests := []struct {
		name     string
		original string
		method   string
		body     string
	}{
		{
			name:     "patch quota null deletion",
			original: "mode: safe\nquota:\n  limit: 5\n",
			method:   http.MethodPatch,
			body:     `{"quota":null}`,
		},
		{
			name:     "patch protected parent null deletion",
			original: "settings:\n  quota_limit: 5\n  mode: safe\n",
			method:   http.MethodPatch,
			body:     `{"settings":null}`,
		},
		{
			name:     "patch protected array change",
			original: "rules:\n  - name: a\n    billing_code: old\n",
			method:   http.MethodPatch,
			body:     `{"rules":[{"name":"a","billing_code":"new"}]}`,
		},
		{
			name:     "patch protected array addition",
			original: "mode: safe\n",
			method:   http.MethodPatch,
			body:     `{"rules":[{"quota_limit":5}]}`,
		},
		{
			name:     "put protected field addition",
			original: "mode: safe\n",
			method:   http.MethodPut,
			body:     `{"mode":"safe","quota":{"limit":5}}`,
		},
		{
			name:     "put protected parent replacement",
			original: "settings:\n  quota_limit: 5\n",
			method:   http.MethodPut,
			body:     `{"settings":null}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &Handler{
				cfg: &config.Config{Plugins: config.PluginsConfig{Configs: map[string]config.PluginInstanceConfig{
					"sample": pluginConfigFromYAML(t, tt.original),
				}}},
				configFilePath: writeTestConfigFile(t),
			}
			before := marshalPluginRaw(t, h.cfg.Plugins.Configs["sample"])
			rec, c := pluginPolicyTestContext(tt.method, "/v0/management/plugins/sample/config", strings.NewReader(tt.body), "sample", managementauth.RoleUser)
			if tt.method == http.MethodPut {
				h.PutPluginConfig(c)
			} else {
				h.PatchPluginConfig(c)
			}
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusForbidden, rec.Body.String())
			}
			after := marshalPluginRaw(t, h.cfg.Plugins.Configs["sample"])
			if after != before {
				t.Fatalf("config changed after rejected request:\nbefore:\n%s\nafter:\n%s", before, after)
			}
		})
	}
}

func TestAccountUserProtectedPluginEnableAndDeleteRequireAdmin(t *testing.T) {
	h := &Handler{
		cfg: &config.Config{Plugins: config.PluginsConfig{Configs: map[string]config.PluginInstanceConfig{
			"protected": pluginConfigFromYAML(t, "enabled: false\nquota:\n  limit: 5\n"),
			"normal":    pluginConfigFromYAML(t, "enabled: false\nmode: safe\n"),
		}}},
		configFilePath: writeTestConfigFile(t),
	}

	protectedEnableRec, protectedEnableCtx := pluginPolicyTestContext(http.MethodPatch, "/v0/management/plugins/protected/enabled", strings.NewReader(`{"enabled":true}`), "protected", managementauth.RoleUser)
	h.PatchPluginEnabled(protectedEnableCtx)
	if protectedEnableRec.Code != http.StatusForbidden {
		t.Fatalf("protected enable status = %d, want %d; body=%s", protectedEnableRec.Code, http.StatusForbidden, protectedEnableRec.Body.String())
	}
	if h.cfg.Plugins.Configs["protected"].Enabled == nil || *h.cfg.Plugins.Configs["protected"].Enabled {
		t.Fatalf("protected enabled changed to %#v", h.cfg.Plugins.Configs["protected"].Enabled)
	}

	normalEnableRec, normalEnableCtx := pluginPolicyTestContext(http.MethodPatch, "/v0/management/plugins/normal/enabled", strings.NewReader(`{"enabled":true}`), "normal", managementauth.RoleUser)
	h.PatchPluginEnabled(normalEnableCtx)
	if normalEnableRec.Code != http.StatusOK {
		t.Fatalf("normal enable status = %d, want %d; body=%s", normalEnableRec.Code, http.StatusOK, normalEnableRec.Body.String())
	}
	if h.cfg.Plugins.Configs["normal"].Enabled == nil || !*h.cfg.Plugins.Configs["normal"].Enabled {
		t.Fatalf("normal enabled = %#v, want true", h.cfg.Plugins.Configs["normal"].Enabled)
	}

	protectedDeleteRec, protectedDeleteCtx := pluginPolicyTestContext(http.MethodDelete, "/v0/management/plugins/protected", nil, "protected", managementauth.RoleUser)
	h.DeletePlugin(protectedDeleteCtx)
	if protectedDeleteRec.Code != http.StatusForbidden {
		t.Fatalf("protected delete status = %d, want %d; body=%s", protectedDeleteRec.Code, http.StatusForbidden, protectedDeleteRec.Body.String())
	}
	if _, ok := h.cfg.Plugins.Configs["protected"]; !ok {
		t.Fatal("protected config was deleted by user")
	}
}

func TestAccountAdminPluginConfigOperationsUnchanged(t *testing.T) {
	h := &Handler{
		cfg: &config.Config{Plugins: config.PluginsConfig{Configs: map[string]config.PluginInstanceConfig{
			"sample": pluginConfigFromYAML(t, "enabled: false\nmode: safe\nquota:\n  limit: 5\n"),
		}}},
		configFilePath: writeTestConfigFile(t),
	}

	patchRec, patchCtx := pluginPolicyTestContext(http.MethodPatch, "/v0/management/plugins/sample/config", strings.NewReader(`{"quota":null}`), "sample", managementauth.RoleAdmin)
	h.PatchPluginConfig(patchCtx)
	if patchRec.Code != http.StatusOK {
		t.Fatalf("admin patch status = %d, want %d; body=%s", patchRec.Code, http.StatusOK, patchRec.Body.String())
	}
	if raw := marshalPluginRaw(t, h.cfg.Plugins.Configs["sample"]); strings.Contains(raw, "quota:") {
		t.Fatalf("admin patch did not remove quota:\n%s", raw)
	}

	enableRec, enableCtx := pluginPolicyTestContext(http.MethodPatch, "/v0/management/plugins/sample/enabled", strings.NewReader(`{"enabled":true}`), "sample", managementauth.RoleAdmin)
	h.PatchPluginEnabled(enableCtx)
	if enableRec.Code != http.StatusOK {
		t.Fatalf("admin enable status = %d, want %d; body=%s", enableRec.Code, http.StatusOK, enableRec.Body.String())
	}
	if h.cfg.Plugins.Configs["sample"].Enabled == nil || !*h.cfg.Plugins.Configs["sample"].Enabled {
		t.Fatalf("admin enabled = %#v, want true", h.cfg.Plugins.Configs["sample"].Enabled)
	}
}

func pluginPolicyTestContext(method, path string, body io.Reader, id, role string) (*httptest.ResponseRecorder, *gin.Context) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Params = gin.Params{{Key: "id", Value: id}}
	c.Request = httptest.NewRequest(method, path, body)
	c.Request.Header.Set("Content-Type", "application/json")
	if role != "" {
		c.Set("management_account", managementauth.User{Username: role, Role: role})
	}
	return rec, c
}

func TestAccountProtectedPluginLifecycleThroughConfig(t *testing.T) {
	original := pluginConfigNode(pluginConfigFromYAML(t, "enabled: true\nquota:\n  limit: 5\nmode: safe\n"))
	for _, raw := range []string{"enabled: false\n", "enabled: null\n"} {
		requested := pluginConfigNode(pluginConfigFromYAML(t, raw))
		if _, err := mergeAccountPluginPUTConfigWithProtectedOriginal(original, requested); err == nil {
			t.Errorf("PUT allowed lifecycle change: %s", raw)
		}
	}
	updated := pluginConfigNode(pluginConfigFromYAML(t, "enabled: false\nquota:\n  limit: 5\n"))
	if err := rejectChangedAccountPluginProtectedYAML(updated, original); err == nil {
		t.Fatal("PATCH allowed lifecycle change")
	}
	requested := pluginConfigNode(pluginConfigFromYAML(t, "mode: faster\n"))
	merged, err := mergeAccountPluginPUTConfigWithProtectedOriginal(original, requested)
	if err != nil {
		t.Fatal(err)
	}
	if enabled := accountYAMLLookupPath(merged, []string{"enabled"}); enabled == nil || enabled.Value != "true" {
		t.Fatal("PUT failed to preserve protected enabled value")
	}
	oldDoc, err := decodeAccountConfigYAML([]byte("plugins:\n  enabled: true\n  dir: plugins\n  configs:\n    sample:\n      enabled: true\n      quota:\n        limit: 5\n"))
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"plugins:\n  enabled: false\n", "plugins:\n  dir: other\n", "plugins:\n  configs:\n    sample:\n      enabled: false\n"} {
		doc, err := decodeAccountConfigYAML([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := mergeAccountConfigYAMLWithProtectedOriginal(oldDoc, doc); err == nil {
			t.Errorf("global YAML allowed plugin lifecycle change: %s", raw)
		}
	}
}

func TestAccountUserCannotReinstallProtectedPlugin(t *testing.T) {
	h := &Handler{cfg: &config.Config{Plugins: config.PluginsConfig{Configs: map[string]config.PluginInstanceConfig{"sample": pluginConfigFromYAML(t, "quota:\n  limit: 5\n")}}}}
	rec, c := pluginPolicyTestContext(http.MethodPost, "/v0/management/plugin-store/sample/install", nil, "sample", managementauth.RoleUser)
	if h.authorizeAccountRequest(c) || rec.Code != http.StatusForbidden {
		t.Fatalf("protected reinstall allowed: %d", rec.Code)
	}
}
