package pluginstore

import (
	"strings"
	"testing"
)

func TestManifestValidateRequiresPinnedReleaseTag(t *testing.T) {
	manifest := validTestManifest()
	manifest.ReleaseTag = ""

	errValidate := manifest.Validate()
	if errValidate == nil {
		t.Fatal("Validate() error = nil, want release-tag error")
	}
	if !strings.Contains(errValidate.Error(), "release-tag") {
		t.Fatalf("Validate() error = %v, want release-tag", errValidate)
	}
}

func TestManifestValidateRejectsReleaseTagVersionMismatch(t *testing.T) {
	manifest := validTestManifest()
	manifest.ReleaseTag = "v0.3.0"

	errValidate := manifest.Validate()
	if errValidate == nil {
		t.Fatal("Validate() error = nil, want version mismatch")
	}
	if !strings.Contains(errValidate.Error(), "resolves version") {
		t.Fatalf("Validate() error = %v, want version mismatch", errValidate)
	}
}

func TestManifestFromReleaseBuildsPinnedManifest(t *testing.T) {
	manifest, errManifest := ManifestFromRelease(
		DefaultSource(),
		Plugin{
			ID:          "sample-provider",
			Name:        "Sample Provider",
			Description: "Adds sample provider support.",
			Author:      "author-name",
			Repository:  "https://github.com/author-name/sample-provider",
		},
		Release{TagName: "v0.2.0"},
	)
	if errManifest != nil {
		t.Fatalf("ManifestFromRelease() error = %v", errManifest)
	}
	if errValidate := manifest.Validate(); errValidate != nil {
		t.Fatalf("Validate() error = %v", errValidate)
	}
	if manifest.Version != "0.2.0" || manifest.ReleaseTag != "v0.2.0" {
		t.Fatalf("manifest version fields = %q/%q, want 0.2.0/v0.2.0", manifest.Version, manifest.ReleaseTag)
	}
}

func validTestManifest() Manifest {
	return Manifest{
		ID:          "sample-provider",
		Name:        "Sample Provider",
		Description: "Adds sample provider support.",
		Author:      "author-name",
		Version:     "0.2.0",
		ReleaseTag:  "v0.2.0",
		Repository:  "https://github.com/author-name/sample-provider",
	}
}
