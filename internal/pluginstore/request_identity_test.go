package pluginstore

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestLatestReleaseCacheKeyNormalizesRepository(t *testing.T) {
	t.Parallel()
	client := Client{}
	key, errKey := client.LatestReleaseCacheKey(Plugin{Repository: "https://github.com/Owner/Repo/"})
	if errKey != nil {
		t.Fatal(errKey)
	}
	other, errOther := client.LatestReleaseCacheKey(Plugin{Repository: "https://github.com/owner/repo"})
	if errOther != nil || key != other {
		t.Fatalf("equivalent repository keys differ: %q / %q (%v)", key, other, errOther)
	}
}

func TestLatestReleaseCacheKeyTracksEnvironmentCredentials(t *testing.T) {
	plugin := Plugin{Repository: "https://github.com/owner/repo"}
	client := Client{Auth: []AuthConfig{{Match: "https://api.github.com/", Type: AuthTypeGitHubToken, TokenEnv: "PLUGIN_STORE_CACHE_TEST_TOKEN"}}}
	t.Setenv("PLUGIN_STORE_CACHE_TEST_TOKEN", "secret-token-one")
	first, errFirst := client.LatestReleaseCacheKey(plugin)
	if errFirst != nil {
		t.Fatal(errFirst)
	}
	t.Setenv("PLUGIN_STORE_CACHE_TEST_TOKEN", "secret-token-two")
	second, errSecond := client.LatestReleaseCacheKey(plugin)
	if errSecond != nil || first == second {
		t.Fatalf("credential rotation did not change key: %v", errSecond)
	}
	if strings.Contains(first+second, "secret-token") {
		t.Fatal("cache keys retain raw credentials")
	}
}

type pluginIdentityDoerFunc func(*http.Request) (*http.Response, error)

func (f pluginIdentityDoerFunc) Do(req *http.Request) (*http.Response, error) { return f(req) }

func TestPrepareLatestReleaseSnapshotsEnvironmentCredentials(t *testing.T) {
	plugin := Plugin{Repository: "https://github.com/owner/repo"}
	client := Client{
		Auth: []AuthConfig{{Match: "https://api.github.com/", Type: AuthTypeGitHubToken, TokenEnv: "PLUGIN_STORE_SNAPSHOT_TEST_TOKEN"}},
		HTTPClient: pluginIdentityDoerFunc(func(req *http.Request) (*http.Response, error) {
			if got := req.Header.Get("Authorization"); got != "Bearer original-token" {
				t.Errorf("request did not use the credential snapshot")
			}
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"tag_name":"v1.0.0"}`))}, nil
		}),
	}
	t.Setenv("PLUGIN_STORE_SNAPSHOT_TEST_TOKEN", "original-token")
	prepared, originalKey, errPrepare := client.PrepareLatestRelease(plugin)
	if errPrepare != nil {
		t.Fatal(errPrepare)
	}
	t.Setenv("PLUGIN_STORE_SNAPSHOT_TEST_TOKEN", "rotated-token")
	rotatedKey, errKey := client.LatestReleaseCacheKey(plugin)
	if errKey != nil || originalKey == rotatedKey {
		t.Fatalf("rotation did not change the unprepared identity: %v", errKey)
	}
	if _, errRelease := prepared.FetchLatestRelease(context.Background(), plugin); errRelease != nil {
		t.Fatal(errRelease)
	}
}

func TestLatestReleaseCacheKeyRejectsExpiredCredentials(t *testing.T) {
	t.Parallel()
	client := Client{
		ResolvedAuth:          []ResolvedAuthConfig{{Match: "https://api.github.com/", Type: AuthTypeGitHubToken, Token: Secret("secret")}},
		ResolvedAuthExpiresAt: time.Unix(1, 0),
	}
	if _, errKey := client.LatestReleaseCacheKey(Plugin{Repository: "https://github.com/owner/repo"}); errKey == nil {
		t.Fatal("expired credentials must not access a cached private release")
	}
}
