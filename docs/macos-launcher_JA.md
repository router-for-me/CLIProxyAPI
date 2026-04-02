# macOS ローカルランチャーパターン

このドキュメントは、CLIProxyAPI を macOS 上でローカル常駐サービスとして運用するための実用的な起動・更新パターンをまとめたものです。特定の私有スクリプトを前提にするのではなく、長期的に安定しやすい設計判断を共有することを目的としています。

## 目的

- CLIProxyAPI の実体を固定の書き込み可能ディレクトリに置く
- Finder / Launchpad / Spotlight / `/Applications` からワンクリック起動できるようにする
- サービス起動後に内蔵管理 Web UI を自動で開く
- ランチャーからローカルプロキシと対応する Web UI ウィンドウをまとめて閉じられるようにする
- コード更新、ランタイム状態、`.app` バンドル本体を分離する

## 推奨ディレクトリ構成

実際に動く本体は `.app` の外に置くのが安全です。例：

```text
~/CLIProxyAPI/
  bin/
  auths/
  logs/
  temp/
  config.yaml
```

役割の例：

- `config.yaml`: ローカル実行設定
- `auths/`: OAuth や provider 認証ファイル
- `logs/`: 実行ログ
- `bin/`: ローカルビルド成果物と補助スクリプト
- `temp/`: PID、ブラウザ profile、一時制御ファイル

## 内蔵 Web UI は `/management.html` を使う

CLIProxyAPI の管理パネルの入口は次です：

```text
http://127.0.0.1:8317/management.html
```

`/` を管理 UI の入口として扱わないでください。ルートは軽量な API 状態確認用です。

一般的には以下を満たす必要があります：

- `remote-management.secret-key` を設定する
- `remote-management.disable-control-panel` を `false` に保つ
- ローカル専用なら localhost にのみ bind する

## `.app` は薄いランチャーにする

macOS の `.app` は「本体そのもの」ではなく「本体を起動する薄いランチャー」として扱うのが安定します。

推奨設計：

- `.app` は実際の checkout ディレクトリにあるスクリプトを呼ぶだけにする
- 実バイナリ、設定、ログは固定ディレクトリ側に置く
- `.app` の中に古くなるバイナリを埋め込まない

## 起動フロー

安定しやすい起動フローは次の通りです：

1. すでに CLIProxyAPI が動いているか確認する
2. 動いていなければ、現在の shell から切り離して起動する
3. HTTP ヘルスチェックが通るまで待つ
4. `/management.html` が利用可能になるまで待つ
5. Web UI を開く

推奨チェック先：

- サービス確認：`http://127.0.0.1:8317/`
- 管理 UI 確認：`http://127.0.0.1:8317/management.html`

## Web UI は専用ブラウザ profile で開く

ランチャーから確実に「Web UI を閉じる」必要があるなら、通常の既存ブラウザタブに管理画面を混ぜない方が扱いやすいです。

たとえば Chrome なら：

```bash
open -na "Google Chrome" --args \
  --user-data-dir="$HOME/CLIProxyAPI/temp/webui-browser-profile" \
  --app="http://127.0.0.1:8317/management.html"
```

この形にすると：

- ランチャーが対象の Web UI プロセスだけを閉じられる
- 普段使っている Chrome のタブを巻き込まない
- 管理画面が軽量な専用アプリ風に扱える

## 停止フロー

ランチャーを再度クリックしたときの実用的な toggle フローは：

1. 代理サービスが動作中か確認する
2. 動作中なら、次の選択肢を出す
   - Web UI を再度開く
   - CLIProxyAPI を停止する
3. 停止を選んだ場合は
   - 専用 Web UI ブラウザ profile のプロセスを終了する
   - CLIProxyAPI のプロセスを停止する
   - 必要なら古い PID ファイルを掃除する

## 更新は `.app` の外で行う

更新処理で app bundle の中のバイナリを直接置き換えるのは避けた方が安全です。

より扱いやすい方法は：

1. 実際の git checkout を `~/CLIProxyAPI` のような固定ディレクトリに置く
2. 別の更新スクリプトで
   - リモートの branch / release を確認
   - fast-forward で更新
   - バイナリを再ビルド
   - 必要ならサービスを再起動
3. `.app` は常にその固定ディレクトリを見る

この方式は `topgrade` や `up` のような既存の更新ワークフローとも相性が良いです。

## 更新時の安全策

- git 追跡ファイルにローカル変更がある場合は自動更新をスキップする
- `git fetch` / `git ls-remote` にタイムアウトを設ける
- 一時バイナリへビルドし、確認後に本番バイナリへ置き換える
- 設定、認証、ログは git 追跡外に保つ

## まとめ

安定した macOS ローカル運用パターンは次の通りです：

- 固定 checkout ディレクトリ
- 薄い `.app` ランチャー
- `management.html` を UI 入口にする
- 専用ブラウザ profile で Web UI を精密に制御する
- 更新はユーザー既存の端末ワークフローに接続する

この設計は壊れにくく、復旧もしやすく、app bundle の中に古いバイナリを抱え込む問題も避けられます。
