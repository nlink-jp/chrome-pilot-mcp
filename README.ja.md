# chrome-pilot-mcp

設計の背景は [RFP](docs/ja/chrome-pilot-mcp-rfp.ja.md) と
[ADR](docs/ja/adr/) を参照してください。

[English](README.md)

依存ゼロの Chrome 自動化 MCP サーバー。Google の
[chrome-devtools-mcp](https://github.com/ChromeDevTools/chrome-devtools-mcp)
のコア自動化機能を、Chrome DevTools Protocol (CDP) を直接話す Go 単一
バイナリとして再実装します — npm も npx も puppeteer も、外部 Go module
すら使いません。

## なぜ作るか

サプライチェーンリスクへの対処です。上流サーバーは npx で実行され、npm の
巨大な推移的依存ツリーを持ち、実行時にブラウザバイナリをダウンロードします。
chrome-pilot-mcp はそれが許容できない環境のために作られています:

- **単一静的バイナリ** — インストール時も実行時も外部から何も取得しない
- **Go module 依存ゼロ** — `go.mod` に `require` なし。WebSocket クライアント
  (RFC 6455、localhost・平文限定) は自前実装
- **インストール済み Chrome を操縦** — 専用プロファイル + `127.0.0.1` bind で
  起動、または既存の debugging endpoint にアタッチ

## ツール一覧 (27)

| カテゴリ | ツール |
|---|---|
| ページ操作 (6) | `list_pages` / `new_page` / `select_page` / `close_page` / `navigate_page` / `wait_for` |
| 入力 (10) | `click` / `click_at` / `drag` / `fill` / `fill_form` / `hover` / `press_key` / `type_text` / `upload_file` / `handle_dialog` |
| デバッグ (5) | `take_snapshot` / `take_screenshot` / `evaluate_script` / `list_console_messages` / `get_console_message` |
| ネットワーク (2) | `list_network_requests` / `get_network_request` |
| エミュレーション (2) | `emulate` / `resize_page` |
| スクリーンキャスト (2) | `screencast_start` / `screencast_stop` (アニメーション GIF) |

主な挙動:

- 要素系ツールは `take_snapshot` (a11y ツリー) が返す `uid` で要素を指定
  します。新しいスナップショットを取ると古い uid は無効になります。
  アクセシブル名を持たないクリック可能要素 (アイコンのみのボタン、空の
  クリック領域) は a11y ツリーに現れないため、DOM から回収してツリーの
  下に別枠で列挙します。
- 操作が `alert`/`confirm`/`prompt` を開いた場合、ページは処理されるまで
  停止します。ツールはダイアログの種類とメッセージを付けて即座に戻るので、
  `handle_dialog` で続行してください。既にダイアログが開いているページを
  操作した場合は `dialog_open` エラーで即座に失敗します (操作はページに
  届いていません)。
- `wait_for` は `text` か、`selector` + `state`
  (`visible` / `hidden` / `present` / `absent`) のどちらかを取ります。
- `list_console_messages` / `list_network_requests` は `lastMsgId` /
  `lastReqId` を返します。これを `sinceMsgId` / `sinceReqId` として渡すと
  前回以降の分だけ取得できます。
- スナップショットは checkbox / radio の `checked` と、該当する場合の
  `disabled=true` を出力します。スクリプトなしでトグル結果を検証できます。
- `emulate` は呼び出しごとにエミュレーション状態全体を設定します。省略した
  パラメータはリセットされるため、引数なしの `emulate` で全解除になります。
  例外は `extraHttpHeaders` で、渡した時のみ変更され、値は
  `{"X-Custom":"value"}` のような JSON オブジェクト文字列です。なお
  `Offline` を解除すると Chrome がエラーページを自動リロードするため、
  オフライン中に失敗した URL が読み込み済みに見えることがあります。
- screencast のフレームはページの再描画時にのみ発生します。完全に静止した
  ページでは 1 枚も得られません。`screencast_stop` は常に `truncated` を
  返し、`recordedMs` (実時間) と `gifDurationMs` (GIF の再生時間) を
  区別します。
- `take_screenshot` は workspace に保存しつつ、小さければ画像を inline
  でも返します。`screencast_stop` は Go 標準ライブラリのみでアニメーション
  GIF を合成します。
- `drag` はマウスイベントベースです (HTML5 dragstart/drop ベースの UI は
  シミュレートしません)。
- console / network の記録は、ツールがページに最初に触れた時点から
  始まります (attach 時にドメインを有効化)。

ツール名と引数スキーマは上流に準拠し、エージェントが既存の使い方をそのまま
流用できるようにします。Lighthouse 監査・performance insight・heap snapshot
解析・extensions・WebMCP は恒久的にスコープ外です。

## インストール

Homebrew(macOS, Apple Silicon — 署名・notarize 済みビルド済みバイナリ):

```sh
brew install nlink-jp/tap/chrome-pilot-mcp
```

または [releases ページ](https://github.com/nlink-jp/chrome-pilot-mcp/releases)
から linux/amd64, linux/arm64, darwin/arm64, windows/amd64 のビルド済み
バイナリを取得してください。

ソースからビルドする場合(Go 1.23+):

```sh
git clone https://github.com/nlink-jp/chrome-pilot-mcp
cd chrome-pilot-mcp
make build          # → dist/chrome-pilot-mcp
```

別途 Google Chrome のインストールが必要です。本サーバーはインストール済みの
Chrome を操作するだけで、ブラウザのダウンロードは一切行いません。

## ビルド

```bash
make build    # → dist/chrome-pilot-mcp
make test     # go test ./...
```

## 使い方

```bash
chrome-pilot-mcp --version   # 版数表示
chrome-pilot-mcp             # stdio で MCP を serve (MCP クライアントから使用)
```

フラグ:

| フラグ | 意味 |
|---|---|
| `--headless` | Chrome を headless で起動 |
| `--channel <stable\|beta\|dev\|canary>` | 起動する Chrome チャンネル (既定 stable) |
| `--executable-path <path>` | Chrome バイナリの明示指定 (`--channel` より優先) |
| `--attach <ws://…\|port\|host:port>` | 起動せず既存の debugging endpoint にアタッチ (loopback のみ) |
| `--workspace-root <dir>` | スクリーンショット等の出力先 (既定: 一時ディレクトリ) |
| `--viewport <WxH>` | 初期ビューポート。例 `1280x800` |
| `--profile <name>` | 名前付き永続プロファイル (回をまたいで保持) |
| `--user-data-dir <path>` | user-data-dir の明示指定 (`--profile` と排他) |
| `--allow-hosts <list>` | ホスト許可リスト (カンマ区切り)。1 つでも指定すると default-deny |
| `--block-hosts <list>` | ホスト拒否リスト (カンマ区切り)。allow より優先 |
| `--block-local` | `file://` と `data:` を拒否 |
| `--config <path>` | 設定ファイル (下記) |

Chrome は最初のツール呼び出し時に遅延起動されます。debugging ポートは
`127.0.0.1` の ephemeral ポートに bind されます。

## 設定ファイル

上記はすべて TOML ファイルにも書けます
([config.example.toml](config.example.toml) 参照)。`--config <path>` で
明示指定 (ファイルがなければ起動エラー)、未指定なら
`~/.config/chrome-pilot-mcp/config.toml` があれば読みます。カレント
ディレクトリは**探索しません** — サーバーの起動ディレクトリ次第で設定を
注入されることを防ぐためです。明示指定したフラグはファイルより優先します。

```toml
[browser]
headless = true
profile  = "work"

[security]
allow_hosts = ["example.com", "*.example.com"]
```

## ブラウザプロファイル

既定では毎回使い捨てプロファイルを作り、終了時に削除します。
`--profile <name>` は `~/.config/chrome-pilot-mcp/profiles/<name>`
(パーミッション 0700) に永続化し、ログイン状態を回にまたいで保持します。
`--user-data-dir <path>` は指定ディレクトリをそのまま使います。どちらも
`--attach` との併用は拒否され、`--user-data-dir` に**実 Chrome の
プロファイルを指定した場合も拒否**します (開いているブラウザを操作したい
場合は `--attach` を使ってください)。

永続プロファイルには Cookie やログイン情報が蓄積されます。当該ディレクトリ
は機微情報として扱ってください。

## ブラウザの行き先を制限する

このサーバーを操作するエージェントはプロンプトインジェクションを受け得る
ため、行き先をエージェントの手の届かない位置から制限できます。リストは
起動時に確定し、どのツールからも変更できません。

```bash
chrome-pilot-mcp --allow-hosts "example.com,*.example.com"
```

`--allow-hosts` を 1 つでも指定すると default-deny になります。
`*.example.com` はサブドメインのみにマッチするため、apex が必要なら別途
列挙してください。`--block-hosts` は allow より優先し、単独で denylist
としても使えます。

強制は二層です: `navigate_page` / `new_page` は事前に
`host_not_allowed` エラーで拒否し、CDP インターセプトがそれ以外の
リクエスト (ページ内 `fetch`、リダイレクト、サブリソース) を
`BlockedByClient` で失敗させます。遮断されたリクエストは
`list_network_requests` に残るので、原因不明の失敗になりません。リスト
未設定時はインターセプト自体を有効化しません。

既知の限界 ([ADR-0001](docs/ja/adr/0001-host-allow-block-lists.ja.md)):
WebSocket 接続はインターセプト対象外で、ツールが attach していない
ページには効きません。これはエージェント事故防止のガードレールであり、
OS レベルの egress 制御の代替ではありません。

MCP クライアント設定:

```json
{
  "mcpServers": {
    "chrome-pilot": {
      "command": "/path/to/chrome-pilot-mcp",
      "args": ["--headless"]
    }
  }
}
```

## 帰属

ツール名・スキーマ・挙動は
[ChromeDevTools/chrome-devtools-mcp](https://github.com/ChromeDevTools/chrome-devtools-mcp)
(Google LLC, Apache-2.0) に着想を得ています。本プロジェクトは独立した
クリーン再実装であり、上流のコードは含みません。

## ライセンス

MIT — [LICENSE](LICENSE) を参照。
