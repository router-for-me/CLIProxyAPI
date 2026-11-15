//go:build proxxe2e

// Package pricinge2e runs the real proxx binary against a mock upstream and checks
// per-request cost resolution, pricing in model listings, the proxx usage/pricing management
// endpoints, snapshot persistence across a restart, and hot reload of prices.
//
// Run: go test -tags proxxe2e -count=1 -v ./test/proxx/pricing_e2e/
package pricinge2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
)

const (
	clientKey = "e2e-client-key-0123456789"
	mgmtKey   = "e2e-mgmt-secret"
	// OpenAI-shaped usage: prompt 1100 (includes 1000 cached), completion 500 (includes 200 reasoning).
	openAIUsage = `"prompt_tokens":1100,"completion_tokens":500,"total_tokens":1600,` +
		`"prompt_tokens_details":{"cached_tokens":1000},"completion_tokens_details":{"reasoning_tokens":200}`
)

func repoRoot(t *testing.T) string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}

// mockUpstream serves OpenAI chat completions and Anthropic messages.
func mockUpstream() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		model := ojson.GetBytes(body, "model").String()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/chat/completions") && ojson.GetBytes(body, "stream").Bool():
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":%q,"+
				"\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hi\"},\"finish_reason\":null}]}\n\n", model)
			fmt.Fprintf(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":%q,"+
				"\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{%s,\"cost\":0.0321}}\n\n", model, openAIUsage)
			fmt.Fprint(w, "data: [DONE]\n\n")
		case strings.HasSuffix(r.URL.Path, "/chat/completions"):
			extra := ""
			switch model {
			case "mock-cost", "mock-override":
				extra = `,"cost":0.0123`
			case "mock-ticks":
				extra = `,"cost_in_usd_ticks":4560000000`
			case "mock-header":
				w.Header().Set("X-Litellm-Response-Cost", "0.0789")
			}
			fmt.Fprintf(w, `{"id":"c1","object":"chat.completion","created":1,"model":%q,`+
				`"choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],`+
				`"usage":{%s%s}}`, model, openAIUsage, extra)
		case strings.HasSuffix(r.URL.Path, "/messages"):
			// Anthropic-shaped usage: input_tokens EXCLUDES cache reads/writes.
			fmt.Fprintf(w, `{"id":"msg_1","type":"message","role":"assistant","model":%q,`+
				`"content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","stop_sequence":null,`+
				`"usage":{"input_tokens":100,"cache_read_input_tokens":1000,"cache_creation_input_tokens":200,"output_tokens":50}}`, model)
		default:
			http.NotFound(w, r)
		}
	}))
}

func configYAML(port int, upstream, dir string, reloaded bool, recentLimit int) string {
	mockConfigInput := "1"
	extra := ""
	override := "false"
	if reloaded {
		mockConfigInput = "3"
		override = "true"
		extra = `
      - model: mock-ticks
        input: 1
        output: 1
        cache-read: 1`
	}
	return fmt.Sprintf(`config-version: 8
server:
  host: "127.0.0.1"
  port: %d
auth-dir: %q
management:
  allow-remote: false
  secret-key: %q
  disable-control-panel: true
access:
  api-keys:
    - %q
observability:
  usage:
    usage-statistics-enabled: false
api-keys:
  claude:
    - name: mock-claude
      base-url: %q
      models:
        - name: claude-sonnet-4-5-20250929
      keys:
        - api-key: "mock-claude-key"
  openai-compatibility:
    - name: openrouter
      base-url: "%s/v1"
      keys:
        - api-key: "mock-or-key"
      models:
        - name: mock-cost
        - name: mock-ticks
        - name: mock-header
        - name: mock-unknown
        - name: mock-config
        - name: mock-override
        - name: gpt-4o-mini
        - name: mock-stream
proxx:
  pricing:
    override-upstream: %s
    cost-headers: ["x-litellm-response-cost"]
    models:
      - model: mock-config
        input: %s
        output: 2
        cache-read: 0.5
      - model: mock-override
        input: 10
        output: 20
        override: true%s
  usage:
    recent-limit: %d
`, port, filepath.Join(dir, "auths"), mgmtKey, clientKey, upstream, upstream, override, mockConfigInput, extra, recentLimit)
}

type proc struct {
	cmd  *exec.Cmd
	base string
	log  *bytes.Buffer
}

func start(t *testing.T, bin, cfgPath string, port int) *proc {
	t.Helper()
	cmd := exec.Command(bin, "--config", cfgPath, "--local-model", "--no-browser")
	logBuf := &bytes.Buffer{}
	cmd.Stdout, cmd.Stderr = logBuf, logBuf
	cmd.Dir = filepath.Dir(cfgPath)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := &proc{cmd: cmd, base: fmt.Sprintf("http://127.0.0.1:%d", port), log: logBuf}
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if code, body := p.do(t, "GET", "/v1/models", "", nil); code == 200 && strings.Contains(body, "mock-cost") {
			return p
		}
		time.Sleep(200 * time.Millisecond)
	}
	p.stop(t)
	t.Fatalf("server did not become ready; log:\n%s", logBuf.String())
	return nil
}

func (p *proc) stop(t *testing.T) {
	t.Helper()
	if p.cmd.Process == nil {
		return
	}
	_ = p.cmd.Process.Signal(syscall.SIGINT)
	done := make(chan error, 1)
	go func() { done <- p.cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		_ = p.cmd.Process.Kill()
		<-done
		t.Errorf("server did not exit on SIGINT")
	}
}

func (p *proc) do(t *testing.T, method, path, body string, headers map[string]string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, p.base+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if strings.HasPrefix(path, "/v8/management") {
		req.Header.Set("Authorization", "Bearer "+mgmtKey)
	} else {
		req.Header.Set("Authorization", "Bearer "+clientKey)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(data)
}

func (p *proc) chat(t *testing.T, model string) {
	t.Helper()
	code, body := p.do(t, "POST", "/v1/chat/completions", fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}]}`, model), nil)
	if code != 200 {
		t.Fatalf("chat %s: %d %s\nlog:\n%s", model, code, body, p.log.String())
	}
}

type recentItem struct {
	Model      string   `json:"model"`
	Stream     bool     `json:"stream"`
	Provider   string   `json:"provider"`
	APIKey     string   `json:"api_key"`
	CostUSD    *float64 `json:"cost_usd"`
	CostSource string   `json:"cost_source"`
	Input      int64    `json:"input_tokens"`
	Cached     int64    `json:"cached_tokens"`
	Reasoning  int64    `json:"reasoning_tokens"`
}

func (p *proc) recent(t *testing.T) []recentItem {
	t.Helper()
	code, body := p.do(t, "GET", "/v8/management/proxx/usage/recent?limit=100", "", nil)
	if code != 200 {
		t.Fatalf("recent: %d %s", code, body)
	}
	var out struct {
		Items []recentItem `json:"items"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	return out.Items
}

// waitRecent polls until n items exist (usage delivery is asynchronous).
func (p *proc) waitRecent(t *testing.T, n int) []recentItem {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		items := p.recent(t)
		if len(items) >= n || time.Now().After(deadline) {
			if len(items) < n {
				t.Fatalf("want %d recent items, got %d: %+v", n, len(items), items)
			}
			return items
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func expectCost(t *testing.T, items []recentItem, model, source string, want *float64) {
	t.Helper()
	for _, it := range items { // newest first
		if it.Model != model {
			continue
		}
		if it.CostSource != source {
			t.Errorf("%s: cost_source = %q, want %q", model, it.CostSource, source)
		}
		switch {
		case want == nil && it.CostUSD != nil:
			t.Errorf("%s: cost_usd = %v, want null", model, *it.CostUSD)
		case want != nil && (it.CostUSD == nil || math.Abs(*it.CostUSD-*want) > 1e-12):
			t.Errorf("%s: cost_usd = %v, want %v", model, ptrStr(it.CostUSD), *want)
		default:
			t.Logf("ok %-30s source=%-8s cost=%s", model, it.CostSource, ptrStr(it.CostUSD))
		}
		return
	}
	t.Errorf("no recent item for %s", model)
}

func ptrStr(v *float64) string {
	if v == nil {
		return "null"
	}
	return fmt.Sprintf("%.10f", *v)
}

func f(v float64) *float64 { return &v }

func freePort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func modelPricing(t *testing.T, body, listPath, idKey, id string) ojson.Result {
	t.Helper()
	for _, m := range ojson.Get(body, listPath).Array() {
		if strings.TrimPrefix(m.Get(idKey).String(), "models/") == id {
			return m.Get("pricing")
		}
	}
	return ojson.Result{}
}

func TestPricingUsageE2E(t *testing.T) {
	root := repoRoot(t)
	dir := t.TempDir()
	bin := filepath.Join(dir, "proxx-pricing")
	build := exec.Command("go", "build", "-o", bin, "./cmd/server")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	up := mockUpstream()
	defer up.Close()
	port := freePort(t)
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(configYAML(port, up.URL, dir, false, 100)), 0o600); err != nil {
		t.Fatal(err)
	}

	p := start(t, bin, cfgPath, port)
	defer func() { p.stop(t) }()

	// 1. Per-request cost for each upstream shape.
	for _, m := range []string{"mock-cost", "mock-ticks", "mock-header", "mock-unknown", "mock-config", "mock-override", "gpt-4o-mini"} {
		p.chat(t, m)
	}
	code, body := p.do(t, "POST", "/v1/messages",
		`{"model":"claude-sonnet-4-5-20250929","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`,
		map[string]string{"anthropic-version": "2023-06-01"})
	if code != 200 {
		t.Fatalf("claude messages: %d %s\nlog:\n%s", code, body, p.log.String())
	}
	code, body = p.do(t, "POST", "/v1/chat/completions", `{"model":"mock-stream","stream":true,"messages":[{"role":"user","content":"hi"}]}`, nil)
	if code != 200 || !strings.Contains(body, "[DONE]") {
		t.Fatalf("stream: %d %s", code, body)
	}
	items := p.waitRecent(t, 9)
	expectCost(t, items, "mock-stream", "upstream", f(0.0321))
	for _, it := range items {
		if it.Model == "mock-stream" && !it.Stream {
			t.Errorf("mock-stream recent item not marked stream: %+v", it)
		}
	}
	expectCost(t, items, "mock-cost", "upstream", f(0.0123))
	expectCost(t, items, "mock-ticks", "upstream", f(0.456))
	expectCost(t, items, "mock-header", "header", f(0.0789))
	expectCost(t, items, "mock-unknown", "unknown", nil)
	// config: 100 uncached × 1 + 1000 cached × 0.5 + 500 output × 2 = 1600 per 1M
	expectCost(t, items, "mock-config", "config", f(1600.0/1e6))
	// override: true beats the upstream cost 0.0123; cache-read unset → input price.
	expectCost(t, items, "mock-override", "config", f((100*10+1000*10+500*20)/1e6))
	// catalog (embedded snapshot, openai gpt-4o-mini 0.15/0.6/0.075)
	expectCost(t, items, "gpt-4o-mini", "catalog", f((100*0.15+1000*0.075+500*0.6)/1e6))
	// Claude: input_tokens excludes cache → 100×3 + 1000×0.3 + 200×3.75 + 50×15 = 2100 per 1M
	expectCost(t, items, "claude-sonnet-4-5-20250929", "catalog", f(2100.0/1e6))
	for _, it := range items {
		if strings.Contains(it.APIKey, clientKey) || it.APIKey == "" {
			t.Errorf("api key not masked: %q", it.APIKey)
		}
		if it.Model == "mock-cost" && (it.Input != 1100 || it.Cached != 1000 || it.Reasoning != 200) {
			t.Errorf("token counts: %+v", it)
		}
	}

	// 2. Model listings carry pricing objects.
	_, body = p.do(t, "GET", "/v1/models", "", nil)
	pr := modelPricing(t, body, "data", "id", "mock-config")
	if pr.Get("input").Float() != 1 || pr.Get("currency").String() != "USD" || pr.Get("unit").String() != "per_1m_tokens" || pr.Get("source").String() != "config" {
		t.Errorf("openai listing pricing for mock-config = %s", pr.Raw)
	}
	if pr := modelPricing(t, body, "data", "id", "gpt-4o-mini"); pr.Get("source").String() != "catalog" || pr.Get("cache_read").Float() != 0.075 {
		t.Errorf("openai listing pricing for gpt-4o-mini = %s", pr.Raw)
	}
	if pr := modelPricing(t, body, "data", "id", "mock-unknown"); pr.Exists() {
		t.Errorf("unknown model must have no pricing object, got %s", pr.Raw)
	}
	_, body = p.do(t, "GET", "/v1/models", "", map[string]string{"anthropic-version": "2023-06-01"})
	if pr := modelPricing(t, body, "data", "id", "claude-sonnet-4-5-20250929"); pr.Get("cache_write").Float() != 3.75 {
		t.Errorf("claude listing pricing = %s (body %.300s)", pr.Raw, body)
	}
	_, body = p.do(t, "GET", "/v1beta/models", "", map[string]string{"x-goog-api-key": clientKey})
	if pr := modelPricing(t, body, "models", "name", "mock-config"); pr.Get("output").Float() != 2 {
		t.Errorf("gemini listing pricing = %s (body %.300s)", pr.Raw, body)
	}

	// 3. Management endpoints.
	code, body = p.do(t, "GET", "/v8/management/proxx/usage/summary?group_by=model", "", nil)
	if code != 200 || ojson.Get(body, "totals.requests").Int() != 9 || ojson.Get(body, "totals.unknown_cost_requests").Int() != 1 {
		t.Fatalf("summary: %d %s", code, body)
	}
	wantTotal := 0.0321 + 0.0123 + 0.456 + 0.0789 + 0.0016 + 0.021 + 0.00039 + 0.0021
	if got := ojson.Get(body, "totals.cost_usd").Float(); math.Abs(got-wantTotal) > 1e-9 {
		t.Errorf("summary cost_usd = %v, want %v", got, wantTotal)
	}
	t.Logf("summary: %s", body)
	for _, g := range []string{"provider", "api_key", "day"} {
		if code, b := p.do(t, "GET", "/v8/management/proxx/usage/summary?group_by="+g, "", nil); code != 200 || len(ojson.Get(b, "rows").Array()) == 0 {
			t.Errorf("summary group_by=%s: %d %s", g, code, b)
		}
	}
	if code, _ := p.do(t, "GET", "/v8/management/proxx/usage/summary?group_by=bogus", "", nil); code != 400 {
		t.Errorf("bad group_by status = %d", code)
	}
	code, body = p.do(t, "GET", "/v8/management/proxx/pricing", "", nil)
	if code != 200 || ojson.Get(body, "catalog_source").String() != "embed" || ojson.Get(body, "override_upstream").Bool() {
		t.Fatalf("pricing: %d %.500s", code, body)
	}
	sources := map[string]string{}
	for _, m := range ojson.Get(body, "models").Array() {
		sources[m.Get("model").String()] = m.Get("source").String()
	}
	if sources["mock-config"] != "config" || sources["gpt-4o-mini"] != "catalog" || sources["mock-unknown"] != "unknown" || sources["claude-sonnet-4-5-20250929"] != "catalog" {
		t.Errorf("pricing sources = %v", sources)
	}

	// 4. Persistence across a restart (SIGINT flushes the snapshot).
	_, before := p.do(t, "GET", "/v8/management/proxx/usage/summary", "", nil)
	p.stop(t)
	if _, err := os.Stat(filepath.Join(dir, "proxx-usage.json")); err != nil {
		t.Fatalf("snapshot file missing: %v\nlog:\n%s", err, p.log.String())
	}
	p = start(t, bin, cfgPath, port)
	_, after := p.do(t, "GET", "/v8/management/proxx/usage/summary", "", nil)
	if ojson.Get(before, "totals").Raw != ojson.Get(after, "totals").Raw || len(p.recent(t)) != 9 {
		t.Fatalf("totals changed across restart:\nbefore %s\nafter  %s", before, after)
	}
	t.Logf("persisted across restart: %s", ojson.Get(after, "totals").Raw)

	// 5. Hot reload: new mock-config price, override-upstream, and a config price for mock-ticks.
	if err := os.WriteFile(cfgPath, []byte(configYAML(port, up.URL, dir, true, 100)), 0o600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		_, body = p.do(t, "GET", "/v1/models", "", nil)
		if modelPricing(t, body, "data", "id", "mock-config").Get("input").Float() == 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("price reload not visible in /v1/models; log:\n%s", p.log.String())
		}
		time.Sleep(200 * time.Millisecond)
	}
	p.chat(t, "mock-config")
	p.chat(t, "mock-ticks")
	items = p.waitRecent(t, 11)
	expectCost(t, items, "mock-config", "config", f(1800.0/1e6))
	// override-upstream: the config price beats the reported 0.456 USD ticks cost.
	expectCost(t, items, "mock-ticks", "config", f(1600.0/1e6))
	if _, body = p.do(t, "GET", "/v8/management/proxx/pricing", "", nil); !ojson.Get(body, "override_upstream").Bool() {
		t.Errorf("override_upstream not reloaded: %.300s", body)
	}

	// 6. Hot reload of proxx.usage: recent-limit shrinks the ring buffer.
	if err := os.WriteFile(cfgPath, []byte(configYAML(port, up.URL, dir, true, 3)), 0o600); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(30 * time.Second)
	for len(p.recent(t)) > 3 {
		if time.Now().After(deadline) {
			t.Fatalf("recent-limit reload not applied; log:\n%s", p.log.String())
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Logf("recent-limit reload applied: %d items", len(p.recent(t)))

	// 7. Reset.
	if code, _ := p.do(t, "DELETE", "/v8/management/proxx/usage", "", nil); code != 200 {
		t.Fatalf("reset status %d", code)
	}
	if _, body = p.do(t, "GET", "/v8/management/proxx/usage/summary", "", nil); ojson.Get(body, "totals.requests").Int() != 0 {
		t.Fatalf("reset failed: %s", body)
	}
}
