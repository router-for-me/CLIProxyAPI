# Homebrew tap source

`cli-proxy-api.rb` 是 Homebrew tap `hrygo/homebrew-cliproxyapi` 的公式源文件。
tap 是独立仓库，本目录只是它的版本化来源，保证公式改动可在本仓库 review。

发布流程见 `docs/plans/2026-10-01-fork-maintenance-runbook.zh-CN.md`。

## 发版时更新公式

1. 在本 fork 打 tag `vX.Y.Z` 并推送（本 fork 首个版本为 `v1.0.0`），等待
   `release.yaml` 产出 Release。
2. 从 Release 的 `checksums.txt` 取对应行得到 `sha256`（填入公式中的
   `PLACEHOLDER_SHA256_*`）：

   ```bash
   curl -fsSL https://github.com/hrygo/CLIProxyAPI/releases/download/vX.Y.Z/checksums.txt \
     | grep "CLIProxyAPI_${X.Y.Z}_darwin_"
   ```

3. 把四个 URL 里的版本号替换为新版本。
4. 同步到 tap 仓库 `hrygo/homebrew-cliproxyapi` 的 `Formula/cli-proxy-api.rb`。
5. 审计通过后再推：

   ```bash
   brew style Formula/cli-proxy-api.rb
   brew audit --tap=hrygo/cliproxyapi
   ```

公式**不声明 `version`**，由 Homebrew 从 URL 扫描得出。显式写 `version` 会被
`brew audit` 判为 `redundant with version scanned from URL`。四个 URL 里的版本号
必须一致，否则不同平台会扫出不同版本。版本号取 tag 去掉前导 `v` 的结果
（release workflow 的 `RELEASE_VERSION=${GITHUB_REF_NAME#v}`）。

## 本机验证

tap 短名是 `hrygo/cliproxyapi`。`hrygo/tap` 会解析到 `hrygo/homebrew-tap`，
是个不存在的仓库。

```bash
brew tap hrygo/cliproxyapi
brew install hrygo/cliproxyapi/cli-proxy-api
brew upgrade cli-proxy-api
brew info hrygo/cliproxyapi/cli-proxy-api
```
