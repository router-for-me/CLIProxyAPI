package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

const catalogLoadingModel = "catalog-loading-test-model-not-registered"

func TestCatalogLoadingUnknownModelRetriesWith503(t *testing.T) {
	registry.SetCatalogLoading(true)
	t.Cleanup(func() { registry.SetCatalogLoading(false) })

	h := &BaseAPIHandler{}
	_, _, errMsg := h.getRequestDetails(catalogLoadingModel)
	if errMsg == nil {
		t.Fatal("expected a rejection while the catalog is loading")
	}
	if errMsg.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", errMsg.StatusCode)
	}
	if !strings.Contains(errMsg.Error.Error(), "provider catalog still loading, retry") {
		t.Fatalf("message = %q, want the catalog-loading text", errMsg.Error.Error())
	}

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	h.WriteErrorResponse(c, errMsg)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("HTTP status = %d, want 503", recorder.Code)
	}
	if got := recorder.Header().Get("Retry-After"); got != "5" {
		t.Fatalf("Retry-After = %q, want \"5\"", got)
	}
	if !strings.Contains(recorder.Body.String(), "catalog_loading") {
		t.Fatalf("body = %q, want the catalog_loading code", recorder.Body.String())
	}
}

func TestCatalogLoadingFlagClearedKeepsUnknownModelAs400(t *testing.T) {
	registry.SetCatalogLoading(false)

	h := &BaseAPIHandler{}
	_, _, errMsg := h.getRequestDetails(catalogLoadingModel)
	if errMsg == nil {
		t.Fatal("expected a rejection for an unknown model")
	}
	if errMsg.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 once the catalog has loaded", errMsg.StatusCode)
	}
	if !strings.Contains(errMsg.Error.Error(), "unknown provider for model") {
		t.Fatalf("message = %q, want the unknown-provider text", errMsg.Error.Error())
	}
}
