# Homebrew installation and tap maintenance

`cli-proxy-api.rb` is the reviewed source for the separate
`hrygo/homebrew-cliproxyapi` tap's `Formula/cli-proxy-api.rb`. Changing this copy
does not publish the tap. Use `hrygo/cliproxyapi`, not `hrygo/tap`.

## Install and upgrade

```bash
brew tap hrygo/cliproxyapi
brew install hrygo/cliproxyapi/cli-proxy-api
brew update
brew outdated hrygo/cliproxyapi/cli-proxy-api
brew upgrade hrygo/cliproxyapi/cli-proxy-api
brew info hrygo/cliproxyapi/cli-proxy-api
cliproxyapi -h
```

The formula installs the executable as `cliproxyapi` and keeps
`config.example.yaml` under the formula's `pkgshare`. Copy and edit that example
to a user-selected configuration path for a new installation, then start with
`cliproxyapi -config /absolute/path/config.yaml`. Preserve existing config and
auth files during upgrades; the formula does not manage them or register a service.

Disk installation and service restart are separate operations. Restart through
the existing service manager only when authorized. For the previously configured
macOS LaunchAgent, this is `launchctl kickstart -k gui/$(id -u)/com.hrygo.cliproxyapi`.
Do not run `brew services` in parallel with an existing custom LaunchAgent.
Verify the banner using `-h` (there is no `-version` flag), then verify the actual
running service and representative client behavior separately.

## Update the tap after a complete release

1. Confirm successful release CI and a published Release with all ten archives
   and `checksums.txt`. See [release procedure](../../docs/maintenance.md).
2. Download its manifest into a unique temporary directory and render a candidate:

   ```bash
   release_dir="$(mktemp -d)"
   gh release download v1.2.3 --repo hrygo/CLIProxyAPI \
     --pattern checksums.txt --dir "$release_dir"
   python3 ops/homebrew/render-formula.py v1.2.3 "$release_dir/checksums.txt" \
     > "$release_dir/cli-proxy-api.rb"
   diff -u ops/homebrew/cli-proxy-api.rb "$release_dir/cli-proxy-api.rb"
   ```

   Replace the tag with the reviewed version. `diff` exits 1 for expected changes.
   The renderer rejects malformed versions, duplicate/invalid hashes and missing
   platform entries, and does not overwrite the source or tap. Review the diff,
   then apply it to this source and the tap checkout under the task's authorization.
3. In the tap checkout, validate the candidate formula before publishing:

   ```bash
   brew style Formula/cli-proxy-api.rb
   brew audit --strict hrygo/cliproxyapi/cli-proxy-api
   brew install hrygo/cliproxyapi/cli-proxy-api
   brew test hrygo/cliproxyapi/cli-proxy-api
   ```

   Use an isolated validation machine/runner for install/test, or an explicitly
   authorized upgrade on a machine where this formula is already installed.
   `brew install` will not test a changed formula over an installed version.
   Exercise the corresponding artifacts on supported target platforms; one
   local test does not validate all four URLs. Only then publish the tap change.

All four URLs and hashes must refer to the same release. Keep URL-derived version
rather than a redundant `version` declaration. SHA256 verifies bytes, not publisher
identity; the release workflow also verifies uploaded assets before publication.

## Prepare rollback before upgrading

Homebrew has no `brew rollback` command. Maintain a tested versioned formula
(e.g. `hrygo/cliproxyapi/cli-proxy-api@1.0.0`) with the old release URLs/checksums.
`brew extract --version=1.0.0 hrygo/cliproxyapi/cli-proxy-api hrygo/cliproxyapi`
can prepare one from available tap history; review and test it before relying on it.
Retain the matching configuration and any migration notes. On authorized rollback,
unlink the current formula, install/link the prepared versioned formula, and
restart via the existing manager. Verify disk version and running behavior.
Do not rely on old Cellar directories surviving `brew cleanup`.

For a legacy manually installed regular file, first verify its ownership and
preserve it at a unique backup path outside Homebrew's managed directories.
Resolve only that known path conflict when installing the tap formula. Never
force-link over unknown files or copy a binary over a Homebrew symlink.
