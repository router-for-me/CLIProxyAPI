package main

import (
	"strings"
	"testing"
)

func TestParseVersionTag_ValidPatterns(t *testing.T) {
	t.Parallel()

	cases := []struct {
		raw      string
		major    int
		minor    int
		patch    int
		batch    int
		hasBatch bool
	}{
		{
			raw:      "v6.8.24",
			major:    6,
			minor:    8,
			patch:    24,
			batch:    -1,
			hasBatch: false,
		},
		{
			raw:      "v6.8.24-3",
			major:    6,
			minor:    8,
			patch:    24,
			batch:    3,
			hasBatch: true,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.raw, func(t *testing.T) {
			t.Parallel()

			got, ok := parseVersionTag(tc.raw)
			if !ok {
				t.Fatalf("parseVersionTag(%q) = false, want true", tc.raw)
			}
			if got.Raw != tc.raw {
				t.Fatalf("parseVersionTag(%q).Raw = %q, want %q", tc.raw, got.Raw, tc.raw)
			}
			if got.Major != tc.major {
				t.Fatalf("Major = %d, want %d", got.Major, tc.major)
			}
			if got.Minor != tc.minor {
				t.Fatalf("Minor = %d, want %d", got.Minor, tc.minor)
			}
			if got.Patch != tc.patch {
				t.Fatalf("Patch = %d, want %d", got.Patch, tc.patch)
			}
			if got.Batch != tc.batch {
				t.Fatalf("Batch = %d, want %d", got.Batch, tc.batch)
			}
			if got.HasBatch != tc.hasBatch {
				t.Fatalf("HasBatch = %v, want %v", got.HasBatch, tc.hasBatch)
			}
		})
	}
}

func TestParseVersionTag_InvalidPatterns(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		"",
		"6.8.24",
		"v6.8",
		"v6.8.24-beta",
		"release-v6.8.24-1",
		"v6.8.24-",
	} {
		raw := raw
		t.Run(raw, func(t *testing.T) {
			t.Parallel()

			if _, ok := parseVersionTag(raw); ok {
				t.Fatalf("parseVersionTag(%q) = true, want false", raw)
			}
		})
	}
}

func TestVersionTagLess(t *testing.T) {
	t.Parallel()

	a, ok := parseVersionTag("v6.8.24")
	if !ok {
		t.Fatal("parseVersionTag(v6.8.24) failed")
	}
	b, ok := parseVersionTag("v6.8.24-1")
	if !ok {
		t.Fatal("parseVersionTag(v6.8.24-1) failed")
	}
	c, ok := parseVersionTag("v6.8.25")
	if !ok {
		t.Fatal("parseVersionTag(v6.8.25) failed")
	}

	if !a.less(b) {
		t.Fatalf("expected v6.8.24 < v6.8.24-1")
	}
	if !a.less(c) {
		t.Fatalf("expected v6.8.24 < v6.8.25")
	}
	if !b.less(c) {
		// Batch-suffixed tags are still ordered inside the same patch line; patch increment still wins.
		t.Fatalf("expected v6.8.24-1 < v6.8.25")
	}
	if a.less(a) {
		t.Fatalf("expected version to not be less than itself")
	}
}

func TestBuildNotes(t *testing.T) {
	t.Parallel()

	got := buildNotes([]string{"abc123 fix bug", "def456 add docs"})
	lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("unexpected changelog lines count: %d", len(lines))
	}
	if lines[0] != "## Changelog" {
		t.Fatalf("header = %q, want %q", lines[0], "## Changelog")
	}
	if lines[1] != "* abc123 fix bug" || lines[2] != "* def456 add docs" {
		t.Fatalf("unexpected changelog bullets: %v", lines[1:3])
	}
}
