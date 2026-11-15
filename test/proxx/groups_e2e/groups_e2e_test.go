package groupse2e

import (
	"fmt"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// providers maps an openai-compatibility provider name to its "upstream: alias" models.
// Every failing member gets its own provider so cooldowns never leak between scenarios.
var providers = []struct {
	name     string
	priority int
	models   [][2]string
}{
	{"pa", 10, [][2]string{{"up-a", "model-a"}, {"up-shared-a", "shared"}}},
	{"pb", 0, [][2]string{{"up-b", "model-b"}, {"up-shared-b", "shared"}}},
	{"pc", 0, [][2]string{{"up-c", "model-c"}}},
	{"p500", 0, [][2]string{{"up-500", "m500"}}},
	{"p429", 0, [][2]string{{"up-429", "m429"}}},
	{"p429long", 0, [][2]string{{"up-429long", "m429long"}}},
	{"psf", 0, [][2]string{{"up-sf", "mstreamfail"}}},
	{"psf2", 0, [][2]string{{"up-sf2", "mstreamfail2"}}},
	{"pbreak", 0, [][2]string{{"up-break", "mbreak"}}},
	{"p400", 0, [][2]string{{"up-400", "m400"}}},
	{"pctx", 0, [][2]string{{"up-ctx", "mctx"}}},
	{"pfree", 0, [][2]string{{"up-free-a", "free-a"}, {"up-free-b", "free-b"}}},
}

const baseGroups = `    - name: auto
      models: [model-a, model-b, model-c]
    - name: g500
      models: [m500, model-b]
    - name: g429
      models: [m429, model-c]
    - name: glong
      models: [m429long]
    - name: gstream
      models: [mstreamfail, model-a]
    - name: gstream2
      models: [mstreamfail2, model-a]
    - name: gbreak
      models: [mbreak, model-b]
    - name: g400
      models: [m400, model-b]
    - name: gctx
      models: [mctx, model-b]
    - name: gpayload
      models: [m500, model-b]
    - name: rr
      strategy: round-robin
      models: [free-a, free-b]
    - name: secret
      hidden: true
      models: [model-c]
`

func renderConfig(port int, upstream string, authDir string, groups string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `config-version: 8
server:
  host: 127.0.0.1
  port: %d
management:
  secret-key: %q
  disable-control-panel: true
access:
  api-keys: [%q]
oauth:
  auth-dir: %q
routing:
  retry:
    # Retry rounds are on (the default) so a cooldown wait inside a member would be visible
    # as latency; the 429 providers inherit them, the others disable them to keep hit order exact.
    request-retry: 3
    max-retry-interval: 60
requests:
  payload:
    override:
      - models:
          - name: model-b
        params:
          temperature: 0.123
api-keys:
  openai-compatibility:
`, port, mgmtKey, clientKey, authDir)
	for _, p := range providers {
		retry := 0
		if strings.HasPrefix(p.name, "p429") {
			retry = -1
		}
		fmt.Fprintf(&b, "    - name: %s\n      base-url: %s/%s/v1\n      priority: %d\n      request-retry: %d\n      keys:\n        - api-key: mock-key-%s\n      models:\n", p.name, upstream, p.name, p.priority, retry, p.name)
		for _, m := range p.models {
			fmt.Fprintf(&b, "        - name: %s\n          alias: %s\n", m[0], m[1])
		}
	}
	b.WriteString("proxx:\n  model-groups:\n")
	b.WriteString(groups)
	return b.String()
}

func TestE2EModelGroups(t *testing.T) {
	if os.Getenv("PROXX_E2E") != "1" {
		t.Skip("set PROXX_E2E=1 to run the proxx model-groups e2e test (builds and launches the proxy)")
	}
	dir := t.TempDir()
	bin := buildBinary(t, dir)
	mock := newMockUpstream()
	defer mock.srv.Close()

	port := freePort(t)
	authDir := dir + "/auths"
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := startProxy(t, bin, dir, renderConfig(port, mock.srv.URL, authDir, baseGroups))
	p.base = fmt.Sprintf("http://127.0.0.1:%d", port)
	waitFor(t, 60*time.Second, "proxy to list models", func() bool {
		ids := p.listModels()
		return contains(ids, "model-a") && contains(ids, "auto")
	})

	t.Run("existing behavior: shared alias across providers with priority", func(t *testing.T) {
		mark := mock.mark()
		for i := 0; i < 3; i++ {
			if res := p.chat("shared", false); res.Status != http.StatusOK {
				t.Fatalf("shared request %d: status %d %s", i, res.Status, res.Body)
			}
		}
		got := models(mock.since(mark))
		t.Logf("shared alias, pa priority 10 > pb priority 0: upstream hits %v", got)
		for _, m := range got {
			if m != "up-shared-a" {
				t.Fatalf("higher-priority provider should serve every request, got %v", got)
			}
		}
		mock.set("up-shared-a", "500")
		mark = mock.mark()
		res := p.chat("shared", false)
		got = models(mock.since(mark))
		t.Logf("shared alias with pa failing 500: status %d, upstream hits %v", res.Status, got)
		if res.Status != http.StatusOK || !reflect.DeepEqual(got, []string{"up-shared-a", "up-shared-b"}) {
			t.Fatalf("expected failover from pa to pb within one request, status %d hits %v body %s", res.Status, got, res.Body)
		}
		mark = mock.mark()
		res = p.chat("shared", false)
		got = models(mock.since(mark))
		t.Logf("shared alias right after the 500: status %d, upstream hits %v", res.Status, got)
		mock.set("up-shared-a", "ok")
	})

	t.Run("non-group request is unaffected", func(t *testing.T) {
		mark := mock.mark()
		res := p.chat("model-c", false)
		if res.Status != http.StatusOK || chatContent(res) != "hello from up-c" {
			t.Fatalf("model-c: status %d body %s", res.Status, res.Body)
		}
		if h := res.Header.Get("X-Proxx-Served-Model"); h != "" {
			t.Fatalf("non-group request got served-model header %q", h)
		}
		if got := models(mock.since(mark)); !reflect.DeepEqual(got, []string{"up-c"}) {
			t.Fatalf("hits %v", got)
		}
		claude := p.do(http.MethodPost, "/v1/messages", map[string]any{"model": "model-c", "max_tokens": 64, "messages": []map[string]any{{"role": "user", "content": "hi"}}}, map[string]string{"anthropic-version": "2023-06-01"})
		t.Logf("direct alias model-c: openai model field=%v, claude model field=%v", res.JSON["model"], claude.JSON["model"])
		if res.JSON["model"] != "up-c" || claude.JSON["model"] != "up-c" {
			t.Fatalf("direct alias model fields changed: openai=%v claude=%v", res.JSON["model"], claude.JSON["model"])
		}
	})

	t.Run("ordered group serves first member", func(t *testing.T) {
		mark := mock.mark()
		res := p.chat("auto", false)
		if res.Status != http.StatusOK {
			t.Fatalf("status %d body %s", res.Status, res.Body)
		}
		hits := mock.since(mark)
		if !reflect.DeepEqual(models(hits), []string{"up-a"}) {
			t.Fatalf("hits %v", models(hits))
		}
		if h := res.Header.Get("X-Proxx-Served-Model"); h != "model-a" {
			t.Fatalf("served header %q", h)
		}
		t.Logf("auto -> %s, response model=%v, content=%q", res.Header.Get("X-Proxx-Served-Model"), res.JSON["model"], chatContent(res))
	})

	t.Run("failover on 500 and payload rules see the member", func(t *testing.T) {
		mock.set("up-500", "500")
		mark := mock.mark()
		res := p.chat("g500", false)
		hits := mock.since(mark)
		if res.Status != http.StatusOK || !reflect.DeepEqual(models(hits), []string{"up-500", "up-b"}) {
			t.Fatalf("status %d hits %v body %s", res.Status, models(hits), res.Body)
		}
		if h := res.Header.Get("X-Proxx-Served-Model"); h != "model-b" {
			t.Fatalf("served header %q", h)
		}
		if hits[1].Temperature != 0.123 {
			t.Fatalf("payload override for model-b not applied via group, temperature=%v", hits[1].Temperature)
		}
		if hits[0].Temperature == 0.123 {
			t.Fatalf("payload override for model-b leaked to m500")
		}
		t.Logf("g500: hits %v in %s, served %s", models(hits), res.Elapsed, res.Header.Get("X-Proxx-Served-Model"))
	})

	t.Run("429 fails over without waiting, cooled member skipped next time", func(t *testing.T) {
		mock.set("up-429", "429:20")
		mark := mock.mark()
		res := p.chat("g429", false)
		hits := models(mock.since(mark))
		if res.Status != http.StatusOK || !reflect.DeepEqual(hits, []string{"up-429", "up-c"}) {
			t.Fatalf("first: status %d hits %v body %s", res.Status, hits, res.Body)
		}
		if res.Elapsed > 3*time.Second {
			t.Fatalf("429 failover took %s (cooldown wait leaked into the group)", res.Elapsed)
		}
		t.Logf("g429 first request: hits %v in %s", hits, res.Elapsed)
		mark = mock.mark()
		res = p.chat("g429", false)
		hits = models(mock.since(mark))
		if res.Status != http.StatusOK || !reflect.DeepEqual(hits, []string{"up-c"}) {
			t.Fatalf("second: status %d hits %v body %s", res.Status, hits, res.Body)
		}
		if res.Elapsed > time.Second {
			t.Fatalf("cooled member was not skipped quickly: %s", res.Elapsed)
		}
		t.Logf("g429 second request (m429 cooling): hits %v in %s", hits, res.Elapsed)
	})

	t.Run("management endpoint reports member availability", func(t *testing.T) {
		res := p.do(http.MethodGet, "/v8/management/proxx/groups", nil, map[string]string{"Authorization": "Bearer " + mgmtKey})
		if res.Status != http.StatusOK {
			t.Fatalf("status %d body %s", res.Status, res.Body)
		}
		groups, _ := res.JSON["groups"].([]any)
		var found bool
		for _, g := range groups {
			gm := g.(map[string]any)
			if gm["name"] != "g429" {
				continue
			}
			members := gm["members"].([]any)
			m429 := members[0].(map[string]any)
			mc := members[1].(map[string]any)
			if m429["model"] != "m429" || m429["available"] != false || m429["cooldown_until"] == nil || m429["credentials"] != float64(1) {
				t.Fatalf("m429 entry %v", m429)
			}
			if mc["available"] != true || mc["cooldown_until"] != nil {
				t.Fatalf("model-c entry %v", mc)
			}
			found = true
			t.Logf("g429 members: %v / %v", m429, mc)
		}
		if !found {
			t.Fatalf("g429 missing from %s", res.Body)
		}
	})

	t.Run("all members cooling returns 429 fast", func(t *testing.T) {
		mock.set("up-429long", "429:120")
		res := p.chat("glong", false)
		if res.Status != http.StatusTooManyRequests {
			t.Fatalf("first: status %d body %s", res.Status, res.Body)
		}
		mark := mock.mark()
		res = p.chat("glong", false)
		if res.Status != http.StatusTooManyRequests || len(mock.since(mark)) != 0 {
			t.Fatalf("second: status %d hits %v body %s", res.Status, models(mock.since(mark)), res.Body)
		}
		if res.Elapsed > time.Second {
			t.Fatalf("all-cooling group took %s", res.Elapsed)
		}
		t.Logf("glong while cooling: status %d Retry-After=%q in %s", res.Status, res.Header.Get("Retry-After"), res.Elapsed)
	})

	t.Run("400 does not fail over, context-length 400 does", func(t *testing.T) {
		mock.set("up-400", "400")
		mark := mock.mark()
		res := p.chat("g400", false)
		if res.Status != http.StatusBadRequest || !reflect.DeepEqual(models(mock.since(mark)), []string{"up-400"}) {
			t.Fatalf("g400: status %d hits %v", res.Status, models(mock.since(mark)))
		}
		mock.set("up-ctx", "400ctx")
		mark = mock.mark()
		res = p.chat("gctx", false)
		if res.Status != http.StatusOK || !reflect.DeepEqual(models(mock.since(mark)), []string{"up-ctx", "up-b"}) {
			t.Fatalf("gctx: status %d hits %v body %s", res.Status, models(mock.since(mark)), res.Body)
		}
	})

	t.Run("stream success", func(t *testing.T) {
		mark := mock.mark()
		res := p.chat("auto", true)
		joined := strings.Join(res.SSEDatas, "\n")
		if res.Status != http.StatusOK || !strings.Contains(joined, "hello from up-a") || !strings.Contains(joined, "[DONE]") {
			t.Fatalf("status %d body %s", res.Status, res.Body)
		}
		if h := res.Header.Get("X-Proxx-Served-Model"); h != "model-a" {
			t.Fatalf("served header %q", h)
		}
		if got := models(mock.since(mark)); !reflect.DeepEqual(got, []string{"up-a"}) {
			t.Fatalf("hits %v", got)
		}
	})

	t.Run("stream fails over before first chunk", func(t *testing.T) {
		mock.set("up-sf", "stream502")
		mark := mock.mark()
		res := p.chat("gstream", true)
		joined := strings.Join(res.SSEDatas, "\n")
		if res.Status != http.StatusOK || !strings.Contains(joined, "hello from up-a") || strings.Contains(joined, "up-sf") {
			t.Fatalf("status %d body %s", res.Status, res.Body)
		}
		if got := models(mock.since(mark)); !reflect.DeepEqual(got, []string{"up-sf", "up-a"}) {
			t.Fatalf("hits %v", got)
		}
		t.Logf("gstream: hits %v, served %s", models(mock.since(mark)), res.Header.Get("X-Proxx-Served-Model"))
	})

	t.Run("stream cut after first chunk does not fail over", func(t *testing.T) {
		mock.set("up-break", "streambreak")
		mark := mock.mark()
		res := p.chat("gbreak", true)
		joined := strings.Join(res.SSEDatas, "\n")
		got := models(mock.since(mark))
		t.Logf("gbreak: status %d hits %v body %q", res.Status, got, joined)
		if !strings.Contains(joined, "hello from up-break") || strings.Contains(joined, "up-b\"") || !reflect.DeepEqual(got, []string{"up-break"}) {
			t.Fatalf("expected only the first member's partial stream, hits %v body %s", got, res.Body)
		}
	})

	t.Run("claude messages format", func(t *testing.T) {
		body := map[string]any{"model": "auto", "max_tokens": 64, "messages": []map[string]any{{"role": "user", "content": "hi"}}}
		res := p.do(http.MethodPost, "/v1/messages", body, map[string]string{"anthropic-version": "2023-06-01"})
		if res.Status != http.StatusOK || !strings.Contains(string(res.Body), "hello from up-a") || res.Header.Get("X-Proxx-Served-Model") != "model-a" {
			t.Fatalf("non-stream: status %d body %s", res.Status, res.Body)
		}
		t.Logf("claude non-stream model field: %v", res.JSON["model"])
		// A fresh failing member: mstreamfail is still cooling from the previous subtest.
		mock.set("up-sf2", "stream502")
		body["model"] = "gstream2"
		body["stream"] = true
		mark := mock.mark()
		res = p.do(http.MethodPost, "/v1/messages", body, map[string]string{"anthropic-version": "2023-06-01"})
		if res.Status != http.StatusOK || !strings.Contains(string(res.Body), "hello from up-a") || !reflect.DeepEqual(models(mock.since(mark)), []string{"up-sf2", "up-a"}) {
			t.Fatalf("stream: status %d hits %v body %s", res.Status, models(mock.since(mark)), res.Body)
		}
	})

	t.Run("responses format", func(t *testing.T) {
		mark := mock.mark()
		res := p.do(http.MethodPost, "/v1/responses", map[string]any{"model": "g500", "input": "hi"}, nil)
		if res.Status != http.StatusOK || !strings.Contains(string(res.Body), "hello from up-b") {
			t.Fatalf("status %d body %s", res.Status, res.Body)
		}
		t.Logf("responses g500: hits %v model field %v", models(mock.since(mark)), res.JSON["model"])
	})

	t.Run("round-robin rotates the first member", func(t *testing.T) {
		mark := mock.mark()
		for i := 0; i < 4; i++ {
			if res := p.chat("rr", false); res.Status != http.StatusOK {
				t.Fatalf("rr %d: status %d", i, res.Status)
			}
		}
		got := models(mock.since(mark))
		counts := map[string]int{}
		for _, m := range got {
			counts[m]++
		}
		if counts["up-free-a"] != 2 || counts["up-free-b"] != 2 {
			t.Fatalf("round-robin hits %v", got)
		}
	})

	t.Run("listing shows groups except hidden, hidden still routes", func(t *testing.T) {
		ids := p.listModels()
		for _, want := range []string{"auto", "g500", "rr"} {
			if !contains(ids, want) {
				t.Fatalf("%s missing from /v1/models: %v", want, ids)
			}
		}
		if contains(ids, "secret") {
			t.Fatalf("hidden group listed")
		}
		gemini := p.do(http.MethodGet, "/v1beta/models", nil, map[string]string{"x-goog-api-key": clientKey})
		t.Logf("/v1beta/models status %d contains auto: %v", gemini.Status, strings.Contains(string(gemini.Body), `"models/auto"`))
		if gemini.Status != http.StatusOK || !strings.Contains(string(gemini.Body), `"models/auto"`) || strings.Contains(string(gemini.Body), "secret") {
			t.Fatalf("gemini listing: status %d body %s", gemini.Status, gemini.Body)
		}
		if res := p.chat("secret", false); res.Status != http.StatusOK || chatContent(res) != "hello from up-c" {
			t.Fatalf("hidden group: status %d body %s", res.Status, res.Body)
		}
	})

	t.Run("hot reload updates groups", func(t *testing.T) {
		groups := strings.Replace(baseGroups, "    - name: rr\n      strategy: round-robin\n      models: [free-a, free-b]\n", "    - name: hot\n      models: [model-c]\n", 1)
		cfg := renderConfig(port, mock.srv.URL, authDir, groups)
		if err := os.WriteFile(p.cfgPath, []byte(cfg), 0o600); err != nil {
			t.Fatal(err)
		}
		waitFor(t, 30*time.Second, "reloaded group listing", func() bool {
			ids := p.listModels()
			return contains(ids, "hot") && !contains(ids, "rr")
		})
		if res := p.chat("hot", false); res.Status != http.StatusOK || res.Header.Get("X-Proxx-Served-Model") != "model-c" {
			t.Fatalf("hot: status %d body %s", res.Status, res.Body)
		}
		if res := p.chat("rr", false); res.Status == http.StatusOK {
			t.Fatalf("removed group still routes")
		}
	})
}
