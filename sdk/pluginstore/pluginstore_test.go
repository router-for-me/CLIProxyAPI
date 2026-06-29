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

func TestManifestFromPluginBuildsDirectManifest(t *testing.T) {
	manifest, errManifest := ManifestFromPlugin(
		DefaultSource(),
		Plugin{
			ID:          "sample-provider",
			Name:        "Sample Provider",
			Description: "Adds sample provider support.",
			Author:      "author-name",
			Version:     "0.4.0",
			Install: InstallPlan{
				Type: InstallTypeDirect,
				Artifacts: []Artifact{{
					GOOS:   "linux",
					GOARCH: "amd64",
					URL:    "https://downloads.example/sample-provider.zip",
					SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
				}},
			},
		},
	)
	if errManifest != nil {
		t.Fatalf("ManifestFromPlugin() error = %v", errManifest)
	}
	if errValidate := manifest.Validate(); errValidate != nil {
		t.Fatalf("Validate() error = %v", errValidate)
	}
	if manifest.SchemaVersion != SchemaVersionV2 || manifest.InstallType() != InstallTypeDirect || manifest.ReleaseTag != "" {
		t.Fatalf("manifest = %#v, want v2 direct without release tag", manifest)
	}
	if manifest.SourceURL != DefaultRegistryURL || len(manifest.Install.Artifacts) != 0 {
		t.Fatalf("manifest source/artifacts = %q/%d, want source URL without artifacts", manifest.SourceURL, len(manifest.Install.Artifacts))
	}
}

func TestPluginArtifactsIncludesVersionArtifacts(t *testing.T) {
	plugin := Plugin{
		ID:          "sample-provider",
		Name:        "Sample Provider",
		Description: "Adds sample provider support.",
		Author:      "author-name",
		Version:     "0.4.0",
		Install: InstallPlan{
			Type: InstallTypeDirect,
			Artifacts: []Artifact{{
				GOOS:   "windows",
				GOARCH: "x64",
				URL:    "https://downloads.example/sample-provider.zip",
				SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			}},
		},
		Versions: []Version{{
			Version: "0.3.0",
			Install: InstallPlan{
				Type: InstallTypeDirect,
				Artifacts: []Artifact{{
					GOOS:   "linux",
					GOARCH: "aarch64",
					URL:    "https://downloads.example/sample-provider-0.3.0.zip",
					SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
				}},
			},
		}},
	}

	artifacts := PluginArtifacts(plugin)
	if len(artifacts) != 2 ||
		artifacts[0].GOARCH != "amd64" ||
		artifacts[1].GOARCH != "arm64" {
		t.Fatalf("PluginArtifacts() = %#v, want normalized top-level and version artifacts", artifacts)
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
