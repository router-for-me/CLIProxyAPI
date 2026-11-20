// Package decisionse2e runs the real proxy binary against mock TypeSafe and OpenAI Decisions
// upstreams to exercise the decision-model passthrough routes (ADR 0008) end to end. It is
// skipped unless PROXX_E2E=1:
//
//	PROXX_E2E=1 go test ./test/proxx/decisions_e2e/ -run E2E -v -count=1
package decisionse2e

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
)

const (
	clientKey = "test-client-key"
	mgmtKey   = "mgmt-test-secret"

	tsLimitedKey = "ts-limited-key-0001"
	tsGoodKey    = "ts-good-key-0002"
	oaGoodKey    = "sk-oa-good-key-0003"
	oaBadKey     = "sk-oa-revoked-0004"
)

type seen struct {
	path, key, contentType string
	body                   []byte
	header                 http.Header
}

// mock serves both vendors. Behavior is chosen by the upstream key it receives.
type mock struct {
	srv  *httptest.Server
	mu   sync.Mutex
	reqs []seen
}

func newMock() *mock {
	m := &mock{}
	m.srv = httptest.NewServer(http.HandlerFunc(m.serve))
	return m
}

func (m *mock) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	m.mu.Lock()
	m.reqs = append(m.reqs, seen{path: r.URL.Path, key: key, contentType: r.Header.Get("Content-Type"), body: body, header: r.Header.Clone()})
	m.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Request-Id", "upstream-req-1")
	switch {
	case key == tsLimitedKey:
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"rate limited"}}`)
	case key == oaBadKey:
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"invalid api key"}}`)
	case r.URL.Path == "/ts/v1/systemone" && key == tsGoodKey:
		_, _ = io.WriteString(w, `{"model":"jev-1.14.0","answers":{"billing":{"type":"noul","noul":0.98}},"usage":{"input_tokens":1000000,"output_tokens":12}}`)
	case r.URL.Path == "/oa/v1/decisions" && key == oaGoodKey:
		_, _ = io.WriteString(w, `{"model":"gpt-6-luna","answers":[{"type":"predicate","name":"damaged","probability":0.95}],"usage":{"input_tokens":2000000,"output_tokens":0,"total_tokens":2000000,"input_tokens_details":{"cached_tokens":1000000,"cache_write_tokens":0}}}`)
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":{"message":"unexpected `+r.URL.Path+`"}}`)
	}
}

func (m *mock) requests() []seen {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]seen(nil), m.reqs...)
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

func renderConfig(port int, upstream, authDir string) string {
	return fmt.Sprintf(`config-version: 8
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
observability:
  logs:
    request-log: true
requests:
  payload:
    override:
      - models:
          - name: "*"
        params:
          state: "INJECTED-BY-PAYLOAD-RULE"
proxx:
  decisions:
    typesafe:
      base-url: %s/ts
      api-keys: [%q, %q]
    openai:
      base-url: %s/oa/v1
      api-keys: [%q, %q]
`, port, mgmtKey, clientKey, authDir, upstream, tsLimitedKey, tsGoodKey, upstream, oaBadKey, oaGoodKey)
}

type proxy struct {
	t       *testing.T
	base    string
	log     string
	cfgPath string
}

func start(t *testing.T, upstream string) (*proxy, string) {
	t.Helper()
	dir := t.TempDir()
	root, _ := filepath.Abs("../../..")
	bin := filepath.Join(dir, "proxx-decisions")
	build := exec.Command("go", "build", "-o", bin, "./cmd/server")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	authDir := filepath.Join(dir, "auths")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatal(err)
	}
	port := freePort(t)
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(renderConfig(port, upstream, authDir)), 0o600); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "proxy.log")
	logFile, _ := os.Create(logPath)
	cmd := exec.Command(bin, "--config", cfgPath, "--local-model", "--no-browser")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "WRITABLE_PATH=")
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := &proxy{t: t, base: fmt.Sprintf("http://127.0.0.1:%d", port), log: logPath, cfgPath: cfgPath}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		_ = logFile.Close()
		if t.Failed() {
			data, _ := os.ReadFile(logPath)
			lines := strings.Split(string(data), "\n")
			if len(lines) > 80 {
				lines = lines[len(lines)-80:]
			}
			t.Logf("proxy log tail:\n%s", strings.Join(lines, "\n"))
		}
	})
	deadline := time.Now().Add(30 * time.Second)
	for {
		if code, _, _ := p.call(http.MethodGet, "/v1/models", "", nil, clientKey, nil); code == http.StatusOK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("proxy did not start")
		}
		time.Sleep(200 * time.Millisecond)
	}
	return p, filepath.Join(authDir, "logs")
}

func (p *proxy) call(method, path, contentType string, body []byte, key string, hdr map[string]string) (int, http.Header, []byte) {
	req, _ := http.NewRequest(method, p.base+path, bytes.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, []byte(err.Error())
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, raw
}

type recentItem struct {
	Model        string   `json:"model"`
	Alias        string   `json:"alias"`
	Provider     string   `json:"provider"`
	APIKey       string   `json:"api_key"`
	Failed       bool     `json:"failed"`
	StatusCode   int      `json:"status_code"`
	InputTokens  int64    `json:"input_tokens"`
	CachedTokens int64    `json:"cached_tokens"`
	CostUSD      *float64 `json:"cost_usd"`
	CostSource   string   `json:"cost_source"`
}

func (p *proxy) recent() []recentItem {
	_, _, body := p.call(http.MethodGet, "/v8/management/proxx/usage/recent?limit=100", "", nil, mgmtKey, nil)
	var out struct {
		Items []recentItem `json:"items"`
	}
	_ = json.Unmarshal(body, &out)
	return out.Items
}

const jevBody = `{"model":"jev-latest","state":"I was charged twice. Refund the extra charge.","questions":{"billing":{"type":"noul","instructions":"Is this about billing?"}}}`
const lunaBody = `{"model":"gpt-6-luna","input":"The package arrived with a broken screen.","questions":[{"type":"predicate","name":"damaged","instructions":"Does the customer report a damaged item?"}]}`

func TestE2EDecisions(t *testing.T) {
	if os.Getenv("PROXX_E2E") != "1" {
		t.Skip("set PROXX_E2E=1 to run the proxx decisions e2e test (builds and launches the proxy)")
	}
	up := newMock()
	defer up.srv.Close()
	p, logDir := start(t, up.srv.URL)

	t.Run("client key required", func(t *testing.T) {
		if code, _, _ := p.call(http.MethodPost, "/v1/systemone", "application/json", []byte(jevBody), "", nil); code != http.StatusUnauthorized {
			t.Fatalf("no client key: %d", code)
		}
		if len(up.requests()) != 0 {
			t.Fatal("unauthenticated request reached upstream")
		}
	})

	t.Run("jev: 429 fails over to the next key, body verbatim", func(t *testing.T) {
		code, hdr, body := p.call(http.MethodPost, "/v1/systemone", "application/json", []byte(jevBody), clientKey,
			map[string]string{"TypeSafe-Danger-Zone-Long-Context": "true", "X-Unrelated": "drop-me"})
		if code != http.StatusOK || !strings.Contains(string(body), `"noul":0.98`) || hdr.Get("X-Request-Id") != "upstream-req-1" {
			t.Fatalf("jev: %d %v %s", code, hdr, body)
		}
		reqs := up.requests()
		if len(reqs) != 2 || reqs[0].key != tsLimitedKey || reqs[1].key != tsGoodKey {
			t.Fatalf("jev upstream calls = %+v", reqs)
		}
		for _, r := range reqs {
			if r.path != "/ts/v1/systemone" || string(r.body) != jevBody {
				t.Fatalf("not forwarded verbatim (payload rule applied?): %s %s", r.path, r.body)
			}
			if r.header.Get("Typesafe-Danger-Zone-Long-Context") != "true" || r.header.Get("X-Unrelated") != "" {
				t.Fatalf("forwarded headers = %v", r.header)
			}
			if strings.Contains(r.header.Get("Authorization"), clientKey) {
				t.Fatal("client key leaked upstream")
			}
		}
	})

	t.Run("jev: limited key cools down", func(t *testing.T) {
		before := len(up.requests())
		if code, _, body := p.call(http.MethodPost, "/v1/systemone", "application/json", []byte(jevBody), clientKey, nil); code != http.StatusOK {
			t.Fatalf("second jev: %d %s", code, body)
		}
		reqs := up.requests()[before:]
		if len(reqs) != 1 || reqs[0].key != tsGoodKey {
			t.Fatalf("cooling key retried: %+v", reqs)
		}
	})

	t.Run("jev multipart forwarded verbatim", func(t *testing.T) {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		_ = mw.WriteField("request", jevBody)
		fw, _ := mw.CreateFormFile("image", "x.png")
		_, _ = fw.Write([]byte("\x89PNG fake"))
		_ = mw.Close()
		before := len(up.requests())
		code, _, body := p.call(http.MethodPost, "/v1/systemone", mw.FormDataContentType(), buf.Bytes(), clientKey, nil)
		reqs := up.requests()[before:]
		if code != http.StatusOK || len(reqs) != 1 || !bytes.Equal(reqs[0].body, buf.Bytes()) || reqs[0].contentType != mw.FormDataContentType() {
			t.Fatalf("multipart: %d %s calls=%d", code, body, len(reqs))
		}
	})

	t.Run("openai: 401 key cools down, next key answers", func(t *testing.T) {
		before := len(up.requests())
		code, _, body := p.call(http.MethodPost, "/v1/decisions", "application/json", []byte(lunaBody), clientKey,
			map[string]string{"OpenAI-Organization": "org-client", "OpenAI-Beta": "decisions=v1"})
		if code != http.StatusOK || !strings.Contains(string(body), `"probability":0.95`) {
			t.Fatalf("openai: %d %s", code, body)
		}
		reqs := up.requests()[before:]
		if len(reqs) != 2 || reqs[0].key != oaBadKey || reqs[1].key != oaGoodKey || string(reqs[1].body) != lunaBody {
			t.Fatalf("openai calls = %+v", reqs)
		}
		if reqs[1].path != "/oa/v1/decisions" || reqs[1].header.Get("Openai-Organization") != "" || reqs[1].header.Get("Openai-Beta") != "decisions=v1" {
			t.Fatalf("openai path/headers = %s %v", reqs[1].path, reqs[1].header)
		}
	})

	t.Run("usage and cost", func(t *testing.T) {
		var items []recentItem
		deadline := time.Now().Add(15 * time.Second)
		for {
			items = p.recent()
			if len(items) >= 6 || time.Now().After(deadline) {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		var jevOK, lunaOK, failed429, failed401 int
		for _, it := range items {
			switch {
			case it.Provider == "typesafe" && !it.Failed:
				jevOK++
				if it.Model != "jev-1.14.0" || it.Alias != "jev-latest" || it.InputTokens != 1_000_000 || it.CostUSD == nil ||
					*it.CostUSD < 0.0419 || *it.CostUSD > 0.0421 || it.APIKey == "" {
					t.Errorf("jev usage = %+v", it)
				}
			case it.Provider == "openai" && !it.Failed:
				lunaOK++
				// 1M uncached × $0.10 + 1M cached × catalog cache read ($0.01) = $0.11
				if it.Model != "gpt-6-luna" || it.CachedTokens != 1_000_000 || it.CostUSD == nil || *it.CostUSD < 0.1099 || *it.CostUSD > 0.1101 {
					t.Errorf("luna usage = %+v cost=%v", it, it.CostUSD)
				}
			case it.Failed && it.StatusCode == http.StatusTooManyRequests:
				failed429++
			case it.Failed && it.StatusCode == http.StatusUnauthorized:
				failed401++
			}
		}
		if jevOK != 3 || lunaOK != 1 || failed429 != 1 || failed401 != 1 {
			t.Fatalf("usage counts jev=%d luna=%d 429=%d 401=%d items=%+v", jevOK, lunaOK, failed429, failed401, items)
		}
	})

	t.Run("request logs: attempts recorded, keys masked", func(t *testing.T) {
		var files []string
		deadline := time.Now().Add(10 * time.Second)
		for {
			files, _ = filepath.Glob(filepath.Join(logDir, "*.log"))
			if len(files) >= 4 || time.Now().After(deadline) {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		if len(files) < 4 {
			t.Fatalf("request log files = %v", files)
		}
		var all strings.Builder
		for _, f := range files {
			data, _ := os.ReadFile(f)
			all.Write(data)
		}
		text := all.String()
		for _, secret := range []string{tsLimitedKey, tsGoodKey, oaGoodKey, oaBadKey} {
			if strings.Contains(text, secret) {
				t.Fatalf("upstream key %s written unmasked to request logs", secret)
			}
		}
		for _, want := range []string{"=== API REQUEST 2 ===", "provider=typesafe", "provider=openai", "/ts/v1/systemone", "/oa/v1/decisions"} {
			if !strings.Contains(text, want) {
				t.Fatalf("request logs missing %q", want)
			}
		}
		// The request log viewer lists the decision requests with their models.
		_, _, body := p.call(http.MethodGet, "/v8/management/proxx/requests?model=jev-latest", "", nil, mgmtKey, nil)
		var listed struct {
			Items []struct {
				Path     string `json:"path"`
				Provider string `json:"provider"`
				Status   int    `json:"status"`
			} `json:"items"`
		}
		_ = json.Unmarshal(body, &listed)
		if len(listed.Items) < 3 || listed.Items[0].Path != "/v1/systemone" || listed.Items[0].Provider != "typesafe" {
			t.Fatalf("viewer rows = %s", body)
		}
	})

	t.Run("keys survive a v8 config write", func(t *testing.T) {
		raw, _ := json.Marshal(false)
		if code, _, body := p.call(http.MethodPut, "/v8/management/config/observability/logs/request-log", "application/json", raw, mgmtKey, nil); code != http.StatusOK {
			t.Fatalf("config write: %d %s", code, body)
		}
		data, _ := os.ReadFile(p.cfgPath)
		if !strings.Contains(string(data), tsGoodKey) || !strings.Contains(string(data), "decisions:") {
			t.Fatalf("proxx.decisions lost on config save:\n%s", data)
		}
		time.Sleep(1500 * time.Millisecond) // let the watcher reload the saved file
		if code, _, body := p.call(http.MethodPost, "/v1/decisions", "application/json", []byte(lunaBody), clientKey, nil); code != http.StatusOK {
			t.Fatalf("after config write: %d %s", code, body)
		}
	})
}
