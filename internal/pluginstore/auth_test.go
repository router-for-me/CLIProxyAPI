package pluginstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPluginStoreAuthMatchesURLHostAndPathBoundaries(t *testing.T) {
	t.Setenv("PLUGIN_STORE_TOKEN", "secret-token")
	auth := []AuthConfig{{
		Match:    "https://downloads.example/private",
		ApplyTo:  []string{RequestKindArtifact},
		Type:     AuthTypeBearer,
		TokenEnv: "PLUGIN_STORE_TOKEN",
	}}

	tests := []struct {
		name     string
		url      string
		wantAuth bool
	}{
		{name: "exact path", url: "https://downloads.example/private", wantAuth: true},
		{name: "child path", url: "https://downloads.example/private/plugin.zip", wantAuth: true},
		{name: "sibling prefix", url: "https://downloads.example/private2/plugin.zip", wantAuth: false},
		{name: "similar host", url: "https://downloads.example.evil/private/plugin.zip", wantAuth: false},
		{name: "different scheme", url: "http://downloads.example/private/plugin.zip", wantAuth: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headers := http.Header{}
			if errAuth := applyPluginStoreAuth(headers, auth, tt.url, RequestKindArtifact); errAuth != nil {
				t.Fatalf("applyPluginStoreAuth() error = %v", errAuth)
			}
			gotAuth := headers.Get("Authorization") != ""
			if gotAuth != tt.wantAuth {
				t.Fatalf("Authorization set = %v, want %v", gotAuth, tt.wantAuth)
			}
		})
	}
}

func TestPluginStoreAuthHeaderIsReevaluatedAcrossRedirect(t *testing.T) {
	t.Setenv("PLUGIN_STORE_HEADER", "secret-token")

	var initialHeader string
	var redirectedHeader string
	artifactData := []byte("artifact-data")
	sum := sha256.Sum256(artifactData)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectedHeader = r.Header.Get("X-Plugin-Token")
		_, _ = w.Write(artifactData)
	}))
	t.Cleanup(target.Close)
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		initialHeader = r.Header.Get("X-Plugin-Token")
		http.Redirect(w, r, target.URL+"/artifact.zip", http.StatusFound)
	}))
	t.Cleanup(source.Close)

	client := Client{
		HTTPClient: source.Client(),
		Auth: []AuthConfig{
			{
				Match:          source.URL + "/private/",
				ApplyTo:        []string{RequestKindArtifact},
				Type:           AuthTypeHeader,
				HeaderName:     "X-Plugin-Token",
				HeaderValueEnv: "PLUGIN_STORE_HEADER",
				AllowInsecure:  true,
			},
			{
				Match:         target.URL + "/",
				ApplyTo:       []string{RequestKindArtifact},
				Type:          AuthTypeNone,
				AllowInsecure: true,
			},
		},
	}
	data, errDownload := client.DownloadArtifact(context.Background(), Artifact{
		GOOS:   "linux",
		GOARCH: "amd64",
		URL:    source.URL + "/private/artifact.zip",
		SHA256: hex.EncodeToString(sum[:]),
	})
	if errDownload != nil {
		t.Fatalf("DownloadArtifact() error = %v", errDownload)
	}
	if string(data) != string(artifactData) {
		t.Fatalf("DownloadArtifact() = %q, want %q", data, artifactData)
	}
	if initialHeader != "secret-token" {
		t.Fatalf("initial auth header = %q, want secret-token", initialHeader)
	}
	if redirectedHeader != "" {
		t.Fatalf("redirected auth header = %q, want empty", redirectedHeader)
	}
}

func TestPluginStoreAuthHeaderIsAppliedToMatchingRedirect(t *testing.T) {
	t.Setenv("PLUGIN_STORE_HEADER", "secret-token")

	var redirectedHeader string
	artifactData := []byte("artifact-data")
	sum := sha256.Sum256(artifactData)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/private/start.zip" {
			http.Redirect(w, r, "/private/artifact.zip", http.StatusFound)
			return
		}
		redirectedHeader = r.Header.Get("X-Plugin-Token")
		_, _ = io.WriteString(w, string(artifactData))
	}))
	t.Cleanup(server.Close)

	client := Client{
		HTTPClient: server.Client(),
		Auth: []AuthConfig{{
			Match:          server.URL + "/private/",
			ApplyTo:        []string{RequestKindArtifact},
			Type:           AuthTypeHeader,
			HeaderName:     "X-Plugin-Token",
			HeaderValueEnv: "PLUGIN_STORE_HEADER",
			AllowInsecure:  true,
		}},
	}
	if _, errDownload := client.DownloadArtifact(context.Background(), Artifact{
		GOOS:   "linux",
		GOARCH: "amd64",
		URL:    server.URL + "/private/start.zip",
		SHA256: hex.EncodeToString(sum[:]),
	}); errDownload != nil {
		t.Fatalf("DownloadArtifact() error = %v", errDownload)
	}
	if redirectedHeader != "secret-token" {
		t.Fatalf("redirected auth header = %q, want secret-token", redirectedHeader)
	}
}
