package managementasset

import "testing"

func TestResolveReleaseURL(t *testing.T) {
	tests := []struct {
		name string
		repo string
		want string
	}{
		{
			name: "empty uses default",
			repo: "",
			want: defaultManagementReleaseURL,
		},
		{
			name: "github repository URL resolves to latest release API",
			repo: "https://github.com/example/panel",
			want: "https://api.github.com/repos/example/panel/releases/latest",
		},
		{
			name: "github repository URL trims git suffix",
			repo: "https://github.com/example/panel.git",
			want: "https://api.github.com/repos/example/panel/releases/latest",
		},
		{
			name: "api repository URL resolves to latest release API",
			repo: "https://api.github.com/repos/example/panel",
			want: "https://api.github.com/repos/example/panel/releases/latest",
		},
		{
			name: "api releases URL resolves to latest release API",
			repo: "https://api.github.com/repos/example/panel/releases",
			want: "https://api.github.com/repos/example/panel/releases/latest",
		},
		{
			name: "api latest release URL is preserved",
			repo: "https://api.github.com/repos/example/panel/releases/latest",
			want: "https://api.github.com/repos/example/panel/releases/latest",
		},
		{
			name: "api pinned release ID URL is preserved",
			repo: "https://api.github.com/repos/router-for-me/Cli-Proxy-API-Management-Center/releases/313351637",
			want: "https://api.github.com/repos/router-for-me/Cli-Proxy-API-Management-Center/releases/313351637",
		},
		{
			name: "api pinned release tag URL is preserved",
			repo: "https://api.github.com/repos/example/panel/releases/tags/v1.8.0",
			want: "https://api.github.com/repos/example/panel/releases/tags/v1.8.0",
		},
		{
			name: "invalid URL uses default",
			repo: "not a url",
			want: defaultManagementReleaseURL,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveReleaseURL(tt.repo); got != tt.want {
				t.Fatalf("resolveReleaseURL(%q) = %q, want %q", tt.repo, got, tt.want)
			}
		})
	}
}
