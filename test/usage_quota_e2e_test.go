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
	"github.com/gorilla/websocket"
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

var (
	usageE2ETokens     = regexp.MustCompile(`in=(\d+) out=(\d+)`)
	usageE2EStop       = regexp.MustCompile(`stop=(\w+)`)
	usageE2ECacheWrite = regexp.MustCompile(`cw(5m|1h)?=(\d+)`)
)

// fakeReply is how the fake upstream ends one response, in the canonical stop
// reason vocabulary, and the cache writes it reports.
type fakeReply struct {
	stop                 string
	cacheWrite5m         int64
	cacheWrite1h         int64
	cacheWriteNoLifetime int64
	overWebsocket        bool
	truncated            bool
}

// parseFakeReply reads the reply plan from a prompt, such as
// "stop=tool_use cw5m=100 cw1h=200". A Claude reply reports cw5m and cw1h,
// and a Codex reply reports cw, which Responses gives without a lifetime.
func parseFakeReply(body []byte) fakeReply {
	reply := fakeReply{stop: "end_turn"}
	if match := usageE2EStop.FindSubmatch(body); match != nil {
		reply.stop = string(match[1])
	}
	for _, match := range usageE2ECacheWrite.FindAllSubmatch(body, -1) {
		tokens, _ := strconv.ParseInt(string(match[2]), 10, 64)
		switch string(match[1]) {
		case "5m":
			reply.cacheWrite5m = tokens
		case "1h":
			reply.cacheWrite1h = tokens
		default:
			reply.cacheWriteNoLifetime = tokens
		}
	}
	return reply
}

// usageE2EStep is how long the fake upstream takes to its first byte and to
// finish, and how long the client waits between requests. The proxy stores
// times in milliseconds, so steps within one millisecond would make the order
// of a completion and the next sample ambiguous.
const usageE2EStep = 3 * time.Millisecond

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
	replies map[string][]fakeReply
	// truncatedOnce holds the prompts whose first stream was already cut short.
	truncatedOnce map[string]bool
	release       chan struct{}
	held          chan struct{}
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
				{id: "primary", reportedFor: always, rates: map[string]float64{"gpt-5.5": 3}, resetsAt: start.Add(5 * time.Hour).Truncate(time.Second)},
				{id: "secondary", reportedFor: always, rates: map[string]float64{"gpt-5.5": 12}, resetsAt: start.Add(150 * time.Hour).Truncate(time.Second)},
			},
		},
		emitted:       map[string][]emittedQuotaState{},
		costUSD:       map[string]float64{},
		replies:       map[string][]fakeReply{},
		truncatedOnce: map[string]bool{},
		release:       make(chan struct{}),
		held:          make(chan struct{}, 1),
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
func (f *fakeQuotaUpstream) complete(provider, model string, input, output int64, reply fakeReply) {
	price := usageE2EPrices[model]
	cost := float64(input)*price[0]/1e6 + float64(output)*price[1]/1e6
	f.mu.Lock()
	defer f.mu.Unlock()
	f.costUSD[provider+"/"+model] += cost
	f.replies[provider] = append(f.replies[provider], reply)
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
	if websocket.IsWebSocketUpgrade(r) {
		f.serveCodexWebsocket(w, r)
		return
	}
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
	reply := parseFakeReply(body)
	if bytes.Contains(body, []byte("truncate-once")) {
		f.mu.Lock()
		reply.truncated = !f.truncatedOnce[string(body)]
		f.truncatedOnce[string(body)] = true
		f.mu.Unlock()
	}
	if bytes.Contains(body, []byte("no-quota")) {
		f.serveClaude(w, model, false, false, input, output, reply, func() {})
		return
	}
	switch {
	case strings.HasSuffix(r.URL.Path, "/v1/messages"):
		f.reportState("claude", model, w.Header())
		f.serveClaude(w, model, stream, hold, input, output, reply, func() { f.complete("claude", model, input, output, reply) })
	case strings.HasSuffix(r.URL.Path, "/responses"):
		f.reportState("codex", model, w.Header())
		f.serveCodex(w, model, stream, input, output, reply, func() { f.complete("codex", model, input, output, reply) })
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

// serveClaude and serveCodex call done just before the response's last bytes,
// so the next request a client sends after reading a response sees its usage.
func (f *fakeQuotaUpstream) serveClaude(w http.ResponseWriter, model string, stream, hold bool, input, output int64, reply fakeReply, done func()) {
	time.Sleep(usageE2EStep)
	defer time.Sleep(usageE2EStep)
	cacheWrites := fmt.Sprintf(`"cache_creation_input_tokens":%d,"cache_creation":{"ephemeral_5m_input_tokens":%d,"ephemeral_1h_input_tokens":%d}`,
		reply.cacheWrite5m+reply.cacheWrite1h, reply.cacheWrite5m, reply.cacheWrite1h)
	if !stream {
		w.Header().Set("Content-Type", "application/json")
		done()
		_, _ = fmt.Fprintf(w, `{"id":"msg_e2e","type":"message","role":"assistant","model":%q,"content":[{"type":"text","text":"ok"}],"stop_reason":%q,"stop_sequence":null,"usage":{"input_tokens":%d,"output_tokens":%d,%s}}`, model, reply.stop, input, output, cacheWrites)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	writeSSE(w, "message_start", fmt.Sprintf(`{"type":"message_start","message":{"id":"msg_e2e","type":"message","role":"assistant","model":%q,"content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":%d,"output_tokens":1,%s}}}`, model, input, cacheWrites))
	writeSSE(w, "content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
	writeSSE(w, "content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`)
	if hold {
		f.held <- struct{}{}
		<-f.release
	}
	writeSSE(w, "content_block_stop", `{"type":"content_block_stop","index":0}`)
	writeSSE(w, "message_delta", fmt.Sprintf(`{"type":"message_delta","delta":{"stop_reason":%q,"stop_sequence":null},"usage":{"output_tokens":%d}}`, reply.stop, output))
	done()
	if !reply.truncated {
		writeSSE(w, "message_stop", `{"type":"message_stop"}`)
	}
}

// codexEvents returns the Responses events of one reply, the terminal event
// last. A tool_use reply ends with a function call item and a max_tokens reply
// with response.incomplete, as Responses has no stop reason.
func codexEvents(model string, input, output int64, reply fakeReply) []string {
	usage := fmt.Sprintf(`{"input_tokens":%d,"input_tokens_details":{"cached_tokens":0,"cache_write_tokens":%d},"output_tokens":%d,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":%d}`,
		input, reply.cacheWriteNoLifetime, output, input+output)
	items := []string{`{"type":"message","id":"msg_e2e","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok","annotations":[]}]}`}
	terminal, status := "response.completed", `"status":"completed"`
	switch reply.stop {
	case "tool_use":
		items = append(items, `{"type":"function_call","id":"fc_e2e","call_id":"call_e2e","status":"completed","name":"shell","arguments":"{}"}`)
	case "max_tokens":
		terminal, status = "response.incomplete", `"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}`
	}
	events := []string{
		fmt.Sprintf(`{"type":"response.created","sequence_number":0,"response":{"id":"resp_e2e","object":"response","created_at":1,"status":"in_progress","model":%q,"output":[]}}`, model),
		`{"type":"response.output_text.delta","sequence_number":1,"item_id":"msg_e2e","output_index":0,"content_index":0,"delta":"ok"}`,
	}
	for i, item := range items {
		events = append(events, fmt.Sprintf(`{"type":"response.output_item.done","sequence_number":%d,"output_index":%d,"item":%s}`, 2+i, i, item))
	}
	completed := fmt.Sprintf(`{"id":"resp_e2e","object":"response","created_at":1,%s,"model":%q,"output":[%s],"usage":%s}`, status, model, strings.Join(items, ","), usage)
	return append(events, fmt.Sprintf(`{"type":%q,"sequence_number":%d,"response":%s}`, terminal, 2+len(items), completed))
}

func (f *fakeQuotaUpstream) serveCodex(w http.ResponseWriter, model string, stream bool, input, output int64, reply fakeReply, done func()) {
	time.Sleep(usageE2EStep)
	defer time.Sleep(usageE2EStep)
	events := codexEvents(model, input, output, reply)
	terminal := []byte(events[len(events)-1])
	if !stream {
		w.Header().Set("Content-Type", "application/json")
		done()
		_, _ = io.WriteString(w, ojson.GetBytes(terminal, "response").Raw)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	for _, event := range events[:len(events)-1] {
		writeSSE(w, ojson.GetBytes([]byte(event), "type").String(), event)
	}
	done()
	writeSSE(w, ojson.GetBytes(terminal, "type").String(), string(terminal))
}

// serveCodexWebsocket answers each response.create of one connection, as the
// Responses websocket transport does. It reports no quota headers.
func (f *fakeQuotaUpstream) serveCodexWebsocket(w http.ResponseWriter, r *http.Request) {
	conn, errUpgrade := (&websocket.Upgrader{}).Upgrade(w, r, nil)
	if errUpgrade != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	for {
		_, message, errRead := conn.ReadMessage()
		if errRead != nil {
			return
		}
		match := usageE2ETokens.FindSubmatch(message)
		if match == nil {
			_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"missing token plan"}}`))
			continue
		}
		input, _ := strconv.ParseInt(string(match[1]), 10, 64)
		output, _ := strconv.ParseInt(string(match[2]), 10, 64)
		model := ojson.GetBytes(message, "model").String()
		reply := parseFakeReply(message)
		reply.overWebsocket = true
		events := codexEvents(model, input, output, reply)
		for i, event := range events {
			if i == len(events)-1 {
				f.complete("codex", model, input, output, reply)
			}
			if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(event)); errWrite != nil {
				return
			}
		}
	}
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
          websockets: true
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
	s.sendWithHeaders(t, provider, model, stream, prompt, nil)
}

func (s *usageE2EServer) sendWithHeaders(t *testing.T, provider, model string, stream bool, prompt string, headers http.Header) {
	t.Helper()
	time.Sleep(usageE2EStep)
	path, body := "/v1/messages", fmt.Sprintf(`{"model":%q,"max_tokens":1024,"stream":%t,"messages":[{"role":"user","content":%q}]}`, model, stream, prompt)
	if provider == "codex" {
		path, body = "/v1/responses", fmt.Sprintf(`{"model":%q,"stream":%t,"input":%q}`, model, stream, prompt)
	}
	req, errReq := http.NewRequest(http.MethodPost, s.baseURL+path, strings.NewReader(body))
	if errReq != nil {
		t.Errorf("build request: %v", errReq)
		return
	}
	for name, values := range headers {
		req.Header[name] = values
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
	for i := range 170 {
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
	for i := range 70 {
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
	for i := range 60 {
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

	_, body = proxy.management(t, "/limit-value", rangeQuery, true)
	var value struct {
		Series []struct {
			Provider   string `json:"provider"`
			Window     string `json:"window"`
			ModelScope string `json:"model_scope"`
			usageE2ESpend
			Models []struct {
				Model string `json:"model"`
				usageE2ESpend
			} `json:"models"`
		} `json:"series"`
	}
	if errDecode := json.Unmarshal(body, &value); errDecode != nil {
		t.Fatalf("decode limit value: %v", errDecode)
	}
	spends := map[string]usageE2ESpend{}
	for _, series := range value.Series {
		key := series.Provider + "/" + series.Window
		spends[key] = series.usageE2ESpend
		for _, model := range series.Models {
			spends[key+" "+model.Model] = model.usageE2ESpend
		}
		if want := map[string]string{"claude/7d_oi": "fable"}[key]; series.ModelScope != want {
			t.Errorf("%s model_scope = %q, want %q", key, series.ModelScope, want)
		}
	}
	// The fake's rates are the truth. Each request is at most 0.3% of a Claude
	// window and every checked figure rests on at least 15 percent of evidence,
	// so 1% quantization leaves under 2% of error.
	const tolerance = 0.02
	for key, truth := range map[string]float64{
		"claude/5h": 8, "claude/7d_oi": 7, "claude/7d_oi " + fable: 7, "claude/7d " + opus: 34, "claude/7d " + fable: 14,
		"codex/5h": 3, "codex/7d": 12,
	} {
		spend, ok := spends[key]
		if !ok || spend.USDPerPercent == nil || math.Abs(*spend.USDPerPercent-truth) > tolerance*truth {
			t.Errorf("%s: limit value = %+v, want $%.2f per percent within %.0f%%", key, spend, truth, tolerance*100)
			continue
		}
		t.Logf("%s: $%.3f per percent over %d intervals and %.0f%% (truth $%.2f, error %+.2f%%)",
			key, *spend.USDPerPercent, spend.Intervals, spend.PercentUsed, truth, (*spend.USDPerPercent/truth-1)*100)
	}
}

type usageE2ESpend struct {
	Intervals     int64    `json:"intervals"`
	PercentUsed   float64  `json:"percent_used"`
	CostUSD       float64  `json:"cost_usd"`
	USDPerPercent *float64 `json:"usd_per_percent"`
}
