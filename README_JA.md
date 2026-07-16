# CLI Proxy API

[English](README.md) | [中文](README_CN.md) | 日本語

CLI向けのOpenAI/Gemini/Claude/Codex/Grok互換APIインターフェースを提供するプロキシサーバーです。

ローカル環境や複数のCLIアカウントを通じて、OpenAI（Responses含む）、Gemini（Interactions含む）、またはClaude互換のクライアントやSDKから、以下のプロバイダーにアクセスできます。

<table>
<tbody>
    <tr>
        <th align="center" width="100">プロバイダー</th>
        <th align="center">説明</th>
    </tr>
    <tr>
        <td align="center"><a href="https://www.kimi.com/code/?aff=cliproxyapi"><img src="./assets/logo/kimi.svg" alt="Kimi" width="28" height="28" /></a></td>
        <td>Kimiシリーズモデル（Kimi K2.7 Code、Kimi K2.6など）。<a href="https://platform.kimi.ai/docs/guide/kimi-k2-7-code-quickstart">Kimi K2.7 Code</a>は、コーディングと複雑なソフトウェアエンジニアリング向けに構築されたオープンソースのエージェント型モデルで、実世界の長期間ワークフローにおけるエンドツーエンド成功率を高めます。K2.6と比較して、thinkingトークンを約30%削減します。CLIProxyAPIはOAuthまたは互換APIインターフェース経由でKimiをサポートします。<a href="https://www.kimi.com/code/?aff=cliproxyapi">Kimi Codeサブスクリプション</a>を試すか、<a href="https://platform.kimi.ai/?aff=cliproxyapi">Kimi Open Platform</a>でAPIキーを取得してください。Kimiのオープンソースコミュニティへの貢献に感謝します！</td>
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

## スポンサー

[![https://www.packyapi.com/register?aff=cliproxyapi](./assets/packycode-en.png)](https://www.packyapi.com/register?aff=cliproxyapi)

PackyCodeのスポンサーシップに感謝します！

PackyCodeは信頼性が高く効率的なAPIリレーサービスプロバイダーで、Claude Code、Codex、Geminiなどのリレーサービスを提供しています。

PackyCodeは当ソフトウェアのユーザーに特別割引を提供しています：<a href="https://www.packyapi.com/register?aff=cliproxyapi">こちらのリンク</a>から登録し、チャージ時にプロモーションコード「cliproxyapi」を入力すると10%割引になります。

---

<table>
<tbody>
<tr>
<td width="180"><a href="https://www.aicodemirror.com/register?invitecode=TJNAIF"><img src="./assets/aicodemirror.png" alt="AICodeMirror" width="150"></a></td>
<td>AICodeMirrorのスポンサーシップに感謝します！AICodeMirrorはClaude Code / Codex / Gemini向けの公式高安定性リレーサービスを提供しており、エンタープライズグレードの同時接続、迅速な請求書発行、24時間365日の専任技術サポートを備えています。Claude Code / Codex / Geminiの公式チャネルが元の価格の38% / 2% / 9%で利用でき、チャージ時にはさらに割引があります！CLIProxyAPIユーザー向けの特別特典：<a href="https://www.aicodemirror.com/register?invitecode=TJNAIF">こちらのリンク</a>から登録すると、初回チャージが20%割引になり、エンタープライズのお客様は最大25%割引を受けられます！</td>
</tr>
<tr>
<td width="180"><a href="https://shop.bmoplus.com/?utm_source=github"><img src="./assets/bmoplus.png" alt="BmoPlus" width="150"></a></td>
<td>本プロジェクトにご支援いただいた BmoPlus に感謝いたします！BmoPlusは、AIサブスクリプションのヘビーユーザー向けに特化した信頼性の高いAIアカウントサービスプロバイダーであり、安定した ChatGPT Plus / ChatGPT Pro (完全保証) / Claude Pro / Super Grok / Gemini Pro の公式代行チャージおよび即納アカウントを提供しています。こちらの<a href="https://shop.bmoplus.com/?utm_source=github">BmoPlus AIアカウント専門店/代行チャージ</a>経由でご登録・ご注文いただいたユーザー様は、GPTを <b>公式サイト価格の約1割（90% OFF）</b> という驚異的な価格でご利用いただけます！</td>
</tr>
<tr>
<td width="180"><a href="https://visioncoder.cn/"><img src="./assets/visioncoder.png" alt="VisionCoder" width="150"></a></td>
<td>VisionCoderのご支援に感謝します。<a href="https://visioncoder.cn/">VisionCoder 開発プラットフォーム</a> は、信頼性が高く効率的なAPIリレーサービスプロバイダーで、Claude Code、Codex、Geminiなどの主要AIモデルを提供し、開発者やチームがより簡単にAI機能を統合して生産性を向上できるよう支援します。さらに、VisionCoderは <b>Claude Max 200 と GPT Pro 200 高級即納アカウント</b> の独占販売チャネルを提供しており、最高クラスのAI算力と体験を手軽に体験できます。</td>
</tr>
<tr>
<td width="180"><a href="https://apikey.fun/register?aff=CLIProxyAPI"><img src="./assets/apikey.png" alt="APIKEY.FUN" width="150"></a></td>
<td>APIKEY.FUNのスポンサーシップに感謝します！APIKEY.FUNはプロフェッショナルなエンタープライズ向けAIリレーサービスで、企業および個人開発者に安定・高効率・低コストなAIモデルAPI接続サービスを提供しています。Claude、OpenAI、Geminiなどの主要人気モデルに対応し、価格は公式価格の7%から利用できます。本プロジェクトの<a href="https://apikey.fun/register?aff=CLIProxyAPI">専用リンク</a>から登録すると、さらに<b>チャージが永続的に5%割引</b>となる特別優待を受けられます。</td>
</tr>
<tr>
<td width="180"><a href="https://runapi.co/register?aff=FivD"><img src="./assets/runapi.png" alt="RunAPI" width="150"></a></td>
<td>RunAPIは高効率で安定したAPIプラットフォームで、OpenRouterの代替として利用できます。1つのAPI KeyでOpenAI、Claude、Gemini、DeepSeek、Grokなど150以上の主要モデルにアクセスでき、価格は公式価格の10%から、非常に安定しており、Claude Code、OpenClawなどのツールとシームレスに互換性があります。RunAPIはCPAユーザー向けに特別特典を提供しています：<a href="https://runapi.co/register?aff=FivD">登録</a>後に管理者へ連絡すると、7元分の無料クレジットを受け取れます。</td>
</tr>
<tr>
<td width="180"><a href="https://catapi.ai/sign-up"><img src="./assets/catapi.png" alt="CatAPI" width="150"></a></td>
<td>Cat APIは、個人開発者やチーム向けのAI大規模モデル集約プラットフォームです。主要な大規模モデルの機能を、シンプルで安定した使いやすい入口に統合することを目指しています。OpenAI、Claude、Geminiと完全互換のAPIを提供し、Claude Code、Cursor、Windsurf、Cline、Roo Code、Continue、Codex、Traeなどの主要なAI IDEやプログラミングツールへシームレスに接続できます。また、CN2高速回線を主な特徴としており、低遅延で高安定なアクセス体験を提供します。<a href="https://catapi.ai/sign-up">登録</a>すると、1$の無料クレジットを受け取れます。</td>
</tr>
<tr>
<td width="180"><a href="https://t.me/CyberWlD/218"><img src="./assets/cyberpay.jpg" alt="CyberPay" width="150"></a></td>
<td>CyberPay（サイバー決済）は2021年に設立されました。AI業界の事業者向けに、安定・高効率・安全な決済精算ソリューションを提供することに取り組んでいます。私たちと連携することで、WebサイトやプラットフォームでのAlipay/WeChat決済の受け取り課題を解決できます。GPT、Gemini、Claude、Codexアカウントやリレープラットフォームなど、各種事業提携にも対応し、事業者の決済回収に関する課題を解決します。<a href="https://t.me/CyberWlD/218">お問い合わせ</a>ください。</td>
</tr>
<tr>
<td width="180"><a href="https://console.claudeapi.com/agent/register/pJq9T52Fpugrhpgo"><img src="./assets/claudeapi.png" alt="ClaudeAPI" width="150"></a></td>
<td>本プロジェクトは <a href="https://console.claudeapi.com/agent/register/pJq9T52Fpugrhpgo">Claude API</a> にご支援いただいています！Claude API は Claude モデルに特化した公式チャネルの API プロバイダーです。Anthropic 公式 Key と AWS Bedrock の公式チャネルを基盤に、Claude Code と Agent アプリケーション向けに安定した接続体験を提供します。Claude 全シリーズのモデルに対応し、Tool Use や長いコンテキストなどの公式機能も維持されています。リバースエンジニアリングではなく、モデル性能のダウングレードもありません。Claude Code のヘビーユーザー、Agent エンジニア、企業の技術チームに適しています。<a href="https://console.claudeapi.com/agent/register/pJq9T52Fpugrhpgo">専用リンク</a> から登録後、カスタマーサポートに連絡すると無料テストクレジットを受け取れます。請求書発行やチーム導入の相談にも対応しています。</td>
</tr>
<tr>
<td width="180"><a href="https://code0.ai/agent/register/slxVMR3uVBoRgNBf"><img src="./assets/code0.png" alt="code0" width="150"></a></td>
<td>本プロジェクトは <a href="https://code0.ai/agent/register/slxVMR3uVBoRgNBf">Code0</a> にご支援いただいています！code0.ai は、開発者と技術チーム向けの AI コーディングワークスペースです。Claude Code や Codex などの主要な Agent 型コーディング機能を統合し、コード生成、プロジェクト理解、デバッグ、コードレビュー、ドキュメント作成など、日常的な開発シーンをサポートします。個人開発者、Agent エンジニア、オープンソースメンテナー、企業の開発チームに適しており、請求書発行やチーム導入にも対応しています。<a href="https://code0.ai/agent/register/slxVMR3uVBoRgNBf">専用リンク</a> から登録後、カスタマーサポートに連絡すると無料テストクレジットを受け取れます。より効率的な AI コーディングワークフローをぜひ体験してください。</td>
</tr>
<tr>
<td width="180"><a href="https://api.fenno.ai/register?redirect=/purchase?tab=subscription%26group=16&amp;aff=DQFAMNB6CBLY"><img src="./assets/fennoai.png" alt="FennoAI" width="150"></a></td>
<td>本プロジェクトは <a href="https://api.fenno.ai/register?redirect=/purchase?tab=subscription%26group=16&amp;aff=DQFAMNB6CBLY">Fenno.ai</a> にご支援いただいています！Fenno.ai は安定した高効率な API リレーサービスプロバイダーで、現在は主に Codex リレーサービスを提供しています。OpenAI および Anthropic プロトコルに対応し、Codex、Claude Code、OpenCode などの主要なコーディングツールへ柔軟に接続できます。1日あたり数千億 token 規模のエンタープライズ利用を安定して支え、国内および海外法人向けのB2B決済と請求書発行にも対応しています。Fenno.ai は CLIProxyAPI ユーザー向けの特典として、<a href="https://api.fenno.ai/register?redirect=/purchase?tab=subscription%26group=16&amp;aff=DQFAMNB6CBLY">こちらのリンク</a>から <b>9.9元 / 150ドル分のクォータ</b> のお得な Coding Plan を購読でき、友人招待では最大20%の報酬を受け取れます。</td>
</tr>
<tr>
<td width="180"><a href="https://s.qiniu.com/7zUJri"><img src="./assets/qiniucloud.png" alt="Qiniu Cloud AI" width="150"></a></td>
<td>本プロジェクトは <a href="https://s.qiniu.com/7zUJri">七牛雲AI</a> にご支援いただいています！七牛雲AI は七牛雲（02567.HK）傘下のエンタープライズ向け大規模モデル MaaS プラットフォームです。世界の主要モデル150以上をワンストップで呼び出せ、世界の主要モデルプロバイダーのプロトコルに対応し、テキスト、画像、音声、動画、ファイル処理などのフルモーダル処理能力をカバーしています。169万を超える企業および開発者ユーザーにサービスを提供しています。専用特典：企業ユーザーは <b>1,200万 Token</b> を無料で受け取れ、友人招待で最大100億 Tokenを獲得できます。</td>
</tr>
<tr>
<td width="180"><a href="https://cubence.com/signup?code=CLIPROXYAPI&source=cpa"><img src="./assets/cubence.png" alt="Cubence" width="150"></a></td>
<td>Cubenceのスポンサーシップに感謝します！Cubenceは信頼性が高く効率的なAPIリレーサービスプロバイダーで、Claude Code、Codex、Geminiなどのリレーサービスを提供しています。Cubenceは当ソフトウェアのユーザーに特別割引を提供しています：<a href="https://cubence.com/signup?code=CLIPROXYAPI&source=cpa">こちらのリンク</a>から登録し、チャージ時にプロモーションコード「CLIPROXYAPI」を入力すると10%割引になります。</td>
</tr>
<tr>
<td width="180"><a href="https://www.fastaitoken.com/"><img src="./assets/fastaitoken.png" alt="FastAIToken" width="150"></a></td>
<td><a href="https://www.fastaitoken.com/">FastAIToken</a> のスポンサーシップに感謝します！FastAIToken は開発者向けの AI API 集約プラットフォームで、速度と安定性を重視しています。OpenAI、Claude、Gemini などの主要 AI モデルに対応し、チャージ比率は 1:1（1元 = 1ドル分の API クレジット）のため、開発者はより低コストで便利に世界トップクラスの AI モデルを利用できます。<a href="https://t.me/+stwq0MLi0PtkZTZl">Telegram サポートグループ</a><br/>プラットフォームでは用途に応じて複数のチャネルを選択できます：超低価格の 0.02× OpenAI プロモーション枠（期間限定）、0.25× からの OpenAI チャネル、95% 固定キャッシュの 0.7× Claude、1.2× Claude Max チャネル。また、各チャネルの稼働率、遅延、運用状況をリアルタイム表示する公開ステータスページも提供しており、透明で信頼性の高いサービスを実現しています。さらに FastAIToken は 24時間365日の真人テクニカルサポート（ボットではありません）を提供し、開発者のニーズに迅速に対応します。エンタープライズ顧客向けには、安定性を保証する SLA 対応の専用チャネルプールを提供し、契約対応、請求書発行、専任保守にも対応しています。</td>
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

### [CPA-Tray-Powershell](https://github.com/IQ-Director/CPA-Tray-Powershell)

PowerShellベースのWindows向けCLIProxyAPIシステムトレイランチャー。コンソールウィンドウを表示せずにバックグラウンドで実行し、管理ページを開き、管理ウィンドウを閉じた後もバックエンドを維持してトレイからページを再表示できます。起動時のCLIProxyAPI更新確認、SHA-256検証と失敗時のロールバック、ワンクリックでのCLIProxyAPI再起動と更新、PID検証に基づくプロセス管理、安全なサービス停止にも対応しています。

### [Grok Search MCP](https://github.com/MapleMapleCat/Grok_Search_Mcp)

HTTP専用のModel Context Protocol（MCP）サーバーです。CLIProxyAPIのデプロイメントを利用して、MCPクライアントにGrokを活用したリアルタイムWeb検索、X/Twitter検索、モデル検出を提供します。MCPトランスポート、クライアントAPIキー管理、クォータ、使用量追跡、Web管理パネルも備えています。

> [!NOTE]
> CLIProxyAPIをベースにプロジェクトを開発した場合は、PRを送ってこのリストに追加してください。

## その他の選択肢

以下のプロジェクトはCLIProxyAPIの移植版またはそれに触発されたものです：

### [9Router](https://github.com/decolua/9router)

CLIProxyAPIに触発されたNext.js実装。インストールと使用が簡単で、フォーマット変換（OpenAI/Claude/Gemini/Ollama）、自動フォールバック付きコンボシステム、指数バックオフ付きマルチアカウント管理、Next.js Webダッシュボード、CLIツール（Cursor、Claude Code、Cline、RooCode）のサポートをゼロから構築 - APIキー不要

### [OmniRoute](https://github.com/diegosouzapw/OmniRoute)

コーディングを止めない。無料および低コストのAIモデルへのスマートルーティングと自動フォールバック。

OmniRouteはマルチプロバイダーLLM向けのAIゲートウェイです：スマートルーティング、負荷分散、リトライ、フォールバックを備えたOpenAI互換エンドポイント。ポリシー、レート制限、キャッシュ、可観測性を追加して、信頼性が高くコストを意識した推論を実現します。

### [Playful Proxy API Panel (PPAP)](https://github.com/daishuge/playful-proxy-api-panel)

上流に近い使い方を維持する公開CLIProxyAPI互換フォーク兼管理パネルです。内蔵の使用量統計を復元し、キャッシュヒット率、初回バイト待ち時間、TPSの記録、Docker向けのセルフホスト手順を追加しています。

### [Codex Switch](https://github.com/9ycrooked/CodexSwitch)

Tauri 2 + Vue 3で構築された、複数のOpenAI Codexデスクトップアカウントを管理するためのツールです。保存済みのChatGPT/Codex認証プロファイルを切り替え、5時間および週次クォータ使用量をリアルタイムで確認し、tokenの状態を検証し、現在のアカウント詳細を表示し、手動コピーなしでauth.jsonファイルをインポートまたは保存できます。

> [!NOTE]
> CLIProxyAPIの移植版またはそれに触発されたプロジェクトを開発した場合は、PRを送ってこのリストに追加してください。

## ライセンス

本プロジェクトはMITライセンスの下でライセンスされています - 詳細は[LICENSE](LICENSE)ファイルを参照してください。
