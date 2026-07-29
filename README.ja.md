# chrome-pilot-mcp

> **Status: 開発中 (Phase 1 コア実装済み)。** ページ操作・a11y スナップ
> ショット・スクリーンショット・スクリプト評価・wait_for が実 Chrome で
> 動作します。入力・console・network・emulation・screencast は今後実装。
> 全体計画は [RFP](docs/ja/chrome-pilot-mcp-rfp.ja.md) を参照してください。

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

## ツール一覧 (計画 27)

| カテゴリ | ツール | 状態 |
|---|---|---|
| ページ操作 (6) | `list_pages` / `new_page` / `select_page` / `close_page` / `navigate_page` / `wait_for` | ✅ 実装済み |
| デバッグ (5 のうち 3) | `take_snapshot` / `take_screenshot` / `evaluate_script` | ✅ 実装済み |
| デバッグ (残り) | `list_console_messages` / `get_console_message` | 予定 |
| 入力 (10) | `click` / `click_at` / `drag` / `fill` / `fill_form` / `hover` / `press_key` / `type_text` / `upload_file` / `handle_dialog` | 予定 |
| ネットワーク (2) | `list_network_requests` / `get_network_request` | 予定 |
| エミュレーション (2) | `emulate` / `resize_page` | 予定 |
| スクリーンキャスト (2) | `screencast_start` / `screencast_stop` (アニメーション GIF) | 予定 |

ツール名と引数スキーマは上流に準拠し、エージェントが既存の使い方をそのまま
流用できるようにします。Lighthouse 監査・performance insight・heap snapshot
解析・extensions・WebMCP は恒久的にスコープ外です。

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

Chrome は最初のツール呼び出し時に遅延起動されます。専用の (一時)
プロファイルを使い、debugging ポートは `127.0.0.1` の ephemeral ポートに
bind されます。

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
