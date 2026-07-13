package notifications

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestWebhookDispatcherSendsMatchingGenericEvent(t *testing.T) {
	resetServiceURLForTest()
	t.Cleanup(resetServiceURLForTest)
	requests := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if got := r.Header.Get("Content-Type"); !strings.Contains(got, "application/json") {
			t.Fatalf("Content-Type = %q, want application/json", got)
		}
		body, _ := io.ReadAll(r.Body)
		requests <- body
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	dispatcher := newWebhookDispatcher(server.Client())
	dispatcher.configure([]config.NotificationWebhookConfig{{
		Name:        "test",
		URL:         server.URL,
		Adapter:     "generic",
		Events:      []string{EventAuthRefreshFailed},
		Providers:   []string{"codex"},
		StatusCodes: []int{http.StatusUnauthorized},
	}})

	dispatcher.publishEvent(webhookTestEvent(EventAuthRequestUnauthorized, "codex", http.StatusUnauthorized))
	assertNoWebhookRequest(t, requests)
	dispatcher.publishEvent(webhookTestEvent(EventAuthRefreshFailed, "antigravity", http.StatusUnauthorized))
	assertNoWebhookRequest(t, requests)
	dispatcher.publishEvent(webhookTestEvent(EventAuthRefreshFailed, "codex", http.StatusTooManyRequests))
	assertNoWebhookRequest(t, requests)

	dispatcher.publishEvent(webhookTestEvent(EventAuthRefreshFailed, "codex", http.StatusUnauthorized))
	got := requireWebhookRequest(t, requests)
	var event Event
	if err := json.Unmarshal(got, &event); err != nil {
		t.Fatalf("unmarshal generic body: %v body=%s", err, string(got))
	}
	if event.Type != EventAuthRefreshFailed || event.Provider != "codex" || event.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unexpected generic event: %+v", event)
	}
	if event.Message != "refresh failed" {
		t.Fatalf("Message = %q, want refresh failed", event.Message)
	}
	if event.ServiceURL != "" {
		t.Fatalf("ServiceURL = %q, want empty without configured service URL", event.ServiceURL)
	}
}

func TestConfigureServiceURLNormalizesURL(t *testing.T) {
	resetServiceURLForTest()
	t.Cleanup(resetServiceURLForTest)

	ConfigureServiceURL(" HTTPS://proxy.example.test/base/?debug=1#fragment ")

	if got := CurrentServiceURL(); got != "https://proxy.example.test/base" {
		t.Fatalf("CurrentServiceURL() = %q, want https://proxy.example.test/base", got)
	}
}

func TestConfigureServiceURLRejectsUnsupportedValues(t *testing.T) {
	resetServiceURLForTest()
	t.Cleanup(resetServiceURLForTest)

	for _, rawURL := range []string{"proxy.example.test", "ftp://proxy.example.test", "://invalid"} {
		ConfigureServiceURL(rawURL)
		if got := CurrentServiceURL(); got != "" {
			t.Fatalf("CurrentServiceURL() = %q after invalid URL %q, want empty", got, rawURL)
		}
	}
}

func TestWebhookDispatcherRequiresEventsFilter(t *testing.T) {
	requests := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests <- body
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	dispatcher := newWebhookDispatcher(server.Client())
	dispatcher.configure([]config.NotificationWebhookConfig{{
		Name:    "missing-events",
		URL:     server.URL,
		Adapter: "generic",
	}})

	dispatcher.publishEvent(webhookTestEvent(EventAuthRefreshFailed, "codex", http.StatusUnauthorized))
	assertNoWebhookRequest(t, requests)
}

func TestWebhookDispatcherWildcardEventMatchesAllEvents(t *testing.T) {
	requests := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests <- body
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	dispatcher := newWebhookDispatcher(server.Client())
	dispatcher.configure([]config.NotificationWebhookConfig{{
		Name:    "all-events",
		URL:     server.URL,
		Adapter: "generic",
		Events:  []string{EventAll},
	}})

	dispatcher.publishEvent(webhookTestEvent(EventAuthRequestUnauthorized, "codex", http.StatusUnauthorized))
	got := requireWebhookRequest(t, requests)
	var event Event
	if err := json.Unmarshal(got, &event); err != nil {
		t.Fatalf("unmarshal wildcard body: %v body=%s", err, string(got))
	}
	if event.Type != EventAuthRequestUnauthorized {
		t.Fatalf("event type = %q, want %q", event.Type, EventAuthRequestUnauthorized)
	}
}

func TestWebhookDispatcherRequestFailedMatchesUnauthorizedSubevent(t *testing.T) {
	requests := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests <- body
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	dispatcher := newWebhookDispatcher(server.Client())
	dispatcher.configure([]config.NotificationWebhookConfig{{
		Name:        "request-failed",
		URL:         server.URL,
		Adapter:     "generic",
		Events:      []string{EventAuthRequestFailed},
		Providers:   []string{"codex"},
		StatusCodes: []int{http.StatusUnauthorized},
	}})

	dispatcher.publishEvent(webhookTestEvent(EventAuthRequestUnauthorized, "codex", http.StatusUnauthorized))
	got := requireWebhookRequest(t, requests)
	var event Event
	if err := json.Unmarshal(got, &event); err != nil {
		t.Fatalf("unmarshal request failed body: %v body=%s", err, string(got))
	}
	if event.Type != EventAuthRequestUnauthorized || event.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unexpected event: %+v", event)
	}
}

func TestWebhookDispatcherFormatsFeishuAndDedupes(t *testing.T) {
	resetServiceURLForTest()
	t.Cleanup(resetServiceURLForTest)
	requests := make(chan []byte, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests <- body
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	dispatcher := newWebhookDispatcher(server.Client())
	dispatcher.configure([]config.NotificationWebhookConfig{{
		Name:          "feishu",
		URL:           server.URL,
		Adapter:       "feishu",
		Events:        []string{EventAuthRefreshFailed},
		StatusCodes:   []int{http.StatusUnauthorized},
		DedupeSeconds: 60,
	}})

	event := webhookTestEvent(EventAuthRefreshFailed, "codex", http.StatusUnauthorized)
	event.ServiceURL = "https://proxy.example.test"
	dispatcher.publishEvent(event)
	dispatcher.publishEvent(event)

	got := requireWebhookRequest(t, requests)
	assertNoWebhookRequest(t, requests)

	var body struct {
		MsgType string `json:"msg_type"`
		Card    struct {
			Header struct {
				Title struct {
					Content string `json:"content"`
				} `json:"title"`
				Template string `json:"template"`
			} `json:"header"`
			Elements []struct {
				Tag    string `json:"tag"`
				Fields []struct {
					Text struct {
						Content string `json:"content"`
					} `json:"text"`
				} `json:"fields"`
				Actions []struct {
					Tag  string `json:"tag"`
					URL  string `json:"url"`
					Text struct {
						Content string `json:"content"`
					} `json:"text"`
				} `json:"actions"`
			} `json:"elements"`
		} `json:"card"`
	}
	if err := json.Unmarshal(got, &body); err != nil {
		t.Fatalf("unmarshal feishu body: %v body=%s", err, string(got))
	}
	if body.MsgType != "interactive" {
		t.Fatalf("msg_type = %q, want interactive", body.MsgType)
	}
	if body.Card.Header.Title.Content != "CLIProxyAPI OAuth refresh failed" || body.Card.Header.Template != "red" {
		t.Fatalf("unexpected card header: %+v", body.Card.Header)
	}
	cardText := string(got)
	assertTextContains(t, cardText, []string{
		"auth.refresh_failed",
		"codex",
		"auth-1",
		"owner@example.com",
		"401",
		"https://proxy.example.test",
		"https://proxy.example.test/management.html#/quota",
	})
}

func TestWebhookDispatcherFormatsFeishuMentions(t *testing.T) {
	requests := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests <- body
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	dispatcher := newWebhookDispatcher(server.Client())
	dispatcher.configure([]config.NotificationWebhookConfig{{
		Name:     "feishu",
		URL:      server.URL,
		Adapter:  "feishu",
		Events:   []string{EventAuthRefreshFailed},
		Mentions: []string{" ou_user_1 ", "all", "bad id"},
	}})

	dispatcher.publishEvent(webhookTestEvent(EventAuthRefreshFailed, "codex", http.StatusUnauthorized))
	got := requireWebhookRequest(t, requests)

	var body struct {
		Card struct {
			Elements []struct {
				Text struct {
					Content string `json:"content"`
				} `json:"text"`
			} `json:"elements"`
		} `json:"card"`
	}
	if err := json.Unmarshal(got, &body); err != nil {
		t.Fatalf("unmarshal feishu body: %v body=%s", err, string(got))
	}
	if len(body.Card.Elements) == 0 {
		t.Fatalf("feishu card elements are empty: %s", string(got))
	}
	mentionContent := body.Card.Elements[0].Text.Content
	assertTextContains(t, mentionContent, []string{
		"**Notify**",
		"<at id=ou_user_1></at>",
		"<at id=all></at>",
	})
	if strings.Contains(mentionContent, "bad id") {
		t.Fatalf("invalid mention id was rendered: %q", mentionContent)
	}
}

func TestFeishuWebhookUsesPlainTextForDynamicMessage(t *testing.T) {
	event := webhookTestEvent(EventAuthRefreshFailed, "codex", http.StatusUnauthorized)
	event.Message = `<at id=all></at>`
	payload, _, err := formatFeishuWebhookBody(event)
	if err != nil {
		t.Fatalf("formatFeishuWebhookBody: %v", err)
	}

	var body struct {
		Card struct {
			Elements []struct {
				Text struct {
					Tag     string `json:"tag"`
					Content string `json:"content"`
				} `json:"text"`
			} `json:"elements"`
		} `json:"card"`
	}
	if err := json.Unmarshal(payload, &body); err != nil {
		t.Fatalf("unmarshal feishu body: %v body=%s", err, string(payload))
	}
	for _, element := range body.Card.Elements {
		if strings.Contains(element.Text.Content, event.Message) {
			if element.Text.Tag != "plain_text" {
				t.Fatalf("dynamic message tag = %q, want plain_text", element.Text.Tag)
			}
			return
		}
	}
	t.Fatalf("dynamic message not found in card: %s", string(payload))
}

func TestFeishuWebhookOmitsActionsWithoutServiceURL(t *testing.T) {
	payload, _, err := formatFeishuWebhookBody(webhookTestEvent(EventAuthRefreshFailed, "codex", http.StatusUnauthorized))
	if err != nil {
		t.Fatalf("formatFeishuWebhookBody: %v", err)
	}
	if strings.Contains(string(payload), `"tag":"action"`) {
		t.Fatalf("feishu card included action without service URL: %s", string(payload))
	}
}

func TestWebhookDispatcherPrunesExpiredDedupeKeys(t *testing.T) {
	dispatcher := newWebhookDispatcher(http.DefaultClient)
	dispatcher.configure([]config.NotificationWebhookConfig{{
		Name:          "dedupe",
		URL:           "https://example.test/webhook",
		Adapter:       "generic",
		Events:        []string{EventAuthRefreshFailed},
		DedupeSeconds: 10,
	}})

	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	dispatcher.mu.Lock()
	dispatcher.lastSent["old"] = now.Add(-11 * time.Second)
	dispatcher.lastSent["recent"] = now.Add(-5 * time.Second)
	dispatcher.lastPrune = now.Add(-webhookDedupePruneInterval)
	dispatcher.mu.Unlock()

	dispatcher.matchDeliveries([]byte(`{}`), webhookTestEvent(EventAuthRefreshFailed, "codex", http.StatusUnauthorized), now)

	dispatcher.mu.Lock()
	defer dispatcher.mu.Unlock()
	if _, ok := dispatcher.lastSent["old"]; ok {
		t.Fatalf("expired dedupe key was not pruned: %#v", dispatcher.lastSent)
	}
	if _, ok := dispatcher.lastSent["recent"]; !ok {
		t.Fatalf("recent dedupe key was pruned: %#v", dispatcher.lastSent)
	}
}

func TestWebhookDispatcherFormatsSlack(t *testing.T) {
	requests := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests <- body
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	dispatcher := newWebhookDispatcher(server.Client())
	dispatcher.configure([]config.NotificationWebhookConfig{{
		Name:    "slack",
		URL:     server.URL,
		Adapter: "slack",
		Events:  []string{EventAuthRefreshFailed},
	}})

	event := webhookTestEvent(EventAuthRefreshFailed, "codex", http.StatusUnauthorized)
	event.Message = "refresh <@U123> & failed"
	event.ServiceURL = "https://proxy.example.test"
	dispatcher.publishEvent(event)
	got := requireWebhookRequest(t, requests)

	var body struct {
		Text   string `json:"text"`
		Blocks []struct {
			Type string `json:"type"`
			Text struct {
				Text string `json:"text"`
			} `json:"text"`
			Elements []struct {
				Type string `json:"type"`
				URL  string `json:"url"`
				Text struct {
					Text string `json:"text"`
				} `json:"text"`
			} `json:"elements"`
		} `json:"blocks"`
	}
	if err := json.Unmarshal(got, &body); err != nil {
		t.Fatalf("unmarshal slack body: %v body=%s", err, string(got))
	}
	if body.Text != "CLIProxyAPI OAuth refresh failed" {
		t.Fatalf("slack text = %q, want card title", body.Text)
	}
	messageFound := false
	for _, block := range body.Blocks {
		if strings.Contains(block.Text.Text, "refresh &lt;@U123&gt; &amp; failed") {
			messageFound = true
			break
		}
	}
	if !messageFound {
		t.Fatalf("escaped Slack message not found: %s", string(got))
	}
	assertTextContains(t, string(got), []string{
		"auth.refresh_failed",
		"https://proxy.example.test",
		"https://proxy.example.test/management.html#/quota",
	})
}

func TestSlackWebhookOmitsActionsWithoutServiceURL(t *testing.T) {
	payload, _, err := formatSlackWebhookBody(webhookTestEvent(EventAuthRefreshFailed, "codex", http.StatusUnauthorized))
	if err != nil {
		t.Fatalf("formatSlackWebhookBody: %v", err)
	}
	if strings.Contains(string(payload), `"type":"actions"`) {
		t.Fatalf("slack card included actions without service URL: %s", string(payload))
	}
}

func TestWebhookDispatcherFormatsTelegram(t *testing.T) {
	requests := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests <- body
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	dispatcher := newWebhookDispatcher(server.Client())
	dispatcher.configure([]config.NotificationWebhookConfig{{
		Name:    "telegram",
		URL:     server.URL,
		Adapter: "telegram",
		Target:  "-1001234567890",
		Events:  []string{EventAuthRefreshFailed},
	}})

	event := webhookTestEvent(EventAuthRefreshFailed, "codex", http.StatusUnauthorized)
	event.ServiceURL = "https://proxy.example.test"
	dispatcher.publishEvent(event)
	got := requireWebhookRequest(t, requests)

	var body struct {
		ChatID                string `json:"chat_id"`
		Text                  string `json:"text"`
		DisableWebPagePreview bool   `json:"disable_web_page_preview"`
		ReplyMarkup           struct {
			InlineKeyboard [][]struct {
				Text string `json:"text"`
				URL  string `json:"url"`
			} `json:"inline_keyboard"`
		} `json:"reply_markup"`
	}
	if err := json.Unmarshal(got, &body); err != nil {
		t.Fatalf("unmarshal telegram body: %v body=%s", err, string(got))
	}
	if body.ChatID != "-1001234567890" {
		t.Fatalf("chat_id = %q, want -1001234567890", body.ChatID)
	}
	if !body.DisableWebPagePreview {
		t.Fatalf("disable_web_page_preview = false, want true")
	}
	assertTextContains(t, body.Text, []string{
		"CLIProxyAPI notification",
		"type: auth.refresh_failed",
		"provider: codex",
	})
	if len(body.ReplyMarkup.InlineKeyboard) != 1 || len(body.ReplyMarkup.InlineKeyboard[0]) != 2 {
		t.Fatalf("inline keyboard = %#v, want two buttons", body.ReplyMarkup.InlineKeyboard)
	}
	if body.ReplyMarkup.InlineKeyboard[0][0].URL != "https://proxy.example.test" || body.ReplyMarkup.InlineKeyboard[0][1].URL != "https://proxy.example.test/management.html#/quota" {
		t.Fatalf("unexpected telegram button URLs: %#v", body.ReplyMarkup.InlineKeyboard)
	}
}

func TestTelegramWebhookOmitsKeyboardWithoutServiceURL(t *testing.T) {
	payload, _, err := formatTelegramWebhookBody("-1001234567890", webhookTestEvent(EventAuthRefreshFailed, "codex", http.StatusUnauthorized))
	if err != nil {
		t.Fatalf("formatTelegramWebhookBody: %v", err)
	}
	if strings.Contains(string(payload), "reply_markup") {
		t.Fatalf("telegram payload included keyboard without service URL: %s", string(payload))
	}
}

func TestFormatWebhookBodyRejectsTelegramWithoutTarget(t *testing.T) {
	_, _, err := formatWebhookBody(webhookDelivery{
		hook:  webhook{adapter: "telegram"},
		event: webhookTestEvent(EventAuthRefreshFailed, "codex", http.StatusUnauthorized),
	})
	if err == nil {
		t.Fatal("formatWebhookBody returned nil error, want telegram target error")
	}
}

func TestManagementPanelURLUsesQuotaHashRoute(t *testing.T) {
	got := managementPanelURL("https://proxy.example.test/api?debug=1")
	want := "https://proxy.example.test/management.html#/quota"
	if got != want {
		t.Fatalf("managementPanelURL() = %q, want %q", got, want)
	}
}

func TestWebhookDispatcherRetriesFailedDelivery(t *testing.T) {
	requests := make(chan []byte, 2)
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests <- body
		if atomic.AddInt32(&attempts, 1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	dispatcher := newWebhookDispatcher(server.Client())
	dispatcher.configure([]config.NotificationWebhookConfig{{
		Name:    "retry",
		URL:     server.URL,
		Adapter: "generic",
		Events:  []string{EventAuthRefreshFailed},
		Retry:   1,
	}})

	dispatcher.publishEvent(webhookTestEvent(EventAuthRefreshFailed, "codex", http.StatusUnauthorized))
	_ = requireWebhookRequest(t, requests)
	_ = requireWebhookRequest(t, requests)
	if got := atomic.LoadInt32(&attempts); got != 2 {
		t.Fatalf("attempts = %d, want 2", got)
	}
}

func webhookTestEvent(eventType, provider string, statusCode int) Event {
	return Event{
		Type:        eventType,
		Timestamp:   time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC),
		Provider:    provider,
		Model:       "gpt-5",
		AuthID:      "auth-id",
		AuthIndex:   "auth-1",
		AccountType: "oauth",
		Account:     "owner@example.com",
		StatusCode:  statusCode,
		Body:        "refresh failed",
		Code:        "unauthorized",
	}
}

func requireWebhookRequest(t *testing.T, requests <-chan []byte) []byte {
	t.Helper()
	select {
	case got := <-requests:
		return got
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for webhook request")
		return nil
	}
}

func assertNoWebhookRequest(t *testing.T, requests <-chan []byte) {
	t.Helper()
	select {
	case got := <-requests:
		t.Fatalf("unexpected webhook request: %s", string(got))
	case <-time.After(100 * time.Millisecond):
	}
}

func assertTextContains(t *testing.T, text string, wants []string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(text, want) {
			t.Fatalf("text missing %q: %q", want, text)
		}
	}
}
