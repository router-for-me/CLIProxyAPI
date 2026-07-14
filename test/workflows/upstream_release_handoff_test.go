package workflows

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestUpstreamReleaseHandoff(t *testing.T) {
	t.Run("automatic_sync_dispatches_release_draft", func(t *testing.T) {
		upstream := loadWorkflow(t, "upstream-sync-pr.yml")

		env := requireChild(t, upstream, "env")
		if got := requireScalar(t, env, "BASE_BRANCH"); got != "main" {
			t.Fatalf("BASE_BRANCH = %q, want main", got)
		}

		permissions := requireChild(t, upstream, "permissions")
		if got := requireScalar(t, permissions, "actions"); got != "write" {
			t.Fatalf("permissions.actions = %q, want write", got)
		}

		syncIndex, _ := findStep(t, upstream, "sync-pr", func(step *yaml.Node) bool {
			return scalarValue(step, "id") == "sync_upstream"
		})
		dispatchIndex, dispatchStep := findStep(t, upstream, "sync-pr", func(step *yaml.Node) bool {
			return scalarValue(step, "name") == "Trigger release draft workflow"
		})
		if dispatchIndex != syncIndex+1 {
			t.Fatalf(
				"release-draft dispatch index = %d, want immediately after sync_upstream at %d",
				dispatchIndex,
				syncIndex,
			)
		}

		if got := strings.TrimSpace(requireScalar(t, dispatchStep, "if")); got != "steps.sync_upstream.outputs.sync_outcome == 'synced'" {
			t.Fatalf("dispatch if = %q, want automatic-sync-only guard", got)
		}

		run := requireScalar(t, dispatchStep, "run")
		for _, want := range []string{
			"set -euo pipefail",
			"gh workflow run release-pr.yml",
			"--repo \"${GITHUB_REPOSITORY}\"",
			"--ref \"${BASE_BRANCH}\"",
		} {
			if !strings.Contains(run, want) {
				t.Fatalf("dispatch script missing %q:\n%s", want, run)
			}
		}

		dispatchEnv := requireChild(t, dispatchStep, "env")
		if got := requireScalar(t, dispatchEnv, "GH_TOKEN"); got != "${{ github.token }}" {
			t.Fatalf("dispatch GH_TOKEN = %q, want github.token", got)
		}
	})

	t.Run("merged_trusted_conflict_review_pr_dispatches_release_draft", func(t *testing.T) {
		release := loadWorkflow(t, "release-pr.yml")
		triggers := requireChild(t, release, "on")

		requireChild(t, triggers, "schedule")
		requireChild(t, triggers, "workflow_dispatch")

		pullRequestTarget := requireChild(t, triggers, "pull_request_target")
		types := requireChild(t, pullRequestTarget, "types")
		branches := requireChild(t, pullRequestTarget, "branches")
		if !sequenceContains(types, "closed") {
			t.Fatal("pull_request_target.types does not include closed")
		}
		if !sequenceContains(branches, "main") {
			t.Fatal("pull_request_target.branches does not include main")
		}

		job := requireChild(t, requireChild(t, release, "jobs"), "draft-release-pr")
		guard := requireScalar(t, job, "if")
		const wantGuard = `
github.event_name == 'schedule' ||
github.event_name == 'workflow_dispatch' ||
(
  github.event_name == 'pull_request_target' &&
  github.event.action == 'closed' &&
  github.event.pull_request.merged == true &&
  github.event.pull_request.base.ref == 'main' &&
  github.event.pull_request.base.repo.full_name == github.repository &&
  github.event.pull_request.head.repo.full_name == 'router-for-me/CLIProxyAPI' &&
  github.event.pull_request.head.ref == 'main'
)
`
		if got := normalizeExpression(guard); got != normalizeExpression(wantGuard) {
			t.Fatalf("draft-release-pr guard = %q, want %q", got, normalizeExpression(wantGuard))
		}

		for _, step := range requireSteps(t, job) {
			run := scalarValue(step, "run")
			for _, forbidden := range []string{
				"git tag -a",
				"refs/tags/",
				"gh release create",
				"gh workflow run release.yaml",
				"gh workflow run docker-image.yml",
			} {
				if strings.Contains(run, forbidden) {
					t.Fatalf("release-pr must remain draft-only; step %q contains %q", scalarValue(step, "name"), forbidden)
				}
			}
		}
	})
}

func loadWorkflow(t *testing.T, name string) *yaml.Node {
	t.Helper()

	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(testFile), "..", ".."))
	path := filepath.Join(repoRoot, ".github", "workflows", name)

	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatalf("read %s: %v", path, errRead)
	}

	var document yaml.Node
	if errDecode := yaml.Unmarshal(raw, &document); errDecode != nil {
		t.Fatalf("parse %s: %v", path, errDecode)
	}
	if len(document.Content) != 1 {
		t.Fatalf("%s document content length = %d, want 1", path, len(document.Content))
	}
	return document.Content[0]
}

func requireChild(t *testing.T, node *yaml.Node, key string) *yaml.Node {
	t.Helper()

	if node == nil || node.Kind != yaml.MappingNode {
		t.Fatalf("node for %q is not a mapping", key)
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	t.Fatalf("required YAML key %q is missing", key)
	return nil
}

func requireScalar(t *testing.T, node *yaml.Node, key string) string {
	t.Helper()

	value := requireChild(t, node, key)
	if value.Kind != yaml.ScalarNode {
		t.Fatalf("YAML key %q kind = %d, want scalar", key, value.Kind)
	}
	return value.Value
}

func scalarValue(node *yaml.Node, key string) string {
	if node == nil || node.Kind != yaml.MappingNode {
		return ""
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key && node.Content[i+1].Kind == yaml.ScalarNode {
			return node.Content[i+1].Value
		}
	}
	return ""
}

func normalizeExpression(expression string) string {
	return strings.Join(strings.Fields(expression), " ")
}

func findStep(
	t *testing.T,
	workflow *yaml.Node,
	jobID string,
	match func(*yaml.Node) bool,
) (int, *yaml.Node) {
	t.Helper()

	job := requireChild(t, requireChild(t, workflow, "jobs"), jobID)
	steps := requireChild(t, job, "steps")
	if steps.Kind != yaml.SequenceNode {
		t.Fatalf("jobs.%s.steps kind = %d, want sequence", jobID, steps.Kind)
	}
	for index, step := range steps.Content {
		if match(step) {
			return index, step
		}
	}

	t.Fatalf("matching step not found in jobs.%s.steps", jobID)
	return -1, nil
}

func requireSteps(t *testing.T, job *yaml.Node) []*yaml.Node {
	t.Helper()

	steps := requireChild(t, job, "steps")
	if steps.Kind != yaml.SequenceNode {
		t.Fatalf("steps kind = %d, want sequence", steps.Kind)
	}
	return steps.Content
}

func sequenceContains(node *yaml.Node, want string) bool {
	if node == nil || node.Kind != yaml.SequenceNode {
		return false
	}
	for _, item := range node.Content {
		if item.Value == want {
			return true
		}
	}
	return false
}
