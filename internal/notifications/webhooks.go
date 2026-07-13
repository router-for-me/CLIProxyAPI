package notifications

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	log "github.com/sirupsen/logrus"
)

const (
	EventAll                         = "*"
	EventAuthRequestFailed           = "auth.request_failed"
	EventAuthRequestUnauthorized     = "auth.request_unauthorized"
	EventAuthRefreshFailed           = "auth.refresh_failed"
	EventAuthManagementRequestFailed = "auth.management_request_failed"

	SeverityError = "error"

	webhookQueueSize           = 256
	webhookWorkerCount         = 4
	webhookUserAgent           = "CLIProxyAPI"
	webhookDedupePruneInterval = time.Minute
)

// Event is the common notification payload sent to configured sinks.
type Event struct {
	Type        string         `json:"type,omitempty"`
	Severity    string         `json:"severity,omitempty"`
	Timestamp   time.Time      `json:"timestamp"`
	Provider    string         `json:"provider,omitempty"`
	Model       string         `json:"model,omitempty"`
	AuthID      string         `json:"auth_id,omitempty"`
	AuthIndex   string         `json:"auth_index,omitempty"`
	AccountType string         `json:"account_type,omitempty"`
	Account     string         `json:"account,omitempty"`
	StatusCode  int            `json:"status_code,omitempty"`
	Body        string         `json:"body,omitempty"`
	Message     string         `json:"message,omitempty"`
	Code        string         `json:"code,omitempty"`
	Retryable   bool           `json:"retryable,omitempty"`
	ServiceURL  string         `json:"service_url,omitempty"`
	Details     map[string]any `json:"details,omitempty"`
}

type webhook struct {
	name        string
	url         string
	adapter     string
	target      string
	mentions    []string
	events      map[string]struct{}
	providers   map[string]struct{}
	statusCodes map[int]struct{}
	timeout     time.Duration
	retry       int
	dedupe      time.Duration
}

type webhookDelivery struct {
	hook    webhook
	payload []byte
	event   Event
}

type webhookDispatcher struct {
	client *http.Client
	queue  chan webhookDelivery

	mu        sync.Mutex
	hooks     []webhook
	lastSent  map[string]time.Time
	lastPrune time.Time
}

var globalWebhookDispatcher = newWebhookDispatcher(http.DefaultClient)

func newWebhookDispatcher(client *http.Client) *webhookDispatcher {
	if client == nil {
		client = http.DefaultClient
	}
	dispatcher := &webhookDispatcher{
		client:   client,
		queue:    make(chan webhookDelivery, webhookQueueSize),
		lastSent: make(map[string]time.Time),
	}
	for range webhookWorkerCount {
		go dispatcher.worker()
	}
	return dispatcher
}

// ConfigureWebhooks updates the process-wide notification webhook sinks.
func ConfigureWebhooks(configs []config.NotificationWebhookConfig) {
	globalWebhookDispatcher.configure(configs)
}

// Configure updates the process-wide notification settings.
func Configure(cfg config.NotificationsConfig) {
	ConfigureServiceURL(cfg.ServiceURL)
	ConfigureWebhooks(cfg.Webhooks)
}

// PublishEvent sends an event to configured webhook sinks.
func PublishEvent(event Event) {
	globalWebhookDispatcher.publishEvent(event)
}

// PublishEventPayload sends a JSON encoded event to configured webhook sinks.
func PublishEventPayload(payload []byte) {
	globalWebhookDispatcher.publishPayload(payload)
}

func (d *webhookDispatcher) configure(configs []config.NotificationWebhookConfig) {
	hooks := make([]webhook, 0, len(configs))
	for _, cfg := range configs {
		hook, ok := webhookFromConfig(cfg)
		if ok {
			hooks = append(hooks, hook)
		}
	}

	d.mu.Lock()
	d.hooks = hooks
	d.lastSent = make(map[string]time.Time)
	d.lastPrune = time.Time{}
	d.mu.Unlock()
}

func webhookFromConfig(cfg config.NotificationWebhookConfig) (webhook, bool) {
	if cfg.Disabled || strings.TrimSpace(cfg.URL) == "" {
		return webhook{}, false
	}

	events := make(map[string]struct{}, len(cfg.Events))
	for _, event := range cfg.Events {
		event = strings.ToLower(strings.TrimSpace(event))
		if event != "" {
			events[event] = struct{}{}
		}
	}
	if len(events) == 0 {
		log.WithField("webhook", strings.TrimSpace(cfg.Name)).Warn("notification webhook skipped: events filter is required")
		return webhook{}, false
	}
	providers := make(map[string]struct{}, len(cfg.Providers))
	for _, provider := range cfg.Providers {
		provider = strings.ToLower(strings.TrimSpace(provider))
		if provider != "" {
			providers[provider] = struct{}{}
		}
	}
	statusCodes := make(map[int]struct{}, len(cfg.StatusCodes))
	for _, code := range cfg.StatusCodes {
		if code > 0 {
			statusCodes[code] = struct{}{}
		}
	}

	name := strings.TrimSpace(cfg.Name)
	if name == "" {
		name = strings.TrimSpace(cfg.URL)
	}
	adapter := strings.ToLower(strings.TrimSpace(cfg.Adapter))
	if adapter == "" {
		adapter = strings.ToLower(strings.TrimSpace(cfg.Format))
	}
	if adapter == "" {
		adapter = config.DefaultNotificationWebhookAdapter
	}
	timeoutSeconds := cfg.TimeoutSeconds
	if timeoutSeconds <= 0 {
		timeoutSeconds = config.DefaultNotificationWebhookTimeoutSeconds
	} else if timeoutSeconds > config.MaxNotificationWebhookTimeoutSeconds {
		timeoutSeconds = config.MaxNotificationWebhookTimeoutSeconds
	}
	retry := cfg.Retry
	if retry < 0 {
		retry = 0
	} else if retry > config.MaxNotificationWebhookRetry {
		retry = config.MaxNotificationWebhookRetry
	}
	dedupeSeconds := cfg.DedupeSeconds
	if dedupeSeconds < 0 {
		dedupeSeconds = 0
	}
	return webhook{
		name:        name,
		url:         strings.TrimSpace(cfg.URL),
		adapter:     adapter,
		target:      strings.TrimSpace(cfg.Target),
		mentions:    webhookMentionsFromConfig(cfg.Mentions),
		events:      events,
		providers:   providers,
		statusCodes: statusCodes,
		timeout:     time.Duration(timeoutSeconds) * time.Second,
		retry:       retry,
		dedupe:      time.Duration(dedupeSeconds) * time.Second,
	}, true
}

func webhookMentionsFromConfig(mentions []string) []string {
	out := make([]string, 0, len(mentions))
	for _, mention := range mentions {
		id := strings.TrimSpace(mention)
		if id == "" {
			continue
		}
		out = append(out, id)
	}
	return out
}

func (d *webhookDispatcher) publishPayload(payload []byte) {
	if len(payload) == 0 {
		return
	}

	var event Event
	if err := json.Unmarshal(payload, &event); err != nil {
		log.WithError(err).Warn("notification webhook skipped malformed event")
		return
	}
	event = normalizeEvent(event)
	payload = mergeEventFieldsIntoPayload(payload, event)
	d.publishEventWithPayload(payload, event)
}

func (d *webhookDispatcher) publishEvent(event Event) {
	event = normalizeEvent(event)
	payload, err := json.Marshal(event)
	if err != nil {
		log.WithError(err).Warn("notification webhook skipped malformed event")
		return
	}
	d.publishEventWithPayload(payload, event)
}

func (d *webhookDispatcher) publishEventWithPayload(payload []byte, event Event) {
	event = normalizeEvent(event)
	payload = bytes.TrimSpace(payload)
	if len(payload) == 0 {
		return
	}

	deliveries := d.matchDeliveries(payload, event, time.Now())
	for _, delivery := range deliveries {
		select {
		case d.queue <- delivery:
		default:
			log.WithField("webhook", delivery.hook.name).Warn("notification webhook queue full; dropping event")
		}
	}
}

func normalizeEvent(event Event) Event {
	event.Type = strings.ToLower(strings.TrimSpace(event.Type))
	if event.Type == "" {
		event.Type = inferEventType(event)
	}
	event.Severity = strings.ToLower(strings.TrimSpace(event.Severity))
	if event.Severity == "" {
		event.Severity = SeverityError
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}
	event.Provider = strings.TrimSpace(event.Provider)
	event.Model = strings.TrimSpace(event.Model)
	event.AuthID = strings.TrimSpace(event.AuthID)
	event.AuthIndex = strings.TrimSpace(event.AuthIndex)
	event.AccountType = strings.TrimSpace(event.AccountType)
	event.Account = strings.TrimSpace(event.Account)
	event.Body = strings.TrimSpace(event.Body)
	event.Message = strings.TrimSpace(event.Message)
	if event.Message == "" {
		event.Message = event.Body
	}
	event.Code = strings.TrimSpace(event.Code)
	event.ServiceURL = strings.TrimRight(strings.TrimSpace(event.ServiceURL), "/")
	if event.ServiceURL == "" {
		event.ServiceURL = CurrentServiceURL()
	}
	return event
}

func mergeEventFieldsIntoPayload(payload []byte, event Event) []byte {
	var body map[string]any
	if err := json.Unmarshal(payload, &body); err != nil {
		return payload
	}
	setStringIfMissing(body, "type", event.Type)
	setStringIfMissing(body, "severity", event.Severity)
	setStringIfMissing(body, "message", event.Message)
	setStringIfMissing(body, "service_url", event.ServiceURL)
	updated, err := json.Marshal(body)
	if err != nil {
		return payload
	}
	return updated
}

func setStringIfMissing(body map[string]any, key, value string) {
	if strings.TrimSpace(value) == "" {
		return
	}
	if _, ok := body[key]; ok {
		return
	}
	body[key] = value
}

func inferEventType(event Event) string {
	if event.StatusCode == http.StatusUnauthorized {
		return EventAuthRequestUnauthorized
	}
	return EventAuthRequestFailed
}

func (d *webhookDispatcher) matchDeliveries(payload []byte, event Event, now time.Time) []webhookDelivery {
	d.mu.Lock()
	defer d.mu.Unlock()

	if len(d.hooks) == 0 {
		return nil
	}
	d.pruneDedupeLocked(now)

	deliveries := make([]webhookDelivery, 0, len(d.hooks))
	for _, hook := range d.hooks {
		if !hook.matches(event) {
			continue
		}
		if hook.dedupe > 0 {
			key := hook.dedupeKey(event)
			if last, ok := d.lastSent[key]; ok && now.Sub(last) < hook.dedupe {
				continue
			}
			d.lastSent[key] = now
		}
		deliveries = append(deliveries, webhookDelivery{
			hook:    hook,
			payload: bytes.Clone(payload),
			event:   event,
		})
	}
	return deliveries
}

func (h webhook) matches(event Event) bool {
	if len(h.events) == 0 {
		return false
	}
	if _, ok := h.events[EventAll]; !ok {
		if !h.matchesEvent(event.Type) {
			return false
		}
	}
	if len(h.providers) > 0 {
		if _, ok := h.providers[strings.ToLower(strings.TrimSpace(event.Provider))]; !ok {
			return false
		}
	}
	if len(h.statusCodes) > 0 {
		if _, ok := h.statusCodes[event.StatusCode]; !ok {
			return false
		}
	}
	return true
}

func (h webhook) matchesEvent(eventType string) bool {
	eventType = strings.ToLower(strings.TrimSpace(eventType))
	if eventType == "" {
		return false
	}
	if _, ok := h.events[eventType]; ok {
		return true
	}
	// Treat auth.request_failed as the parent request-failure subscription.
	// auth.request_unauthorized remains available for users who want only 401s.
	_, hasRequestFailed := h.events[EventAuthRequestFailed]
	return eventType == EventAuthRequestUnauthorized && hasRequestFailed
}

func (d *webhookDispatcher) pruneDedupeLocked(now time.Time) {
	if len(d.lastSent) == 0 {
		return
	}
	if !d.lastPrune.IsZero() && now.Sub(d.lastPrune) < webhookDedupePruneInterval {
		return
	}
	d.lastPrune = now

	maxDedupe := time.Duration(0)
	for _, hook := range d.hooks {
		if hook.dedupe > maxDedupe {
			maxDedupe = hook.dedupe
		}
	}
	if maxDedupe <= 0 {
		for key := range d.lastSent {
			delete(d.lastSent, key)
		}
		return
	}
	cutoff := now.Add(-maxDedupe)
	for key, sentAt := range d.lastSent {
		if !sentAt.After(cutoff) {
			delete(d.lastSent, key)
		}
	}
}

func (h webhook) dedupeKey(event Event) string {
	authKey := strings.TrimSpace(event.AuthIndex)
	if authKey == "" {
		authKey = strings.TrimSpace(event.AuthID)
	}
	return fmt.Sprintf("%s|%s|%s|%d|%s", h.name, event.Type, authKey, event.StatusCode, strings.TrimSpace(event.Code))
}

func (d *webhookDispatcher) worker() {
	for delivery := range d.queue {
		d.send(delivery)
	}
}

func (d *webhookDispatcher) send(delivery webhookDelivery) {
	body, contentType, err := formatWebhookBody(delivery)
	if err != nil {
		log.WithError(err).WithField("webhook", delivery.hook.name).Warn("notification webhook skipped invalid body")
		return
	}

	attempts := delivery.hook.retry + 1
	for attempt := 0; attempt < attempts; attempt++ {
		err = d.sendOnce(delivery.hook, body, contentType)
		if err == nil {
			return
		}
		if attempt+1 < attempts {
			time.Sleep(time.Duration(attempt+1) * 250 * time.Millisecond)
		}
	}
	log.WithError(err).WithField("webhook", delivery.hook.name).Warn("notification webhook delivery failed")
}

func (d *webhookDispatcher) sendOnce(hook webhook, body []byte, contentType string) error {
	timeout := hook.timeout
	if timeout <= 0 {
		timeout = time.Duration(config.DefaultNotificationWebhookTimeoutSeconds) * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, hook.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("User-Agent", webhookUserAgent)

	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return nil
}

func formatWebhookBody(delivery webhookDelivery) ([]byte, string, error) {
	switch strings.ToLower(strings.TrimSpace(delivery.hook.adapter)) {
	case "", "generic", "json":
		return delivery.payload, "application/json", nil
	case "slack":
		return formatSlackWebhookBody(delivery.event)
	case "feishu", "lark":
		return formatFeishuWebhookBody(delivery.event, delivery.hook.mentions...)
	case "telegram":
		if strings.TrimSpace(delivery.hook.target) == "" {
			return nil, "", fmt.Errorf("telegram webhook target is required")
		}
		return formatTelegramWebhookBody(delivery.hook.target, delivery.event)
	default:
		return delivery.payload, "application/json", nil
	}
}

func formatSlackWebhookBody(event Event) ([]byte, string, error) {
	event = normalizeEvent(event)
	fields := []any{
		slackTextField("Event", event.Type),
		slackTextField("Severity", event.Severity),
		slackTextField("Provider", event.Provider),
		slackTextField("Model", event.Model),
		slackTextField("Auth", firstNonEmpty(event.AuthIndex, event.AuthID)),
		slackTextField("Account", event.Account),
	}
	if event.StatusCode > 0 {
		fields = append(fields, slackTextField("Status", fmt.Sprintf("%d", event.StatusCode)))
	}
	if event.Code != "" {
		fields = append(fields, slackTextField("Code", event.Code))
	}

	blocks := []any{
		map[string]any{
			"type": "header",
			"text": map[string]string{
				"type": "plain_text",
				"text": cardTitle(event),
			},
		},
		map[string]any{
			"type":   "section",
			"fields": fields,
		},
	}
	if event.Message != "" {
		blocks = append(blocks, map[string]any{
			"type": "section",
			"text": map[string]string{
				"type": "mrkdwn",
				"text": "*Message*\n" + escapeSlackMrkdwn(event.Message),
			},
		})
	}
	if actions := slackCardActions(event.ServiceURL); len(actions) > 0 {
		blocks = append(blocks, map[string]any{
			"type":     "actions",
			"elements": actions,
		})
	}

	body := map[string]any{
		"text":   cardTitle(event),
		"blocks": blocks,
	}
	payload, err := json.Marshal(body)
	return payload, "application/json", err
}

func slackTextField(label, value string) map[string]string {
	value = strings.TrimSpace(value)
	if value == "" {
		value = "-"
	}
	return map[string]string{
		"type": "mrkdwn",
		"text": fmt.Sprintf("*%s*\n%s", label, escapeSlackMrkdwn(value)),
	}
}

func escapeSlackMrkdwn(value string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return replacer.Replace(value)
}

func slackCardActions(serviceURL string) []any {
	serviceURL = strings.TrimRight(strings.TrimSpace(serviceURL), "/")
	if serviceURL == "" {
		return nil
	}
	actions := []any{
		slackButton("Open Service", serviceURL, "primary"),
	}
	if managementURL := managementPanelURL(serviceURL); managementURL != "" && managementURL != serviceURL {
		actions = append(actions, slackButton("Management Panel", managementURL, ""))
	}
	return actions
}

func slackButton(label, targetURL, style string) map[string]any {
	button := map[string]any{
		"type": "button",
		"text": map[string]string{
			"type": "plain_text",
			"text": label,
		},
		"url": targetURL,
	}
	if style != "" {
		button["style"] = style
	}
	return button
}

func formatTelegramWebhookBody(target string, event Event) ([]byte, string, error) {
	event = normalizeEvent(event)
	body := map[string]any{
		"chat_id":                  strings.TrimSpace(target),
		"text":                     formatWebhookText(event),
		"disable_web_page_preview": true,
	}
	if keyboard := telegramInlineKeyboard(event.ServiceURL); len(keyboard) > 0 {
		body["reply_markup"] = map[string]any{
			"inline_keyboard": keyboard,
		}
	}
	payload, err := json.Marshal(body)
	return payload, "application/json", err
}

func telegramInlineKeyboard(serviceURL string) [][]map[string]string {
	serviceURL = strings.TrimRight(strings.TrimSpace(serviceURL), "/")
	if serviceURL == "" {
		return nil
	}
	row := []map[string]string{
		{"text": "Open Service", "url": serviceURL},
	}
	if managementURL := managementPanelURL(serviceURL); managementURL != "" && managementURL != serviceURL {
		row = append(row, map[string]string{"text": "Management Panel", "url": managementURL})
	}
	return [][]map[string]string{row}
}

func formatFeishuWebhookBody(event Event, mentions ...string) ([]byte, string, error) {
	event = normalizeEvent(event)
	fields := []any{
		feishuCardField("Event", event.Type),
		feishuCardField("Severity", event.Severity),
		feishuCardField("Provider", event.Provider),
		feishuCardField("Model", event.Model),
		feishuCardField("Auth", firstNonEmpty(event.AuthIndex, event.AuthID)),
		feishuCardField("Account", event.Account),
	}
	if event.StatusCode > 0 {
		fields = append(fields, feishuCardField("Status", fmt.Sprintf("%d", event.StatusCode)))
	}
	if event.Code != "" {
		fields = append(fields, feishuCardField("Code", event.Code))
	}

	elements := make([]any, 0, 4)
	if mentionText := feishuMentionText(mentions); mentionText != "" {
		elements = append(elements, map[string]any{
			"tag": "div",
			"text": map[string]string{
				"tag":     "lark_md",
				"content": "**Notify**\n" + mentionText,
			},
		})
	}
	elements = append(elements, map[string]any{
		"tag":    "div",
		"fields": fields,
	})
	if event.Message != "" {
		elements = append(elements, map[string]any{
			"tag": "div",
			"text": map[string]string{
				"tag":     "plain_text",
				"content": "Message\n" + event.Message,
			},
		})
	}
	if actions := feishuCardActions(event.ServiceURL); len(actions) > 0 {
		elements = append(elements, map[string]any{
			"tag":     "action",
			"actions": actions,
		})
	}

	body := map[string]any{
		"msg_type": "interactive",
		"card": map[string]any{
			"config": map[string]bool{
				"wide_screen_mode": true,
			},
			"header": map[string]any{
				"template": feishuCardTemplate(event.Severity),
				"title": map[string]string{
					"tag":     "plain_text",
					"content": cardTitle(event),
				},
			},
			"elements": elements,
		},
	}
	payload, err := json.Marshal(body)
	return payload, "application/json", err
}

func feishuMentionText(mentions []string) string {
	parts := make([]string, 0, len(mentions))
	for _, mention := range mentions {
		id := safeFeishuMentionID(mention)
		if id == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf("<at id=%s></at>", id))
	}
	return strings.Join(parts, " ")
}

func safeFeishuMentionID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '-' || r == '.' || r == '@':
		default:
			return ""
		}
	}
	return id
}

func feishuCardField(label, value string) map[string]any {
	value = strings.TrimSpace(value)
	if value == "" {
		value = "-"
	}
	return map[string]any{
		"is_short": true,
		"text": map[string]string{
			"tag":     "plain_text",
			"content": fmt.Sprintf("%s\n%s", label, value),
		},
	}
}

func feishuCardActions(serviceURL string) []any {
	serviceURL = strings.TrimRight(strings.TrimSpace(serviceURL), "/")
	if serviceURL == "" {
		return nil
	}
	actions := []any{
		feishuCardButton("Open Service", serviceURL, "primary"),
	}
	if managementURL := managementPanelURL(serviceURL); managementURL != "" && managementURL != serviceURL {
		actions = append(actions, feishuCardButton("Management Panel", managementURL, "default"))
	}
	return actions
}

func feishuCardButton(label, targetURL, buttonType string) map[string]any {
	return map[string]any{
		"tag": "button",
		"text": map[string]string{
			"tag":     "plain_text",
			"content": label,
		},
		"url":  targetURL,
		"type": buttonType,
	}
}

func cardTitle(event Event) string {
	switch event.Type {
	case EventAuthRefreshFailed:
		return "CLIProxyAPI OAuth refresh failed"
	case EventAuthRequestUnauthorized:
		return "CLIProxyAPI unauthorized request"
	case EventAuthRequestFailed:
		return "CLIProxyAPI request failed"
	case EventAuthManagementRequestFailed:
		return "CLIProxyAPI management request failed"
	default:
		return "CLIProxyAPI notification"
	}
}

func feishuCardTemplate(severity string) string {
	switch strings.ToLower(strings.TrimSpace(severity)) {
	case SeverityError:
		return "red"
	default:
		return "blue"
	}
}

func formatWebhookText(event Event) string {
	event = normalizeEvent(event)
	var b strings.Builder
	b.WriteString("CLIProxyAPI notification")
	appendTextField(&b, "type", event.Type)
	appendTextField(&b, "severity", event.Severity)
	appendTextField(&b, "provider", event.Provider)
	appendTextField(&b, "model", event.Model)
	appendTextField(&b, "auth_index", event.AuthIndex)
	appendTextField(&b, "auth_id", event.AuthID)
	appendTextField(&b, "account", event.Account)
	if event.StatusCode > 0 {
		appendTextField(&b, "status_code", fmt.Sprintf("%d", event.StatusCode))
	}
	appendTextField(&b, "code", event.Code)
	appendTextField(&b, "message", event.Message)
	appendTextField(&b, "service_url", event.ServiceURL)
	return b.String()
}

func managementPanelURL(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	parsed.Path = "/management.html"
	parsed.RawQuery = ""
	parsed.Fragment = "/quota"
	return parsed.String()
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func appendTextField(b *strings.Builder, label, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	b.WriteByte('\n')
	b.WriteString(label)
	b.WriteString(": ")
	b.WriteString(value)
}
