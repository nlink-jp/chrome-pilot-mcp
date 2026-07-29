# RFP: chrome-pilot-mcp

> Generated: 2026-07-29
> Status: Draft

## 1. Problem Statement

npm エコシステム由来のサプライチェーンリスクを排除しつつ、MCP クライアント
(Claude Code 等) からのブラウザ自動化を可能にする。

上流の [chrome-devtools-mcp](https://github.com/ChromeDevTools/chrome-devtools-mcp)
(Google, Apache-2.0) は npx で実行され、puppeteer / lighthouse /
chrome-devtools-frontend など多数の推移的依存を持ち、puppeteer は実行時に
ブラウザバイナリのダウンロードも行う。昨今の npm サプライチェーン攻撃の状況を
踏まえると、この形態を組織内に取り込むことには一定以上のリスクがある。

chrome-pilot-mcp は、そのコア自動化機能 (27 ツール) を **Go 単一バイナリ・
外部依存ゼロ (stdlib のみ)** で再実装する。Chrome DevTools Protocol (CDP) を
WebSocket 越しに直接話し、実行時に外部から何も取得せず、インストール済みの
Chrome を起動またはアタッチするのみ。対象ユーザーは nlink-jp org 内で
ブラウザ自動化を必要とする MCP クライアント利用者本人。

## 2. Functional Specification

### Commands / API Surface

MCP サーバー (stdio transport)。提供ツールは 27 種:

| カテゴリ | ツール |
|---|---|
| ページ操作 (6) | `list_pages` / `new_page` / `select_page` / `close_page` / `navigate_page` / `wait_for` |
| 入力 (10) | `click` / `click_at` / `drag` / `fill` / `fill_form` / `hover` / `press_key` / `type_text` / `upload_file` / `handle_dialog` |
| デバッグ (5) | `take_snapshot` / `take_screenshot` / `evaluate_script` / `list_console_messages` / `get_console_message` |
| ネットワーク (2) | `list_network_requests` / `get_network_request` |
| エミュレーション (2) | `emulate` / `resize_page` |
| スクリーンキャスト (2) | `screencast_start` / `screencast_stop` (アニメーション GIF 出力) |

ツール名・引数スキーマは上流のコア自動化ツールに準拠する (エージェントが
既存の使い方をそのまま流用できるようにするため)。

CLI フラグ (MCP サーバーとしての起動オプション):

- `--headless` — headless モードで Chrome を起動
- `--channel <stable|beta|dev|canary>` — 起動する Chrome チャンネル
- `--executable-path <path>` — Chrome バイナリの明示指定
- `--attach <ws://...|port>` — 既存 Chrome の debugging endpoint にアタッチ
  (自前起動せず)
- `--workspace-root <dir>` — スクリーンショット / GIF などの出力先
- `--viewport <WxH>` — 初期ビューポート
- `--version` — 版数表示 (brew test が叩くため必須応答)

### Input / Output

- 入出力は MCP (JSON-RPC 2.0 over stdio)。
- 大きな出力は file-mediated: スクリーンショット・GIF は workspace 配下に
  保存しパスを返す。スクリーンショットは併せて MCP image content としても
  返し、クライアントが直接表示できるようにする。
- a11y スナップショット・コンソール・ネットワーク一覧はテキストで返すが、
  文字数予算を持ち構築時に truncate する。

### Configuration

CLI フラグのみ。config ファイル・環境変数なし (秘密情報を扱わないため)。

> **2026-07-29 更新**: ADR-0002 により config.toml サポートを追加
> (フラグ > config.toml > デフォルトの優先順位)。本節はその ADR に
> 更新される。

### External Dependencies

- **Go module 依存: ゼロ** (stdlib のみ)。WebSocket クライアント (RFC 6455、
  localhost・平文限定) は自作する。
- **実行時依存: ユーザーがインストール済みの Chrome のみ**。ダウンロードは
  一切行わない。

## 3. Design Decisions

- **言語は Go**: 単一バイナリ・クロスコンパイル・org の標準スタック。
  MCP サーバー骨格 (transport/jsonrpc/mcpserver/toolerr) は data-toolbox-mcp
  から移植する (org の新規 MCP scaffold 慣行)。
- **依存ゼロ (stdlib のみ)**: サプライチェーンリスク排除がプロジェクトの
  存在理由であるため、Go 側でも外部 module を持たない。唯一 stdlib にない
  WebSocket クライアントは、CDP 用途 (localhost・平文・非圧縮) に限定した
  最小実装 (~300 行) を自作する。
- **CDP 直接**: puppeteer / chromedp のような抽象層を挟まず、CDP domain
  (Page / DOM / Runtime / Input / Network / Accessibility / Emulation) を
  直接使う。重仕事 (レンダリング、スクリーンショット PNG 化、a11y ツリー
  構築) は Chrome 側が行うため、Go 側は薄いプロトコル層で済む。
- **screencast は GIF で代替**: 上流は動画だが、CDP `Page.startScreencast`
  の JPEG/PNG フレームを `image/gif` (Floyd–Steinberg ディザ + 256 色
  パレット化) でアニメ GIF に合成すれば stdlib で閉じる。
- **スコープ外 (恒久)**: Lighthouse 監査、performance trace insight、
  heap snapshot 解析 (12 ツール)、extensions、WebMCP、3p developer tools。
  これらは chrome-devtools-frontend / lighthouse エンジンの移植に相当し、
  工数が本体の数倍になる。エージェント用途の実利用はコア自動化に集中して
  いるため採らない。
- **補完関係**: org 内の他 MCP サーバー群 (data-toolbox-mcp,
  pcap-analyzer-mcp 等) と同じ file-mediated workspace モデル・構造化
  tool エラー ({code, message}) の慣行に従う。
- **帰属**: ツール名・スキーマ・description は Apache-2.0 の上流に由来する
  ため、README / NOTICE で inspired-by 帰属を明記する。

## 4. Development Plan

### Phase 1: Core

- data-toolbox-mcp から MCP 骨格 4 パッケージ移植
  (`internal/{transport,jsonrpc,mcpserver,toolerr}`)
- 自作 WebSocket クライアント (RFC 6455) + CDP クライアント層
  (call/event dispatch、target/session 管理)
- Chrome launcher (専用 user-data-dir、headless 対応) / attach
- ツール: pages 6 種 + `evaluate_script` + `take_snapshot` +
  `take_screenshot`
- fake CDP サーバーによるユニットテスト基盤 (外部依存モック可能設計)

Phase 1 完了時点で「navigate して snapshot を取り screenshot する」最小
MCP として単独レビュー可能。

### Phase 2: Features

- input 10 種
- console / network / emulation / `wait_for`
- screencast GIF (フレーム間引き・縮小既定値付き)
- 実 Chrome での E2E ハーネス (リリース前実データ E2E 慣行)

### Phase 3: Release

- docs/{en,ja} 3 層、README.md / README.ja.md / CHANGELOG.md / AGENTS.md
- make build / build-all、署名・notarize、リリース zip (canonical binary 名)
- umbrella (util-series) submodule 追加、org profile 更新、check-org.sh

## 5. Required API Scopes / Permissions

None — 外部サービス・API キー・認証情報は一切使わない。ローカルの Chrome
プロセスと通信するのみ。

## 6. Series Placement

Series: **util-series**

Reason: 汎用のブラウザ自動化 MCP サーバーであり、org の他 MCP サーバー群
(data-toolbox-mcp, voice-studio-mcp 等) と同じ series に置く。セキュリティ
特化でも実験でもないため cybersecurity-series / lab-series は不適。

## 7. External Platform Constraints

- **CDP は安定版 API ではない** (tip-of-tree で変化する)。stable な domain
  (Page / DOM / Runtime / Input / Network / Accessibility / Emulation) に
  限定して使い、実験的 domain には依存しない。
- **Chrome バージョン間の挙動差** — 対応バージョンポリシーを明記する
  (現行 stable + 直近 2 版程度を動作確認対象とする)。
- **`--remote-debugging-port` はローカル攻撃面** — 127.0.0.1 bind 必須、
  専用 user-data-dir、ポートは ephemeral とし、セキュリティ設計として
  ドキュメントに明記する (セキュリティは実装同時)。
- **a11y スナップショットの巨大化** — 文字数予算で構築時 truncate。
- **GIF screencast の制約** — 256 色・ファイルサイズ増大。既定で縮小・
  フレーム間引きを行う。

---

## Discussion Log

- 2026-07-29: 発端は「npx 実行・npm 依存多数の chrome-devtools-mcp を
  組織内に取り込むサプライチェーンリスク」。単一バイナリ・外部依存なしの
  クリーン再実装を志向。
- 上流 v1.6.0 を調査: TS 約 17,000 行・52 ツール。依存の重さは分析系
  (lighthouse / chrome-devtools-frontend / puppeteer) に集中しており、
  コア自動化は CDP 直接で置き換え可能と判断。
- スコープ: フルパリティ (52) / コア自動化 (~30) / 最小 (~15) を比較し、
  **コア自動化**を採用。分析系はエンジン移植相当で工数数倍のため恒久
  スコープ外。
- 依存ポリシー: vetted WebSocket ライブラリ 1 つ許容案と比較し、
  **stdlib のみ・依存ゼロ**を採用。WebSocket は localhost/平文限定の
  最小自作 (~300 行)。
- 命名: browser-pilot-mcp / chrome-pilot-mcp / cdp-mcp を比較し
  **chrome-pilot-mcp** を採用 (Chrome 専用であることを名前に出す)。
- 接続方式: **自前起動 + attach 両対応**。既定は専用 user-data-dir での
  自前起動、`--attach` でログイン済みセッションの検証にも対応。
- 出力: **file-mediated + スクリーンショットは MCP image content 併用**
  (org 慣行との整合 + クライアントでの直接表示)。
- 設定: **CLI フラグのみ** (秘密情報なし、MCP サーバーはクライアント設定の
  args で起動されるため)。
- screencast: 当初除外予定だったが、ユーザー提案により **stdlib の
  image/gif でアニメ GIF 合成**が可能と確認し、スコープに復帰 (計 27
  ツール)。
- screencast 形式: APNG (24bit フルカラー、チャンク自作で依存ゼロ実装可)
  と比較し **GIF を採用**。理由: Slack 添付でアニメ再生されるのは GIF のみ
  (org は ChatOps 中心)、全画面フレームの APNG は肥大しやすく差分最適化まで
  やると実装が本格化する、録画対象は平坦色 + テキスト主体の Web UI で GIF の
  弱点が出にくい。画質要望が出た場合の `format: "apng"` opt-in 追加は技術的に
  閉じていることを確認済み。
