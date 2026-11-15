package test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
)

const (
	usageE2EEnv           = "CLIPROXY_E2E"
	usageE2EClientKey     = "e2e-client-key"
	usageE2EManagementKey = "e2e-management-key"
)

// usageE2EPrices mirror the built-in list prices of the served models, in USD
// per million input and output tokens.
var usageE2EPrices = map[string][2]float64{
	"claude-opus-5-5":  {4, 20},
	"claude-fable-5-1": {10, 50},
	"gpt-5.5":          {5, 30},
}

var usageE2ETokens = regexp.MustCompile(`in=(\d+) out=(\d+)`)

// fakeQuotaWindow is a quota window the fake upstream meters. A request moves
// it by its list-price cost divided by the model's rate, when it completes.
type fakeQuotaWindow struct {
	id          string
	reportedFor func(model string) bool
	rates       map[string]float64
	used        float64
	resetsAt    time.Time
}

// emittedQuotaState is one window state the fake reported in a response.
type emittedQuotaState struct {
	usedPercent float64
	resetsAtMs  int64
}

type fakeQuotaUpstream struct {
	mu      sync.Mutex
	windows map[string][]*fakeQuotaWindow
	emitted map[string][]emittedQuotaState
	costUSD map[string]float64
	release chan struct{}
	held    chan struct{}
}

func newFakeQuotaUpstream(start time.Time) *fakeQuotaUpstream {
	always := func(string) bool { return true }
	return &fakeQuotaUpstream{
		windows: map[string][]*fakeQuotaWindow{
			"claude": {
				{id: "5h", reportedFor: always, rates: map[string]float64{"claude-opus-5-5": 8, "claude-fable-5-1": 8}, resetsAt: start.Add(5 * time.Hour).Truncate(time.Second)},
				{id: "7d", reportedFor: always, rates: map[string]float64{"claude-opus-5-5": 34, "claude-fable-5-1": 14}, resetsAt: start.Add(100 * time.Hour).Truncate(time.Second)},
				{id: "7d_oi", reportedFor: func(model string) bool { return strings.Contains(model, "fable") }, rates: map[string]float64{"claude-fable-5-1": 7}, resetsAt: start.Add(100 * time.Hour).Truncate(time.Second)},
			},
			"codex": {
				{id: "primary", reportedFor: always, rates: map[string]float64{"gpt-5.5": 1.5}, resetsAt: start.Add(5 * time.Hour).Truncate(time.Second)},
				{id: "secondary", reportedFor: always, rates: map[string]float64{"gpt-5.5": 12}, resetsAt: start.Add(150 * time.Hour).Truncate(time.Second)},
			},
		},
		emitted: map[string][]emittedQuotaState{},
		costUSD: map[string]float64{},
		release: make(chan struct{}),
		held:    make(chan struct{}, 1),
	}
}

// reportState snapshots the quota headers for a response that starts now.
func (f *fakeQuotaUpstream) reportState(provider, model string, headers http.Header) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, window := range f.windows[provider] {
		if !window.reportedFor(model) {
			continue
		}
		// Rounding first keeps a sum such as 0.07+0.93 from landing a hair below a whole percent.
		used := math.Floor(math.Round(window.used*1e9) / 1e9)
		switch provider {
		case "claude":
			headers.Set("Anthropic-Ratelimit-Unified-"+window.id+"-Utilization", strconv.FormatFloat(used/100, 'f', 2, 64))
			headers.Set("Anthropic-Ratelimit-Unified-"+window.id+"-Reset", strconv.FormatInt(window.resetsAt.Unix(), 10))
			headers.Set("Anthropic-Ratelimit-Unified-"+window.id+"-Status", "allowed")
		case "codex":
			slot := "X-Codex-" + strings.ToUpper(window.id[:1]) + window.id[1:] + "-"
			minutes := map[string]string{"primary": "300", "secondary": "10080"}[window.id]
			headers.Set(slot+"Used-Percent", strconv.FormatFloat(used, 'f', 0, 64))
			headers.Set(slot+"Window-Minutes", minutes)
			headers.Set(slot+"Reset-At", strconv.FormatInt(window.resetsAt.Unix(), 10))
		}
		key := provider + "/" + window.id
		f.emitted[key] = append(f.emitted[key], emittedQuotaState{usedPercent: used, resetsAtMs: window.resetsAt.UnixMilli()})
	}
}

// complete counts a finished request against every window its model moves.
func (f *fakeQuotaUpstream) complete(provider, model string, input, output int64) {
	price := usageE2EPrices[model]
	cost := float64(input)*price[0]/1e6 + float64(output)*price[1]/1e6
	f.mu.Lock()
	defer f.mu.Unlock()
	f.costUSD[provider+"/"+model] += cost
	for _, window := range f.windows[provider] {
		if rate, ok := window.rates[model]; ok {
			window.used += cost / rate
		}
	}
}

func (f *fakeQuotaUpstream) resetWindow(provider, id string, next time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, window := range f.windows[provider] {
		if window.id == id {
			window.used = 0
			window.resetsAt = window.resetsAt.Add(next)
		}
	}
}

func (f *fakeQuotaUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, errRead := io.ReadAll(r.Body)
	if errRead != nil {
		http.Error(w, errRead.Error(), http.StatusBadRequest)
		return
	}
	model := ojson.GetBytes(body, "model").String()
	stream := ojson.GetBytes(body, "stream").Bool()
	match := usageE2ETokens.FindSubmatch(body)
	if match == nil {
		http.Error(w, "missing token plan", http.StatusBadRequest)
		return
	}
	input, _ := strconv.ParseInt(string(match[1]), 10, 64)
	output, _ := strconv.ParseInt(string(match[2]), 10, 64)
	hold := bytes.Contains(body, []byte("hold-until-released"))
	if bytes.Contains(body, []byte("no-quota")) {
		f.serveClaude(w, model, false, false, input, output)
		return
	}
	switch {
	case strings.HasSuffix(r.URL.Path, "/v1/messages"):
		f.reportState("claude", model, w.Header())
		f.serveClaude(w, model, stream, hold, input, output)
		f.complete("claude", model, input, output)
	case strings.HasSuffix(r.URL.Path, "/responses"):
		f.reportState("codex", model, w.Header())
		f.serveCodex(w, model, stream, input, output)
		f.complete("codex", model, input, output)
	default:
		http.NotFound(w, r)
	}
}

func writeSSE(w http.ResponseWriter, event, data string) {
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (f *fakeQuotaUpstream) serveClaude(w http.ResponseWriter, model string, stream, hold bool, input, output int64) {
	if !stream {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":"msg_e2e","type":"message","role":"assistant","model":%q,"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":%d,"output_tokens":%d}}`, model, input, output)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	writeSSE(w, "message_start", fmt.Sprintf(`{"type":"message_start","message":{"id":"msg_e2e","type":"message","role":"assistant","model":%q,"content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":%d,"output_tokens":1}}}`, model, input))
	writeSSE(w, "content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
	writeSSE(w, "content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`)
	if hold {
		f.held <- struct{}{}
		<-f.release
	}
	writeSSE(w, "content_block_stop", `{"type":"content_block_stop","index":0}`)
	writeSSE(w, "message_delta", fmt.Sprintf(`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":%d}}`, output))
	writeSSE(w, "message_stop", `{"type":"message_stop"}`)
}

func (f *fakeQuotaUpstream) serveCodex(w http.ResponseWriter, model string, stream bool, input, output int64) {
	usage := fmt.Sprintf(`{"input_tokens":%d,"input_tokens_details":{"cached_tokens":0},"output_tokens":%d,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":%d}`, input, output, input+output)
	completed := fmt.Sprintf(`{"id":"resp_e2e","object":"response","created_at":1,"status":"completed","model":%q,"output":[{"type":"message","id":"msg_e2e","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok","annotations":[]}]}],"usage":%s}`, model, usage)
	if !stream {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, completed)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	writeSSE(w, "response.created", fmt.Sprintf(`{"type":"response.created","sequence_number":0,"response":{"id":"resp_e2e","object":"response","created_at":1,"status":"in_progress","model":%q,"output":[]}}`, model))
	writeSSE(w, "response.output_text.delta", `{"type":"response.output_text.delta","sequence_number":1,"item_id":"msg_e2e","output_index":0,"content_index":0,"delta":"ok"}`)
	writeSSE(w, "response.completed", fmt.Sprintf(`{"type":"response.completed","sequence_number":2,"response":%s}`, completed))
}

// usageE2EServer is the proxy binary under test.
type usageE2EServer struct {
	baseURL string
	client  *http.Client
}

func startUsageE2EServer(t *testing.T, upstreamURL string) *usageE2EServer {
	t.Helper()
	dir := t.TempDir()
	binary := filepath.Join(dir, "quota-e2e-server")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.Command("go", "build", "-o", binary, "./cmd/server")
	build.Dir = ".."
	if output, errBuild := build.CombinedOutput(); errBuild != nil {
		t.Fatalf("build server: %v\n%s", errBuild, output)
	}

	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("reserve port: %v", errListen)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if errClose := listener.Close(); errClose != nil {
		t.Fatalf("release port: %v", errClose)
	}
	configPath := filepath.Join(dir, "config.yaml")
	config := fmt.Sprintf(`config-version: 8
server:
  host: "127.0.0.1"
  port: %d
management:
  secret-key: %q
  disable-control-panel: true
  disable-auto-update-panel: true
oauth:
  auth-dir: %q
access:
  api-keys: [%q]
observability:
  usage:
    usage-statistics-enabled: true
api-keys:
  claude:
    - name: fake-claude
      base-url: %q
      models:
        - name: claude-opus-5-5
          alias: claude-opus-5-5
        - name: claude-fable-5-1
          alias: claude-fable-5-1
      keys:
        - api-key: "sk-ant-e2e-fake-claude-key"
  codex:
    - name: fake-codex
      base-url: %q
      models:
        - name: gpt-5.5
          alias: gpt-5.5
      keys:
        - api-key: "sk-e2e-fake-codex-key"
`, port, usageE2EManagementKey, filepath.Join(dir, "auths"), usageE2EClientKey, upstreamURL, upstreamURL)
	if errWrite := os.WriteFile(configPath, []byte(config), 0o600); errWrite != nil {
		t.Fatalf("write config: %v", errWrite)
	}

	logFile, errLog := os.Create(filepath.Join(dir, "server.log"))
	if errLog != nil {
		t.Fatalf("create server log: %v", errLog)
	}
	server := exec.Command(binary, "--config", configPath, "--local-model", "--no-browser")
	server.Dir = dir
	server.Stdout = logFile
	server.Stderr = logFile
	// No host store, Home, or outbound network: upstream traffic reaches the fake only.
	server.Env = append(os.Environ(),
		"HOME_JWT=", "PGSTORE_DSN=", "GITSTORE_GIT_URL=", "OBJECTSTORE_ENDPOINT=", "DEPLOY=", "WRITABLE_PATH=",
		"HTTP_PROXY=http://127.0.0.1:1", "HTTPS_PROXY=http://127.0.0.1:1", "NO_PROXY=127.0.0.1,localhost",
	)
	if errStart := server.Start(); errStart != nil {
		t.Fatalf("start server: %v", errStart)
	}
	t.Cleanup(func() {
		_ = server.Process.Kill()
		_ = server.Wait()
		_ = logFile.Close()
		if t.Failed() {
			if logged, errRead := os.ReadFile(logFile.Name()); errRead == nil {
				t.Logf("server log:\n%s", logged)
			}
		}
	})

	proxy := &usageE2EServer{baseURL: fmt.Sprintf("http://127.0.0.1:%d", port), client: &http.Client{}}
	deadline := time.Now().Add(60 * time.Second)
	for {
		status, _ := proxy.management(t, "/settings", nil, false)
		if status == http.StatusOK {
			return proxy
		}
		if time.Now().After(deadline) {
			t.Fatalf("server did not become ready, last status %d", status)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func (s *usageE2EServer) management(t *testing.T, path string, query url.Values, must bool) (int, []byte) {
	t.Helper()
	target := s.baseURL + "/v8/management/observability/usage" + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, errReq := http.NewRequest(http.MethodGet, target, nil)
	if errReq != nil {
		t.Fatalf("build management request: %v", errReq)
	}
	req.Header.Set("Authorization", "Bearer "+usageE2EManagementKey)
	resp, errDo := s.client.Do(req)
	if errDo != nil {
		if must {
			t.Fatalf("GET %s: %v", path, errDo)
		}
		return 0, nil
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if must && resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d: %s", path, resp.StatusCode, body)
	}
	return resp.StatusCode, body
}

// send makes one client request through the proxy and drains the response.
func (s *usageE2EServer) send(t *testing.T, provider, model string, stream bool, prompt string) {
	t.Helper()
	path, body := "/v1/messages", fmt.Sprintf(`{"model":%q,"max_tokens":1024,"stream":%t,"messages":[{"role":"user","content":%q}]}`, model, stream, prompt)
	if provider == "codex" {
		path, body = "/v1/responses", fmt.Sprintf(`{"model":%q,"stream":%t,"input":%q}`, model, stream, prompt)
	}
	req, errReq := http.NewRequest(http.MethodPost, s.baseURL+path, strings.NewReader(body))
	if errReq != nil {
		t.Errorf("build request: %v", errReq)
		return
	}
	req.Header.Set("Authorization", "Bearer "+usageE2EClientKey)
	req.Header.Set("Content-Type", "application/json")
	resp, errDo := s.client.Do(req)
	if errDo != nil {
		t.Errorf("%s %s: %v", provider, model, errDo)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	payload, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("%s %s: status %d: %s", provider, model, resp.StatusCode, payload)
	}
}

// waitForRequests sends a request that reports no quota and waits until the
// proxy has recorded it after the want earlier ones. The proxy records usage
// in order, so every earlier quota sample is stored by then.
func (s *usageE2EServer) waitForRequests(t *testing.T, from time.Time, want int64) {
	t.Helper()
	s.send(t, "claude", "claude-opus-5-5", false, "in=1 out=1 no-quota")
	want++
	deadline := time.Now().Add(30 * time.Second)
	for {
		_, body := s.management(t, "/stats", url.Values{"from": {strconv.FormatInt(from.UnixMilli(), 10)}}, true)
		var stats struct {
			Groups []struct {
				Requests int64 `json:"requests"`
			} `json:"groups"`
		}
		if errDecode := json.Unmarshal(body, &stats); errDecode != nil {
			t.Fatalf("decode stats: %v", errDecode)
		}
		var got int64
		for _, group := range stats.Groups {
			got += group.Requests
		}
		if got >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("recorded %d requests, want %d", got, want)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

type usageE2EPoint struct {
	TimestampMs int64   `json:"ts_ms"`
	UsedPercent float64 `json:"used_percent"`
	ResetsAtMs  int64   `json:"resets_at_ms"`
}

type usageE2ESeries struct {
	Provider  string          `json:"provider"`
	AuthIndex string          `json:"auth_index"`
	Account   string          `json:"account"`
	Window    string          `json:"window"`
	CarryIn   *usageE2EPoint  `json:"carry_in"`
	Points    []usageE2EPoint `json:"points"`
	Thinned   bool            `json:"thinned"`
}

// changesOf drops each state that repeats the one before it, which is what the
// recorder stores.
func changesOf(states []emittedQuotaState) []emittedQuotaState {
	var out []emittedQuotaState
	for _, state := range states {
		if len(out) == 0 || out[len(out)-1] != state {
			out = append(out, state)
		}
	}
	return out
}

// codexWindowIDs map the fake's Codex header slots to the window ids the
// proxy derives from their lengths.
var codexWindowIDs = map[string]string{"primary": "5h", "secondary": "7d"}

func TestUsageQuotaEndToEnd(t *testing.T) {
	if os.Getenv(usageE2EEnv) != "1" {
		t.Skipf("set %s=1 to build the server binary and run it against a fake upstream", usageE2EEnv)
	}
	start := time.Now()
	fake := newFakeQuotaUpstream(start)
	upstream := httptest.NewServer(fake)
	defer upstream.Close()
	proxy := startUsageE2EServer(t, upstream.URL)

	const opus, fable = "claude-opus-5-5", "claude-fable-5-1"
	// Every request costs $4.00 at list prices.
	opusPrompt, fablePrompt := "in=10000 out=198000", "in=4000 out=79200"
	requests := int64(0)
	sendClaude := func(model, prompt string, i int) {
		proxy.send(t, "claude", model, i%5 != 4, prompt)
		requests++
	}
	for i := range 85 {
		sendClaude(opus, opusPrompt, i)
	}
	fake.resetWindow("claude", "5h", 5*time.Hour)

	// A $34 opus stream starts with the fable phase and finishes after it, so
	// its cost lands after many samples were taken.
	heldDone := make(chan struct{})
	go func() {
		defer close(heldDone)
		proxy.send(t, "claude", opus, true, "in=10000 out=1698000 hold-until-released")
	}()
	<-fake.held
	requests++
	for i := range 35 {
		sendClaude(fable, fablePrompt, i)
	}
	close(fake.release)
	<-heldDone
	for i := range 40 {
		if i%2 == 0 {
			sendClaude(opus, opusPrompt, i)
		} else {
			sendClaude(fable, fablePrompt, i)
		}
	}
	// Every Codex request costs $3.00 at list prices.
	for i := range 30 {
		proxy.send(t, "codex", "gpt-5.5", i%3 != 2, "in=6000 out=99000")
		requests++
	}
	if t.Failed() {
		t.FailNow()
	}
	proxy.waitForRequests(t, start.Add(-time.Minute), requests)
	end := time.Now().Add(time.Minute)

	rangeQuery := url.Values{"from": {strconv.FormatInt(start.Add(-time.Minute).UnixMilli(), 10)}, "to": {strconv.FormatInt(end.UnixMilli(), 10)}}
	_, body := proxy.management(t, "/quota-samples", rangeQuery, true)
	var samples struct {
		Series   []usageE2ESeries `json:"series"`
		BucketMs int64            `json:"bucket_ms"`
	}
	if errDecode := json.Unmarshal(body, &samples); errDecode != nil {
		t.Fatalf("decode quota samples: %v", errDecode)
	}
	byKey := map[string]usageE2ESeries{}
	for _, series := range samples.Series {
		byKey[series.Provider+"/"+series.Window] = series
		t.Logf("quota series %s/%s account=%s auth_index=%s points=%d", series.Provider, series.Window, series.Account, series.AuthIndex, len(series.Points))
	}
	if len(samples.Series) != 5 || samples.BucketMs != 0 {
		t.Fatalf("series = %s, want claude 5h/7d/7d_oi and codex 5h/7d unthinned", body)
	}
	fake.mu.Lock()
	emitted := fake.emitted
	fake.mu.Unlock()
	for key, states := range emitted {
		provider, window, _ := strings.Cut(key, "/")
		if provider == "codex" {
			window = codexWindowIDs[window]
		}
		series, ok := byKey[provider+"/"+window]
		if !ok {
			t.Errorf("no series for %s", key)
			continue
		}
		want := changesOf(states)
		if series.CarryIn != nil || len(series.Points) != len(want) {
			t.Errorf("%s: %d points (carry-in %v), want the %d state changes the upstream reported", key, len(series.Points), series.CarryIn, len(want))
			for i := range max(len(series.Points), len(want)) {
				var got usageE2EPoint
				var expected emittedQuotaState
				if i < len(series.Points) {
					got = series.Points[i]
				}
				if i < len(want) {
					expected = want[i]
				}
				t.Logf("  %d stored=%+v want=%+v", i, got, expected)
			}
			continue
		}
		for i, point := range series.Points {
			if point.UsedPercent != want[i].usedPercent || point.ResetsAtMs != want[i].resetsAtMs {
				t.Errorf("%s point %d = %+v, want %+v", key, i, point, want[i])
			}
			if i > 0 && point.TimestampMs < series.Points[i-1].TimestampMs {
				t.Errorf("%s point %d is older than the previous one", key, i)
			}
		}
		t.Logf("%s: upstream reported %d responses, %d distinct states; stored %d points from %.0f%% to %.0f%%",
			key, len(states), len(want), len(series.Points), series.Points[0].UsedPercent, series.Points[len(series.Points)-1].UsedPercent)
	}
}
