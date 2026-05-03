package managementasset

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
)

func githubHostRewritingClient(serverURL string) (*http.Client, error) {
	target, err := url.Parse(serverURL)
	if err != nil {
		return nil, err
	}
	base := http.DefaultTransport
	return &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		clone := req.Clone(req.Context())
		clone.URL.Scheme = target.Scheme
		clone.URL.Host = target.Host
		return base.RoundTrip(clone)
	})}, nil
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestFetchLatestAssetUsesPanelGitHubTokenWithoutGitStoreMode(t *testing.T) {
	oldPanelToken, hadPanelToken := os.LookupEnv("CLIPROXYAPI_PANEL_GITHUB_TOKEN")
	oldGitURL, hadGitURL := os.LookupEnv("GITSTORE_GIT_URL")
	oldGitToken, hadGitToken := os.LookupEnv("GITSTORE_GIT_TOKEN")
	defer func() {
		if hadPanelToken {
			_ = os.Setenv("CLIPROXYAPI_PANEL_GITHUB_TOKEN", oldPanelToken)
		} else {
			_ = os.Unsetenv("CLIPROXYAPI_PANEL_GITHUB_TOKEN")
		}
		if hadGitURL {
			_ = os.Setenv("GITSTORE_GIT_URL", oldGitURL)
		} else {
			_ = os.Unsetenv("GITSTORE_GIT_URL")
		}
		if hadGitToken {
			_ = os.Setenv("GITSTORE_GIT_TOKEN", oldGitToken)
		} else {
			_ = os.Unsetenv("GITSTORE_GIT_TOKEN")
		}
	}()

	_ = os.Setenv("CLIPROXYAPI_PANEL_GITHUB_TOKEN", "panel-token")
	_ = os.Unsetenv("GITSTORE_GIT_URL")
	_ = os.Unsetenv("GITSTORE_GIT_TOKEN")

	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(releaseResponse{
			Assets: []releaseAsset{{
				Name:               managementAssetName,
				BrowserDownloadURL: "https://example.test/management.html",
				Digest:             "sha256:abcd",
			}},
		})
	}))
	defer server.Close()

	releaseURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse server url: %v", err)
	}
	releaseURL.Host = "api.github.com"

	client, err := githubHostRewritingClient(server.URL)
	if err != nil {
		t.Fatalf("build github host rewriting client: %v", err)
	}

	asset, hash, err := fetchLatestAsset(context.Background(), client, releaseURL.String())
	if err != nil {
		t.Fatalf("fetchLatestAsset returned error: %v", err)
	}
	if gotAuth != "Bearer panel-token" {
		t.Fatalf("expected Authorization header from CLIPROXYAPI_PANEL_GITHUB_TOKEN, got %q", gotAuth)
	}
	if asset == nil || asset.Name != managementAssetName {
		t.Fatalf("unexpected asset: %#v", asset)
	}
	if hash != "abcd" {
		t.Fatalf("unexpected hash: %q", hash)
	}
}

func TestFetchLatestAssetPrefersPanelTokenOverGitStoreToken(t *testing.T) {
	oldPanelToken, hadPanelToken := os.LookupEnv("CLIPROXYAPI_PANEL_GITHUB_TOKEN")
	oldGitURL, hadGitURL := os.LookupEnv("GITSTORE_GIT_URL")
	oldGitToken, hadGitToken := os.LookupEnv("GITSTORE_GIT_TOKEN")
	defer func() {
		if hadPanelToken {
			_ = os.Setenv("CLIPROXYAPI_PANEL_GITHUB_TOKEN", oldPanelToken)
		} else {
			_ = os.Unsetenv("CLIPROXYAPI_PANEL_GITHUB_TOKEN")
		}
		if hadGitURL {
			_ = os.Setenv("GITSTORE_GIT_URL", oldGitURL)
		} else {
			_ = os.Unsetenv("GITSTORE_GIT_URL")
		}
		if hadGitToken {
			_ = os.Setenv("GITSTORE_GIT_TOKEN", oldGitToken)
		} else {
			_ = os.Unsetenv("GITSTORE_GIT_TOKEN")
		}
	}()

	_ = os.Setenv("CLIPROXYAPI_PANEL_GITHUB_TOKEN", "panel-token")
	_ = os.Setenv("GITSTORE_GIT_URL", "https://github.com/router-for-me/Cli-Proxy-API-Management-Center")
	_ = os.Setenv("GITSTORE_GIT_TOKEN", "gitstore-token")

	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(releaseResponse{
			Assets: []releaseAsset{{
				Name:               managementAssetName,
				BrowserDownloadURL: "https://example.test/management.html",
				Digest:             "sha256:abcd",
			}},
		})
	}))
	defer server.Close()

	releaseURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse server url: %v", err)
	}
	releaseURL.Host = "api.github.com"

	client, err := githubHostRewritingClient(server.URL)
	if err != nil {
		t.Fatalf("build github host rewriting client: %v", err)
	}

	_, _, err = fetchLatestAsset(context.Background(), client, releaseURL.String())
	if err != nil {
		t.Fatalf("fetchLatestAsset returned error: %v", err)
	}
	if gotAuth != "Bearer panel-token" {
		t.Fatalf("expected panel token to win over gitstore token, got %q", gotAuth)
	}
}

func TestFetchLatestAssetDoesNotLeakPanelTokenToNonGitHubHosts(t *testing.T) {
	oldPanelToken, hadPanelToken := os.LookupEnv("CLIPROXYAPI_PANEL_GITHUB_TOKEN")
	defer func() {
		if hadPanelToken {
			_ = os.Setenv("CLIPROXYAPI_PANEL_GITHUB_TOKEN", oldPanelToken)
		} else {
			_ = os.Unsetenv("CLIPROXYAPI_PANEL_GITHUB_TOKEN")
		}
	}()

	_ = os.Setenv("CLIPROXYAPI_PANEL_GITHUB_TOKEN", "panel-token")

	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(releaseResponse{
			Assets: []releaseAsset{{
				Name:               managementAssetName,
				BrowserDownloadURL: "https://example.test/management.html",
				Digest:             "sha256:abcd",
			}},
		})
	}))
	defer server.Close()

	_, _, err := fetchLatestAsset(context.Background(), server.Client(), server.URL)
	if err != nil {
		t.Fatalf("fetchLatestAsset returned error: %v", err)
	}
	if gotAuth != "" {
		t.Fatalf("expected no Authorization header for non-GitHub host, got %q", gotAuth)
	}
}

func TestFetchLatestAssetDoesNotLeakGitStoreTokenToNonGitHubHosts(t *testing.T) {
	oldGitURL, hadGitURL := os.LookupEnv("GITSTORE_GIT_URL")
	oldGitToken, hadGitToken := os.LookupEnv("GITSTORE_GIT_TOKEN")
	defer func() {
		if hadGitURL {
			_ = os.Setenv("GITSTORE_GIT_URL", oldGitURL)
		} else {
			_ = os.Unsetenv("GITSTORE_GIT_URL")
		}
		if hadGitToken {
			_ = os.Setenv("GITSTORE_GIT_TOKEN", oldGitToken)
		} else {
			_ = os.Unsetenv("GITSTORE_GIT_TOKEN")
		}
	}()

	_ = os.Setenv("GITSTORE_GIT_URL", "https://github.com/router-for-me/Cli-Proxy-API-Management-Center")
	_ = os.Setenv("GITSTORE_GIT_TOKEN", "gitstore-token")

	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(releaseResponse{
			Assets: []releaseAsset{{
				Name:               managementAssetName,
				BrowserDownloadURL: "https://example.test/management.html",
				Digest:             "sha256:abcd",
			}},
		})
	}))
	defer server.Close()

	_, _, err := fetchLatestAsset(context.Background(), server.Client(), server.URL)
	if err != nil {
		t.Fatalf("fetchLatestAsset returned error: %v", err)
	}
	if gotAuth != "" {
		t.Fatalf("expected no Authorization header for non-GitHub host from gitstore token path, got %q", gotAuth)
	}
}
