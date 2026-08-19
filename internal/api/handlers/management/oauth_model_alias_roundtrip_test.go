package management

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestOAuthModelAlias_RoundtripPreservesOrderedEntries(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg:            &config.Config{},
		configFilePath: writeTestConfigFile(t),
	}

	putBody := []byte(`{
		"codex": [
			{"name": "gpt-5", "alias": "g5"},
			{"name": "gpt-5-mini", "alias": "g5"}
		]
	}`)
	putRec := httptest.NewRecorder()
	putCtx, _ := gin.CreateTestContext(putRec)
	putCtx.Request = httptest.NewRequest(http.MethodPut, "/v0/management/oauth-model-alias", bytes.NewReader(putBody))
	h.PutOAuthModelAlias(putCtx)
	if putRec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want %d; body=%s", putRec.Code, http.StatusOK, putRec.Body.String())
	}

	aliases := h.cfg.OAuthModelAlias["codex"]
	if len(aliases) != 2 {
		t.Fatalf("after PUT, expected 2 ordered aliases, got %d: %+v", len(aliases), aliases)
	}
	if aliases[0].Name != "gpt-5" || aliases[0].Alias != "g5" {
		t.Fatalf("after PUT, first entry = %+v, want name=gpt-5 alias=g5", aliases[0])
	}
	if aliases[1].Name != "gpt-5-mini" || aliases[1].Alias != "g5" {
		t.Fatalf("after PUT, second entry = %+v, want name=gpt-5-mini alias=g5", aliases[1])
	}

	getRec := httptest.NewRecorder()
	getCtx, _ := gin.CreateTestContext(getRec)
	getCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/oauth-model-alias", nil)
	h.GetOAuthModelAlias(getCtx)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want %d; body=%s", getRec.Code, http.StatusOK, getRec.Body.String())
	}
	var got struct {
		OAuthModelAlias map[string][]config.OAuthModelAlias `json:"oauth-model-alias"`
	}
	if errDecode := json.Unmarshal(getRec.Body.Bytes(), &got); errDecode != nil {
		t.Fatalf("decode GET body: %v", errDecode)
	}
	gotCodex := got.OAuthModelAlias["codex"]
	if len(gotCodex) != 2 {
		t.Fatalf("GET returned %d aliases, want 2: %+v", len(gotCodex), gotCodex)
	}
	if gotCodex[0].Name != "gpt-5" || gotCodex[1].Name != "gpt-5-mini" {
		t.Fatalf("GET order lost: %+v", gotCodex)
	}

	patchBody := []byte(`{
		"channel": "codex",
		"aliases": [
			{"name": "gpt-5", "alias": "g5"},
			{"name": "gpt-5-mini", "alias": "g5"},
			{"name": "gpt-5-nano", "alias": "g5"}
		]
	}`)
	patchRec := httptest.NewRecorder()
	patchCtx, _ := gin.CreateTestContext(patchRec)
	patchCtx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/oauth-model-alias", bytes.NewReader(patchBody))
	h.PatchOAuthModelAlias(patchCtx)
	if patchRec.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d, want %d; body=%s", patchRec.Code, http.StatusOK, patchRec.Body.String())
	}
	if got := len(h.cfg.OAuthModelAlias["codex"]); got != 3 {
		t.Fatalf("after PATCH, expected 3 ordered aliases, got %d: %+v", got, h.cfg.OAuthModelAlias["codex"])
	}
}
