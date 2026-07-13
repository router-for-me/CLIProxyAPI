package management

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestPutNotificationWebhooks_NormalizesAndPersists(t *testing.T) {
	t.Parallel()

	configPath := writeTestConfigFile(t)
	h := &Handler{
		cfg:            &config.Config{},
		configFilePath: configPath,
	}

	body := []byte(`{
		"webhooks": [{
			"name": "  oauth-refresh  ",
			"url": "  https://example.test/webhook  ",
			"adapter": " Feishu ",
			"mentions": [" ou_user_1 ", " "],
			"events": [" AUTH.REFRESH_FAILED ", ""],
			"providers": [" CODEX "],
			"status-codes": [401, -1],
			"timeout-seconds": 99,
			"retry": 9,
			"dedupe-seconds": -1
		}]
	}`)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPut, "/v0/management/notification-webhooks", bytes.NewReader(body))

	h.PutNotificationWebhooks(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := len(h.cfg.Notifications.Webhooks); got != 1 {
		t.Fatalf("webhooks len = %d, want 1", got)
	}
	hook := h.cfg.Notifications.Webhooks[0]
	if hook.Name != "oauth-refresh" || hook.URL != "https://example.test/webhook" || hook.Adapter != "feishu" {
		t.Fatalf("unexpected normalized webhook identity: %+v", hook)
	}
	if hook.TimeoutSeconds != config.MaxNotificationWebhookTimeoutSeconds || hook.Retry != config.MaxNotificationWebhookRetry || hook.DedupeSeconds != 0 {
		t.Fatalf("unexpected normalized limits: %+v", hook)
	}
	if len(hook.Mentions) != 1 || hook.Mentions[0] != "ou_user_1" {
		t.Fatalf("mentions = %#v, want normalized mention list", hook.Mentions)
	}
	if len(hook.Events) != 1 || hook.Events[0] != "auth.refresh_failed" {
		t.Fatalf("events = %#v, want auth.refresh_failed", hook.Events)
	}
	if len(hook.Providers) != 1 || hook.Providers[0] != "codex" {
		t.Fatalf("providers = %#v, want codex", hook.Providers)
	}
	if len(hook.StatusCodes) != 1 || hook.StatusCodes[0] != 401 {
		t.Fatalf("status codes = %#v, want [401]", hook.StatusCodes)
	}

	loaded, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("load persisted config: %v", err)
	}
	if len(loaded.Notifications.Webhooks) != 1 || loaded.Notifications.Webhooks[0].Name != "oauth-refresh" {
		t.Fatalf("persisted webhooks = %+v", loaded.Notifications.Webhooks)
	}
	if len(loaded.Notifications.Webhooks[0].Mentions) != 1 || loaded.Notifications.Webhooks[0].Mentions[0] != "ou_user_1" {
		t.Fatalf("persisted mentions = %+v", loaded.Notifications.Webhooks[0].Mentions)
	}
}

func TestPutNotificationWebhooks_RejectsEnabledWebhookWithoutEvents(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			Notifications: config.NotificationsConfig{
				Webhooks: []config.NotificationWebhookConfig{{Name: "existing", URL: "https://example.test/webhook", Events: []string{"auth.refresh_failed"}}},
			},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPut, "/v0/management/notification-webhooks", bytes.NewReader([]byte(`[
		{"name":"bad","url":"https://example.test/webhook","adapter":"slack"}
	]`)))

	h.PutNotificationWebhooks(c)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if got := h.cfg.Notifications.Webhooks[0].Name; got != "existing" {
		t.Fatalf("existing config changed to %q", got)
	}
}

func TestPutNotificationWebhooks_AllowsDisabledDraft(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg:            &config.Config{},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPut, "/v0/management/notification-webhooks", bytes.NewReader([]byte(`{
		"webhooks": [{"name":"draft","adapter":"telegram","disabled":true}]
	}`)))

	h.PutNotificationWebhooks(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := len(h.cfg.Notifications.Webhooks); got != 1 {
		t.Fatalf("webhooks len = %d, want 1", got)
	}
	if !h.cfg.Notifications.Webhooks[0].Disabled {
		t.Fatal("disabled draft was not preserved")
	}
}

func TestPatchAndDeleteNotificationWebhook(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			Notifications: config.NotificationsConfig{
				Webhooks: []config.NotificationWebhookConfig{
					{Name: "feishu", URL: "https://example.test/feishu", Adapter: "feishu", Events: []string{"auth.refresh_failed"}},
					{Name: "slack", URL: "https://example.test/slack", Adapter: "slack", Events: []string{"auth.request_failed"}},
				},
			},
		},
		configFilePath: writeTestConfigFile(t),
	}

	patchBody, err := json.Marshal(gin.H{
		"name": "slack",
		"value": config.NotificationWebhookConfig{
			Name:        "slack",
			URL:         "https://example.test/slack-2",
			Adapter:     "slack",
			Events:      []string{"auth.refresh_failed"},
			StatusCodes: []int{401},
		},
	})
	if err != nil {
		t.Fatalf("marshal patch: %v", err)
	}
	patchRec := httptest.NewRecorder()
	patchCtx, _ := gin.CreateTestContext(patchRec)
	patchCtx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/notification-webhooks", bytes.NewReader(patchBody))

	h.PatchNotificationWebhook(patchCtx)

	if patchRec.Code != http.StatusOK {
		t.Fatalf("patch status = %d, want %d; body=%s", patchRec.Code, http.StatusOK, patchRec.Body.String())
	}
	if got := h.cfg.Notifications.Webhooks[1].URL; got != "https://example.test/slack-2" {
		t.Fatalf("patched url = %q, want slack-2 url", got)
	}
	sharedBeforeDelete := h.cfg.Notifications.Webhooks

	deleteRec := httptest.NewRecorder()
	deleteCtx, _ := gin.CreateTestContext(deleteRec)
	deleteCtx.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/notification-webhooks?index=0", nil)

	h.DeleteNotificationWebhook(deleteCtx)

	if deleteRec.Code != http.StatusOK {
		t.Fatalf("delete status = %d, want %d; body=%s", deleteRec.Code, http.StatusOK, deleteRec.Body.String())
	}
	if got := len(h.cfg.Notifications.Webhooks); got != 1 {
		t.Fatalf("webhooks len after delete = %d, want 1", got)
	}
	if got := h.cfg.Notifications.Webhooks[0].Name; got != "slack" {
		t.Fatalf("remaining webhook = %q, want slack", got)
	}
	if sharedBeforeDelete[0].Name != "feishu" || sharedBeforeDelete[1].Name != "slack" {
		t.Fatalf("delete mutated shared webhook slice: %+v", sharedBeforeDelete)
	}
}
