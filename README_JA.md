# CLI Proxy API

[English](README.md) | [中文](README_CN.md) | 日本語

デスクトップで CLIProxyAPI を利用したい場合は、[EasyCLIProxyAPI](https://github.com/router-for-me/EasyCLIProxyAPI) デスクトップクライアントをおすすめします。グラフィカルな設定画面、自動更新、システムトレイ連携、CLIProxyAPI サービスのワンクリック起動/停止などの機能を提供します。

CLIProxyAPI は、CLI向けのOpenAI/Gemini/Claude/Codex/Grok互換APIインターフェースを提供するプロキシサーバーです。

ローカル環境や複数のCLIアカウントを通じて、OpenAI（Responses含む）、Gemini（Interactions含む）、またはClaude互換のクライアントやSDKから、以下のプロバイダーにアクセスできます。

<table>
<tbody>
    <tr>
        <th align="center" width="100">プロバイダー</th>
        <th align="center">説明</th>
    </tr>
    <tr>
        <td align="center"><a href="https://www.kimi.com/code/"><img src="./assets/logo/kimi.svg" alt="Kimi" width="28" height="28" /></a></td>
        <td>Kimiシリーズモデル（Kimi K3、Kimi K2.7 Codeなど）。<a href="https://platform.kimi.ai/docs/guide/kimi-k3-quickstart">Kimi K3</a>は、Moonshot AIで最も高性能なモデルであり、世界初のオープンな3兆パラメータ級モデルです。2.8兆のパラメータ、ネイティブな視覚機能、100万トークンのコンテキストウィンドウを備え、長期間にわたるコーディング、知識作業、推論向けに構築されています。CLIProxyAPIはOAuthまたは互換APIインターフェース経由でKimiをサポートします。<strong>Kimi Code プラン</strong>（<a href="https://www.kimi.com/code">中文站</a> | <a href="https://www.kimi.ai/code">Global</a>）を試すか、Kimi Open Platform（<a href="https://platform.kimi.com">中文站</a> | <a href="https://platform.kimi.ai">Global</a>）で<strong>APIキー</strong>を取得してください。CLIProxyAPIとオープンソースコミュニティを支援してくださるKimiに感謝します！</td>
    </tr>
    <tr>
        <td align="center"><a href="https://platform.openai.com/docs/guide/gpt-5.6"><img src="./assets/logo/openai.svg" alt="OpenAI" width="28" height="28" /></a></td>
        <td>OpenAI GPTシリーズモデル（GPT 5.6、GPT 5.5など）。GPT-5.6は、複雑な本番ワークフロー向けに新しい品質と効率の基準を打ち立てます。GPT-5.6は特にトークン効率が高く、レイアウト、視覚的階層、デザイン判断を含むフロントエンドの美的品質も向上しています。</td>
    </tr>
    <tr>
        <td align="center"><a href="https://www.anthropic.com/claude"><img src="./assets/logo/claude.svg" alt="Anthropic" width="28" height="28" /></a></td>
        <td>Anthropic Claudeシリーズモデル（Claude Fable、Claude Opus、Claude Sonnetなど）。Claude Fable 5は、Anthropicが広く公開している中で最も高性能なモデルであり、最も要求の厳しい推論と長期間のエージェント作業向けに構築されています。</td>
    </tr>
    <tr>
        <td align="center"><a href="https://antigravity.google/"><img src="./assets/logo/antigravity.svg" alt="Antigravity" width="28" height="28" /></a></td>
        <td>Google Geminiシリーズモデル（Gemini 3.5 Flash、Gemini 3.1 Proなど）。Gemini 3.5 Flashは、実世界タスク向けに最適化された持続的なフロンティア級の知能を、より高速かつ低コストで提供します。エージェント時代向けに設計されており、サブエージェント展開、多段階ワークフロー、大規模な長期間タスクに優れています。このモデルは、複雑なコーディングサイクルと反復を含む迅速なエージェントループに特に効果的です。</td>
    </tr>
    <tr>
        <td align="center"><a href="https://x.ai/grok"><img src="./assets/logo/xai.svg" alt="xAI" width="28" height="28" /></a></td>
        <td>xAI Grokシリーズモデル（Grok 4.5、Grok Composer 2.5 Fastなど）。Grok 4.5は、コーディング、エージェントタスク、知識作業向けに構築されたSpaceXAIのフロンティアモデルです。科学、工学、数学にわたる新しいデータセットを用いて、SpaceXAIのメンフィスにあるデータセンターで訓練されました。</td>
    </tr>
</tbody>
</table>

## 概要

- CLIモデル向けのOpenAI/Gemini/Claude/Grok互換APIエンドポイント
- OAuthログインによるOpenAI Codexサポート（GPTモデル）
- OAuthログインによるClaude Codeサポート
- OAuthログインによるGrok Buildサポート
- ストリーミング、非ストリーミング、および対応環境でのWebSocketレスポンス
- 関数呼び出し/ツールのサポート
- マルチモーダル入力サポート（テキストと画像）
- ラウンドロビン負荷分散による複数アカウント対応（Gemini、OpenAI、Claude、Grok）
- シンプルなCLI認証フロー（Gemini、OpenAI、Claude、Grok）
- Generative Language APIキーのサポート
- AI Studioビルドのマルチアカウント負荷分散
- Claude Codeのマルチアカウント負荷分散
- OpenAI Codexのマルチアカウント負荷分散
- Grok Buildのマルチアカウント負荷分散
- 設定によるOpenAI互換アップストリームプロバイダー（例：OpenRouter）
- プロキシ埋め込み用の再利用可能なGo SDK（`docs/sdk-usage.md`を参照）

## はじめに

CLIProxyAPIガイド：[https://help.router-for.me/](https://help.router-for.me/)

## 管理API

[MANAGEMENT_API.md](https://help.router-for.me/management/api)を参照

## 使用量統計

v6.10.0以降、CLIProxyAPIおよび [CPAMC](https://github.com/router-for-me/Cli-Proxy-API-Management-Center) プロジェクトには使用量統計機能がプリセットされなくなりました。使用量統計が必要な場合は、次のプロジェクトをご利用ください：

### [CPA Usage Keeper](https://github.com/Willxup/cpa-usage-keeper)

CLIProxyAPI向けの独立した使用量永続化・可視化サービス。CLIProxyAPIデータを定期同期してSQLiteに保存し、集計APIと、使用量や各種統計を確認できる組み込みダッシュボードを提供します。

### [CPA-Manager-Plus](https://github.com/seakee/CPA-Manager-Plus)

リクエスト単位の監視とコスト推定を備えたCLIProxyAPI向けのフル管理センターです。CPA-Managerは、収集したリクエストをアカウント、モデル、チャネル、レイテンシ、ステータス、Token使用量ごとに追跡し、編集可能なモデル価格とLiteLLM価格のワンクリック同期でコストを推定します。SQLiteでイベントを永続化し、Codexアカウントプール向けに一括検査、クォータ判定、異常アカウント検出、クリーンアップ提案、ワンクリック実行を提供し、日常的なマルチアカウント運用に適しています。

## SDKドキュメント

- 使い方：[docs/sdk-usage.md](docs/sdk-usage.md)
- 上級（エグゼキューターとトランスレーター）：[docs/sdk-advanced.md](docs/sdk-advanced.md)
- アクセス：[docs/sdk-access.md](docs/sdk-access.md)
- ウォッチャー：[docs/sdk-watcher.md](docs/sdk-watcher.md)
- カスタムプロバイダーの例：`examples/custom-provider`

## コントリビューション

コントリビューションを歓迎します！お気軽にPull Requestを送ってください。

1. リポジトリをフォーク
2. フィーチャーブランチを作成（`git checkout -b feature/amazing-feature`）
3. 変更をコミット（`git commit -m 'Add some amazing feature'`）
4. ブランチにプッシュ（`git push origin feature/amazing-feature`）
5. Pull Requestを作成

## 関連プロジェクト

CLIProxyAPIをベースにした以下のプロジェクトがあります：

### [vibeproxy](https://github.com/automazeio/vibeproxy)

macOSネイティブのメニューバーアプリで、Claude CodeとChatGPTのサブスクリプションをAIコーディングツールで使用可能 - APIキー不要

### [Subtitle Translator](https://github.com/VjayC/SRT-Subtitle-Translator-Validator)

CLIProxyAPI経由で既存のLLMサブスクリプション（Gemini、ChatGPT、Claude, etc.）を使用してSRT字幕を翻訳および検証する、クロスプラットフォームのデスクトップおよびWebアプリ - APIキー不要。

### [CCS (Claude Code Switch)](https://github.com/kaitranntt/ccs)

CLIProxyAPI OAuthを使用して複数のClaudeアカウントや代替モデル（Gemini、Codex、Antigravity）を即座に切り替えるCLIラッパー - APIキー不要

### [Quotio](https://github.com/nguyenphutrong/quotio)

Claude、Gemini、OpenAI、Antigravityのサブスクリプションを統合し、リアルタイムのクォータ追跡とスマート自動フェイルオーバーを備えたmacOSネイティブのメニューバーアプリ。Claude Code、OpenCode、Droidなどのコーディングツール向け - APIキー不要

### [ProxyPilot](https://github.com/Finesssee/ProxyPilot)

TUI、システムトレイ、マルチプロバイダーOAuthを備えたWindows向けCLIProxyAPIフォーク - AIコーディングツール用、APIキー不要

### [Claude Proxy VSCode](https://github.com/uzhao/claude-proxy-vscode)

Claude Codeモデルを素早く切り替えるVSCode拡張機能。バックエンドとしてCLIProxyAPIを統合し、バックグラウンドでの自動ライフサイクル管理を搭載

### [ZeroLimit](https://github.com/0xtbug/zero-limit)

CLIProxyAPIを使用してAIコーディングアシスタントのクォータを監視するTauri + React製のWindowsデスクトップアプリ。Gemini、Claude、OpenAI Codex、Antigravityアカウントの使用量をリアルタイムダッシュボード、システムトレイ統合、ワンクリックプロキシコントロールで追跡 - APIキー不要

### [CPA-XXX Panel](https://github.com/ferretgeek/CPA-X)

CLIProxyAPI向けの軽量Web管理パネル。ヘルスチェック、リソース監視、リアルタイムログ、自動更新、リクエスト統計、料金表示機能を搭載。ワンクリックインストールとsystemdサービスに対応

### [CLIProxyAPI Tray](https://github.com/kitephp/CLIProxyAPI_Tray)

PowerShellスクリプトで実装されたWindowsトレイアプリケーション。サードパーティライブラリに依存せず、ショートカットの自動作成、サイレント実行、パスワード管理、チャネル切り替え（Main / Plus）、自動ダウンロードおよび自動更新に対応

### [霖君](https://github.com/wangdabaoqq/LinJun)

霖君はAIプログラミングアシスタントを管理するクロスプラットフォームデスクトップアプリケーションで、macOS、Windows、Linuxシステムに対応。Claude Code、Gemini、OpenAI Codexなどのコーディングツールを統合管理し、ローカルプロキシによるマルチアカウントクォータ追跡とワンクリック設定が可能

### [CLIProxyAPI Dashboard](https://github.com/itsmylife44/cliproxyapi-dashboard)

Next.js、React、PostgreSQLで構築されたCLIProxyAPI用のモダンなWebベース管理ダッシュボード。リアルタイムログストリーミング、構造化された設定編集、APIキー管理、Claude/Gemini/Codex向けOAuthプロバイダー統合、使用量分析、コンテナ管理、コンパニオンプラグインによるOpenCodeとの設定同期機能を搭載 - 手動でのYAML編集は不要

### [All API Hub](https://github.com/qixing-jk/all-api-hub)

New API互換リレーサイトアカウントをワンストップで管理するブラウザ拡張機能。残高と使用量のダッシュボード、自動チェックイン、一般的なアプリへのワンクリックキーエクスポート、ページ内API可用性テスト、チャネル/モデルの同期とリダイレクト機能を搭載。Management APIを通じてCLIProxyAPIと統合し、ワンクリックでプロバイダーのインポートと設定同期が可能

### [Shadow AI](https://github.com/HEUDavid/shadow-ai)

Shadow AIは制限された環境向けに特別に設計されたAIアシスタントツールです。ウィンドウや痕跡のないステルス動作モードを提供し、LAN（ローカルエリアネットワーク）を介したクロスデバイスAI質疑応答のインタラクションと制御を可能にします。本質的には「画面/音声キャプチャ + AI推論 + 低摩擦デリバリー」の自動化コラボレーションレイヤーであり、制御されたデバイスや制限された環境でアプリケーション横断的にAIアシスタントを没入的に使用できるようユーザーを支援します。

### [ProxyPal](https://github.com/buddingnewinsights/proxypal)

CLIProxyAPIをネイティブGUIでラップしたクロスプラットフォームデスクトップアプリ（macOS、Windows、Linux）。Claude、ChatGPT、Gemini、GitHub Copilot、カスタムOpenAI互換エンドポイントに対応し、使用状況分析、リクエスト監視、人気コーディングツールの自動設定機能を搭載 - APIキー不要

### [CLIProxyAPI Quota Inspector](https://github.com/AllenReder/CLIProxyAPI-Quota-Inspector)

CLIProxyAPI向けのすぐに使えるクロスプラットフォームのクォータ確認ツール。アカウントごとの codex 5h/7d クォータ表示、プラン別ソート、ステータス色分け、複数アカウントの集計分析に対応。

### [CLIProxy Pool Watch](https://github.com/murasame612/CLIProxyPoolWidget)

CLIProxyAPIプール内のChatGPT/Codexアカウントクォータを監視するmacOSネイティブSwiftUIアプリ。Management APIを通じて、アカウントの可用性、Plus基準の容量、5時間/週次クォータバー、プラン重み、復元予測を表示します。

### [Panopticon](https://github.com/eltmon/panopticon-cli)

AIコーディングアシスタント向けのマルチエージェントオーケストレーションツール。CLIProxyAPIをローカルsidecarとして実行することで、エージェントがChatGPTサブスクリプション経由でGPTモデルを利用できるようにし、Claude CodeをAnthropic互換エンドポイントへ向けるため、OpenAI APIキーは不要です。

### [Tunnel Agent](https://github.com/Villoh/tunnel-agent)

CLIProxyAPIとPerplexity WebUI Scraperをひとつのインターフェースで管理するWindowsデスクトップUI。QuotioとVibeProxyにインスパイアされ、OAuthプロバイダー（Claude、Gemini、Codex、Kimi、Antigravity）、カスタムAPIキー、Perplexityセッションアカウントを接続し、任意のコーディングエージェントをローカルエンドポイントに向けることができます。

### [Quotio Desktop](https://github.com/xiaocoss/quotio-desktop)

Quotio のクロスプラットフォーム（Tauri）移植版（Windows / macOS / Linux 対応）。CLIProxyAPI 経由で複数の AI アカウント（Codex、Claude Code、GitHub Copilot、Gemini、Antigravity、Kiro、Cursor、Trae、GLM）のプールを管理し、アカウントごとの 5 時間 / 週間クォータバー、Codex のリセットクレジットとワンクリックリセット、スマートスケジューリング、使用統計、Codex マルチインスタンスに対応。API キー不要。

### [Universal Chat Provider](https://github.com/maxdewald/vscode-universal-chat-provider)

Claude、ChatGPT/Codex、Antigravity、Grok、Kimi のサブスクリプションを GitHub Copilot Chat のネイティブ言語モデルとして利用できる VS Code 拡張機能です。Git のコミットメッセージ、チャットタイトル、要約の生成にも使えます。CLIProxyAPI を完全管理されたバックグラウンドライフサイクル（ダウンロード、検証、監視）で実行し、すべてのウィンドウで共有するため、セットアップは不要です。API キーは不要で、OAuth だけで利用できます。

### [CPA-Tray-Powershell](https://github.com/ztzpro/CPA-Tray-Powershell.git)

PowerShellベースのWindows向けCLIProxyAPIシステムトレイランチャー。コンソールウィンドウを表示せずにバックグラウンドで実行し、管理ページを開き、管理ウィンドウを閉じた後もバックエンドを維持してトレイからページを再表示できます。起動時のCLIProxyAPI更新確認、SHA-256検証と失敗時のロールバック、ワンクリックでのCLIProxyAPI再起動と更新、PID検証に基づくプロセス管理、安全なサービス停止にも対応しています。

### [Grok Search MCP](https://github.com/MapleMapleCat/Grok_Search_Mcp)

HTTP専用のModel Context Protocol（MCP）サーバーです。CLIProxyAPIのデプロイメントを利用して、MCPクライアントにGrokを活用したリアルタイムWeb検索、X/Twitter検索、モデル検出を提供します。MCPトランスポート、クライアントAPIキー管理、クォータ、使用量追跡、Web管理パネルも備えています。

### [AIUsage](https://github.com/sylearn/AIUsage)

macOSネイティブのSwiftUI製AIサブスクリプションダッシュボード兼コーディングプロキシ管理アプリ。公式CLIProxyAPIリリースのダウンロード、検証、起動・監視、更新、ロールバックをアプリ内で管理し、OAuthアカウントとライブモデルを統合します。1つのゲートウェイをCodex、Claude Code/Science、OpenCode、OpenAI/Anthropic/Geminiクライアントへ接続でき、LANアクセスにも対応します。

### [Claude Dialects](https://github.com/stefandevo/claude-dialects)

ネイティブ同様の操作感を持つ複数のClaude Codeコマンドを実行し、それぞれを異なるモデル（Codex、GLM、Kimi、Gemini、Grok、MiniMax、DeepSeek、Cursor、Copilot、Claude）で動作させます。各コマンドは、独立した設定、履歴、ポート、およびGo SDKで連携した組み込みCLIProxyAPIインスタンスを備えた本物のClaude Codeインターフェースを起動するため、プロキシを別途インストールする必要はありません。macOSのみ対応。詳細は[claude-dialects.cc](https://claude-dialects.cc/)をご覧ください。

### [WebBrain](https://github.com/webbrain-one/webbrain)

CLIProxyAPI のローカル OpenAI 互換エンドポイントをモデルプロバイダーとして利用できるブラウザーエージェントです。EasyCLIProxyAPI を通じて CLIProxyAPI を使用する場合は、WebBrain の独立した[セットアップ、セキュリティ、アカウントリスクのガイド](https://webbrain.one/docs/easy-cli-proxy/)をご覧ください。

### [Infinitus](https://github.com/deathemperor/infinitus)

CLIProxyAPI の Management API 経由で複数の Claude アカウントを管理するネイティブ macOS メニューバーアプリ（claude-swap と 9Router にも対応）。5 時間 / 7 日 / モデル別のクォータゲージ、ポップアップからの切り替え / 保留 / スター、現在のペースから各ウィンドウが尽きる時刻を予測する機能に加え、iPhone からも同じ状態を確認できます。

### [PiCloud](https://github.com/cookerpapa/pi-cloud)

Pi SDK をベースにしたセルフホスト型コーディングエージェント基盤。Web UI、並列サブエージェント、CubeSandbox KVM ワークスペースを備えています。CLIProxyAPI をモデルプロバイダーへのゲートウェイとして利用し、プロバイダーの認証情報をゲストのワークスペースから分離します。

### [cc-status-line](https://github.com/kinka/cc-status-line)

Claude Code のステータスライン。現在の CPA インスタンスに対応する Codex / Grok / Antigravity / Claude のアカウント別クォータ（5h / 7d / 週）とリセットまでの時間を表示します。`ANTHROPIC_BASE_URL` でインスタンスを選び、Management API 経由でクォータを取得します。

### [CLIProxy Quota Tray](https://github.com/ZYHUO/CLIProxy-Quota-Tray)

CLIProxyAPI 向けのクロスプラットフォームな Electron トレイダッシュボード。ChatGPT/Codex、Claude、Gemini/Antigravity、Grok、Kimi、Cursor の各アカウントにおける実際の OAuth クォータウィンドウを表示し、利用キューコストを試算し、OpenAI/Claude のサービスステータスを追跡します。ダークなターミナル風 UI。Windows および Linux で動作します。

> [!NOTE]
> CLIProxyAPIをベースにプロジェクトを開発した場合は、PRを送ってこのリストに追加してください。

## その他の選択肢

以下のプロジェクトはCLIProxyAPIの移植版またはそれに触発されたものです：

### [9Router](https://github.com/decolua/9router)

CLIProxyAPIに触発されたNext.js実装。インストールと使用が簡単で、フォーマット変換（OpenAI/Claude/Gemini/Ollama）、自動フォールバック付きコンボシステム、指数バックオフ付きマルチアカウント管理、Next.js Webダッシュボード、CLIツール（Cursor、Claude Code、Cline、RooCode）のサポートをゼロから構築 - APIキー不要

### [OmniRoute](https://github.com/diegosouzapw/OmniRoute)

コーディングを止めない。無料および低コストのAIモデルへのスマートルーティングと自動フォールバック。

OmniRouteはマルチプロバイダーLLM向けのAIゲートウェイです：スマートルーティング、負荷分散、リトライ、フォールバックを備えたOpenAI互換エンドポイント。ポリシー、レート制限、キャッシュ、可観測性を追加して、信頼性が高くコストを意識した推論を実現します。

### [Codex Switch](https://github.com/9ycrooked/CodexSwitch)

Tauri 2 + Vue 3で構築された、複数のOpenAI Codexデスクトップアカウントを管理するためのツールです。保存済みのChatGPT/Codex認証プロファイルを切り替え、5時間および週次クォータ使用量をリアルタイムで確認し、tokenの状態を検証し、現在のアカウント詳細を表示し、手動コピーなしでauth.jsonファイルをインポートまたは保存できます。

> [!NOTE]
> CLIProxyAPIの移植版またはそれに触発されたプロジェクトを開発した場合は、PRを送ってこのリストに追加してください。

## ライセンス

本プロジェクトはMITライセンスの下でライセンスされています - 詳細は[LICENSE](LICENSE)ファイルを参照してください。
