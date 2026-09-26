package management

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// fakeUpstreamProviderStore is an in-memory UpstreamProviderStore for
// management endpoint tests. Mirrors the PG store's child-row id stamping
// closely enough for seed/refresh/quota probe coverage.
type fakeUpstreamProviderStore struct {
	mu    sync.Mutex
	seq   int64
	rows  map[int64]store.UpstreamProvider
	seqE  int64
	fails error
}

func newFakeUpstreamProviderStore() *fakeUpstreamProviderStore {
	return &fakeUpstreamProviderStore{rows: make(map[int64]store.UpstreamProvider)}
}

func (f *fakeUpstreamProviderStore) List(ctx context.Context) ([]store.UpstreamProvider, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]store.UpstreamProvider, 0, len(f.rows))
	for _, p := range f.rows {
		out = append(out, p)
	}
	return out, f.fails
}

func (f *fakeUpstreamProviderStore) Get(ctx context.Context, id int64) (*store.UpstreamProvider, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p, ok := f.rows[id]; ok {
		return &p, nil
	}
	return nil, store.ErrUpstreamProviderNotFound
}

func (f *fakeUpstreamProviderStore) Create(ctx context.Context, p store.UpstreamProvider) (*store.UpstreamProvider, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	p.ID = f.seq
	// Stamp child ids like the PG store does.
	for i := range p.APIKeyEntries {
		f.seqE++
		p.APIKeyEntries[i].ID = f.seqE
		p.APIKeyEntries[i].ProviderID = p.ID
	}
	f.rows[p.ID] = p
	out := p
	return &out, f.fails
}

func (f *fakeUpstreamProviderStore) Update(ctx context.Context, p store.UpstreamProvider) (*store.UpstreamProvider, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.rows[p.ID]; !ok {
		return nil, store.ErrUpstreamProviderNotFound
	}
	// Preserve child entry ids for entries that carry one; stamp new ones.
	for i := range p.APIKeyEntries {
		if p.APIKeyEntries[i].ID == 0 {
			f.seqE++
			p.APIKeyEntries[i].ID = f.seqE
		}
		p.APIKeyEntries[i].ProviderID = p.ID
	}
	f.rows[p.ID] = p
	out := p
	return &out, f.fails
}

func (f *fakeUpstreamProviderStore) Delete(ctx context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.rows, id)
	return f.fails
}

func (f *fakeUpstreamProviderStore) Count(ctx context.Context) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return int64(len(f.rows)), f.fails
}

// SetEntryAutoDisabled stubs the auto-disable sink primitive. The management
// routes that exercise the sink use a dedicated adapter stub (see
// auto_disable_sink_test.go); this fake needs the method only to satisfy the
// UpstreamProviderStore interface.
func (f *fakeUpstreamProviderStore) SetEntryAutoDisabled(ctx context.Context, entryID int64, code string) (bool, error) {
	if f == nil {
		return false, nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fails != nil {
		return false, f.fails
	}
	for _, p := range f.rows {
		for i := range p.APIKeyEntries {
			if p.APIKeyEntries[i].ID == entryID {
				if p.APIKeyEntries[i].AutoDisabled || (p.APIKeyEntries[i].Disabled && !p.APIKeyEntries[i].AutoDisabled) {
					return false, nil
				}
				p.APIKeyEntries[i].Disabled = true
				p.APIKeyEntries[i].AutoDisabled = true
				now := time.Now()
				p.APIKeyEntries[i].AutoDisabledAt = &now
				p.APIKeyEntries[i].AutoDisabledReason = code
				f.rows[p.ID] = p
				return true, nil
			}
		}
	}
	return false, nil
}

// ReenableExpiredAutoDisabled stubs the auto-re-enable sweeper primitive. The
// sweeper has its own dedicated stub (auto_disable_sweeper_test.go); this fake
// needs the method only to satisfy the UpstreamProviderStore interface. It
// re-enables a real auto-disabled (auto_disabled=true, not manually disabled)
// entry whose cooldown has elapsed, mirroring the store's sweep semantics.
func (f *fakeUpstreamProviderStore) ReenableExpiredAutoDisabled(ctx context.Context) (int64, error) {
	if f == nil {
		return 0, nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fails != nil {
		return 0, f.fails
	}
	var reenabled int64
	for id, p := range f.rows {
		changed := false
		for i := range p.APIKeyEntries {
			e := &p.APIKeyEntries[i]
			if !e.AutoDisabled {
				continue
			}
			cd := p.AutoDisableCooldownSeconds
			if cd == nil || *cd <= 0 {
				continue // manual re-enable only
			}
			if e.AutoDisabledAt == nil || !e.AutoDisabledAt.Before(time.Now().Add(-time.Duration(*cd)*time.Second)) {
				continue // cooldown not yet elapsed
			}
			e.AutoDisabled = false
			e.Disabled = false
			e.AutoDisabledAt = nil
			e.AutoDisabledReason = ""
			changed = true
			reenabled++
		}
		if changed {
			f.rows[id] = p
		}
	}
	return reenabled, nil
}

// newSeedModelsHandler builds a Handler with the fake store wired, plus a
// POST helper targeting /upstream-providers/:id/<action>.
func newSeedModelsHandler(t *testing.T, st *fakeUpstreamProviderStore) *Handler {
	t.Helper()
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, coreauth.NewManager(nil, nil, nil))
	h.SetUpstreamProvidersStore(st)
	return h
}

func postProviderAction(t *testing.T, h *Handler, id int64, action, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(http.MethodPost, "/v0/management/upstream-providers/"+strconv.FormatInt(id, 10)+"/"+action, nil)
	} else {
		req = httptest.NewRequest(http.MethodPost, "/v0/management/upstream-providers/"+strconv.FormatInt(id, 10)+"/"+action, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	ginCtx.Request = req
	ginCtx.Params = gin.Params{{Key: "id", Value: strconv.FormatInt(id, 10)}}
	if action == "seed-models" {
		h.SeedUpstreamProviderModels(ginCtx)
		return rec
	}
	if action == "refresh-models" {
		h.RefreshUpstreamProviderModels(ginCtx)
		return rec
	}
	if action == "quota" {
		h.UpstreamProviderQuota(ginCtx)
		return rec
	}
	t.Fatalf("unknown action %q", action)
	return rec
}

func TestSeedUpstreamProviderModels_Idempotent(t *testing.T) {
	st := newFakeUpstreamProviderStore()
	created, errCreate := st.Create(context.Background(), store.UpstreamProvider{
		ProviderType: "opencode-go",
		Name:         "ocgo",
		BaseURL:      "https://opencode.ai/zen/go/v1",
	})
	if errCreate != nil {
		t.Fatalf("Create: %v", errCreate)
	}
	h := newSeedModelsHandler(t, st)

	rec := postProviderAction(t, h, created.ID, "seed-models", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("first seed status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"added":24`) {
		t.Fatalf("first seed body = %s, want added:24", rec.Body.String())
	}

	// Second call adds nothing.
	rec2 := postProviderAction(t, h, created.ID, "seed-models", "")
	if rec2.Code != http.StatusOK || !strings.Contains(rec2.Body.String(), `"added":0`) {
		t.Fatalf("second seed = %d %s, want added:0", rec2.Code, rec2.Body.String())
	}

	row, errGet := st.Get(context.Background(), created.ID)
	if errGet != nil {
		t.Fatalf("Get: %v", errGet)
	}
	if len(row.Models) != 24 {
		t.Fatalf("model count = %d, want 24", len(row.Models))
	}
	// Anthropic wire formats survive the round trip.
	found := false
	for _, m := range row.Models {
		if m.Name == "minimax-m3" {
			found = m.WireFormat == "anthropic"
		}
	}
	if !found {
		t.Fatal("minimax-m3 missing anthropic wire format after seed")
	}
}

func TestSeedUpstreamProviderModels_PreservesExistingModels(t *testing.T) {
	st := newFakeUpstreamProviderStore()
	created, errCreate := st.Create(context.Background(), store.UpstreamProvider{
		ProviderType: "opencode-go",
		Name:         "ocgo",
		BaseURL:      "https://opencode.ai/zen/go/v1",
		Models: []store.UpstreamProviderModel{
			{Name: "glm-5.2"},             // collides with seed: must not duplicate
			{Name: "my-custom-model-xyz"}, // must survive untouched
		},
	})
	if errCreate != nil {
		t.Fatalf("Create: %v", errCreate)
	}
	h := newSeedModelsHandler(t, st)
	rec := postProviderAction(t, h, created.ID, "seed-models", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("seed status = %d body=%s", rec.Code, rec.Body.String())
	}
	row, errGet := st.Get(context.Background(), created.ID)
	if errGet != nil {
		t.Fatalf("Get: %v", errGet)
	}
	counts := map[string]int{}
	custom := 0
	for _, m := range row.Models {
		counts[m.Name]++
		if m.Name == "my-custom-model-xyz" {
			custom++
		}
	}
	if counts["glm-5.2"] != 1 {
		t.Fatalf("glm-5.2 duplicated: %d", counts["glm-5.2"])
	}
	if custom != 1 {
		t.Fatalf("custom model count = %d, want 1", custom)
	}
}

func TestSeedUpstreamProviderModels_NotFound(t *testing.T) {
	h := newSeedModelsHandler(t, newFakeUpstreamProviderStore())
	rec := postProviderAction(t, h, 999, "seed-models", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}
