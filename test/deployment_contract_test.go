package test

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// These static contracts supplement, but do not replace, container smoke tests.
func TestReleaseWorkflowRejectsBranchDispatch(t *testing.T) {
	body, err := os.ReadFile("../.github/workflows/release.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			If    string `yaml:"if"`
			Needs any    `yaml:"needs"`
			Steps []struct {
				Run string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if errParse := yaml.Unmarshal(body, &workflow); errParse != nil {
		t.Fatal(errParse)
	}
	prepare, ok := workflow.Jobs["prepare-release"]
	if !ok || prepare.If != "github.ref_type == 'tag'" {
		t.Fatal("release preparation must reject branch refs before any side effects")
	}
	for id, job := range workflow.Jobs {
		for _, step := range job.Steps {
			if strings.Contains(step.Run, "gh release create") && !strings.Contains(step.Run, "--verify-tag") {
				t.Fatalf("%s can implicitly create a tag", id)
			}
			if strings.Contains(step.Run, "gh release upload") && job.Needs != "prepare-release" && job.If != "always() && github.ref_type == 'tag'" {
				t.Fatalf("%s release upload bypasses the tag guard", id)
			}
		}
	}
}

func TestDockerRuntimePreservesDeploymentCompatibility(t *testing.T) {
	body, err := os.ReadFile("../Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	dockerfile := string(body)
	for _, required := range []string{"CGO_ENABLED=1", "FROM debian:bookworm-slim", "USER 0:0", "ca-certificates", "tzdata"} {
		if !strings.Contains(dockerfile, required) {
			t.Errorf("Dockerfile lost deployment contract %q", required)
		}
	}
	if strings.Contains(dockerfile, "chmod -R 777") {
		t.Fatal("image must not make credentials world-writable")
	}
}
