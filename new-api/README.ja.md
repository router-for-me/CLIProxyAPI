<div align="center">

![new-api](/web/public/logo.png)

# New API

**モデル、アプリケーション、Agent をつなぐ AI ゲートウェイ**

<p align="center">
  <a href="./README.zh_CN.md">简体中文</a> |
  <a href="./README.zh_TW.md">繁體中文</a> |
  <a href="./README.md">English</a> |
  <a href="./README.fr.md">Français</a> |
  <strong>日本語</strong>
</p>

<p align="center">
  <a href="https://raw.githubusercontent.com/Calcium-Ion/new-api/main/LICENSE">
    <img src="https://img.shields.io/github/license/Calcium-Ion/new-api?color=brightgreen" alt="license">
  </a><!--
  --><a href="https://github.com/Calcium-Ion/new-api/releases/latest">
    <img src="https://img.shields.io/github/v/release/Calcium-Ion/new-api?color=brightgreen&include_prereleases" alt="release">
  </a><!--
  --><a href="https://hub.docker.com/r/CalciumIon/new-api">
    <img src="https://img.shields.io/badge/docker-dockerHub-blue" alt="docker">
  </a>
  <a href="https://atomgit.com/QuantumNous/new-api" target="_blank">
    <img alt="AtomGit G-Star" src="https://atomgit.com/QuantumNous/new-api/star/badge.svg"/>
  </a>
</p>

<p align="center">
  <a href="https://trendshift.io/repositories/20180" target="_blank">
    <img src="https://trendshift.io/api/badge/repositories/20180" alt="QuantumNous%2Fnew-api | Trendshift" style="width: 250px; height: 55px;" width="250" height="55"/>
  </a>
  <br>
  <a href="https://hellogithub.com/repository/QuantumNous/new-api" target="_blank">
    <img src="https://api.hellogithub.com/v1/widgets/recommend.svg?rid=539ac4217e69431684ad4a0bab768811&claim_uid=tbFPfKIDHpc4TzR" alt="Featured｜HelloGitHub" style="width: 250px; height: 54px;" width="250" height="54" />
  </a><!--
  -->
  <a href="https://atomgit.com/QuantumNous/new-api" target="_blank">
    <img alt="AtomGit G-Star" src="https://atomgit.com/QuantumNous/new-api/star/new_badge.svg" width="250" height="55" />
  </a>
</p>

<p align="center">
  <a href="#capabilities">主な機能</a> •
  <a href="#quick-start">クイックスタート</a> •
  <a href="#deployment">デプロイ</a> •
  <a href="#development">開発</a> •
  <a href="#documentation">ドキュメント</a>
</p>

</div>

---

## 📝 プロジェクト説明

New API は、アプリケーション、Agent、チーム向けのセルフホスト型 AI ゲートウェイです。各社のモデルサービスを共通の入口に接続し、ルーティング、アクセス権、使用量、コストを一つの管理画面で扱えます。

チーム内で許可済みのモデルを共有する、クライアントの再設定を減らしてプロバイダーを切り替える、複数モデルを扱うプライベートサービスを構築するといった用途に使えます。OpenAI、Anthropic、Google Gemini、Azure OpenAI、AWS Bedrock、Vertex AI、DeepSeek、Qwen などに対応しています。

> [!IMPORTANT]
> - 本プロジェクトは、合法的に許可された AI API ゲートウェイ、組織レベルの認証、マルチモデル管理、利用量分析、コスト管理、プライベートデプロイのシナリオのみを対象としています。
> - ユーザーは、上流の API キー、アカウント、モデルサービス、インターフェース権限を合法的に取得し、上流のサービス利用規約および適用される法律法規を遵守する必要があります。
> - ユーザーは、利用方法が上流のサービス利用規約および適用される法律法規に準拠していることを確認してください。
> - 生成 AI サービスを公衆に提供する場合、ユーザーは適用される規制要件を遵守し、管轄区域で求められる届出、ライセンス、コンテンツセキュリティ、本人確認、ログ保持、税務、上流認可などのすべての義務を履行してください。

<!-- -->

> [!WARNING]
> 本プロジェクトを公衆向け生成 AI サービスまたは API 再販サービスとして運営する場合、ユーザーは届出、コンテンツセキュリティ、本人確認、ログ保持、税務、決済、上流認可などの必要なコンプライアンス義務を先に完了してください。

---

## 🤝 信頼できるパートナー

<p align="center">
  <em>順不同</em>
</p>

<p align="center">
  <a href="https://www.cherry-ai.com/" target="_blank">
    <img src="./docs/images/cherry-studio.png" alt="Cherry Studio" height="80" />
  </a><!--
  --><a href="https://github.com/iOfficeAI/AionUi/" target="_blank">
    <img src="./docs/images/aionui.png" alt="Aion UI" height="80" />
  </a><!--
  --><a href="https://bda.pku.edu.cn/" target="_blank">
    <img src="./docs/images/pku.png" alt="北京大学" height="80" />
  </a><!--
  --><a href="https://www.compshare.cn/?ytag=GPU_yy_gh_newapi" target="_blank">
    <img src="./docs/images/ucloud.png" alt="UCloud 優刻得" height="80" />
  </a><!--
  --><a href="https://www.aliyun.com/" target="_blank">
    <img src="./docs/images/aliyun.png" alt="Alibaba Cloud" height="80" />
  </a><!--
  --><a href="https://io.net/" target="_blank">
    <img src="./docs/images/io-net.png" alt="IO.NET" height="80" />
  </a>
</p>

---

## 🙏 特別な感謝

<p align="center">
  <a href="https://www.jetbrains.com/?from=new-api" target="_blank">
    <img src="https://resources.jetbrains.com/storage/products/company/brand/logos/jb_beam.png" alt="JetBrains Logo" width="120" />
  </a>
</p>

<p align="center">
  <strong>感謝 <a href="https://www.jetbrains.com/?from=new-api">JetBrains</a> が本プロジェクトに無料のオープンソース開発ライセンスを提供してくれたことに感謝します</strong>
</p>

---

<a id="capabilities"></a>

## 主な機能

| 分野 | できること |
| --- | --- |
| モデル接続 | OpenAI Chat Completions、Responses、Anthropic Messages、Gemini に対応。上流が対応するストリーミング、ツール呼び出し、推論、マルチモーダル入力を利用可能 |
| ルーティング | モデル名のマッピング、チャネルの優先度と重み、再試行、チャネルアフィニティ、複数キーの管理 |
| 使用量とコスト | クォータ、サブスクリプション、使用ログ、キャッシュの課金、式による段階的な料金設定 |
| アクセス制御 | ユーザー、グループ、詳細な権限、API キーの制限。OAuth/OIDC、パスキー、二要素認証、ログインセッション管理 |
| 非同期タスク | JavaScript プラグインで画像・動画などのタスク API を拡張し、状態の確認と成果物の取得に対応 |
| Web 管理画面 | チャネルとモデルの設定、使用・監査ログの確認、Playground でのモデル検証。英語、簡体字中国語、繁体字中国語、フランス語、日本語、ロシア語、ベトナム語に対応 |

### プロトコルとエンドポイント

| インターフェース | 主なエンドポイント |
| --- | --- |
| OpenAI Chat / Responses | `POST /v1/chat/completions`、`POST /v1/responses` |
| Anthropic Messages | `POST /v1/messages` |
| Gemini | `POST /v1beta/models/{model}:generateContent`、`POST /v1beta/models/{model}:streamGenerateContent` |
| Realtime / Responses WebSocket | `GET /v1/realtime`、`GET /v1/responses`（WebSocket アップグレード） |
| 画像 / 音声 | `/v1/images/generations`、`/v1/images/edits`、`/v1/audio/speech`、`/v1/audio/transcriptions`、`/v1/audio/translations` |
| 埋め込み / リランク | `POST /v1/embeddings`、`POST /v1/rerank` |
| タスクプラグイン | `POST /v1/tasks/{pluginKey}`、`GET /v1/tasks/{taskId}`、各プラグインが宣言するルート |

[RelayKit](./relaykit/README.md) は、上記 4 種類のテキストプロトコル間でリクエスト、レスポンス、ストリームを変換します。利用できる機能はチャネル、上流モデル、変換経路に依存し、固有のツールやフィールドを完全には変換できない場合があります。WebSocket も対応する上流とチャネル設定が必要です。

この README は現在のソースコードを説明しています。デプロイするバージョンのリリースノートも確認してください。

<a id="quick-start"></a>

## クイックスタート

### Docker でローカル実行

SQLite を使う単一インスタンスを、ローカルホスト限定で起動します。

```bash
mkdir -p data
docker run --name new-api -d --restart unless-stopped \
  -p 127.0.0.1:3000:3000 \
  -e TZ=Asia/Shanghai \
  -v "$(pwd)/data:/data" \
  calciumion/new-api:latest
```

[http://localhost:3000](http://localhost:3000) を開き、初期設定ウィザードで管理者アカウントを作成します。SQLite データベースはマウントした `data` ディレクトリに保存され、コンテナを置き換えても残ります。

### 最初のリクエスト

1. 上流の API キー、利用するモデル、グループを設定してチャネルを追加し、チャネルテストを実行します。
2. モデルの料金を設定し、ユーザーに利用可能なクォータまたは有効なサブスクリプションがあることを確認します。
3. 管理画面で、同じグループとモデルにアクセスできる API キーを作成します。
4. OpenAI 互換クライアントでは Base URL を `http://localhost:3000/v1` に設定し、**New API が発行したキー**を使います。

シェルの `NEW_API_KEY` にそのキーを設定し、アクセス可能なモデルを確認します。

```bash
curl --fail-with-body http://localhost:3000/v1/models \
  -H "Authorization: Bearer ${NEW_API_KEY}"
```

次に Responses を呼び出します。`your-enabled-model` は、このインターフェースに対応する有効なモデル名に置き換えてください。

```bash
curl --fail-with-body http://localhost:3000/v1/responses \
  -H "Authorization: Bearer ${NEW_API_KEY}" \
  -H "Content-Type: application/json" \
  -d '{"model":"your-enabled-model","input":"Hello!"}'
```

<a id="deployment"></a>

## デプロイと運用

### Docker Compose

リポジトリの [Compose 設定](./docker-compose.yml) は、既定で **New API + PostgreSQL + Redis** を起動します。MySQL と独立した ClickHouse ログデータベースの設定例も含まれます。

```bash
git clone https://github.com/QuantumNous/new-api.git
cd new-api
```

起動前に `docker-compose.yml` を編集し、データベースと Redis のサービスおよび接続文字列にあるサンプルパスワードを変更します。永続的に使うランダムな `SESSION_SECRET` も設定してください（`openssl rand -hex 32` で生成可能）。HTTPS の管理画面には `SESSION_COOKIE_SECURE=true` を設定し、公開 HTTPS オリジンを `SESSION_COOKIE_TRUSTED_URL` に正確に指定します。

```bash
docker compose up -d
docker compose logs -f new-api
```

### ストレージと設定

| コンポーネント | 選択肢 |
| --- | --- |
| メインデータベース | SQLite、MySQL ≥ 5.7.8、PostgreSQL ≥ 9.6 |
| 独立したログデータベース | `LOG_SQL_DSN` で設定。ClickHouse にも対応 |
| キャッシュ | 任意の Redis とメモリキャッシュ。ノード間でレート制限を共有する場合は Redis を共有 |
| コンテナのプラットフォーム | Linux amd64 / arm64 |

| 環境変数 | 用途 |
| --- | --- |
| `SQL_DSN` | メインデータベースへの接続。未設定なら SQLite |
| `LOG_SQL_DSN` | 任意の独立ログデータベースへの接続 |
| `REDIS_CONN_STRING` | Redis 接続文字列 |
| `SESSION_SECRET` | 永続的な認証用シークレット。全ノードで同じ値を使用 |
| `CRYPTO_SECRET` | 既定は `SESSION_SECRET`。Redis を共有するノードは同じ実効値を使用 |
| `SESSION_COOKIE_SECURE` | HTTPS の管理画面では `true`。Secure な更新用 Cookie と、更新・ログアウト時の厳格なオリジン検証を有効化 |
| `SESSION_COOKIE_TRUSTED_URL` | Secure モードでは必須。パスやワイルドカードを含まない正確な HTTPS オリジンをカンマ区切りで指定。ローカル HTTP では未設定にする |
| `TRUSTED_PROXIES` | 信頼するリバースプロキシの IP/CIDR、または `none`。実際のネットワークに合わせて明示的に設定 |

詳細は[環境変数の例](./.env.example)、[環境変数リファレンス](https://docs.newapi.ai/ja/docs/installation/config-maintenance/environment-variables)、[認証とセッションの説明](./docs/authentication.md)を参照してください。コンテナの変数は Compose の `environment` または `env_file` で渡します。`.env.example` のコピーだけではコンテナに反映されません。

本番環境では HTTPS を使い、リバースプロキシでストリーミングと WebSocket アップグレードを有効にします。データベースとマウントデータを永続化し、バックアップしてください。複数ノードはメインデータベースと認証シークレットを共有します。独立した Redis やメモリ内のレート制限はノードごとに計数します。構成別のセッション伝播については認証の説明を参照してください。

[リリース一覧](https://github.com/QuantumNous/new-api/releases)からイメージのバージョンを固定し、更新内容を読んでバックアップしてからアップグレードしてください。`latest` は公開ビルドに応じて変わります。既存環境の移行と互換性は、利用中のバージョンに応じて確認してください。

<a id="development"></a>

## 開発と拡張

バックエンドは Go と Gin、管理画面は React 19、TypeScript、Rsbuild、TanStack、Tailwind CSS 4 を使用します。フロントエンドの依存関係とスクリプトには Bun を使います。Go 言語の基準バージョンは [go.mod](./go.mod)、コンテナのビルドツールチェーンは [Dockerfile](./Dockerfile) を参照してください。

バックエンドは `web/dist` を埋め込むため、先にフロントエンドをビルドします。

```bash
# リポジトリのルート
cd web
bun install --frozen-lockfile
bun run build
cd ..
go run .
```

別のターミナルでフロントエンド開発サーバーを起動します。

```bash
cd web
bun run dev -- --port 5173
```

[http://localhost:5173](http://localhost:5173) を開きます。API リクエストはポート 3000 のバックエンドに転送されます。開発用バックエンドをコンテナで動かす場合は [docker-compose.dev.yml](./docker-compose.dev.yml) と [makefile](./makefile) の `make dev` を参照してください。

| 場所 | 役割 |
| --- | --- |
| `router/`、`middleware/`、`controller/` | HTTP ルート、アクセス検証、API ハンドラー |
| `relay/` | 上流アダプターとリクエストのルーティング |
| `service/`、`model/` | ビジネスロジックと永続化 |
| [relaykit/](./relaykit/README.md) | プロトコル DTO と変換を提供する、独立してビルド可能な Go モジュール |
| [plugins/tasks/](./plugins/tasks/) | JavaScript タスクプラグイン。作成方法とホストの境界は [Task Plugin API v1](./docs/plugin-api/v1.md) を参照 |
| `web/` | Web 管理画面。[フロントエンドの規約](./web/AGENTS.md)を参照 |
| [electron/](./electron/README.md) | デスクトップアプリのラッパーとパッケージング |

貢献前に [AGENTS.md](./AGENTS.md) を確認してください。変更に応じて、Go モジュールは `make test`、フロントエンドは `web/` で `bun run typecheck`、`bun run lint`、`bun run test`、`bun run build` を実行します。RelayKit を変更した場合は、`relaykit/` で `GOWORK=off go build ./...` も実行してください。

<a id="documentation"></a>

## ドキュメントとコミュニティ

| リソース | リンク |
| --- | --- |
| 公式ドキュメント | [ガイド](https://docs.newapi.ai/ja/docs) · [インストール](https://docs.newapi.ai/ja/docs/installation) · [API リファレンス](https://docs.newapi.ai/ja/docs/api) |
| プロジェクトの解説 | [DeepWiki](https://deepwiki.com/QuantumNous/new-api) |
| 質問と交流 | [FAQ](https://docs.newapi.ai/ja/docs/support/faq) · [コミュニティ](https://docs.newapi.ai/ja/docs/support/community-interaction) |
| 不具合と機能提案 | [GitHub Issues](https://github.com/QuantumNous/new-api/issues) |
| 脆弱性 | [セキュリティポリシー](./.github/SECURITY.md)に従って非公開で報告 |

不具合の報告には、バージョン、デプロイ方法、再現手順、機密情報を除いたログを添えてください。ドキュメント、翻訳、プロバイダー連携、対象を絞った回帰テストへの貢献も歓迎します。

---

## 🔗 関連プロジェクト

### 上流プロジェクト

| プロジェクト | 説明 |
|------|------|
| [One API](https://github.com/songquanpeng/one-api) | オリジナルプロジェクトベース |
| [Midjourney-Proxy](https://github.com/novicezk/midjourney-proxy) | Midjourneyインターフェースサポート |

### 補助ツール

| プロジェクト | 説明 |
|------|------|
| [new-api-key-tool](https://github.com/Calcium-Ion/new-api-key-tool) | キー使用量クォータ照会ツール |
| [new-api-horizon](https://github.com/Calcium-Ion/new-api-horizon) | New API高性能最適化版 |

---

## 📜 ライセンス

このプロジェクトは [GNU Affero General Public License v3.0 (AGPLv3)](./LICENSE) の下でライセンスされています。

AGPLv3 第 7 条に基づく[追加条項](./NOTICE)が適用されます。変更版では、適切な法的表示と、画面上の目立つ概要・法的情報・フッター・著作者表示の箇所に `Frontend design and development by New API contributors.` を残し、元のプロジェクト <https://github.com/QuantumNous/new-api> への可視リンクを保持する必要があります。

本プロジェクトは、[One API](https://github.com/songquanpeng/one-api)（MITライセンス）をベースに開発されたオープンソースプロジェクトです。

お客様の組織のポリシーがAGPLv3ライセンスのソフトウェアの使用を許可していない場合、またはAGPLv3のオープンソース義務を回避したい場合は、こちらまでお問い合わせください：[support@quantumnous.com](mailto:support@quantumnous.com)

著作者と依存関係の表示については [NOTICE](./NOTICE) と[サードパーティライセンス](./THIRD-PARTY-LICENSES.md)を参照してください。

---

## 🌟 スター履歴

<div align="center">

[![スター履歴チャート](https://api.star-history.com/svg?repos=Calcium-Ion/new-api&type=Date)](https://star-history.com/#Calcium-Ion/new-api&Date)

</div>

---

<div align="center">

### 💖 New APIをご利用いただきありがとうございます

このプロジェクトがあなたのお役に立てたなら、ぜひ ⭐️ スターをください！

**[公式ドキュメント](https://docs.newapi.ai/ja/docs)** • **[問題フィードバック](https://github.com/Calcium-Ion/new-api/issues)** • **[最新リリース](https://github.com/Calcium-Ion/new-api/releases)**

<sub>❤️ で構築された QuantumNous</sub>

</div>
