# CLIProxyAPI Custom Update Instructions

This repository contains the `CLIProxyAPI` source code with custom patches applied:
1. **Usage Persistence** (Port of PR #1944) - Automatically saves token/request usage statistics to `./usage/usage.json` every 5 minutes and on shutdown.
2. **Graceful Shutdown Fix** - Fixes a bug in `sdk/cliproxy/service.go` where the shutdown context instantly expired, which previously caused 0-byte usage files to be created on shutdown.

---

## How to Update from Upstream (Mainline)

When a new version of `CLIProxyAPI` is released, you can easily pull the updates while keeping these custom features intact using `git rebase`.

### Step 1: Fetch the Latest Upstream Code
Add the original repository as a remote (if you haven't already):
```bash
git remote add upstream https://github.com/router-for-me/CLIProxyAPI.git
```

Fetch the latest changes:
```bash
git fetch upstream
```

### Step 2: Rebase Your Custom Branch
Assuming your custom changes are on your `main` branch (or a feature branch), rebase them on top of the latest upstream release/branch. For example, to update to the upstream `main` branch:

```bash
# Make sure you are on your custom branch
git checkout main

# Replay your custom commits on top of the latest upstream code
git rebase upstream/main
```
*(Note: If there are merge conflicts, git will pause and ask you to resolve them. Open the conflicting files, fix them, run `git add <file>`, and then `git rebase --continue`)*

### Step 3: Rebuild the Binary
Once the rebase is complete, compile the new binary:
```bash
go build -o cli-proxy-api ./cmd/server
```

### Step 4: Deploy the New Binary
Stop the service, replace the binary, and start it again:
```bash
# Stop the service
systemctl --user stop cliproxyapi.service

# Backup the old binary (optional but recommended)
cp /home/nonos/cliproxyapi/cli-proxy-api /home/nonos/cliproxyapi/cli-proxy-api.backup

# Replace the binary
mv cli-proxy-api /home/nonos/cliproxyapi/cli-proxy-api

# Start the service
systemctl --user start cliproxyapi.service

# Verify it is running and persistence loaded successfully
journalctl --user -u cliproxyapi.service -n 50 --no-pager
```

### Step 5: Push Your Updated Custom Repo
Save the rebased branch back to your private GitHub repository:
```bash
# Force push is required after a rebase
git push -f origin main
```