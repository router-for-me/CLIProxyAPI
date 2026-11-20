package requestlogse2e

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
)

// row mirrors the list Row contract.
type row struct {
	ID              string   `json:"id"`
	File            string   `json:"file"`
	Time            string   `json:"time"`
	TimeMs          int64    `json:"time_ms"`
	Method          string   `json:"method"`
	Path            string   `json:"path"`
	Status          int      `json:"status"`
	DurationMs      *int64   `json:"duration_ms"`
	Stream          bool     `json:"stream"`
	RequestedModel  string   `json:"requested_model"`
	ServedModel     string   `json:"served_model"`
	Group           string   `json:"group"`
	Provider        string   `json:"provider"`
	InputTokens     *int64   `json:"input_tokens"`
	OutputTokens    *int64   `json:"output_tokens"`
	ReasoningTokens *int64   `json:"reasoning_tokens"`
	CachedTokens    *int64   `json:"cached_tokens"`
	CostUSD         *float64 `json:"cost_usd"`
	CostSource      string   `json:"cost_source"`
	Attempts        int      `json:"attempts"`
	Error           bool     `json:"error"`
	SizeBytes       int64    `json:"size_bytes"`
}

type listResponse struct {
	Items             []row  `json:"items"`
	NextCursor        string `json:"next_cursor"`
	RequestLogEnabled bool   `json:"request_log_enabled"`
	TotalSizeBytes    int64  `json:"total_size_bytes"`
	MaxTotalSizeBytes int64  `json:"max_total_size_bytes"`
	LogDir            string `json:"log_dir"`
}

type attempt struct {
	Index        int                 `json:"index"`
	URL          string              `json:"url"`
	Method       string              `json:"method"`
	Headers      map[string][]string `json:"headers"`
	Body         string              `json:"body"`
	Status       *int                `json:"status"`
	RespHeaders  map[string][]string `json:"response_headers"`
	ResponseBody string              `json:"response_body"`
	Error        string              `json:"error"`
	Timestamp    string              `json:"timestamp"`
}

type detailResponse struct {
	Row    row `json:"row"`
	Client struct {
		URL           string              `json:"url"`
		Method        string              `json:"method"`
		Headers       map[string][]string `json:"headers"`
		Body          string              `json:"body"`
		BodyTruncated bool                `json:"body_truncated"`
	} `json:"client"`
	Attempts []attempt `json:"attempts"`
	Response struct {
		Status        int                 `json:"status"`
		Headers       map[string][]string `json:"headers"`
		Body          string              `json:"body"`
		BodyTruncated bool                `json:"body_truncated"`
		SSEEvents     []struct {
			Event string `json:"event"`
			Data  string `json:"data"`
		} `json:"sse_events"`
		Assembled *struct {
			Text      string `json:"text"`
			Reasoning string `json:"reasoning"`
			ToolCalls []any  `json:"tool_calls"`
		} `json:"assembled"`
	} `json:"response"`
	WebsocketTimeline    string `json:"websocket_timeline"`
	APIWebsocketTimeline string `json:"api_websocket_timeline"`
	RawSizeBytes         int64  `json:"raw_size_bytes"`
}

func (p *proxy) list(query string) listResponse {
	p.t.Helper()
	res := p.mgmt(http.MethodGet, "/v8/management/proxx/requests"+query, nil)
	if res.Status != http.StatusOK {
		p.t.Fatalf("list %s: %d %s", query, res.Status, res.Body)
	}
	var out listResponse
	if err := json.Unmarshal(res.Body, &out); err != nil {
		p.t.Fatalf("list json: %v", err)
	}
	return out
}

func (p *proxy) detail(id string) detailResponse {
	p.t.Helper()
	res := p.mgmt(http.MethodGet, "/v8/management/proxx/requests/"+id, nil)
	if res.Status != http.StatusOK {
		p.t.Fatalf("detail %s: %d %s", id, res.Status, res.Body)
	}
	var out detailResponse
	if err := json.Unmarshal(res.Body, &out); err != nil {
		p.t.Fatalf("detail json: %v", err)
	}
	return out
}

func i64(p *int64) int64 {
	if p == nil {
		return -1
	}
	return *p
}

// openStream connects to the SSE endpoint and forwards each "event: request" Row.
func (p *proxy) openStream(ctx context.Context) (<-chan row, <-chan struct{}) {
	p.t.Helper()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, p.base+"/v8/management/proxx/requests/stream", nil)
	req.Header.Set("Authorization", "Bearer "+mgmtKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		p.t.Fatalf("stream: %v", err)
	}
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		p.t.Fatalf("stream status %d content-type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	rows := make(chan row, 64)
	connected := make(chan struct{})
	go func() {
		defer func() { _ = resp.Body.Close() }()
		reader := bufio.NewReader(resp.Body)
		event := ""
		once := false
		for {
			line, errRead := reader.ReadString('\n')
			if errRead != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			switch {
			case strings.HasPrefix(line, ": connected"):
				if !once {
					once = true
					close(connected)
				}
			case strings.HasPrefix(line, "event: "):
				event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: ") && event == "request":
				var r row
				if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &r) == nil {
					rows <- r
				}
			case line == "":
				event = ""
			}
		}
	}()
	return rows, connected
}

func byModel(items []row, model string) (row, bool) {
	for _, r := range items {
		if r.RequestedModel == model {
			return r, true
		}
	}
	return row{}, false
}

func TestE2ERequestLogs(t *testing.T) {
	if os.Getenv("PROXX_E2E") != "1" {
		t.Skip("set PROXX_E2E=1 to run the proxx request-logs e2e test (builds and launches the proxy)")
	}
	dir := t.TempDir()
	bin := buildBinary(t, dir)
	mock := newMockUpstream()
	defer mock.srv.Close()
	mock.set("up-429", "429")
	mock.set("up-400", "400")

	port := freePort(t)
	authDir := filepath.Join(dir, "auths")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := startProxy(t, bin, dir, renderConfig(port, mock.srv.URL, authDir, ""))
	p.base = fmt.Sprintf("http://127.0.0.1:%d", port)
	waitFor(t, 30*time.Second, "proxy up", func() bool {
		return p.do(http.MethodGet, "/v1/models", nil, nil).Status == http.StatusOK
	})

	initial := p.list("")
	wantDir := filepath.Join(authDir, "logs")
	if !initial.RequestLogEnabled || initial.MaxTotalSizeBytes != 10240*1024*1024 || len(initial.Items) != 0 {
		t.Fatalf("initial list = %+v", initial)
	}
	if resolved, _ := filepath.EvalSymlinks(filepath.Dir(initial.LogDir)); resolved != mustEval(t, authDir) || filepath.Base(initial.LogDir) != "logs" {
		t.Fatalf("log_dir = %q, want %q", initial.LogDir, wantDir)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	streamRows, connected := p.openStream(ctx)
	select {
	case <-connected:
	case <-time.After(10 * time.Second):
		t.Fatal("stream did not connect")
	}

	if res := p.chat("model-ok", false, "alpha-nonstream"); res.Status != http.StatusOK {
		t.Fatalf("non-stream: %d %s", res.Status, res.Body)
	}
	if res := p.chat("model-ok", true, "beta-stream"); res.Status != http.StatusOK || len(res.SSEDatas) == 0 {
		t.Fatalf("stream: %d %s", res.Status, res.Body)
	}
	if res := p.chat("auto", false, "gamma-failover"); res.Status != http.StatusOK || res.Header.Get("X-Proxx-Served-Model") != "model-ok" {
		t.Fatalf("failover: %d %v %s", res.Status, res.Header, res.Body)
	}
	if res := p.chat("m400", false, "delta-bad"); res.Status != http.StatusBadRequest {
		t.Fatalf("400: %d %s", res.Status, res.Body)
	}

	var listed listResponse
	waitFor(t, 15*time.Second, "4 request logs with joined usage", func() bool {
		listed = p.list("")
		if len(listed.Items) != 4 {
			return false
		}
		r, _ := byModel(listed.Items, "auto")
		return r.InputTokens != nil
	})

	t.Run("rows", func(t *testing.T) {
		if listed.TotalSizeBytes <= 0 {
			t.Fatalf("total_size_bytes = %d", listed.TotalSizeBytes)
		}
		for i := 1; i < len(listed.Items); i++ {
			if listed.Items[i].TimeMs > listed.Items[i-1].TimeMs {
				t.Fatal("not newest first")
			}
		}
		if listed.Items[0].RequestedModel != "m400" {
			t.Fatalf("newest = %+v", listed.Items[0])
		}
		ns, _ := byModel(listed.Items, "model-ok")
		for _, r := range listed.Items {
			if r.RequestedModel == "model-ok" && !r.Stream {
				ns = r
			}
		}
		if ns.Status != 200 || ns.Method != "POST" || ns.Path != "/v1/chat/completions" || ns.Error || ns.Attempts != 1 ||
			i64(ns.InputTokens) != 3 || i64(ns.OutputTokens) != 4 || ns.DurationMs == nil || ns.Provider != "openai-compatible-pok" || len(ns.ID) != 8 ||
			ns.CostSource == "" || ns.SizeBytes <= 0 {
			t.Fatalf("non-stream row = %+v", ns)
		}
		var st row
		for _, r := range listed.Items {
			if r.RequestedModel == "model-ok" && r.Stream {
				st = r
			}
		}
		if !st.Stream || i64(st.InputTokens) != 5 || i64(st.OutputTokens) != 6 {
			t.Fatalf("stream row = %+v", st)
		}
		fo, _ := byModel(listed.Items, "auto")
		if fo.Attempts != 2 || fo.Group != "auto" || fo.ServedModel != "model-ok" || fo.Provider != "openai-compatible-pok" || i64(fo.InputTokens) != 3 || fo.Error {
			t.Fatalf("failover row = %+v", fo)
		}
		bad, _ := byModel(listed.Items, "m400")
		if bad.Status != 400 || !bad.Error || !strings.HasPrefix(bad.File, "error-") {
			t.Fatalf("400 row = %+v", bad)
		}
	})

	t.Run("filters and paging", func(t *testing.T) {
		cases := map[string]int{
			"?status=4xx":                     1,
			"?status=error":                   1,
			"?status=2xx":                     3,
			"?status=5xx":                     0,
			"?model=auto":                     1,
			"?model=model-ok":                 3,
			"?provider=openai-compatible-pok": 3,
			"?q=" + url.QueryEscape("GAMMA-failover"): 1,
			"?q=" + url.QueryEscape("rate limited"):   1,
			"?q=nothing-matches-this":                 0,
		}
		for query, want := range cases {
			if got := len(p.list(query).Items); got != want {
				t.Errorf("%s -> %d, want %d", query, got, want)
			}
		}
		future := time.Now().Add(time.Hour).UnixMilli()
		if got := len(p.list(fmt.Sprintf("?since=%d", future)).Items); got != 0 {
			t.Errorf("since future -> %d", got)
		}
		if got := len(p.list(fmt.Sprintf("?until=%d", future)).Items); got != 4 {
			t.Errorf("until future -> %d", got)
		}
		seen := map[string]bool{}
		cursor := ""
		for i := 0; i < 10; i++ {
			page := p.list("?limit=1&cursor=" + url.QueryEscape(cursor))
			if len(page.Items) != 1 {
				t.Fatalf("page %d has %d items", i, len(page.Items))
			}
			seen[page.Items[0].ID] = true
			if page.NextCursor == "" {
				break
			}
			cursor = page.NextCursor
		}
		if len(seen) != 4 {
			t.Fatalf("paging saw %d distinct rows", len(seen))
		}
		if res := p.mgmt(http.MethodGet, "/v8/management/proxx/requests?status=weird", nil); res.Status != http.StatusBadRequest {
			t.Fatalf("bad status filter -> %d", res.Status)
		}
		if res := p.do(http.MethodGet, "/v8/management/proxx/requests", nil, nil); res.Status != http.StatusUnauthorized {
			t.Fatalf("client key on management -> %d", res.Status)
		}
	})

	t.Run("detail and raw", func(t *testing.T) {
		var stID string
		for _, r := range listed.Items {
			if r.Stream {
				stID = r.ID
			}
		}
		d := p.detail(stID)
		if d.Row.ID != stID || !strings.Contains(d.Client.Body, "beta-stream") || d.Client.Headers["Authorization"][0] == "Bearer "+clientKey {
			t.Fatalf("stream detail client = %+v", d.Client)
		}
		if d.Response.Assembled == nil || d.Response.Assembled.Text != "streamed hello from up-ok" || len(d.Response.SSEEvents) < 4 {
			t.Fatalf("stream detail response = %+v", d.Response)
		}
		if len(d.Attempts) != 1 || d.Attempts[0].Status == nil || *d.Attempts[0].Status != 200 || !strings.Contains(d.Attempts[0].URL, "/pok/v1/chat/completions") {
			t.Fatalf("stream attempts = %+v", d.Attempts)
		}
		if d.RawSizeBytes <= 0 {
			t.Fatalf("raw_size_bytes = %d", d.RawSizeBytes)
		}

		fo, _ := byModel(listed.Items, "auto")
		fd := p.detail(fo.ID)
		if len(fd.Attempts) != 2 || *fd.Attempts[0].Status != 429 || *fd.Attempts[1].Status != 200 ||
			!strings.Contains(fd.Attempts[0].Body, `"up-429"`) || !strings.Contains(fd.Attempts[1].ResponseBody, "hello from up-ok") {
			t.Fatalf("failover attempts = %+v", fd.Attempts)
		}
		if fd.Response.Assembled != nil || fd.Response.SSEEvents != nil || fd.Response.Status != 200 {
			t.Fatalf("non-stream response = %+v", fd.Response)
		}

		bad, _ := byModel(listed.Items, "m400")
		bd := p.detail(bad.ID)
		if bd.Response.Status != 400 || !strings.Contains(bd.Response.Body, "invalid parameter foo") || len(bd.Attempts) != 1 || *bd.Attempts[0].Status != 400 {
			t.Fatalf("400 detail = %+v", bd)
		}

		raw := p.mgmt(http.MethodGet, "/v8/management/proxx/requests/"+bad.ID+"/raw", nil)
		if raw.Status != 200 || !strings.HasPrefix(raw.Header.Get("Content-Type"), "text/plain") || !strings.HasPrefix(string(raw.Body), "=== REQUEST INFO ===") || int64(len(raw.Body)) != bad.SizeBytes {
			t.Fatalf("raw = %d %q len %d", raw.Status, raw.Header.Get("Content-Type"), len(raw.Body))
		}
		if res := p.mgmt(http.MethodGet, "/v8/management/proxx/requests/deadbeef", nil); res.Status != http.StatusNotFound {
			t.Fatalf("missing id -> %d", res.Status)
		}
	})

	t.Run("stream delivers rows", func(t *testing.T) {
		got := map[string]row{}
		deadline := time.After(15 * time.Second)
		for len(got) < 4 {
			select {
			case r := <-streamRows:
				got[r.ID] = r
			case <-deadline:
				t.Fatalf("stream delivered %d rows, want 4", len(got))
			}
		}
		for _, r := range listed.Items {
			if _, ok := got[r.ID]; !ok {
				t.Fatalf("stream missed %s", r.ID)
			}
		}
		// A new request after the stream is open is delivered as a completed row.
		if res := p.chat("model-ok", true, "epsilon-live"); res.Status != http.StatusOK {
			t.Fatalf("live request: %d", res.Status)
		}
		select {
		case r := <-streamRows:
			if r.RequestedModel != "model-ok" || !r.Stream || r.Status != 200 || r.SizeBytes <= 0 {
				t.Fatalf("live row = %+v", r)
			}
			d := p.detail(r.ID)
			if d.Response.Assembled == nil || d.Response.Assembled.Text != "streamed hello from up-ok" {
				t.Fatalf("live row file incomplete: %+v", d.Response)
			}
		case <-time.After(15 * time.Second):
			t.Fatal("no row for live request")
		}
	})

	t.Run("toggle request-log via v8 config", func(t *testing.T) {
		res := p.mgmt(http.MethodPut, "/v8/management/config/observability/logs/request-log", false)
		if res.Status != http.StatusOK {
			t.Fatalf("disable request-log: %d %s", res.Status, res.Body)
		}
		waitFor(t, 15*time.Second, "request_log_enabled=false", func() bool { return !p.list("").RequestLogEnabled })
		res = p.mgmt(http.MethodPut, "/v8/management/config/observability/logs/request-log", true)
		if res.Status != http.StatusOK {
			t.Fatalf("enable request-log: %d %s", res.Status, res.Body)
		}
		waitFor(t, 15*time.Second, "request_log_enabled=true", func() bool { return p.list("").RequestLogEnabled })
	})

	t.Run("retention", func(t *testing.T) {
		big := strings.Repeat("z", 400*1024)
		for i := 0; i < 3; i++ {
			if res := p.chat("model-ok", false, fmt.Sprintf("big-%d-%s", i, big)); res.Status != http.StatusOK {
				t.Fatalf("big request %d: %d", i, res.Status)
			}
			time.Sleep(50 * time.Millisecond)
		}
		waitFor(t, 15*time.Second, "big logs indexed", func() bool { return len(p.list("").Items) == 8 })
		before := p.list("")
		if before.TotalSizeBytes <= 1024*1024 {
			t.Fatalf("total %d not above 1 MB", before.TotalSizeBytes)
		}
		newest := before.Items[0]
		// Hot reload a 1 MB cap.
		cfg := renderConfig(port, mock.srv.URL, authDir, "  request-logs:\n    max-total-size-mb: 1\n")
		if err := os.WriteFile(p.cfgPath, []byte(cfg), 0o600); err != nil {
			t.Fatal(err)
		}
		var after listResponse
		waitFor(t, 20*time.Second, "retention under 1 MB", func() bool {
			after = p.list("")
			return after.MaxTotalSizeBytes == 1024*1024 && after.TotalSizeBytes <= 1024*1024
		})
		if len(after.Items) == 0 || after.Items[0].ID != newest.ID || len(after.Items) >= len(before.Items) {
			t.Fatalf("retention kept %d of %d (newest %s, want %s)", len(after.Items), len(before.Items), firstID(after.Items), newest.ID)
		}
		for _, r := range after.Items {
			if r.TimeMs < before.Items[len(after.Items)-1].TimeMs {
				t.Fatalf("retention kept an older file %s", r.ID)
			}
		}
		if _, err := os.Stat(p.logPath); err != nil {
			t.Fatal("proxy stdout log removed")
		}
	})

	t.Run("unlimited cap survives v8 config writes", func(t *testing.T) {
		res := p.mgmt(http.MethodPut, "/v8/management/config/proxx/request-logs/max-total-size-mb", 0)
		if res.Status != http.StatusOK {
			t.Fatalf("set cap 0 via v8 config: %d %s", res.Status, res.Body)
		}
		waitFor(t, 15*time.Second, "max_total_size_bytes=0", func() bool { return p.list("").MaxTotalSizeBytes == 0 })
		// Another v8 config write must keep the explicit 0 (unset would mean 10 GB).
		if res = p.mgmt(http.MethodPut, "/v8/management/config/observability/logs/request-log", true); res.Status != http.StatusOK {
			t.Fatalf("v8 write: %d %s", res.Status, res.Body)
		}
		data, err := os.ReadFile(p.cfgPath)
		if err != nil || !strings.Contains(string(data), "max-total-size-mb: 0") {
			t.Fatalf("config lost max-total-size-mb: 0 (err=%v):\n%s", err, data)
		}
		time.Sleep(2 * time.Second) // let any reload triggered by the write land
		if got := p.list("").MaxTotalSizeBytes; got != 0 {
			t.Fatalf("max_total_size_bytes after v8 write = %d", got)
		}
		get := p.mgmt(http.MethodGet, "/v8/management/config/proxx/request-logs/max-total-size-mb", nil)
		if get.Status != http.StatusOK || strings.TrimSpace(string(get.Body)) == "" {
			t.Fatalf("get cap: %d %s", get.Status, get.Body)
		}
		t.Logf("GET cap -> %s", get.Body)
	})

	t.Run("delete", func(t *testing.T) {
		// Make sure there are at least two files, then delete one and then all.
		for i := 0; i < 2; i++ {
			_ = p.chat("model-ok", false, fmt.Sprintf("tiny-%d", i))
		}
		var cur listResponse
		waitFor(t, 15*time.Second, "new files", func() bool {
			cur = p.list("")
			return len(cur.Items) >= 2 && cur.Items[0].RequestedModel == "model-ok" && strings.Contains(string(p.mgmt(http.MethodGet, "/v8/management/proxx/requests/"+cur.Items[0].ID+"/raw", nil).Body), "tiny-1")
		})
		id := cur.Items[0].ID
		if res := p.mgmt(http.MethodDelete, "/v8/management/proxx/requests/"+id, nil); res.Status != http.StatusOK {
			t.Fatalf("delete one: %d %s", res.Status, res.Body)
		}
		if res := p.mgmt(http.MethodGet, "/v8/management/proxx/requests/"+id, nil); res.Status != http.StatusNotFound {
			t.Fatalf("deleted id still found: %d", res.Status)
		}
		res := p.mgmt(http.MethodDelete, "/v8/management/proxx/requests", nil)
		removed, _ := res.JSON["removed"].(float64)
		if res.Status != http.StatusOK || int(removed) != len(cur.Items)-1 {
			t.Fatalf("delete all: %d %s (had %d)", res.Status, res.Body, len(cur.Items)-1)
		}
		if left := p.list(""); len(left.Items) != 0 || left.TotalSizeBytes != 0 {
			t.Fatalf("after delete all: %+v", left)
		}
	})
}

func firstID(items []row) string {
	if len(items) == 0 {
		return ""
	}
	return items[0].ID
}

func mustEval(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}
