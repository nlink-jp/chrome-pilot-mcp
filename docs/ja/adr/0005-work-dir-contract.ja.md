# ADR-0005: 出力先は `work_dir` に統一し、既定ワークスペースと起動フラグを捨てる

- Status: Accepted
- Date: 2026-09-13
- Amends: [ADR-0004](0004-per-call-workspace-root.ja.md)（呼び出しごとの workspaceRoot）

## Context

ADR-0004 は本日、「呼び出し側が読み戻せる場所に書く」ために `workspaceRoot` を
導入した。判断は正しかったが、2 点が組織 ADR-021（フリート共通の work dir 契約）と
食い違う。

1. **綴り。** ADR-0004 は本サーバーの camelCase スキーマ様式に合わせて
   `workspaceRoot` とし、「兄弟サーバーは `workspace_root`、間違えたら名前で
   拒否される」と受け入れた。ADR-021 はその割れ自体を閉じる —— **全サーバーで
   `work_dir` 1 つ**。
2. **省略時の既定。** ADR-0004 は「省略時は従来どおり（config の root、無ければ
   一時ディレクトリ）」を残した。ADR-021 §2 は、パスを返す呼び出しにサーバー既定を
   使わない —— 運用者が書いた場所が呼び出し側に読めるかは偶然でしかなく、
   読めなければ「成功したのにパスが開けない」に戻る。

## Decision

1. 引数は **`work_dir`**（`take_screenshot` / `screencast_start` / `debug` 系）。
   **必須**にする。
2. 解決順は 引数 → `_meta["jp.nlink/work_dir"]` → エラー。**既定は持たない。**
3. `--workspace-root` フラグと `[workspace] root` キーを**削除**する。キーが残った
   config は起動時に名指しで落とす。
4. 検証は閉じた一覧（絶対 / `~` 無し / `..` 無し / 存在する dir / 書込可 /
   システム・資格情報の位置でない）。**dir は作らない** —— 呼び出し側の
   ディレクトリは必ず存在するので、無いパスは打ち間違いである。
5. `screencast_start` が停止時ではなく開始時に検証する規律（ADR-0004）は維持する。

## Consequences

- **破壊的。** `workspaceRoot` を送る呼び出しは拒否され、上流
  chrome-devtools-mcp 向けに書かれた呼び出し（引数なしの `take_screenshot`）も
  `work_dir_required` で落ちる。drop-in 性はツール名と挙動については維持されるが、
  **出力先だけは呼び出し側が名指しする必要がある**。エラーが名前を告げるので
  1 ターンで直せる
- 一時ディレクトリへのフォールバックが消え、「撮れたのに開けない」経路が無くなる
- `internal/workdir` はフリート内の他サーバーと同一ファイル（移植物）

## References

- 組織 ADR-021、[ADR-0004](0004-per-call-workspace-root.ja.md)、
  voice-scribe ADR-0010（参照実装）
