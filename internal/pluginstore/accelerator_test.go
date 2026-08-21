package pluginstore

import (
	"strings"
	"testing"
)

func TestNormalizeAcceleratorBase(t *testing.T) {
	t.Parallel()

	base, err := NormalizeAcceleratorBase("https://gh-proxy.com")
	if err != nil {
		t.Fatalf("NormalizeAcceleratorBase() error = %v", err)
	}
	if base != "https://gh-proxy.com/" {
		t.Fatalf("NormalizeAcceleratorBase() = %q", base)
	}
}

func TestNormalizeAcceleratorBaseRejectsHTTP(t *testing.T) {
	t.Parallel()

	if _, err := NormalizeAcceleratorBase("http://gh-proxy.com"); err == nil {
		t.Fatal("NormalizeAcceleratorBase() error = nil, want rejection for http accelerator")
	}
}

func TestApplyAcceleratorBasePreservesTokenQuery(t *testing.T) {
	t.Parallel()

	rewritten, err := ApplyAcceleratorBase(
		"https://gh-proxy.com",
		"https://objects.githubusercontent.com/github-production-release-asset/file.zip?token=temp-github-token",
	)
	if err != nil {
		t.Fatalf("ApplyAcceleratorBase() error = %v", err)
	}
	want := "https://gh-proxy.com/https://objects.githubusercontent.com/github-production-release-asset/file.zip?token=temp-github-token"
	if rewritten != want {
		t.Fatalf("ApplyAcceleratorBase() = %q, want %q", rewritten, want)
	}
	if !strings.Contains(rewritten, "token=temp-github-token") {
		t.Fatalf("rewritten URL lost token query: %q", rewritten)
	}
}

func TestApplyAcceleratorBaseSkipsGitHubAPI(t *testing.T) {
	t.Parallel()

	apiURL := "https://api.github.com/repos/owner/repo/releases/latest"
	rewritten, err := ApplyAcceleratorBase("https://gh-proxy.com", apiURL)
	if err != nil {
		t.Fatalf("ApplyAcceleratorBase() error = %v", err)
	}
	if rewritten != apiURL {
		t.Fatalf("ApplyAcceleratorBase() = %q, want original API URL", rewritten)
	}
	if !IsGitHubAcceleratorURL("https://github.com/owner/repo/releases/download/v1/x.zip") {
		t.Fatal("expected github.com release URL to be accelerator-eligible")
	}
	if IsGitHubAcceleratorURL(apiURL) {
		t.Fatal("api.github.com must not be accelerator-eligible")
	}
	if IsGitHubAcceleratorURL("http://github.com/owner/repo/releases/download/v1/x.zip") {
		t.Fatal("http github.com URL must not be accelerator-eligible (would bypass allow-insecure)")
	}
}
