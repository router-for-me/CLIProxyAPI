# CLIProxyAPI

[English](README.md) | [中文](README_CN.md) | [日本語](README_JA.md)

CLIProxyAPI は、CLI アカウントとモデルに対して OpenAI 互換、Gemini 互換、
Claude 互換 API を提供するオープンソースの Go プロキシです。OAuth と API
キー認証、複数アカウントのルーティング、ストリーミング応答、ツール呼び出しを
サポートし、他のサービスへ組み込める Go SDK も提供します。

このリポジトリは [`hrygo/CLIProxyAPI`](https://github.com/hrygo/CLIProxyAPI) の
独立して保守される fork です。
[`router-for-me/CLIProxyAPI`](https://github.com/router-for-me/CLIProxyAPI) の
リリースを評価して必要な変更を取り込み、独立した `vX.Y.Z` として公開します。

## プロジェクト状況

| 項目 | 現在の値 |
| --- | --- |
| 開発 trunk | `main` |
| Go ツールチェーン | `1.26.4` |
| Module path | `github.com/router-for-me/CLIProxyAPI/v8` |
| 上流ポリシー | 公開済みリリースのみを対象とし、個別にコミットを評価 |
| リリース配布先 | [GitHub Releases](https://github.com/hrygo/CLIProxyAPI/releases) |
| Homebrew tap | `hrygo/cliproxyapi` |

各 fork リリースがどの上流リリースに対応しているかは、そのリリースのノートに
記録します。[保守とリリース](docs/maintenance.md)を参照してください。

## 主な機能

- OpenAI、Gemini、Claude、Grok 互換 API
- ストリーミング、非ストリーミング、対応環境での WebSocket 応答
- 関数呼び出し、`tool_search`、マルチモーダル入力
- OAuth 認証と API キー認証
- 複数アカウントのラウンドロビン実行と精密なルートポリシー
- 設定可能な OpenAI 互換上流プロバイダー
- ファイル、PostgreSQL、Git、オブジェクトストレージの永続化バックエンド
- Management API、設定のホットリロード、可観測性
- 再利用可能な Go SDK

## クイックスタート

### 必要条件

- Go `1.26.4` または互換性のある新しいバージョン
- 対象モデルを利用できるプロバイダーアカウントまたは API キー

### ビルド

```bash
git clone https://github.com/hrygo/CLIProxyAPI.git
cd CLIProxyAPI
go build -o cli-proxy-api ./cmd/server
```

### 設定

```bash
cp config.example.yaml config.yaml
```

`config.yaml` の待ち受けアドレス、プロバイダー、ルーティング、管理設定を編集
します。作業ディレクトリの `.env` は自動的に読み込まれ、認証情報の保存先は
既定で `auths/` です。

### 実行

```bash
./cli-proxy-api --config config.yaml
```

主なフラグは `--tui`、`--standalone`、`--local-model`、`--no-browser`、
`--oauth-callback-port <port>` です。

### Homebrew

この fork の公開 Release からビルドされたバイナリをインストールします。

```bash
brew tap hrygo/cliproxyapi
brew install hrygo/cliproxyapi/cli-proxy-api
cliproxyapi -h
```

Homebrew は実行ファイルと設定例のみをインストールし、既存の設定を管理하거나
サービスを自動起動しません。アップグレードとロールバック手順は
[Homebrew 保守](ops/homebrew/README.md)にまとめています。

## 設定

| パスまたは変数 | 用途 |
| --- | --- |
| `config.yaml` | サーバー、プロバイダー、ルーティング、管理設定 |
| `config.example.yaml` | 例付きの設定テンプレート |
| `.env` | 作業ディレクトリから自動読み込みされる任意の環境変数 |
| `auths/` | 既定の OAuth 認証情報保存先 |
| `PGSTORE_*` | PostgreSQL ストレージバックエンド設定 |
| `GITSTORE_*` | Git ストレージバックエンド設定 |
| `OBJECTSTORE_*` | オブジェクトストレージバックエンド設定 |

管理 API は [Management API](docs/management-api-v8.md)を参照してください。
その他のプロトコルと SDK の文書は[ドキュメント](#ドキュメント)にまとまって
います。

## 開発

```bash
gofmt -w path/to/changed.go
go vet ./...
go test ./... -count=1
go build -o cli-proxy-api ./cmd/server
```

リリース、上流取り込み、モジュール横断の動作変更では、完全な保守ゲートを
実行してください。

```bash
ops/upstream-intake/verify-absorb.sh
python3 ops/tests/test_maintenance.py
```

ゲートは有効な Go ツールチェーンの `gofmt` を使います。Go のバージョンによって
整形結果が異なる場合があるため、ローカルと PR CI は同じリリースを使います。

貢献先は `hrygo/CLIProxyAPI:main` です。短期の `codex/<task>` ブランチを作成し、
[AGENTS.md](AGENTS.md)に従ってください。

## 保守とリリース

- [保守・リリース・ロールバック](docs/maintenance.md)
- [上流 release の取り込み](ops/upstream-intake/README.md)
- [取り込み記録](ops/upstream-intake/absorbed.md)
- [リリースノートテンプレート](docs/releases/RELEASE_TEMPLATE.md)
- [Homebrew tap の保守](ops/homebrew/README.md)

各リリースには人手で整理した `docs/releases/<tag>.md` が必要です。ユーザーから
見える変更、検証範囲、既知の問題、および対応した上流 release を明記します。

## ドキュメント

- [設定例](config.example.yaml)
- [Management API](docs/management-api-v8.md)
- [Responses ツール](docs/responses-tools.md)
- [SDK 使用法](docs/sdk-usage.md)
- [SDK 認証](docs/sdk-access.md)
- [SDK 上級](docs/sdk-advanced.md)
- [SDK ウォッチャー](docs/sdk-watcher.md)
- [カスタムプロバイダー例](examples/custom-provider)

## ライセンス

[MIT](LICENSE)

## 謝辞

CLIProxyAPI は
[`router-for-me/CLIProxyAPI`](https://github.com/router-for-me/CLIProxyAPI)
に由来します。この fork はリリース単位の移植を容易にするため、上流の
module path を維持しています。上流のメンテナーとコントリビューターに感謝します。
