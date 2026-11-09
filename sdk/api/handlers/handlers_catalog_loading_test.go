package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
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

func TestCatalogLoadingPublicationBetweenLookupAndRejection(t *testing.T) {
	for _, suffix := range []string{"", "(8192)"} {
		t.Run(suffix, func(t *testing.T) {
			registry.SetCatalogLoading(true)
			defer registry.SetCatalogLoading(false)
			reg := registry.GetGlobalRegistry()
			clientID := "catalog-publication-boundary" + suffix
			defer reg.UnregisterClient(clientID)
			h := &BaseAPIHandler{}
			_, _, errMsg := h.getRequestDetailsWithProviderLookup(catalogLoadingModel+suffix, false, false, func(string) []string {
				// The lookup saw no model, then the last startup publication
				// completed before this request chose its rejection status.
				reg.RegisterClient(clientID, "gemini", []*registry.ModelInfo{{ID: catalogLoadingModel}})
				registry.SetCatalogLoading(false)
				return nil
			})
			if errMsg == nil || errMsg.StatusCode != http.StatusServiceUnavailable {
				t.Fatalf("startup lookup must remain retryable, got %v", errMsg)
			}
			providers, _, retryErr := h.getRequestDetailsWithProviderLookup(catalogLoadingModel+suffix, false, false, util.GetProviderName)
			if retryErr != nil || len(providers) != 1 || providers[0] != "gemini" {
				t.Fatalf("published model not routable on retry: providers=%v error=%v", providers, retryErr)
			}
		})
	}
}
