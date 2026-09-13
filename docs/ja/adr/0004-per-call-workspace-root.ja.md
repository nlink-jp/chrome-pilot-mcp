# ADR-0004: ファイルを生成するツールの呼び出し単位 workspace root

- Status: Accepted
- Date: 2026-09-13
- Driver: magi
- Depends on: ADR-0002 (config.toml スキーマ)

---

## Context

ファイルを生成するツールは 2 つある。`take_screenshot` は PNG/JPEG を、
`screencast_stop` は `screencast_start` が録り始めた GIF を書き出す。どちらも
起動時に一度だけ決まる workspace root — `--workspace-root` または
`[workspace] root`、既定は新規の一時ディレクトリ — の下に置かれ、ツール結果は
そのパスを返す。

呼び出し側は通常エージェントランタイムであり、本サーバーを登録している
ランタイムはいずれも自身のファイルアクセスを限定している。

- **gem-agent / lagent** は組み込みファイルツールのパスを 2 つの root
  (プロジェクトディレクトリとセッション work ディレクトリ) だけに解決し、
  symlink 経由も含めそれ以外を拒否する。`/var/folders/...` のパスは
  `view_image` に拒否されるため、直前に撮ったスクリーンショットを自分で
  開けない。
- **Claude Code** も同様に workspace ディレクトリに限定される。

起動フラグだけではこの隙間は埋まらない。有効な値はセッション単位
(`$GEMAGENT_WORK_DIR` / `$LAGENT_WORK_DIR`) なので各ランタイムのサーバー登録に
個別に書く必要があり、展開できる変数を持たないランタイムや work ディレクトリの
ないセッションでは元の状態に戻る。そもそも出力先を選ぶのに必要な情報は、起動行を
書いた者ではなく呼び出し側が持っている。

`screencast_start` は既に任意の絶対パス `filePath` を受け付け、その親
ディレクトリを作成していた。つまり「呼び出しが出力先を指定する」こと自体は本
サーバーにとって新しい能力ではなく、スクリーンショットに相当物がなかっただけ
である。

## Decision

`take_screenshot` と `screencast_start` に任意引数 `workspaceRoot` を追加する。

- 指定時は `<workspaceRoot>/screenshots` または `<workspaceRoot>/screencasts`
  の下に書き出す。ディレクトリは無ければ作成する。
- 省略時の挙動は従来と同一 (設定された root、無ければ新規一時ディレクトリ)。
- `screencast_start` の `filePath` を明示した場合は引き続きそちらが優先される
  — こちらは root ではなくファイルそのものの指定だからである。
- `workspaceRoot` は**絶対パス必須**。ツール引数は JSON であり、`~` を展開する
  ものも相対パスを解決するものも途中に無い。どちらも許すとサーバーの作業
  ディレクトリの隣に黙って書かれる。いずれも `invalid_arguments` で拒否する。
- `screencast_start` は書き出しが stop 時であっても root を start で検証する。
  誤った引数を渡した呼び出しこそ修正できる主体であり、終了時に初めて失敗する
  録画はフレームを既に捨てている。
- 綴りは本サーバーのスキーマ様式 (`filePath`, `fullPage`) に合わせて
  `workspaceRoot` とする。組織内の同系 MCP サーバーは snake_case スキーマで
  同じ概念を `workspace_root` と綴るが、取り違えた呼び出しは
  `DisallowUnknownFields` によりフィールド名付きで拒否されるため、黙って無視
  されるのではなく呼び出し側が訂正できる。

## Consequences

- エージェントは自分で開けるパスを受け取れる。運用者がセッション単位の変数を
  サーバー起動行に通す必要はない。
- サーバーは呼び出し側が指定した場所に書く。これは意図的に境界ではない: この
  ツール引数は従来から無制限だった `filePath` と同じ信頼レベルであり、
  エージェント主導の書き込みで効く封じ込めはエージェント自身の sandbox にある。
  **sandbox と読んではならない** — chrome-pilot-mcp にファイルシステムポリシーは
  無く、導入するなら別 ADR の仕事である。
- 起動時設定は、何も渡さない呼び出し (手動運用や自前のディレクトリを持たない
  MCP クライアント) の既定として引き続き意味を持つ。
