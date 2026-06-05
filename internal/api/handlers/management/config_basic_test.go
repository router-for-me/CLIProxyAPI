package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestGetLatestVersionFallsBackToReleasePageRedirect(t *testing.T) {
	gin.SetMode(gin.TestMode)

	originalAPIURL := latestReleaseAPIURL
	originalPageURL := latestReleasePageURL
	t.Cleanup(func() {
		latestReleaseAPIURL = originalAPIURL
		latestReleasePageURL = originalPageURL
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/releases/latest":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"API rate limit exceeded"}`))
		case "/releases/latest":
			http.Redirect(w, r, "/releases/tag/v9.9.9", http.StatusFound)
		case "/releases/tag/v9.9.9":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	latestReleaseAPIURL = server.URL + "/api/releases/latest"
	latestReleasePageURL = server.URL + "/releases/latest"

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/latest-version", nil)

	h := &Handler{}
	h.GetLatestVersion(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var payload struct {
		LatestVersion string `json:"latest-version"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if payload.LatestVersion != "v9.9.9" {
		t.Fatalf("latest-version = %q, want %q", payload.LatestVersion, "v9.9.9")
	}
}

func TestParseReleaseVersionFromURL(t *testing.T) {
	got := parseReleaseVersionFromURL("https://github.com/router-for-me/CLIProxyAPI/releases/tag/v7.1.45")
	if got != "v7.1.45" {
		t.Fatalf("version = %q, want %q", got, "v7.1.45")
	}
}
