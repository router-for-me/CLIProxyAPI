package pluginstore

import "testing"

func TestUpdateAvailable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		installed string
		latest    string
		want      bool
	}{
		{name: "unknown installed", installed: "", latest: "0.2.0", want: false},
		{name: "same version", installed: "0.1.0", latest: "0.1.0", want: false},
		{name: "same version with v prefix", installed: "v0.1.0", latest: "0.1.0", want: false},
		{name: "newer registry version", installed: "0.1.0", latest: "0.2.0", want: true},
		{name: "newer registry version with v prefix", installed: "v0.1.0", latest: "0.2.0", want: true},
		{name: "numeric not lexicographic", installed: "0.1.9", latest: "0.1.10", want: true},
		{name: "installed newer than registry", installed: "0.2.0", latest: "0.1.0", want: false},
		{name: "missing segments treated as zero", installed: "0.1", latest: "0.1.0", want: false},
		{name: "prerelease falls back to inequality", installed: "0.1.0-rc1", latest: "0.1.0", want: true},
		{name: "non numeric falls back to inequality", installed: "dev", latest: "0.1.0", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := UpdateAvailable(tt.installed, tt.latest); got != tt.want {
				t.Fatalf("UpdateAvailable(%q, %q) = %v, want %v", tt.installed, tt.latest, got, tt.want)
			}
		})
	}
}
