package pluginstore

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/githubauth"
)

func TestGlobalGitHubTokenAuth(t *testing.T) {
	githubauth.SetToken("global-token")
	t.Cleanup(func() { githubauth.SetToken("") })
	const apiURL = "https://api.github.com/repos/owner/plugin/releases/assets/1"
	for _, tc := range []struct {
		name   string
		client Client
		want   string
	}{
		{name: "global", want: "Bearer global-token"},
		{name: "explicit none", client: Client{Auth: []AuthConfig{{Match: "https://api.github.com/", Type: AuthTypeNone}}}},
		{name: "resolved", client: Client{ResolvedAuth: []ResolvedAuthConfig{{Match: "https://api.github.com/", Type: AuthTypeGitHubToken, Token: Secret("specific-token")}}}, want: "Bearer specific-token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			headers, authenticated, err := tc.client.authHeaders(apiURL, RequestKindArtifact)
			if err != nil {
				t.Fatal(err)
			}
			if headers.Get("Authorization") != tc.want || authenticated != (tc.want != "") {
				t.Fatal("incorrect credential selection")
			}
			if tc.client.releaseAssetAPIAuthenticated(apiURL) != (tc.want != "") {
				t.Fatal("asset API selection ignored credentials")
			}
		})
	}
	client := Client{}
	plugin := Plugin{Repository: "https://github.com/owner/plugin"}
	prepared, key, err := client.PrepareLatestRelease(plugin)
	if err != nil {
		t.Fatal(err)
	}
	githubauth.SetToken("rotated-token")
	nextKey, err := client.LatestReleaseCacheKey(plugin)
	if err != nil {
		t.Fatal(err)
	}
	if key == nextKey || strings.Contains(key, "global-token") {
		t.Fatal("cache identity must isolate credentials without exposing tokens")
	}
	headers, _, err := prepared.authHeaders("https://api.github.com/repos/owner/plugin/releases/latest", RequestKindMetadata)
	if err != nil || headers.Get("Authorization") != "Bearer global-token" {
		t.Fatal("prepared credentials were not snapshotted")
	}
}

func TestGlobalGitHubTokenDownloadRedirect(t *testing.T) {
	githubauth.SetToken("global-token")
	t.Cleanup(func() { githubauth.SetToken("") })
	calls := 0
	client := Client{HTTPClient: pluginIdentityDoerFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			if req.URL.Host != "api.github.com" || req.Header.Get("Authorization") != "Bearer global-token" {
				t.Fatal("asset API did not receive global credential")
			}
			return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": []string{"https://downloads.example/plugin.zip"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		if req.Header.Get("Authorization") != "" {
			t.Fatal("global credential leaked across redirect")
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("artifact"))}, nil
	})}
	data, err := client.DownloadAsset(context.Background(), ReleaseAsset{APIURL: "https://api.github.com/repos/owner/plugin/releases/assets/1", BrowserDownloadURL: "https://github.com/owner/plugin/releases/download/v1/plugin.zip"})
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "artifact" || calls != 2 {
		t.Fatal("unexpected artifact download")
	}
}
