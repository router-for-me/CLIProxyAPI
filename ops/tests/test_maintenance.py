#!/usr/bin/env python3
"""Offline checks for release intake and formula rendering."""

import importlib.util
import os
import subprocess
import sys
import tempfile
import textwrap
import unittest
from pathlib import Path

sys.dont_write_bytecode = True

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location(
    "formula", ROOT / "ops/homebrew/render-formula.py",
)
formula = importlib.util.module_from_spec(spec)
spec.loader.exec_module(formula)


class FormulaTests(unittest.TestCase):
    template = (ROOT / "ops/homebrew/cli-proxy-api.rb").read_text()
    manifest = "".join(
        f"{index:064x}  CLIProxyAPI_1.2.3_{target}.tar.gz\n"
        for index, target in enumerate(
            ["darwin_aarch64", "darwin_amd64", "linux_aarch64", "linux_amd64"], 1,
        )
    )

    def test_all_platforms_updated_together(self):
        result = formula.render("v1.2.3", self.manifest, self.template)
        self.assertEqual(result.count("/download/v1.2.3/"), 4)
        self.assertNotIn("/download/v1.0.0/", result)
        self.assertIn(f'sha256 "{4:064x}"', result)

    def test_invalid_or_incomplete_input_fails(self):
        cases = [
            ("v1.2.3-rc1", self.manifest),
            ("v01.2.3", self.manifest),
            ("v1.2.3", self.manifest.splitlines()[0]),
            ("v1.2.3", self.manifest + self.manifest),
            ("v1.2.3", self.manifest.replace("0000", "xxxx", 1)),
        ]
        for tag, manifest in cases:
            with self.subTest(tag=tag, manifest=manifest[:30]):
                with self.assertRaises(ValueError):
                    formula.render(tag, manifest, self.template)


class IntakeTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.base = Path(self.temp.name)
        self.upstream = self.base / "upstream"
        self.local = self.base / "local"
        self.bin = self.base / "bin"
        self.bin.mkdir()
        self.env = dict(os.environ, PATH=f"{self.bin}:{os.environ['PATH']}")
        for repo in [self.upstream, self.local]:
            repo.mkdir()
            self.git(repo, "init", "-q")
            self.git(repo, "config", "user.name", "Fixture")
            self.git(repo, "config", "user.email", "fixture@example.invalid")
        (self.upstream / "data").write_text("baseline")
        self.git(self.upstream, "add", "data")
        self.git(self.upstream, "commit", "-qm", "baseline")
        self.git(self.upstream, "tag", "v8.0.4")
        (self.upstream / "data").write_text("candidate")
        self.git(self.upstream, "commit", "-qam", "fix: fixture")
        self.git(self.upstream, "tag", "-a", "v8.0.6", "-m", "release")
        self.git(self.local, "remote", "add", "upstream", str(self.upstream))
        self.gh("2000-01-01T00:00:00Z")

    def git(self, repo, *args):
        return subprocess.check_output(["git", "-C", str(repo), *args], text=True)

    def gh(self, published):
        path = self.bin / "gh"
        path.write_text(f"#!/bin/sh\nprintf '%s\\n' '{published}'\n")
        path.chmod(0o755)

    def assess(self, *args, **overrides):
        return subprocess.run(
            ["bash", str(ROOT / "ops/upstream-intake/assess-release.sh"), *args],
            cwd=self.local, env=dict(self.env, **overrides),
            text=True, capture_output=True,
        )

    def test_fetch_does_not_create_normal_tags(self):
        result = self.assess("v8.0.6", "v8.0.4")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.git(self.local, "tag", "--list"), "")
        self.assertEqual(
            len(self.git(self.local, "for-each-ref", "--format=%(refname)",
                         "refs/upstream/tags").splitlines()), 2,
        )

    def test_reject_before_fetch(self):
        for args, env in [
            (["v8.0.6"], {}),
            (["v8.0.6-rc1", "v8.0.4"], {}),
            (["v8.0.6", "../bad"], {}),
            (["v8.0.6", "v8.0.4"], {"MIN_AGE_HOURS": "1"}),
            (["v8.0.6", "v8.0.4"], {"MIN_AGE_HOURS": "invalid"}),
        ]:
            result = self.assess(*args, **env)
            self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertEqual(self.git(self.local, "for-each-ref"), "")

    def test_unpublished_and_recent_release_rejected(self):
        for published in ["", "2999-01-01T00:00:00Z"]:
            self.gh(published)
            result = self.assess("v8.0.6", "v8.0.4")
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(self.git(self.local, "for-each-ref"), "")

    def test_reversed_baseline_rejected(self):
        result = self.assess("v8.0.4", "v8.0.6")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("not an ancestor", result.stderr)

    def test_formatter_execution_failure_fails_gate(self):
        source = self.local / "fixture.go"
        source.write_text("package fixture\n")
        self.git(self.local, "add", source.name)
        fake_goroot = self.base / "fake-goroot"
        fake_gofmt = fake_goroot / "bin" / "gofmt"
        fake_gofmt.parent.mkdir(parents=True)
        fake_gofmt.write_text("#!/bin/sh\necho 'formatter failed' >&2\nexit 7\n")
        fake_gofmt.chmod(0o755)
        fake_go = self.bin / "go"
        fake_go.write_text(
            "#!/bin/sh\n"
            "if [ \"$1\" = env ] && [ \"$2\" = GOROOT ]; then\n"
            "  printf '%s\\n' \"$FAKE_GOROOT\"\n"
            "fi\n"
            "exit 0\n"
        )
        fake_go.chmod(0o755)
        result = subprocess.run(
            ["bash", str(ROOT / "ops/upstream-intake/verify-absorb.sh")],
            cwd=self.local,
            env=dict(self.env, FAKE_GOROOT=str(fake_goroot)),
            text=True, capture_output=True,
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("FAIL: gofmt", result.stdout)
        self.assertIn("REGRESSION GATE: FAIL", result.stdout)
        self.assertIn("formatter failed", result.stderr)


class ReleaseTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.base = Path(self.temp.name)
        self.dist = self.base / "dist"
        self.remote = self.base / "remote"
        self.bin = self.base / "bin"
        for path in [self.dist, self.remote, self.bin]:
            path.mkdir()
        self.names = [
            f"CLIProxyAPI_1.2.3_{target}.{extension}"
            for target, extension in [
                ("darwin_amd64", "tar.gz"), ("darwin_aarch64", "tar.gz"),
                ("windows_amd64", "zip"), ("windows_aarch64", "zip"),
                ("linux_amd64", "tar.gz"), ("linux_aarch64", "tar.gz"),
                ("linux_amd64_no-plugin", "tar.gz"),
                ("linux_aarch64_no-plugin", "tar.gz"),
                ("freebsd_amd64", "tar.gz"),
                ("freebsd_aarch64_no-plugin", "tar.gz"),
            ]
        ]
        for name in self.names:
            for path in [self.dist, self.remote]:
                (path / name).write_bytes(name.encode())
        workflow = (ROOT / ".github/workflows/release.yaml").read_text()
        step = workflow.split("      - name: Verify complete assets and publish release\n", 1)[1]
        self.script = textwrap.dedent(step.split("        run: |\n", 1)[1])
        gh = self.bin / "gh"
        gh.write_text("""#!/bin/sh
set -eu
case "$1 $2" in
  "release download")
    while [ "$#" -gt 0 ]; do
      if [ "$1" = "--dir" ]; then shift; cp "$FAKE_REMOTE/"* "$1/"; fi
      shift
    done
    ;;
  "release upload"|"release edit")
    printf '%s\\n' "$*" >> "$FAKE_EVENTS"
    ;;
  *) exit 1 ;;
esac
""")
        gh.chmod(0o755)
        self.events = self.base / "events"

    def run_finalize(self):
        return subprocess.run(
            ["bash", "-euo", "pipefail", "-c", self.script], cwd=self.base,
            env=dict(os.environ, PATH=f"{self.bin}:{os.environ['PATH']}",
                     RELEASE_TAG="v1.2.3", FAKE_REMOTE=str(self.remote),
                     FAKE_EVENTS=str(self.events)),
            text=True, capture_output=True,
        )

    def test_complete_verified_release_publishes(self):
        result = self.run_finalize()
        self.assertEqual(result.returncode, 0, result.stderr)
        events = self.events.read_text().splitlines()
        self.assertEqual(len(events), 2)
        self.assertIn("release upload v1.2.3 checksums.txt", events[0])
        self.assertIn("release edit v1.2.3 --draft=false --latest", events[1])

    def test_incomplete_or_corrupt_assets_never_publish(self):
        cases = [
            self.dist / self.names[0], self.remote / self.names[0],
            self.remote / self.names[1],
        ]
        for index, path in enumerate(cases):
            original = path.read_bytes()
            try:
                if index == 2:
                    path.write_bytes(b"corrupt")
                else:
                    path.unlink()
                result = self.run_finalize()
                self.assertNotEqual(result.returncode, 0, result.stdout)
                self.assertFalse(self.events.exists())
            finally:
                path.write_bytes(original)


if __name__ == "__main__":
    unittest.main()
