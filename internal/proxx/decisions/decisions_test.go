package decisions

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

type fakeClock struct{ t time.Time }

func (f *fakeClock) now() time.Time { return f.t }

func withClock(t *testing.T) *fakeClock {
	t.Helper()
	clk := &fakeClock{t: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)}
	prev := nowFunc
	nowFunc = clk.now
	t.Cleanup(func() { nowFunc = prev })
	return clk
}

func TestPoolRotatesAndSkipsCoolingKeys(t *testing.T) {
	clk := withClock(t)
	p := newPool()
	keys := []string{"a", "b", "c"}
	first, _ := p.order("v", keys)
	second, _ := p.order("v", keys)
	if first[0] == second[0] || len(first) != 3 {
		t.Fatalf("no rotation: %v then %v", first, second)
	}
	p.cool("v", "b", clk.t.Add(time.Minute))
	got, _ := p.order("v", keys)
	for _, i := range got {
		if keys[i] == "b" {
			t.Fatalf("cooling key offered: %v", got)
		}
	}
	p.cool("v", "a", clk.t.Add(30*time.Second))
	p.cool("v", "c", clk.t.Add(2*time.Minute))
	got, earliest := p.order("v", keys)
	if len(got) != 0 || !earliest.Equal(clk.t.Add(30*time.Second)) {
		t.Fatalf("all cooling: got %v earliest %v", got, earliest)
	}
	clk.t = clk.t.Add(31 * time.Second)
	if got, _ := p.order("v", keys); len(got) != 1 || keys[got[0]] != "a" {
		t.Fatalf("after cooldown: %v", got)
	}
}

func TestRequestModel(t *testing.T) {
	if m := requestModel("application/json", []byte(`{"model":"jev-latest","state":"x"}`)); m != "jev-latest" {
		t.Fatalf("json model = %q", m)
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("request", `{"model":"jev-1.14.0","questions":{}}`)
	fw, _ := mw.CreateFormFile("image", "a.png")
	_, _ = fw.Write([]byte("PNG"))
	_ = mw.Close()
	if m := requestModel(mw.FormDataContentType(), buf.Bytes()); m != "jev-1.14.0" {
		t.Fatalf("multipart model = %q", m)
	}
	if m := requestModel("application/json", []byte(`not json`)); m != "" {
		t.Fatalf("invalid body model = %q", m)
	}
}

func TestParseResponseUsage(t *testing.T) {
	model, d := parseResponse([]byte(`{"model":"gpt-6-luna","answers":[],"usage":{"input_tokens":42,"output_tokens":0,"total_tokens":42,"input_tokens_details":{"cached_tokens":10,"cache_write_tokens":2}}}`))
	b := d.TokenBreakdown
	if model != "gpt-6-luna" || d.InputTokens != 42 || d.CachedTokens != 10 || !b.Valid() || b.Quality != usage.TokenAccountingQualityComplete ||
		b.Input.UncachedTokens != 30 || b.Input.CacheReadTokens != 10 || b.Input.CacheWriteTokens != 2 || b.TotalTokens != 42 {
		t.Fatalf("openai usage: model=%q detail=%+v", model, d)
	}
	model, d = parseResponse([]byte(`{"model":"jev-1.14.0","answers":{},"usage":{"input_tokens":120,"output_tokens":12}}`))
	if model != "jev-1.14.0" || d.InputTokens != 120 || d.OutputTokens != 12 || d.TotalTokens != 132 || !d.TokenBreakdown.Valid() {
		t.Fatalf("typesafe usage: model=%q detail=%+v", model, d)
	}
	if model, d = parseResponse([]byte(`{"error":"x"}`)); model != "" || d.TotalTokens != 0 {
		t.Fatalf("no usage: %q %+v", model, d)
	}
}

// upstream is a scripted mock vendor: each request pops the next status (200 when exhausted).
type upstream struct {
	mu       sync.Mutex
	statuses []int
	headers  map[int]http.Header
	keys     []string
	bodies   [][]byte
	reqHdrs  []http.Header
	srv      *httptest.Server
}

func newUpstream(t *testing.T, path string, statuses ...int) *upstream {
	u := &upstream{statuses: statuses, headers: map[int]http.Header{}}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			http.Error(w, "bad path "+r.URL.Path, http.StatusNotFound)
			return
		}
		body, _ := io.ReadAll(r.Body)
		u.mu.Lock()
		u.keys = append(u.keys, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		u.bodies = append(u.bodies, body)
		u.reqHdrs = append(u.reqHdrs, r.Header.Clone())
		status := http.StatusOK
		if len(u.statuses) > 0 {
			status, u.statuses = u.statuses[0], u.statuses[1:]
		}
		extra := u.headers[status]
		u.mu.Unlock()
		for k, v := range extra {
			w.Header()[k] = v
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Set-Cookie", "upstream=1")
		w.Header().Set("X-Request-Id", "req_123")
		w.WriteHeader(status)
		if status == http.StatusOK {
			_, _ = io.WriteString(w, `{"model":"jev-1.14.0","answers":{"q":{"type":"noul","noul":0.9}},"usage":{"input_tokens":100,"output_tokens":5}}`)
			return
		}
		_, _ = io.WriteString(w, `{"error":{"message":"status `+http.StatusText(status)+`"}}`)
	}))
	t.Cleanup(u.srv.Close)
	return u
}

type harness struct {
	t       *testing.T
	engine  *gin.Engine
	records []usage.Record
	mu      sync.Mutex
}

func newHarness(t *testing.T, v *Vendor, vc config.ProxxDecisionsVendor) *harness {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := &harness{t: t, engine: gin.New()}
	prevPublish := publishFn
	publishFn = func(_ context.Context, r usage.Record) {
		h.mu.Lock()
		h.records = append(h.records, r)
		h.mu.Unlock()
	}
	t.Cleanup(func() { publishFn = prevPublish })
	cfg := &config.Config{}
	if v == TypeSafe {
		cfg.Proxx.Decisions.TypeSafe = vc
	} else {
		cfg.Proxx.Decisions.OpenAI = vc
	}
	pools = newPool()
	h.engine.POST(v.Route, NewHandler(v, func() *config.Config { return cfg }))
	return h
}

func (h *harness) do(path, contentType string, body []byte, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer client-key")
	req.Header.Set("Accept-Encoding", "gzip")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.engine.ServeHTTP(rec, req)
	return rec
}

const jevBody = `{"model":"jev-latest","state":"refund please","questions":{"q":{"type":"noul","instructions":"billing?"}}}`

func TestHandlerPassthroughAndFailover(t *testing.T) {
	withClock(t)
	up := newUpstream(t, "/v1/systemone", http.StatusTooManyRequests, http.StatusOK)
	up.headers[http.StatusTooManyRequests] = http.Header{"Retry-After": {"30"}}
	h := newHarness(t, TypeSafe, config.ProxxDecisionsVendor{BaseURL: up.srv.URL, APIKeys: []string{"ts-key-1", "ts-key-2"}})

	rec := h.do("/v1/systemone", "application/json", []byte(jevBody), map[string]string{"TypeSafe-Danger-Zone-Images": "true", "X-Other": "drop"})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"noul":0.9`) {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
	if rec.Header().Get("Set-Cookie") != "" || rec.Header().Get("X-Request-Id") != "req_123" {
		t.Fatalf("response headers = %v", rec.Header())
	}
	if len(up.keys) != 2 || up.keys[0] == up.keys[1] || up.keys[0] == "client-key" || up.keys[1] == "client-key" {
		t.Fatalf("upstream keys = %v", up.keys)
	}
	for i, b := range up.bodies {
		if string(b) != jevBody {
			t.Fatalf("body %d not forwarded verbatim: %s", i, b)
		}
	}
	hdr := up.reqHdrs[1]
	if hdr.Get("Typesafe-Danger-Zone-Images") != "true" || hdr.Get("X-Other") != "" {
		t.Fatalf("forwarded headers = %v", hdr)
	}
	if len(h.records) != 2 || !h.records[0].Failed || h.records[0].Fail.StatusCode != 429 || h.records[1].Failed {
		t.Fatalf("records = %+v", h.records)
	}
	ok := h.records[1]
	if ok.Provider != "typesafe" || ok.Model != "jev-1.14.0" || ok.Alias != "jev-latest" || ok.ExecutorType != ExecutorType ||
		ok.Detail.InputTokens != 100 || !ok.Detail.TokenBreakdown.Valid() || strings.Contains(ok.AuthID, "ts-key") {
		t.Fatalf("usage record = %+v", ok)
	}

	// The 429'd key is cooling for 30s: the next two requests both go to the other key.
	first429 := up.keys[0]
	h.do("/v1/systemone", "application/json", []byte(jevBody), nil)
	h.do("/v1/systemone", "application/json", []byte(jevBody), nil)
	for _, k := range up.keys[2:] {
		if k == first429 {
			t.Fatalf("cooling key reused: %v", up.keys)
		}
	}
}

func TestHandlerAllCoolingReturns429WithRetryAfter(t *testing.T) {
	withClock(t)
	up := newUpstream(t, "/decisions", http.StatusTooManyRequests)
	up.headers[http.StatusTooManyRequests] = http.Header{"Retry-After": {"45"}}
	h := newHarness(t, OpenAI, config.ProxxDecisionsVendor{BaseURL: up.srv.URL, APIKeys: []string{"sk-1"}})
	rec := h.do("/v1/decisions", "application/json", []byte(`{"model":"gpt-6-luna"}`), nil)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("first: %d", rec.Code)
	}
	rec = h.do("/v1/decisions", "application/json", []byte(`{"model":"gpt-6-luna"}`), nil)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "45" || len(up.keys) != 1 {
		t.Fatalf("cooling: %d retry-after %q upstream calls %d", rec.Code, rec.Header().Get("Retry-After"), len(up.keys))
	}
}

func TestHandlerClientErrorIsNotRetried(t *testing.T) {
	withClock(t)
	up := newUpstream(t, "/decisions", http.StatusBadRequest)
	h := newHarness(t, OpenAI, config.ProxxDecisionsVendor{BaseURL: up.srv.URL, APIKeys: []string{"sk-1", "sk-2"}})
	rec := h.do("/v1/decisions", "application/json", []byte(`{"model":"gpt-6-luna"}`), nil)
	if rec.Code != http.StatusBadRequest || len(up.keys) != 1 || !strings.Contains(rec.Body.String(), "Bad Request") {
		t.Fatalf("status %d calls %d body %s", rec.Code, len(up.keys), rec.Body)
	}
}

func TestHandlerServerErrorTriesEveryKeyOnce(t *testing.T) {
	withClock(t)
	up := newUpstream(t, "/decisions", http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusOK)
	h := newHarness(t, OpenAI, config.ProxxDecisionsVendor{BaseURL: up.srv.URL, APIKeys: []string{"sk-1", "sk-2"}})
	rec := h.do("/v1/decisions", "application/json", []byte(`{"model":"gpt-6-luna"}`), nil)
	if rec.Code != http.StatusServiceUnavailable || len(up.keys) != 2 {
		t.Fatalf("status %d calls %d", rec.Code, len(up.keys))
	}
}

func TestHandlerNoKeysAndOversize(t *testing.T) {
	withClock(t)
	h := newHarness(t, TypeSafe, config.ProxxDecisionsVendor{})
	if rec := h.do("/v1/systemone", "application/json", []byte(jevBody), nil); rec.Code != http.StatusServiceUnavailable ||
		!strings.Contains(rec.Body.String(), "proxx.decisions.typesafe.api-keys") {
		t.Fatalf("no keys: %d %s", rec.Code, rec.Body)
	}

	up := newUpstream(t, "/v1/systemone")
	h = newHarness(t, TypeSafe, config.ProxxDecisionsVendor{BaseURL: up.srv.URL, APIKeys: []string{"k"}})
	prev := maxBodyBytes
	maxBodyBytes = 16
	t.Cleanup(func() { maxBodyBytes = prev })
	if rec := h.do("/v1/systemone", "application/json", []byte(jevBody), nil); rec.Code != http.StatusRequestEntityTooLarge || len(up.keys) != 0 {
		t.Fatalf("oversize: %d calls %d", rec.Code, len(up.keys))
	}
}

func TestHandlerForwardsMultipartVerbatim(t *testing.T) {
	withClock(t)
	up := newUpstream(t, "/v1/systemone")
	h := newHarness(t, TypeSafe, config.ProxxDecisionsVendor{BaseURL: up.srv.URL, APIKeys: []string{"k"}})
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("request", jevBody)
	_ = mw.Close()
	rec := h.do("/v1/systemone", mw.FormDataContentType(), buf.Bytes(), nil)
	if rec.Code != http.StatusOK || !bytes.Equal(up.bodies[0], buf.Bytes()) || up.reqHdrs[0].Get("Content-Type") != mw.FormDataContentType() {
		t.Fatalf("multipart: %d same=%v ct=%q", rec.Code, bytes.Equal(up.bodies[0], buf.Bytes()), up.reqHdrs[0].Get("Content-Type"))
	}
	if h.records[0].Alias != "jev-latest" {
		t.Fatalf("multipart alias = %q", h.records[0].Alias)
	}
}
