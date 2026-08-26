package util

import "testing"

func TestResolveGitHubToken(t *testing.T) {
	tests := []struct {
		name          string
		githubToken   string
		lowerToken    string
		gitStoreToken string
		gitStoreURL   string
		want          string
	}{
		{
			name:          "GITHUB_TOKEN has highest priority",
			githubToken:   " primary-token ",
			lowerToken:    "lower-token",
			gitStoreToken: "gitstore-token",
			gitStoreURL:   "https://github.com/example/repo.git",
			want:          "primary-token",
		},
		{
			name:          "lowercase token is second priority",
			githubToken:   " ",
			lowerToken:    " lower-token ",
			gitStoreToken: "gitstore-token",
			gitStoreURL:   "https://github.com/example/repo.git",
			want:          "lower-token",
		},
		{
			name:          "Git store token is used for GitHub",
			gitStoreToken: " gitstore-token ",
			gitStoreURL:   "HTTPS://GITHUB.COM/example/repo.git",
			want:          "gitstore-token",
		},
		{
			name:          "Git store token is ignored for other hosts",
			gitStoreToken: "gitstore-token",
			gitStoreURL:   "https://gitlab.com/example/repo.git",
		},
		{
			name:          "Git store token is ignored without URL",
			gitStoreToken: "gitstore-token",
		},
		{
			name: "no token configured",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("GITHUB_TOKEN", tt.githubToken)
			t.Setenv("github_token", tt.lowerToken)
			t.Setenv("GITSTORE_GIT_TOKEN", tt.gitStoreToken)
			t.Setenv("GITSTORE_GIT_URL", tt.gitStoreURL)

			if got := ResolveGitHubToken(); got != tt.want {
				t.Fatalf("ResolveGitHubToken() = %q, want %q", got, tt.want)
			}
		})
	}
}
