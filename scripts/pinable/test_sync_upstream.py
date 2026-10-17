#!/usr/bin/env python3
"""Exercise the sync script with isolated repositories and a fake Go tool."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

SCRIPT = Path(__file__).with_name("sync-upstream.sh").resolve()


class SyncTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.upstream = self.root / "upstream"
        self.fork = self.root / "fork"
        self.bin = self.root / "bin"
        self.bin.mkdir()
        go = self.bin / "go"
        go.write_text('#!/bin/sh\n[ "${FAIL_GO:-0}" = 0 ]\n')
        go.chmod(0o755)
        self.env = dict(os.environ, PATH=str(self.bin) + os.pathsep + os.environ["PATH"],
                        GIT_AUTHOR_NAME="Sync test", GIT_AUTHOR_EMAIL="test@example.invalid",
                        GIT_COMMITTER_NAME="Sync test", GIT_COMMITTER_EMAIL="test@example.invalid",
                        GIT_CONFIG_NOSYSTEM="1", GIT_CONFIG_GLOBAL=os.devnull)
        self.run_cmd(self.root, "git", "init", "-b", "main", str(self.upstream))
        self.commit(self.upstream, "shared", "base\n")
        self.run_cmd(self.root, "git", "clone", str(self.upstream), str(self.fork))
        self.run_cmd(self.fork, "git", "remote", "add", "upstream", str(self.upstream))
        self.original = self.git(self.fork, "rev-parse", "HEAD")

    def run_cmd(self, cwd, *args, expected=0, **env):
        result = subprocess.run(args, cwd=cwd, env=dict(self.env, **env), text=True,
                                stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=20)
        if expected is not None:
            self.assertEqual(result.returncode, expected, result.stdout)
        return result

    def git(self, cwd, *args):
        return self.run_cmd(cwd, "git", *args).stdout.strip()

    def commit(self, repo, name, text):
        (repo / name).write_text(text)
        self.run_cmd(repo, "git", "add", name)
        self.run_cmd(repo, "git", "commit", "-m", name)
        return self.git(repo, "rev-parse", "HEAD")

    def sync(self, *args, expected=0, **env):
        return self.run_cmd(self.fork, "bash", str(SCRIPT), *args, expected=expected, **env)

    def test_merge_preserves_both_parents_without_moving_main(self):
        fork_tip = self.commit(self.fork, "host", "host extension\n")
        target = self.commit(self.upstream, "upstream", "new feature\n")
        self.sync()
        parents = self.git(self.fork, "show", "-s", "--format=%P", "HEAD").split()
        self.assertEqual(parents, [fork_tip, target])
        self.assertEqual(self.git(self.fork, "rev-parse", "main"), fork_tip)
        self.assertEqual(self.git(self.fork, "config", "--local", "rerere.enabled"), "true")
        self.assertEqual(self.git(self.fork, "config", "--local", "rerere.autoupdate"), "false")

    def test_repeated_sync_is_noop(self):
        self.commit(self.upstream, "upstream", "new feature\n")
        self.sync()
        head = self.git(self.fork, "rev-parse", "HEAD")
        self.sync()
        self.assertEqual(self.git(self.fork, "rev-parse", "HEAD"), head)

    def test_dirty_worktree_is_not_discarded(self):
        (self.fork / "shared").write_text("local edit\n")
        self.sync(expected=1)
        self.assertEqual((self.fork / "shared").read_text(), "local edit\n")
        self.assertEqual(self.git(self.fork, "rev-parse", "HEAD"), self.original)

    def test_untracked_files_are_not_discarded(self):
        (self.fork / "local").write_text("untracked\n")
        self.sync(expected=1)
        self.assertEqual((self.fork / "local").read_text(), "untracked\n")

    def test_conflict_requires_review_then_continue(self):
        self.commit(self.fork, "shared", "fork side\n")
        target = self.commit(self.upstream, "shared", "upstream side\n")
        self.sync(expected=1)
        self.assertTrue(self.git(self.fork, "ls-files", "-u"))
        self.sync("--continue", expected=1)
        (self.fork / "shared").write_text("reviewed combination\n")
        self.run_cmd(self.fork, "git", "add", "shared")
        self.sync("--continue")
        self.run_cmd(self.fork, "git", "merge-base", "--is-ancestor", target, "HEAD")
        self.assertEqual((self.fork / "shared").read_text(), "reviewed combination\n")

    def test_failed_validation_leaves_merge_uncommitted(self):
        self.commit(self.upstream, "upstream", "new feature\n")
        self.sync(expected=1, FAIL_GO="1")
        self.assertEqual(self.git(self.fork, "rev-parse", "HEAD"), self.original)
        self.assertTrue((self.fork / ".git" / "MERGE_HEAD").exists())
        self.sync("--continue")
        self.assertNotEqual(self.git(self.fork, "rev-parse", "HEAD"), self.original)

    def test_abort_restores_original_head(self):
        fork_tip = self.commit(self.fork, "shared", "fork side\n")
        self.commit(self.upstream, "shared", "upstream side\n")
        self.sync(expected=1)
        self.sync("--abort")
        self.assertEqual(self.git(self.fork, "rev-parse", "HEAD"), fork_tip)
        self.assertEqual(self.git(self.fork, "status", "--porcelain"), "")

    def test_unknown_option_is_rejected(self):
        self.sync("--force", expected=1)
        self.assertEqual(self.git(self.fork, "rev-parse", "HEAD"), self.original)


if __name__ == "__main__":
    unittest.main()
