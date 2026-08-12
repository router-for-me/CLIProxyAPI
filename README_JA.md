# CLI Proxy API

[English](README.md) | [简体中文](README_CN.md) | 日本語

CLIProxyAPI は、CLI サブスクリプション、OAuth アカウント、API Key、互換アップストリームを OpenAI、Anthropic、Gemini 互換 API として公開するセルフホスト型プロキシです。複数認証情報の負荷分散、ストリーミング、ツール呼び出し、マルチモーダル入力、Web 管理画面、利用量集計をサポートします。

## 主な機能

- OpenAI 互換の `/v1/models`、Chat Completions、Responses、画像、動画、Realtime ルート
- Anthropic 互換の `/v1/messages` と Token カウント
- Gemini 互換の `/v1beta/models`、`generateContent`、Interactions
- Codex、Claude、Antigravity、Kimi、xAI の OAuth ログイン
- Vertex サービスアカウントと OpenAI 互換アップストリーム
- 複数アカウントのラウンドロビン、再試行、クールダウン
- 対応する上流でのストリーミング、WebSocket、ツール、テキスト・画像入力
- API Key の別名、無効化、Token・費用・成功率の集計とエクスポート
- ディスクから読み込む Web 管理画面と組み込み用途向け Go SDK

## 対応する認証情報

| プロバイダー | 認証方法 | コマンドまたは設定 |
|---|---|---|
| OpenAI Codex | OAuth / デバイスコード | `--codex-login` / `--codex-device-login` |
| Anthropic Claude | OAuth | `--claude-login` |
| Google Antigravity | OAuth | `--antigravity-login` |
| Kimi | OAuth デバイスコード | `--kimi-login` |
| xAI / Grok | OAuth デバイスコード | `--xai-login` |
| Google Vertex AI | サービスアカウント JSON | `--vertex-import FILE` |
| Gemini API Key / 互換アップストリーム | YAML | `config.yaml` |

OAuth 認証情報は `auth-dir` に保存されます。既定値は実行ユーザーの `~/.cli-proxy-api` です。

## 配布ファイル

ビルド済みファイルは [GitHub Releases](https://github.com/router-for-me/CLIProxyAPI/releases) から取得できます。

| 環境 | ファイル名 |
|---|---|
| Windows x64 | `CLIProxyAPI_<version>_windows_amd64.zip` |
| Windows ARM64 | `CLIProxyAPI_<version>_windows_aarch64.zip` |
| macOS Intel | `CLIProxyAPI_<version>_darwin_amd64.tar.gz` |
| macOS Apple Silicon | `CLIProxyAPI_<version>_darwin_aarch64.tar.gz` |
| Linux x64 | `CLIProxyAPI_<version>_linux_amd64.tar.gz` |
| Linux ARM64 | `CLIProxyAPI_<version>_linux_aarch64.tar.gz` |
| musl Linux / OpenWrt | ファイル名に `_no-plugin` を追加 |

標準 Linux 版は GLIBC 2.17 以降が必要で、動的プラグインを利用できます。`_no-plugin` 版は動的プラグインを含まないポータブルな静的バイナリです。

## 初期設定

起動前に設定ファイルをコピーします。

```powershell
# Windows PowerShell
Copy-Item .\config.example.yaml .\config.yaml
```

```bash
# Linux / macOS
cp config.example.yaml config.yaml
```

ローカル専用の最小例：

```yaml
host: "127.0.0.1"
port: 8317

remote-management:
  allow-remote: false
  secret-key: "CHANGE_THIS_MANAGEMENT_KEY"
  web-directory: "web/management"

api-keys:
  - "CHANGE_THIS_CLIENT_API_KEY"

usage-statistics-enabled: true
```

`your-api-key-*` のようなサンプル値は必ず変更してください。テンプレートのままではセーフモードが有効になり、プロキシ API は HTTP 403 を返します。`host: ""` は全 IPv4 / IPv6 インターフェースで待ち受けるため、ローカル利用では `127.0.0.1` を推奨します。

## Windows で起動

ZIP を展開し、そのディレクトリで PowerShell を開きます。

```powershell
.\cli-proxy-api.exe --config .\config.yaml
```

停止するには `Ctrl+C` を押します。この実行ファイルはコンソールアプリであり、Windows Service を自動作成しません。常駐化にはタスク スケジューラ、NSSM、または既存のプロセスマネージャーを使用してください。

## Linux で起動

```bash
mkdir -p cliproxyapi
tar -xzf CLIProxyAPI_<version>_linux_amd64.tar.gz -C cliproxyapi
cd cliproxyapi
cp config.example.yaml config.yaml
chmod +x ./cli-proxy-api
./cli-proxy-api --config ./config.yaml
```

ARM 環境では ARM64 版を使用します。musl、OpenWrt、または古い GLIBC 環境では `_no-plugin` 版を選択してください。長時間運用する場合は systemd、OpenRC、supervisord などで同じコマンドを管理します。リポジトリには service unit は含まれていません。

## macOS で起動

```bash
mkdir -p cliproxyapi
tar -xzf CLIProxyAPI_<version>_darwin_aarch64.tar.gz -C cliproxyapi
cd cliproxyapi
cp config.example.yaml config.yaml
chmod +x ./cli-proxy-api
./cli-proxy-api --config ./config.yaml
```

Intel Mac は `darwin_amd64`、Apple Silicon は `darwin_aarch64` を使用します。配布ファイルは Apple の公証を受けていないため、Gatekeeper がブロックした場合は入手元と SHA-256 を確認してからシステム設定で許可してください。

## Docker で起動

最初に `config.yaml` を作成します。存在しない bind mount 元は Docker によってディレクトリとして作成される場合があります。

```bash
cp config.example.yaml config.yaml
docker compose up -d --remove-orphans --no-build
docker compose logs -f cli-proxy-api
```

停止と再起動：

```bash
docker compose restart cli-proxy-api
docker compose down
```

現在のソースからイメージを作成する場合は、Linux/macOS で `./docker-build.sh`、Windows PowerShell で `.\docker-build.ps1` を実行し、選択肢 2 を選びます。

Compose は次を永続化します。

| ホスト | コンテナ |
|---|---|
| `./config.yaml` | `/CLIProxyAPI/config.yaml` |
| `./auths` | `/root/.cli-proxy-api` |
| `./logs` | `/CLIProxyAPI/logs` |
| `./plugins` | `/CLIProxyAPI/plugins` |

ローカル専用にする場合、ポート公開を `127.0.0.1:8317:8317` に変更してください。現在の Compose では `env_file` がコメントアウトされているため、ストレージ用環境変数や `MANAGEMENT_PASSWORD` は明示的にコンテナへ渡す必要があります。

## ソースからビルド

Go 1.26 以降が必要です。

```bash
git clone https://github.com/router-for-me/CLIProxyAPI.git
cd CLIProxyAPI
cp config.example.yaml config.yaml
go build -o cli-proxy-api ./cmd/server
./cli-proxy-api --config ./config.yaml
```

Windows PowerShell：

```powershell
git clone https://github.com/router-for-me/CLIProxyAPI.git
Set-Location CLIProxyAPI
Copy-Item .\config.example.yaml .\config.yaml
go build -o cli-proxy-api.exe ./cmd/server
.\cli-proxy-api.exe --config .\config.yaml
```

通常のビルドは動的プラグイン対応のため CGO と C ツールチェーンを使用します。動的プラグインが不要なら `CGO_ENABLED=0` でポータブル版を構築できます。

## OAuth ログイン

同じ設定ファイルで、一度に一つのログインコマンドを実行します。

```bash
./cli-proxy-api --config ./config.yaml --codex-login
./cli-proxy-api --config ./config.yaml --codex-device-login
./cli-proxy-api --config ./config.yaml --claude-login
./cli-proxy-api --config ./config.yaml --antigravity-login
./cli-proxy-api --config ./config.yaml --kimi-login
./cli-proxy-api --config ./config.yaml --xai-login
```

ログインが完了するとプロセスは終了します。その後、通常のサーバー起動コマンドを実行してください。ヘッドレス環境では `--no-browser` を追加し、必要に応じて `--oauth-callback-port PORT` を指定します。Vertex 認証情報のインポート：

```bash
./cli-proxy-api --config ./config.yaml --vertex-import ./service-account.json
```

## API の利用

ヘルスチェック：

```bash
curl http://127.0.0.1:8317/healthz
```

モデル一覧：

```bash
curl http://127.0.0.1:8317/v1/models \
  -H "Authorization: Bearer YOUR_CLIENT_API_KEY"
```

一覧で返されたモデル ID を使用します。

```bash
curl http://127.0.0.1:8317/v1/chat/completions \
  -H "Authorization: Bearer YOUR_CLIENT_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"MODEL_ID","messages":[{"role":"user","content":"Hello"}]}'
```

| クライアント | Base URL |
|---|---|
| OpenAI 互換 SDK | `http://127.0.0.1:8317/v1` |
| Anthropic | `http://127.0.0.1:8317`（`/v1/messages`） |
| Gemini | `http://127.0.0.1:8317`（`/v1beta`） |
| Codex `chatgpt_base_url` | `http://127.0.0.1:8317/backend-api/codex` |

認証情報が URL やプロキシログに残らないよう、Key はクエリ文字列ではなく認証ヘッダーで送信してください。

## Web 管理画面

[http://127.0.0.1:8317/management.html](http://127.0.0.1:8317/management.html) を開きます。管理 API は `remote-management.secret-key` または `MANAGEMENT_PASSWORD` が設定されている場合のみ有効です。両方が空なら `/v0/management/*` は 404 になります。

ソースツリーの新しい UI は `web/management` にあります。HTML、CSS、JavaScript はリクエストごとにディスクから読み込まれるため、変更後はブラウザーを更新するだけで、実行ファイルの再ビルドは不要です。

現在の Release アーカイブと Docker イメージには `web/management` が含まれていません。ディレクトリが見つからない場合、`/management.html` は従来の単一ファイルパネルへフォールバックします。新しい UI を Release 版で使うには `web/management` を `config.yaml` と同じ場所へコピーします。Docker では次の mount も追加します。

```yaml
volumes:
  - ./web/management:/CLIProxyAPI/web/management:ro
```

## 利用量統計

`usage-statistics-enabled` を有効にし、`api-key-metadata` で Key の安定 ID、別名、状態を設定します。必要なら `usage-pricing` で推定単価を定義できます。

- `attempts`、`success`、`failed`：外部クライアント要求ごとの最終結果。ユーザー集計や費用推定向けです。
- `upstream_attempts`、`upstream_failed_attempts`：認証情報、モデル、プロバイダーへの各試行。運用と再試行分析向けです。

スナップショットは生のクライアント Key や要求本文を保存しません。推定費用は支払い、残高、請求書、正式な精算ではありません。

## TUI

```bash
./cli-proxy-api --config ./config.yaml --tui
```

`--standalone` を追加すると TUI と内蔵ローカルサーバーを同時に起動します。

## SDK と詳細資料

- [SDK 基本ガイド](docs/sdk-usage.md)
- [高度な実行設定](docs/sdk-advanced.md)
- [認証とアクセス](docs/sdk-access.md)
- [認証情報のロードと監視](docs/sdk-watcher.md)
- [カスタムプロバイダー例](examples/custom-provider)
- [プラグイン例](examples/plugin)
- [Management API](https://help.router-for.me/management/api)

## セキュリティ

- ローカル利用では `host: "127.0.0.1"` を使用してください。
- クライアント Key と管理パスワードには別々の強い値を設定してください。
- リモート管理が不要なら `allow-remote: false` のままにしてください。
- 信頼できないネットワークから管理する場合は HTTPS を使用してください。
- `config.yaml`、`.env`、`auths/`、利用量スナップショットを Git にコミットしないでください。

## トラブルシューティング

| 症状 | 確認項目 |
|---|---|
| API が 403 | `api-keys` のテンプレート値を変更する |
| 管理 API が 404 | 管理シークレットまたは `MANAGEMENT_PASSWORD` を設定する |
| 新しい管理画面が出ない | `web/management/index.html` の位置を確認して強制再読み込みする |
| OAuth が別の端末で開く | `--no-browser` を追加して表示された手順に従う |
| Linux バイナリが起動しない | `_no-plugin` 版または互換 GLIBC を使用する |

## コントリビューション

1. リポジトリを Fork し、機能ブランチを作成します。
2. 変更と必要なテストを行います。
3. Go の変更では `gofmt -w .`、`go test ./...`、クリーンビルドを実行します。
4. ブランチを push し、検証結果を記載した Pull Request を作成します。

## ライセンス

本プロジェクトは [MIT License](LICENSE) で提供されます。
