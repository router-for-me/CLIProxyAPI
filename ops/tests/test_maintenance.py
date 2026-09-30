#!/usr/bin/env python3
"""Offline checks for release intake and formula rendering."""

import importlib.util
import os
import re
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
release_notes_spec = importlib.util.spec_from_file_location(
    "release_notes", ROOT / "ops/release-notes/validate.py",
)
release_notes = importlib.util.module_from_spec(release_notes_spec)
release_notes_spec.loader.exec_module(release_notes)


class ReadmeTests(unittest.TestCase):
    files = {
        "README.md": [
            "## Project Status", "## Capabilities", "## Quick Start",
            "## Configuration", "## Development", "## Maintenance and Releases",
            "## Documentation", "## License", "## Acknowledgements",
        ],
        "README_CN.md": [
            "## 项目状态", "## 核心能力", "## 快速开始", "## 配置",
            "## 开发", "## 维护与发布", "## 文档", "## 许可证", "## 致谢",
        ],
        "README_JA.md": [
            "## プロジェクト状況", "## 主な機能", "## クイックスタート",
            "## 設定", "## 開発", "## 保守とリリース", "## ドキュメント",
            "## ライセンス", "## 謝辞",
        ],
    }
    commercial = re.compile(
        r"sponsor|赞助|スポンサー|aff=|invitecode|promo code|优惠码|"
        r"返佣|API relay service|中转服务商|who is with us|更多选择|more choices",
        re.IGNORECASE,
    )

    def test_readmes_use_open_source_structure(self):
        for name, headings in self.files.items():
            with self.subTest(readme=name):
                text = (ROOT / name).read_text()
                for heading in headings:
                    self.assertIn(heading, text)
                self.assertIsNone(self.commercial.search(text))

    def test_readme_local_links_resolve(self):
        for name in self.files:
            readme = ROOT / name
            for target in re.findall(r"\]\(([^)]+)\)", readme.read_text()):
                if re.match(r"[a-z]+://", target) or target.startswith("#"):
                    continue
                path = target.split("#", 1)[0]
                self.assertTrue((readme.parent / path).exists(), f"{name}: {target}")


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


class ReleaseNoteTests(unittest.TestCase):
    aligned = """# v1.2.3 - 2026-10-01

## Summary

- Align the fork with the reviewed upstream release and harden release tooling.

## Upstream alignment

- Upstream release: `v8.0.6`
- Previous baseline: `v8.0.4`
- Intake record: [absorbed.md](../../ops/upstream-intake/absorbed.md)
- Ported: protocol compatibility and security fixes
- Skipped: upstream CI and dependency churn

## Breaking changes

- None.

## Added

- Release-note validation in CI.

## Changed

- Release bodies now use curated notes.

## Fixed

- Missing assets no longer publish a draft release.

## Security

- None.

## Validation

- Full regression gate and release workflow.

## Known issues

- None.
"""

    def test_valid_aligned_release_note(self):
        self.assertEqual(release_notes.validate_note("v1.2.3", self.aligned), [])

    def test_valid_fork_only_release_note(self):
        note = self.aligned.replace("`v8.0.6`", "none (fork-only release)")
        note = note.replace("`v8.0.4`", "none")
        note = note.replace(
            "- Ported: protocol compatibility and security fixes",
            "- Ported: none; fork-specific maintenance only",
        )
        self.assertEqual(release_notes.validate_note("v1.2.3", note), [])

    def test_invalid_release_notes_are_rejected(self):
        cases = {
            "wrong heading": self.aligned.replace("# v1.2.3", "# 1.2.3"),
            "missing section": self.aligned.replace("## Security\n\n- None.\n", ""),
            "placeholder": self.aligned.replace("protocol compatibility", "<describe>"),
            "invalid upstream": self.aligned.replace("`v8.0.6`", "`upstream-latest`"),
            "missing intake": self.aligned.replace(
                "- Intake record: [absorbed.md](../../ops/upstream-intake/absorbed.md)",
                "- Intake record: not recorded",
            ),
            "duplicate alignment field": self.aligned.replace(
                "- Previous baseline: `v8.0.4`",
                "- Previous baseline: `v8.0.4`\n- Previous baseline: `v8.0.5`",
            ),
            "manual generated sections": self.aligned + (
                "\n## Release assets\n\n- Duplicate section.\n"
                "\n## Full changelog\n\n- Duplicate section.\n"
            ),
            "empty section": re.sub(
                r"## Fixed\n\n.*?(?=\n## )", "## Fixed\n\n", self.aligned,
                flags=re.DOTALL,
            ),
            "invalid tag": self.aligned.replace("v1.2.3", "v1.2", 1),
        }
        for name, note in cases.items():
            with self.subTest(name=name):
                self.assertNotEqual(release_notes.validate_note("v1.2.3", note), [])

    def test_template_contains_required_alignment_fields(self):
        template = (ROOT / "docs/releases/RELEASE_TEMPLATE.md").read_text()
        for heading in ["## Summary", "## Upstream alignment", "## Breaking changes",
                        "## Added", "## Changed", "## Fixed", "## Security",
                        "## Validation", "## Known issues"]:
            self.assertIn(heading, template)
        for field in ["Upstream release:", "Previous baseline:", "Intake record:",
                      "Ported:", "Skipped:"]:
            self.assertIn(field, template)

    def test_rendered_release_note_keeps_curated_content_first(self):
        rendered = release_notes.render_note(
            "v1.2.3", self.aligned, "v1.2.2", "hrygo/CLIProxyAPI",
        )
        self.assertLess(rendered.index("## Summary"), rendered.index("## Release assets"))
        self.assertLess(rendered.index("## Release assets"), rendered.index("## Full changelog"))
        self.assertIn(
            "https://github.com/hrygo/CLIProxyAPI/compare/v1.2.2...v1.2.3",
            rendered,
        )
        self.assertIn("CLIProxyAPI_<version>_linux_<arch>_no-plugin.tar.gz", rendered)

    def test_first_release_uses_commit_history_link(self):
        note = self.aligned.replace("# v1.2.3", "# v1.0.0", 1)
        rendered = release_notes.render_note(
            "v1.0.0", note, None, "hrygo/CLIProxyAPI",
        )
        self.assertIn("https://github.com/hrygo/CLIProxyAPI/commits/v1.0.0", rendered)

    def test_workflow_requires_and_renders_curated_notes(self):
        workflow = (ROOT / ".github/workflows/release.yaml").read_text()
        self.assertIn(
            'python3 ops/release-notes/validate.py validate '
            '"$RELEASE_TAG" "$RELEASE_NOTES_FILE"',
            workflow,
        )
        self.assertRegex(
            workflow,
            r'python3 ops/release-notes/validate\.py render "\$RELEASE_TAG" \\\n'
            r'\s+"\$RELEASE_NOTES_FILE"',
        )
        self.assertNotIn("releases/generate-notes", workflow)


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
