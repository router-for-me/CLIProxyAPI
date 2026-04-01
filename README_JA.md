# CLI Proxy API

[English](README.md) | [中文](README_CN.md) | 日本語

CLI向けのOpenAI/Gemini/Claude/Codex互換APIインターフェースを提供するプロキシサーバーです。OAuth認証と複数のAIプロバイダーをサポートしています。

## 機能

### コア機能
- CLIモデル向けのOpenAI/Gemini/Claude/Codex互換APIエンドポイント
- ストリーミングおよび非ストリーミングレスポンス
- 関数呼び出し/ツールのサポート
- マルチモーダル入力サポート（テキストと画像）

### OAuth認証
- OpenAI Codexサポート（OAuthログイン）
- Claude Codeサポート（OAuthログイン）
- Qwen Codeサポート（OAuthログイン）
- iFlowサポート（OAuthログイン）

### マルチアカウント管理
- ラウンドロビン負荷分散による複数アカウント対応
- Geminiマルチアカウント（AI Studio Build、Gemini CLI）
- OpenAI Codexマルチアカウント
- Claude Codeマルチアカウント
- Qwen Codeマルチアカウント
- iFlowマルチアカウント

### 高度な機能
- 設定によるOpenAI互換アップストリームプロバイダー（例：OpenRouter）
- モデルエイリアスとスマートルーティング
- OpenAI互換プロバイダー向けのサーキットブレーカーサポート
- 公平なスケジューリングのための重み付きプロバイダー ローテーション
- Anthropic APIキー認証
- リクエストレベル404エラー処理の最適化

### 統合
- Amp CLIおよびIDE拡張機能サポート
- プロバイダールートエイリアス（`/api/provider/{provider}/v1...`）
- OAuth認証用管理プロキシ
- 自動ルーティングによるスマートモデルフォールバック

## クイックスタート

### インストール

```bash
git clone https://github.com/router-for-me/CLIProxyAPI.git
cd CLIProxyAPI
cp config.example.yaml config.yaml
./cli-proxy-new
```

### Docker

```bash
docker run -v ./config.yaml:/app/config.yaml -p 8080:8080 ghcr.io/router-for-me/cliproxyapi:latest
```

## プロジェクト構造

```
cmd/               # エントリーポイント
internal/          # コアビジネスコード
  api/             # HTTP APIサーバー
  runtime/         # ランタイムとエグゼキューター
  translator/      # プロトコル変換
  auth/            # 認証モジュール
sdk/               # 再利用可能SDK
test/              # 統合テスト
docs/             # ドキュメント
examples/        # サンプルコード
```

## 開発

ビルド/テストコマンドとコードスタイルガイドは [AGENTS.md](AGENTS.md) を参照してください。

### ビルド

```bash
go build -o cli-proxy-new ./cmd/server
```

### テスト

```bash
go test ./...
go test -v -run TestFunctionName ./package/
```

## SDKドキュメント

- 使い方：[docs/sdk-usage.md](docs/sdk-usage.md)
- 上級：[docs/sdk-advanced.md](docs/sdk-advanced.md)
- アクセス：[docs/sdk-access.md](docs/sdk-access.md)
- ウォッチャー：[docs/sdk-watcher.md](docs/sdk-watcher.md)

## コントリビューション

コントリビューションを歓迎します！

1. リポジトリをフォーク
2. フィーチャーブランチを作成（`git checkout -b feature/amazing-feature`）
3. 変更をコミット（`git commit -m 'Add some amazing feature'`）
4. ブランチにプッシュ（`git push origin feature/amazing-feature`）
5. Pull Requestを作成

## 関連プロジェクト

- [vibeproxy](https://github.com/automazeio/vibeproxy) - macOSメニューバーアプリ
- [CCS](https://github.com/kaitranntt/ccs) - Claudeアカウント切り替えCLI
- [Quotio](https://github.com/nguyenphutrong/quotio) - macOSメニューバーアプリ
- [CodMate](https://github.com/loocor/CodMate) - macOS SwiftUIアプリ
- [ProxyPilot](https://github.com/Finesssee/ProxyPilot) - Windows CLI
- [霖君](https://github.com/wangdabaoqq/LinJun) - クロスプラットフォームデスクトップアプリ
- [CLIProxyAPI Dashboard](https://github.com/itsmylife44/cliproxyapi-dashboard) - Web管理パネル

> プロジェクトをこのリストに追加するにはPRを送ってください。

## ライセンス

MITライセンス - 詳細は [LICENSE](LICENSE) ファイルを参照してください。
