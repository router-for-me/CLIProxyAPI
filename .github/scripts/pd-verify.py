#!/usr/bin/env python3
"""Check formatting and tests, blocking only on problems the fork causes.

Upstream's CI builds but never runs its tests or gofmt, so upstream main and
its release tags often carry failing or flaky tests. Editing upstream's tests
to get a green run only creates merge conflicts later, so this script compares
the fork with the upstream commit it is built on instead:

- gofmt: a Go file the fork adds must be formatted. A file the fork changes
  must be formatted if upstream's copy is.
- tests: everything runs. A failing test blocks when the fork owns it (the
  upstream package has no test of that name) or when it fails again on the
  fork while passing every rerun on upstream. Tests that also fail on upstream,
  and tests that pass every rerun, are reported without blocking.

Every rerun is a new `go test -count=1` process: some upstream tests leave
global state behind and fail a second run in the same process.

The upstream base is --base, else $PD_UPSTREAM_BASE, else the merge base of
HEAD and upstream/main (fetched when missing).
"""
import argparse
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

UPSTREAM_URL = "https://github.com/router-for-me/CLIProxyAPI.git"
RERUNS = 3


def git(*args, check=True, cwd=None):
    return subprocess.run(["git", *args], check=check, cwd=cwd, capture_output=True, text=True).stdout.strip()


def upstream_base(explicit):
    if explicit:
        return git("rev-parse", "--verify", explicit + "^{commit}")
    if subprocess.run(["git", "rev-parse", "--verify", "-q", "upstream/main"], capture_output=True).returncode:
        git("fetch", "--quiet", "--no-tags", UPSTREAM_URL, "+refs/heads/main:refs/remotes/upstream/main")
    return git("merge-base", "HEAD", "upstream/main")


def module_path(root):
    for line in (root / "go.mod").read_text().splitlines():
        if line.startswith("module "):
            return line.split()[1]
    raise SystemExit("go.mod has no module line")


def check_format(base):
    """Go files the fork leaves unformatted where upstream's copy is formatted."""
    changed = git("diff", "--name-only", "--no-renames", "--diff-filter=AM", base, "--", "*.go").splitlines()
    unformatted = set(subprocess.run(["gofmt", "-l", *changed], capture_output=True, text=True).stdout.split()) if changed else set()
    problems = []
    for path in sorted(unformatted):
        upstream = subprocess.run(["git", "show", f"{base}:{path}"], capture_output=True)
        if upstream.returncode == 0:
            result = subprocess.run(["gofmt", "-l"], input=upstream.stdout, capture_output=True)
            if result.stdout.strip():
                continue  # upstream ships it unformatted; leave it to upstream
        problems.append(path)
    return problems


def go_test(cwd, packages, run=None, skip=None):
    """Run go test -json and return ({package: {failed top-level tests}}, packages that failed with no test named)."""
    command = ["go", "test", "-count=1", "-json"]
    if run:
        command += ["-run", run]
    if skip:
        command += ["-skip", skip]
    result = subprocess.run(command + list(packages), cwd=cwd, capture_output=True, text=True)
    failed, package_failed = {}, set()
    for line in result.stdout.splitlines():
        try:
            event = json.loads(line)
        except ValueError:
            continue
        package = event.get("Package") or event.get("ImportPath")
        if event.get("Action") not in ("fail", "build-fail") or not package:
            continue
        package = package.split(" ")[0]  # "pkg [pkg.test]" for build failures
        if event.get("Test"):
            failed.setdefault(package, set()).add(event["Test"].split("/")[0])
        else:
            package_failed.add(package)
    package_failed -= set(failed)
    if result.returncode and not failed and not package_failed:
        # go test failed before running anything, e.g. a package pattern that didn't resolve.
        package_failed.update(packages)
        sys.stderr.write(result.stderr[-4000:])
    return failed, package_failed


def exact(names):
    return "^(" + "|".join(re.escape(name) for name in sorted(names)) + ")$"


class Verifier:
    def __init__(self, root, base):
        self.root, self.base = root, base
        self.module = module_path(root)
        self.base_tree = None

    def directory(self, package):
        relative = package[len(self.module):].lstrip("/") if package.startswith(self.module) else package
        return relative or "."

    def upstream_has_package(self, package):
        directory = self.directory(package)
        listed = git("ls-tree", "--name-only", f"{self.base}:{directory}" if directory != "." else self.base, check=False)
        return any(name.endswith(".go") for name in listed.splitlines())

    def fork_owned(self, package, test):
        """True when upstream's package has no test function of this name."""
        directory = self.directory(package)
        pattern = f":(glob){directory}/*_test.go" if directory != "." else ":(glob)*_test.go"
        found = subprocess.run(["git", "grep", "-q", "-E", rf"^func {re.escape(test)}\(", self.base, "--", pattern],
                               cwd=self.root, capture_output=True)
        return found.returncode != 0

    def upstream_tree(self):
        if self.base_tree is None:
            self.base_tree = Path(tempfile.mkdtemp(prefix="pd-upstream-")) / "tree"
            git("worktree", "add", "--quiet", "--detach", str(self.base_tree), self.base)
        return self.base_tree

    def cleanup(self):
        if self.base_tree is not None:
            git("worktree", "remove", "--force", str(self.base_tree), check=False)
            shutil.rmtree(self.base_tree.parent, ignore_errors=True)

    def fails_in_any_rerun(self, cwd, package, test=None):
        for _ in range(RERUNS):
            failed, package_failed = go_test(cwd, [package], run=exact([test]) if test else None)
            if package in package_failed or (test and test in failed.get(package, set())) or (not test and failed):
                return True
        return False

    def classify(self, failed, package_failed):
        """Split failures into blocking and reported lists of (name, reason)."""
        blocking, reported = [], []
        for package in sorted(package_failed):
            name = f"{package} (package)"
            if not self.upstream_has_package(package):
                blocking.append((name, "fork package fails to build or run"))
            elif self.fails_in_any_rerun(self.upstream_tree(), package):
                reported.append((name, "fails on upstream too"))
            elif self.fails_in_any_rerun(self.root, package):
                blocking.append((name, "fails with the fork, passes on upstream"))
            else:
                reported.append((name, "flaky: passed every rerun"))
        for package in sorted(failed):
            for test in sorted(failed[package]):
                name = f"{package} {test}"
                if self.fork_owned(package, test):
                    blocking.append((name, "fork test"))
                elif self.fails_in_any_rerun(self.upstream_tree(), package, test):
                    reported.append((name, "fails on upstream too"))
                elif self.fails_in_any_rerun(self.root, package, test):
                    blocking.append((name, "fails with the fork, passes on upstream"))
                else:
                    reported.append((name, "flaky: passed every rerun"))
        return blocking, reported

    def run_tests(self):
        failed, package_failed = go_test(self.root, ["./..."])
        blocking, reported = self.classify(failed, package_failed)
        # A failing test can stop the rest of its package (a panic or timeout),
        # so run each affected package again without the excused tests.
        excused = {}
        for name, _ in reported:
            package, _, test = name.partition(" ")
            if not test.startswith("("):
                excused.setdefault(package, set()).add(test)
        for package in sorted(set(failed) - {name.split(" ")[0] for name, _ in blocking}):
            again, again_package = go_test(self.root, [package], skip=exact(excused[package]) if package in excused else None)
            more_blocking, more_reported = self.classify(again, again_package)
            blocking += more_blocking
            reported += [item for item in more_reported if item not in reported]
        return blocking, reported


def summarize(base, unformatted, blocking, reported):
    lines = [f"Verified against upstream {base[:12]}."]
    if unformatted:
        lines += ["", "Not gofmt-formatted (run gofmt -w):"] + [f"- {path}" for path in unformatted]
    if blocking:
        lines += ["", "Failures caused by the fork:"] + [f"- {name}: {reason}" for name, reason in blocking]
    if reported:
        lines += ["", "Upstream test failures, not blocking:"] + [f"- {name}: {reason}" for name, reason in reported]
    if not (unformatted or blocking):
        lines += ["", "No problems caused by the fork."]
    text = "\n".join(lines) + "\n"
    print(text, end="")
    summary = os.environ.get("GITHUB_STEP_SUMMARY")
    if summary:
        with open(summary, "a") as handle:
            handle.write("## Fork verification\n\n" + text)


def main():
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("--base", default=os.environ.get("PD_UPSTREAM_BASE"), help="upstream commit or tag the fork is built on")
    args = parser.parse_args()
    root = Path(git("rev-parse", "--show-toplevel"))
    os.chdir(root)
    base = upstream_base(args.base)
    unformatted = check_format(base)
    verifier = Verifier(root, base)
    try:
        blocking, reported = verifier.run_tests()
    finally:
        verifier.cleanup()
    summarize(base, unformatted, blocking, reported)
    return 1 if unformatted or blocking else 0


if __name__ == "__main__":
    sys.exit(main())
