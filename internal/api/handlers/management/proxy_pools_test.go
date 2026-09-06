package management

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// fakePoolStore satisfies store.ProxyPoolStore in-memory.
type fakePoolStore struct {
	pools      map[int64]store.ProxyPool
	nextID     int64
	boundCount int64
}

func newFakePoolStore() *fakePoolStore {
	return &fakePoolStore{pools: map[int64]store.ProxyPool{}, nextID: 1}
}

func (f *fakePoolStore) List(ctx context.Context) ([]store.ProxyPool, error) {
	out := make([]store.ProxyPool, 0, len(f.pools))
	for _, p := range f.pools {
		out = append(out, p)
	}
	return out, nil
}

func (f *fakePoolStore) Get(ctx context.Context, id int64) (*store.ProxyPool, error) {
	if p, ok := f.pools[id]; ok {
		return &p, nil
	}
	return nil, store.ErrProxyPoolNotFound
}

func (f *fakePoolStore) Create(ctx context.Context, p store.ProxyPool) (*store.ProxyPool, error) {
	p.ID = f.nextID
	f.nextID++
	if p.TestStatus == "" {
		p.TestStatus = "unknown"
	}
	f.pools[p.ID] = p
	return &p, nil
}

func (f *fakePoolStore) Update(ctx context.Context, p store.ProxyPool) (*store.ProxyPool, error) {
	if _, ok := f.pools[p.ID]; !ok {
		return nil, store.ErrProxyPoolNotFound
	}
	f.pools[p.ID] = p
	return &p, nil
}

func (f *fakePoolStore) Delete(ctx context.Context, id int64) error {
	if _, ok := f.pools[id]; !ok {
		return store.ErrProxyPoolNotFound
	}
	delete(f.pools, id)
	return nil
}

func (f *fakePoolStore) BoundEntryCount(ctx context.Context, poolID int64) (int64, error) {
	return f.boundCount, nil
}

func newPoolTestRouter(h *Handler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/proxy-pools", h.ListProxyPools)
	r.POST("/proxy-pools", h.CreateProxyPool)
	r.GET("/proxy-pools/:id", h.GetProxyPool)
	r.PUT("/proxy-pools/:id", h.UpdateProxyPool)
	r.DELETE("/proxy-pools/:id", h.DeleteProxyPool)
	r.POST("/proxy-pools/:id/test", h.TestProxyPool)
	r.POST("/proxy-pools/batch-import", h.BatchImportProxyPools)
	r.POST("/proxy-pools/relay-deploy", h.DeployRelayProxyPool)
	return r
}

func postJSON(t *testing.T, r *gin.Engine, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	r.ServeHTTP(res, req)
	return res
}

func sendMethod(t *testing.T, r *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	res := httptest.NewRecorder()
	r.ServeHTTP(res, req)
	return res
}

func TestProxyPoolStoreSetterWiring(t *testing.T) {
	h := &Handler{}
	if _, ok := h.proxyPoolsStore(nil); ok {
		t.Fatal("nil store must report not-ok")
	}
	h.SetProxyPoolStore(nil)
	if _, ok := h.proxyPoolsStore(nil); ok {
		t.Fatal("explicit nil must report not-ok")
	}
}

func TestProxyPoolRoutes503WithoutStore(t *testing.T) {
	h := &Handler{}
	r := newPoolTestRouter(h)
	res := sendMethod(t, r, http.MethodGet, "/proxy-pools", "")
	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("list without store = %d, want 503", res.Code)
	}
}

func TestCreateProxyPoolValidation(t *testing.T) {
	h := &Handler{}
	h.SetProxyPoolStore(newFakePoolStore())
	r := newPoolTestRouter(h)

	// Missing name.
	res := postJSON(t, r, "/proxy-pools", `{"proxy_url":"http://1.2.3.4:8080"}`)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("missing name = %d", res.Code)
	}
	// Missing URL.
	res = postJSON(t, r, "/proxy-pools", `{"name":"x"}`)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("missing url = %d", res.Code)
	}
	// Bad scheme.
	res = postJSON(t, r, "/proxy-pools", `{"name":"x","proxy_url":"ftp://1.2.3.4:21"}`)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("bad scheme = %d", res.Code)
	}
	// Bad type.
	res = postJSON(t, r, "/proxy-pools", `{"name":"x","proxy_url":"http://1.2.3.4:8080","type":"socks4a"}`)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("bad type = %d", res.Code)
	}
	// Relay type with non-https URL.
	res = postJSON(t, r, "/proxy-pools", `{"name":"x","proxy_url":"http://relay.example","type":"cloudflare"}`)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("relay http base = %d", res.Code)
	}

	// Good create with defaults.
	res = postJSON(t, r, "/proxy-pools", `{"name":"std","proxy_url":"socks5://u:p@1.2.3.4:1080","no_proxy":".corp"}`)
	if res.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", res.Code, res.Body.String())
	}
	var body struct {
		Pool store.ProxyPool `json:"pool"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Defaults applied by the handler: active + strict, type http, untested.
	if !body.Pool.IsActive || !body.Pool.StrictProxy || body.Pool.Type != "http" || body.Pool.TestStatus != "unknown" {
		t.Fatalf("defaults = %+v", body.Pool)
	}
}

func TestUpdateProxyPoolMerges(t *testing.T) {
	h := &Handler{}
	fs := newFakePoolStore()
	h.SetProxyPoolStore(fs)
	r := newPoolTestRouter(h)

	res := postJSON(t, r, "/proxy-pools", `{"name":"up","proxy_url":"http://1.2.3.4:8080"}`)
	if res.Code != http.StatusCreated {
		t.Fatalf("create = %d", res.Code)
	}
	res = sendMethod(t, r, http.MethodPut, "/proxy-pools/1", `{"name":"up","proxy_url":"http://1.2.3.4:8080","is_active":false}`)
	if res.Code != http.StatusOK {
		t.Fatalf("update = %d: %s", res.Code, res.Body.String())
	}
	got, _ := fs.Get(context.Background(), 1)
	if got.IsActive {
		t.Fatal("is_active=false must persist")
	}
	// Update of a missing pool → 404.
	res = sendMethod(t, r, http.MethodPut, "/proxy-pools/999", `{"name":"x","proxy_url":"http://1:1"}`)
	if res.Code != http.StatusNotFound {
		t.Fatalf("update missing = %d", res.Code)
	}
}

func TestDeleteProxyPoolBlockedWhileBound(t *testing.T) {
	h := &Handler{}
	fs := newFakePoolStore()
	fs.boundCount = 3
	if _, err := fs.Create(context.Background(), store.ProxyPool{Name: "b", ProxyURL: "http://1:1", IsActive: true}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	h.SetProxyPoolStore(fs)
	r := newPoolTestRouter(h)

	res := sendMethod(t, r, http.MethodDelete, "/proxy-pools/1", "")
	if res.Code != http.StatusConflict {
		t.Fatalf("delete bound = %d, want 409", res.Code)
	}
	if !strings.Contains(res.Body.String(), `"bound_entry_count":3`) {
		t.Fatalf("body = %s", res.Body.String())
	}

	// Unbound pool deletes cleanly.
	fs.boundCount = 0
	res = sendMethod(t, r, http.MethodDelete, "/proxy-pools/1", "")
	if res.Code != http.StatusOK {
		t.Fatalf("delete unbound = %d", res.Code)
	}
	if _, err := fs.Get(context.Background(), 1); !errors.Is(err, store.ErrProxyPoolNotFound) {
		t.Fatalf("pool must be gone, got %v", err)
	}
}

func TestListProxyPoolsIncludeUsage(t *testing.T) {
	h := &Handler{}
	fs := newFakePoolStore()
	fs.boundCount = 5
	if _, err := fs.Create(context.Background(), store.ProxyPool{Name: "u", ProxyURL: "http://1:1", IsActive: true}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	h.SetProxyPoolStore(fs)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/proxy-pools", h.ListProxyPools)

	res := sendMethod(t, r, http.MethodGet, "/proxy-pools?include_usage=1", "")
	if res.Code != http.StatusOK {
		t.Fatalf("list = %d", res.Code)
	}
	if !strings.Contains(res.Body.String(), `"bound_entry_count":5`) {
		t.Fatalf("body = %s", res.Body.String())
	}
	if !strings.Contains(res.Body.String(), `"pools"`) {
		t.Fatalf("list key must be pools: %s", res.Body.String())
	}
}

// TestBatchImportParseLines pins the 9router parse semantics server-side.
func TestBatchImportParseLines(t *testing.T) {
	cases := []struct{ in, want string }{
		{"1.2.3.4:8080", "http://1.2.3.4:8080"},
		{"user:pass@host:3128", "http://user:pass@host:3128"},
		{"socks5://h:1080", "socks5://h:1080"},
		{"https://h:8443", "https://h:8443"},
		{"http://h", "http://h"},
	}
	for _, tc := range cases {
		got, err := parseProxyLine(tc.in)
		if err != nil || got != tc.want {
			t.Fatalf("parse(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
	for _, bad := range []string{"nonsense", "a:b:c:d:e", ":pass@h:1", "u:@h:1"} {
		if _, err := parseProxyLine(bad); err == nil {
			t.Fatalf("parse(%q) must error", bad)
		}
	}
}

func TestBatchImportHandler(t *testing.T) {
	h := &Handler{}
	h.SetProxyPoolStore(newFakePoolStore())
	r := newPoolTestRouter(h)

	res := postJSON(t, r, "/proxy-pools/batch-import", `{"lines":["1.2.3.4:8080","user:pass@h:3128","garbage-line","1.2.3.4:8080"]}`)
	if res.Code != http.StatusOK {
		t.Fatalf("import = %d: %s", res.Code, res.Body.String())
	}
	var body struct {
		Created int `json:"created"`
		Skipped int `json:"skipped"`
		Failed  int `json:"failed"`
		Errors  []struct {
			Line  int    `json:"line"`
			Error string `json:"error"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Created != 2 || body.Skipped != 1 || body.Failed != 1 {
		t.Fatalf("counts = %+v", body)
	}
	if len(body.Errors) != 1 || body.Errors[0].Line != 3 {
		t.Fatalf("errors = %+v", body.Errors)
	}
}
