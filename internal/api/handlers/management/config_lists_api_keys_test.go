package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func newAPIKeysHandler(t *testing.T, entries []config.APIKeyEntry) *Handler {
	t.Helper()
	return &Handler{
		cfg:            &config.Config{SDKConfig: config.SDKConfig{APIKeys: entries}},
		configFilePath: writeTestConfigFile(t),
	}
}

func TestPutAPIKeysAcceptsPlainAndMixedPayloads(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []config.APIKeyEntry
	}{
		{
			name: "plain string array",
			body: `["k1","k2"]`,
			want: []config.APIKeyEntry{{Key: "k1"}, {Key: "k2"}},
		},
		{
			name: "mixed array",
			body: `["k1",{"key":"k2","name":"alice"}]`,
			want: []config.APIKeyEntry{{Key: "k1"}, {Key: "k2", Name: "alice"}},
		},
		{
			name: "items wrapper",
			body: `{"items":["k1",{"key":"k2","name":"bob"}]}`,
			want: []config.APIKeyEntry{{Key: "k1"}, {Key: "k2", Name: "bob"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newAPIKeysHandler(t, nil)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPut, "/v0/management/api-keys", strings.NewReader(tt.body))

			h.PutAPIKeys(c)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
			}
			if len(h.cfg.APIKeys) != len(tt.want) {
				t.Fatalf("api keys = %#v, want %#v", h.cfg.APIKeys, tt.want)
			}
			for i := range tt.want {
				if h.cfg.APIKeys[i] != tt.want[i] {
					t.Fatalf("entry[%d] = %#v, want %#v", i, h.cfg.APIKeys[i], tt.want[i])
				}
			}
		})
	}
}

func TestGetAPIKeysRendersUnnamedEntriesAsStrings(t *testing.T) {
	h := newAPIKeysHandler(t, []config.APIKeyEntry{{Key: "k1"}, {Key: "k2", Name: "alice"}})
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/api-keys", nil)

	h.GetAPIKeys(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var payload struct {
		APIKeys []json.RawMessage `json:"api-keys"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.APIKeys) != 2 {
		t.Fatalf("api keys = %s", rec.Body.String())
	}
	if string(payload.APIKeys[0]) != `"k1"` {
		t.Fatalf("unnamed entry = %s, want plain string", payload.APIKeys[0])
	}
	if !strings.Contains(string(payload.APIKeys[1]), `"name":"alice"`) {
		t.Fatalf("named entry = %s, want object with name", payload.APIKeys[1])
	}
}

func TestPatchAPIKeysStringValuePreservesName(t *testing.T) {
	h := newAPIKeysHandler(t, []config.APIKeyEntry{{Key: "k1", Name: "alice"}})
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/api-keys", strings.NewReader(`{"index":0,"value":"k1-new"}`))

	h.PatchAPIKeys(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if h.cfg.APIKeys[0] != (config.APIKeyEntry{Key: "k1-new", Name: "alice"}) {
		t.Fatalf("entry = %#v, want k1-new/alice", h.cfg.APIKeys[0])
	}
}

func TestPatchAPIKeysOldNewPreservesNameAndAppends(t *testing.T) {
	h := newAPIKeysHandler(t, []config.APIKeyEntry{{Key: "k1", Name: "alice"}})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/api-keys", strings.NewReader(`{"old":"k1","new":"k1-new"}`))
	h.PatchAPIKeys(c)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if h.cfg.APIKeys[0] != (config.APIKeyEntry{Key: "k1-new", Name: "alice"}) {
		t.Fatalf("entry = %#v, want k1-new/alice", h.cfg.APIKeys[0])
	}

	rec = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/api-keys", strings.NewReader(`{"old":"missing","new":{"key":"k2","name":"bob"}}`))
	h.PatchAPIKeys(c)
	if rec.Code != http.StatusOK {
		t.Fatalf("append status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if len(h.cfg.APIKeys) != 2 || h.cfg.APIKeys[1] != (config.APIKeyEntry{Key: "k2", Name: "bob"}) {
		t.Fatalf("entries = %#v, want appended k2/bob", h.cfg.APIKeys)
	}
}

func TestDeleteAPIKeysByIndexAndValue(t *testing.T) {
	h := newAPIKeysHandler(t, []config.APIKeyEntry{{Key: "k1"}, {Key: "k2", Name: "alice"}})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/api-keys?index=0", nil)
	h.DeleteAPIKeys(c)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if len(h.cfg.APIKeys) != 1 || h.cfg.APIKeys[0].Key != "k2" {
		t.Fatalf("entries = %#v, want only k2", h.cfg.APIKeys)
	}

	rec = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/api-keys?value=k2", nil)
	h.DeleteAPIKeys(c)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if len(h.cfg.APIKeys) != 0 {
		t.Fatalf("entries = %#v, want empty", h.cfg.APIKeys)
	}
}
