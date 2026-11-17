package managementasset

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/proxx/selfupdate"
)

func TestPanelMinBackendAPI(t *testing.T) {
	cases := map[string]int{
		"":                                       0,
		"notes only":                             0,
		"proxx-min-backend-api: 2":               2,
		"## Notes\n\nproxx-min-backend-api: 7\n": 7,
		"<!-- proxx-min-backend-api: 3 -->":      3,
		"text proxx-min-backend-api: 9":          0, // must start the line
	}
	for body, want := range cases {
		if got := panelMinBackendAPI(body); got != want {
			t.Errorf("panelMinBackendAPI(%q) = %d, want %d", body, got, want)
		}
	}
}

func TestFetchLatestAssetSkipsPanelNeedingNewerBackend(t *testing.T) {
	body := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, `{"body":%q,"assets":[{"name":"management.html","browser_download_url":"http://x/m","digest":""}]}`, body)
	}))
	defer srv.Close()

	body = fmt.Sprintf("proxx-min-backend-api: %d", selfupdate.APILevel+1)
	if _, _, err := fetchLatestAsset(context.Background(), srv.Client(), srv.URL); err == nil {
		t.Fatal("panel needing a newer backend was not skipped")
	}
	body = fmt.Sprintf("proxx-min-backend-api: %d", selfupdate.APILevel)
	if asset, _, err := fetchLatestAsset(context.Background(), srv.Client(), srv.URL); err != nil || asset == nil {
		t.Fatalf("compatible panel skipped: %v", err)
	}
	body = "no declaration"
	if _, _, err := fetchLatestAsset(context.Background(), srv.Client(), srv.URL); err != nil {
		t.Fatalf("panel without declaration skipped: %v", err)
	}
}
