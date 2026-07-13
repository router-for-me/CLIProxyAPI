package config

import "testing"

func TestParseConfigBytesNormalizesNotificationWebhooks(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte(`
notifications:
  service-url: "  https://proxy.example.test/base/  "
  webhooks:
    - name: "  alerts  "
      url: "  https://example.test/webhook  "
      adapter: "  FEISHU  "
      target: "  chat-1  "
      mentions: ["  ou_user_1  ", ""]
      events: [" Auth.Refresh_Failed ", "", "auth.request_unauthorized"]
      providers: [" Codex ", "", "ANTIGRAVITY"]
      status-codes: [0, 401, -1]
      retry: -2
      timeout-seconds: 0
      dedupe-seconds: -30
    - url: "https://example.test/legacy"
      format: "slack"
`))
	if err != nil {
		t.Fatalf("ParseConfigBytes returned error: %v", err)
	}
	if len(cfg.Notifications.Webhooks) != 2 {
		t.Fatalf("webhooks len = %d, want 2", len(cfg.Notifications.Webhooks))
	}
	if cfg.Notifications.ServiceURL != "https://proxy.example.test/base" {
		t.Fatalf("ServiceURL = %q, want normalized service URL", cfg.Notifications.ServiceURL)
	}

	hook := cfg.Notifications.Webhooks[0]
	if hook.Name != "alerts" || hook.URL != "https://example.test/webhook" {
		t.Fatalf("unexpected trimmed fields: name=%q url=%q", hook.Name, hook.URL)
	}
	if hook.Adapter != "feishu" {
		t.Fatalf("Adapter = %q, want feishu", hook.Adapter)
	}
	if hook.Target != "chat-1" {
		t.Fatalf("Target = %q, want chat-1", hook.Target)
	}
	if len(hook.Mentions) != 1 || hook.Mentions[0] != "ou_user_1" {
		t.Fatalf("Mentions = %#v, want normalized mention list", hook.Mentions)
	}
	if hook.TimeoutSeconds != DefaultNotificationWebhookTimeoutSeconds {
		t.Fatalf("TimeoutSeconds = %d, want %d", hook.TimeoutSeconds, DefaultNotificationWebhookTimeoutSeconds)
	}
	if hook.Retry != 0 {
		t.Fatalf("Retry = %d, want 0", hook.Retry)
	}
	if hook.DedupeSeconds != 0 {
		t.Fatalf("DedupeSeconds = %d, want 0", hook.DedupeSeconds)
	}
	if len(hook.StatusCodes) != 1 || hook.StatusCodes[0] != 401 {
		t.Fatalf("StatusCodes = %#v, want [401]", hook.StatusCodes)
	}
	if len(hook.Events) != 2 || hook.Events[0] != "auth.refresh_failed" || hook.Events[1] != "auth.request_unauthorized" {
		t.Fatalf("Events = %#v, want normalized event list", hook.Events)
	}
	if len(hook.Providers) != 2 || hook.Providers[0] != "codex" || hook.Providers[1] != "antigravity" {
		t.Fatalf("Providers = %#v, want normalized provider list", hook.Providers)
	}

	legacy := cfg.Notifications.Webhooks[1]
	if legacy.Adapter != "slack" {
		t.Fatalf("legacy format Adapter = %q, want slack", legacy.Adapter)
	}
}
