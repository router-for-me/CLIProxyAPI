package notify

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/prox/router"
)

func testCfg() Config {
	c := Default()
	c.Timezone = "UTC"
	if err := c.validate(); err != nil {
		panic(err)
	}
	return c
}

func acct(id string, ws ...router.WindowView) router.AccountView {
	return router.AccountView{ID: id, Provider: "claude", Display: "someone.ab@example.com", Windows: ws}
}

func win(kind string, remaining float64, resets time.Time) router.WindowView {
	r := resets
	return router.WindowView{Key: kind, Kind: kind, Remaining: remaining, Used: 100 - remaining, ResetsAt: &r}
}

func titles(ms []Message) []string {
	var out []string
	for _, m := range ms {
		out = append(out, m.Title)
	}
	return out
}

func TestThresholdAlertsOncePerLevel(t *testing.T) {
	cfg := testCfg()
	e := NewEvaluator()
	now := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	reset := now.Add(72 * time.Hour)
	if ms := e.Evaluate(cfg, []router.AccountView{acct("a", win("weekly", 80, reset))}, now); len(ms) != 0 {
		t.Fatalf("first sighting must be silent: %v", titles(ms))
	}
	ms := e.Evaluate(cfg, []router.AccountView{acct("a", win("weekly", 48, reset))}, now)
	if len(ms) != 1 || !strings.HasPrefix(ms[0].Title, "🟡 Claude · 7d 48% | 03d") {
		t.Fatalf("expected notice alert, got %v", titles(ms))
	}
	if ms := e.Evaluate(cfg, []router.AccountView{acct("a", win("weekly", 45, reset))}, now); len(ms) != 0 {
		t.Fatalf("same level must not repeat: %v", titles(ms))
	}
	ms = e.Evaluate(cfg, []router.AccountView{acct("a", win("weekly", 9, reset))}, now)
	if len(ms) != 1 || !strings.HasPrefix(ms[0].Title, "🔴") {
		t.Fatalf("expected critical alert, got %v", titles(ms))
	}
}

func TestRecoveryAfterExhaustedFiveHour(t *testing.T) {
	cfg := testCfg()
	e := NewEvaluator()
	now := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	reset := now.Add(time.Hour)
	e.Evaluate(cfg, []router.AccountView{acct("a", win("five_hour", 30, reset))}, now)
	e.Evaluate(cfg, []router.AccountView{acct("a", win("five_hour", 0, reset))}, now.Add(50*time.Minute))
	after := reset.Add(time.Minute)
	w := router.WindowView{Key: "five_hour", Kind: "five_hour", Remaining: 100}
	ms := e.Evaluate(cfg, []router.AccountView{acct("a", w)}, after)
	if len(ms) != 1 || !strings.Contains(ms[0].Title, "5h reset") || !strings.Contains(ms[0].Body, "ended with 0% left") {
		t.Fatalf("expected recovery, got %+v", ms)
	}
}

func TestNoRecoveryForUnusedCycle(t *testing.T) {
	cfg := testCfg()
	e := NewEvaluator()
	now := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	reset := now.Add(time.Hour)
	e.Evaluate(cfg, []router.AccountView{acct("a", win("weekly", 100, reset))}, now)
	ms := e.Evaluate(cfg, []router.AccountView{acct("a", win("weekly", 100, reset.Add(7*24*time.Hour)))}, reset.Add(time.Minute))
	if len(ms) != 0 {
		t.Fatalf("unused cycle must not notify: %v", titles(ms))
	}
}

func TestEarlyResetAndReminder(t *testing.T) {
	cfg := testCfg()
	e := NewEvaluator()
	now := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	reset := now.Add(3 * 24 * time.Hour)
	e.Evaluate(cfg, []router.AccountView{acct("a", win("weekly", 15, reset))}, now)
	ms := e.Evaluate(cfg, []router.AccountView{acct("a", win("weekly", 100, reset))}, now)
	if len(ms) != 1 || !strings.Contains(ms[0].Title, "restored early") {
		t.Fatalf("expected early reset, got %v", titles(ms))
	}
	later := reset.Add(-20 * time.Hour)
	ms = e.Evaluate(cfg, []router.AccountView{acct("a", win("weekly", 60, reset))}, later)
	if len(ms) != 1 || !strings.HasPrefix(ms[0].Title, "⏰") {
		t.Fatalf("expected reminder, got %v", titles(ms))
	}
	if ms := e.Evaluate(cfg, []router.AccountView{acct("a", win("weekly", 60, reset))}, later.Add(time.Hour)); len(ms) != 0 {
		t.Fatalf("reminder must fire once: %v", titles(ms))
	}
}

func TestPlanChangeAndCredential(t *testing.T) {
	cfg := testCfg()
	e := NewEvaluator()
	now := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	a := acct("a")
	a.Plan = "max_5x"
	e.Evaluate(cfg, []router.AccountView{a}, now)
	a.Plan = "pro"
	if ms := e.Evaluate(cfg, []router.AccountView{a}, now); len(ms) != 1 || !strings.Contains(ms[0].Title, "max_5x → pro") {
		t.Fatalf("expected plan change, got %v", titles(ms))
	}
	a.PollError = "quota_http_401"
	var got []Message
	for i := 0; i < 4; i++ {
		ts := now.Add(time.Duration(i) * 15 * time.Minute)
		a.LastPollAt = &ts
		got = append(got, e.Evaluate(cfg, []router.AccountView{a}, ts)...)
	}
	if len(got) != 1 || !strings.HasPrefix(got[0].Title, "🔑") {
		t.Fatalf("expected one credential alert, got %v", titles(got))
	}
	a.PollError = ""
	ts := now.Add(2 * time.Hour)
	a.LastPollAt = &ts
	if ms := e.Evaluate(cfg, []router.AccountView{a}, ts); len(ms) != 1 || !strings.Contains(ms[0].Title, "works again") {
		t.Fatalf("expected credential recovery, got %v", titles(ms))
	}
}

func TestLabels(t *testing.T) {
	accts := []router.AccountView{
		{ID: "1", Provider: "claude", Display: "jane.rk@example.com"},
		{ID: "2", Provider: "claude", Display: "x@example.com", Alias: "main"},
		{ID: "3", Provider: "codex", Display: "solo@example.com"},
	}
	l := Labels(accts)
	if l["1"] != "Claude#rk" || l["2"] != "Claude#main" || l["3"] != "GPT" {
		t.Fatalf("labels: %v", l)
	}
}

func TestSendBarkAndWebhook(t *testing.T) {
	var barkBody, hookBody map[string]any
	bark := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &barkBody)
		_, _ = w.Write([]byte(`{"code":200}`))
	}))
	defer bark.Close()
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &hookBody)
		if r.Header.Get("Authorization") != "Bearer t" {
			w.WriteHeader(401)
			return
		}
		_, _ = w.Write([]byte(`{"code":0}`))
	}))
	defer hook.Close()
	cfg := testCfg()
	cfg.BarkURL = bark.URL + "/devicekey"
	cfg.Webhook = Webhook{URL: hook.URL, Method: "POST", Headers: map[string]string{"Authorization": "Bearer t"},
		Body: `{"msg_type":"text","content":{"text":"{{text}}"}}`, SuccessJSON: map[string]any{"code": 0}}
	if err := cfg.validate(); err != nil {
		t.Fatal(err)
	}
	ok, errs := Sender{}.Send(context.Background(), cfg, Message{Title: `🟡 "q"`, Body: "line1\nline2", Level: "warning"})
	if !ok || len(errs) != 0 {
		t.Fatalf("send failed: %v", errs)
	}
	if barkBody["title"] != `🟡 "q"` || barkBody["group"] != "prox" {
		t.Fatalf("bark body: %v", barkBody)
	}
	content, _ := hookBody["content"].(map[string]any)
	if content["text"] != "🟡 \"q\"\n\nline1\nline2" {
		t.Fatalf("webhook body: %v", hookBody)
	}
}

func TestDecodeValidation(t *testing.T) {
	if _, err := Decode([]byte("notify:\n  thresholds: [20, 50, 10]\n")); err == nil {
		t.Fatal("expected descending threshold error")
	}
	if _, err := Decode([]byte("notify:\n  webhook:\n    url: https://x\n    method: GET\n    body: hi\n")); err == nil {
		t.Fatal("expected GET body error")
	}
	cfg, err := Decode([]byte("notify:\n  bark_url: https://api.day.app/key\n"))
	if err != nil || !cfg.Enabled() || cfg.Recovery.Weekly != PolicyAll {
		t.Fatalf("decode: %+v %v", cfg, err)
	}
}
