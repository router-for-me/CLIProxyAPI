package notify

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"time"

	"github.com/MoeForge/moego/json"
)

// Message is one notification.
type Message struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	// Level is "info", "warning" or "critical".
	Level string `json:"level"`
}

// Text joins the title and body.
func (m Message) Text() string {
	if m.Body == "" {
		return m.Title
	}
	return m.Title + "\n\n" + m.Body
}

// Sender delivers messages to every configured channel.
type Sender struct {
	Client *http.Client
}

// Send delivers m. It succeeds when at least one channel accepts it and
// returns the errors of channels that failed.
func (s Sender) Send(ctx context.Context, cfg Config, m Message) (delivered bool, errs []error) {
	// Notification endpoints are third-party side channels, not upstream
	// model traffic; bound each delivery so a hung endpoint cannot pile up.
	ctx, cancel := context.WithTimeout(ctx, time.Duration(cfg.TimeoutSeconds)*time.Second)
	defer cancel()
	if cfg.BarkURL != "" {
		if err := s.bark(ctx, cfg, m); err != nil {
			errs = append(errs, fmt.Errorf("bark: %w", err))
		} else {
			delivered = true
		}
	}
	if cfg.Webhook.URL != "" {
		if err := s.webhook(ctx, cfg.Webhook, m); err != nil {
			errs = append(errs, fmt.Errorf("webhook: %w", err))
		} else {
			delivered = true
		}
	}
	return delivered, errs
}

func (s Sender) client() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	return http.DefaultClient
}

func (s Sender) bark(ctx context.Context, cfg Config, m Message) error {
	payload := map[string]any{"title": m.Title, "body": m.Body}
	if cfg.BarkGroup != "" {
		payload["group"] = cfg.BarkGroup
	}
	if cfg.BarkIcon != "" {
		payload["icon"] = cfg.BarkIcon
	}
	if m.Level == "critical" {
		payload["level"] = "timeSensitive"
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(cfg.BarkURL, "/"), bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := s.client().Do(req)
	if err != nil {
		return errors.New("request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("http %d", resp.StatusCode)
	}
	var out struct {
		Code *int `json:"code"`
	}
	if json.Unmarshal(body, &out) == nil && out.Code != nil && *out.Code != 200 {
		return fmt.Errorf("bark code %d", *out.Code)
	}
	return nil
}

func (s Sender) webhook(ctx context.Context, w Webhook, m Message) error {
	var body []byte
	contentType := ""
	if w.Method != http.MethodGet {
		if strings.TrimSpace(w.Body) == "" {
			raw, err := json.Marshal(map[string]any{"title": m.Title, "body": m.Body, "text": m.Text(), "level": m.Level, "source": "prox"})
			if err != nil {
				return err
			}
			body, contentType = raw, "application/json; charset=utf-8"
		} else {
			body = []byte(RenderTemplate(w.Body, m))
			if json.Valid(body) {
				contentType = "application/json; charset=utf-8"
			} else {
				contentType = "text/plain; charset=utf-8"
			}
		}
	}
	target := w.URL
	if w.Method == http.MethodGet {
		target = renderURL(target, m)
	}
	req, err := http.NewRequestWithContext(ctx, w.Method, target, bytes.NewReader(body))
	if err != nil {
		return err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for k, v := range w.Headers {
		req.Header.Set(k, v)
	}
	resp, err := s.client().Do(req)
	if err != nil {
		return errors.New("request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("http %d", resp.StatusCode)
	}
	if len(w.SuccessJSON) == 0 {
		return nil
	}
	var got map[string]any
	if json.Unmarshal(respBody, &got) != nil {
		return errors.New("response is not a JSON object")
	}
	for k, want := range w.SuccessJSON {
		if !sameJSONValue(got[k], want) {
			return fmt.Errorf("response field %q did not match", k)
		}
	}
	return nil
}

// sameJSONValue compares a decoded JSON value with a YAML-configured one,
// treating all numbers as float64.
func sameJSONValue(got, want any) bool {
	norm := func(v any) any {
		switch n := v.(type) {
		case int:
			return float64(n)
		case int64:
			return float64(n)
		case uint64:
			return float64(n)
		case float32:
			return float64(n)
		}
		return v
	}
	return reflect.DeepEqual(norm(got), norm(want))
}

// RenderTemplate fills {{title}}, {{body}}, {{text}} and {{level}}. Values
// are JSON-escaped when the template is a JSON document.
func RenderTemplate(tpl string, m Message) string {
	trimmed := strings.TrimSpace(tpl)
	jsonish := strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[")
	esc := func(s string) string {
		if !jsonish {
			return s
		}
		raw, _ := json.Marshal(s)
		return string(raw[1 : len(raw)-1])
	}
	r := strings.NewReplacer("{{title}}", esc(m.Title), "{{body}}", esc(m.Body), "{{text}}", esc(m.Text()), "{{level}}", esc(m.Level))
	return r.Replace(tpl)
}

func renderURL(u string, m Message) string {
	q := func(s string) string { return strings.ReplaceAll(url.QueryEscape(s), "+", "%20") }
	r := strings.NewReplacer("{{title}}", q(m.Title), "{{body}}", q(m.Body), "{{text}}", q(m.Text()), "{{level}}", q(m.Level))
	return r.Replace(u)
}
