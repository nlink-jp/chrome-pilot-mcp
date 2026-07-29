# ADR-0001: ホスト allow/block リストによるナビゲーション制限

- Status: Accepted
- Date: 2026-07-29
- Driver: magi
- Generalises to: nlink-jp の他ブラウザ/ネットワーク系ツール

---

## Context

chrome-pilot-mcp を使う MCP クライアント (LLM エージェント) はプロンプト
インジェクションを受け得る。ブラウザ自動化は攻撃者にとって:

- 任意サイトへのアクセス手段 (マルウェア配布ページ、フィッシング)
- `--attach` 時はログイン済みセッションの操作手段
- データ持ち出し経路 (窃取した情報を攻撃者ホストへ POST)

になり得る。LLM 側の判断に依存しない**サーバー側の行き先制限**が欲しい。
設定は起動時に確定する (CLI フラグ、または config.toml — ADR-0002)。
実行中に MCP ツール経由で変更する手段は提供しないため、LLM がツール
呼び出しで緩めることはできない。この「実行中に変更不能」であることが
本機能の安全性の核である。

命名は white/black ではなく **allow/block** とする (Chrome 本体の用語
移行に合わせる)。

## Decision

### フラグとセマンティクス

- `--allow-hosts "<glob>,<glob>,..."` — 許可ホスト。**1 つでも指定したら
  default-deny** (リスト外はすべて拒否)。未指定なら全許可 (現状互換)。
- `--block-hosts "<glob>,..."` — 拒否ホスト。**allow より優先**。
  allow 未指定 + block のみ、の組み合わせも可 (denylist 運用)。
- `--block-local` — `file://` と `data:` へのナビゲーションを拒否する
  独立フラグ。既定は許可 (ローカル HTML 検証が主用途のため)。

ホスト照合規則:

- 大文字小文字を区別しない完全一致、または glob (`*` は 1 つ以上の
  ラベル列にマッチ)。
- `*.example.com` は **サブドメインのみ** にマッチし、`example.com`
  自体には**マッチしない** (セキュリティ設定は明示優先)。apex も許可
  するなら `example.com,*.example.com` と両方書く。
- ポートは照合対象外 (ホスト名のみ)。対象スキームは http/https。

### 二層強制

1. **ツール引数レイヤー** — `navigate_page` / `new_page` の URL を判定し、
   拒否時は構造化エラー `host_not_allowed` を即座に返す。エージェントに
   「そこは行けない」と明確に伝える UX 層。
2. **CDP ネットワークレイヤー** — リストが 1 つでも設定されている場合、
   各ページ session の attach 時に `Fetch.enable` し、`Fetch.requestPaused`
   でリクエスト URL のホストを判定して `continueRequest` / `failRequest
   (BlockedByClient)` を返す。ページ内 JS のリダイレクト・fetch/XHR・
   サブリソース・クリック起因のナビゲーションもここで止まる。

リスト未設定時は `Fetch.enable` を呼ばない (割り込み往復のオーバー
ヘッドをゼロに保つ)。ブロックされたリクエストは `loadingFailed
(BlockedByClient)` として network コレクターに記録され、
`list_network_requests` で観察できる (デバッグ性)。

### 実装配置

- 判定ロジックは純関数 `hostAllowed(host) bool` として `internal/tools`
  に置き、テーブルテストする。
- `Fetch.requestPaused` はコレクター (CDP read loop) で受けるが、
  continue/fail の CDP call が必要なため screencast ack と同じく
  goroutine 経由で応答する。

## Alternatives considered

- **`Network.setBlockedURLs` のみ**: 実装は最少だが denylist 表現しか
  できず、default-deny (allowlist) が組めない。不採用。
- **Chrome `--host-rules` / `--host-resolver-rules`**: ホストを
  127.0.0.1 へマップして事実上遮断する方式。allowlist 表現が困難で、
  エラーが不透明 (接続拒否にしか見えない)。不採用。
- **in-process CONNECT プロキシ (`--proxy-server=127.0.0.1:<port>`)**:
  バイナリ内に stdlib で CONNECT プロキシを立て、Chrome 全体の egress を
  プロセスレベルで制御する案。WebSocket や attach 対象外ページも含む
  **最も強い境界**で、TLS 復号も不要 (CONNECT の宛先ホストで判定)。
  ただし launch モード限定 (attach 済みブラウザにはプロキシを差し込め
  ない)。今回は採用しないが、下記 Consequences の残余リスクが問題に
  なった場合の**強化パスとして温存**する。

## Consequences

- 既定 (リスト未指定) は挙動もオーバーヘッドも現状と完全互換。
- リスト設定時は全リクエストに CDP 割り込みの往復が 1 回入る (localhost
  往復なので実用上軽微)。
- **既知の限界 (v1)**:
  - WebSocket 接続は `Fetch` ドメインの割り込み対象外 → allow-mode でも
    非許可ホストへの WS は遮断されない。
  - launch (headful) でユーザーが手で開いたタブなど、ツールが attach
    していないページには効かない。
  - `file://` / `data:` の強制はツール引数レイヤーのみ (`Fetch` は
    ネットワークリクエスト対象)。
  - これらを塞ぐ必要が出たら CONNECT プロキシ案に移行する。
- ドキュメントには「これはエージェント事故防止の境界であり、OS レベルの
  egress 制御の代替ではない」と明記する。
