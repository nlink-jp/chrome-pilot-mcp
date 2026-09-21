# ADR-0006: 録画ファイルもアップロードするファイルも `work_dir` の中に限る

- Status: Accepted
- Date: 2026-09-22
- Amends: [ADR-0005](0005-work-dir-contract.ja.md)

## Context

ADR-0005 は、ファイルを書くすべてのツールで `work_dir` を必須にした。組織の契約
（ADR-021）の一覧でも、このサーバーは対応済みとされている。ところが、ファイルを
指す引数が 2 つ、その外に残っていた。

1. **`screencast_start` の `filePath`。** 検査は末尾が `.gif` かどうかだけだった。
   呼び出し側は、プロセスが書き込める場所ならどこでも指定できた。途中のディレクトリは
   作られ、既存のファイルは上書きされた。ADR-021 §7 は、書き込みは `work_dir` の中
   だけとし、「このフリートに、サーバーがほかの場所へ書く必要のある作業はない」と
   定める。それ以外への書き込みは、モデルの一言で開く持続化の経路（シェルの設定
   ファイル、git hook、launchd の plist）になる。
2. **`upload_file` の `filePath`。** 検査は存在するかどうかだけで、ディレクトリも
   通り、パスはそのまま Chrome に渡っていた。まさにこのために `internal/workdir` が
   持つ認証情報の一覧は、一度も参照されていなかった。ADR-021 §7 の例外は、ファイルを
   マシンの外へ送り出せるものに渡すサーバーを扱い、「入力は `work_dir` の中からだけ
   取る」と定める。Web ページのファイル選択欄はまさにそれにあたる。ファイルの行き先は
   ページが決める。

どちらの挙動もテストが固定していた（`TestScreencastFilePathBeatsWorkDir` は、あえて
`work_dir` の外に書いていた）。意図的な例外だと記録した文書は無い。ADR-0004 の
「意図的に境界にしない」は ADR-021 より前の記述で、同じ日に ADR-0005 が置き換えている。

## Decision

1. **`screencast_start` の `filePath` は `work_dir` の中の場所を指す。** 相対パスは
   `work_dir` からの相対とする。絶対パスは `work_dir` の中になければならず、
   ディレクトリの綴りは呼び出し側が渡したままでも、シンボリックリンクを解決した
   形でもよい。検査はパス上にすでにあるシンボリックリンクをすべてたどり、パスを
   指定した呼び出しである `screencast_start` で、フレームを 1 枚も集める前に行う
   （ADR-0004 の規律）。ファイルは `work_dir` を根とする `os.Root` を通して書く。
   開始から停止までの間に、書き込みをディレクトリの外へ運ぶシンボリックリンクが
   現れても、`os.Root` がそれを拒む。
2. **`upload_file` は `work_dir` を必須で受け取り、その中のファイルだけをページに
   渡す。** `filePath` は `work_dir` からの相対パスか、その中を指す絶対パスとする。
   ファイルの実体のパスが `work_dir` の中にあり、認証情報の一覧に当たらず、通常の
   ファイルでなければならない。Chrome に渡すのはその実体のパスである。一覧の検査を
   先に、パスの両方の綴りで行う（ADR-021 §7）。そのため、`work_dir` から `~/.ssh` を
   指すリンクは、単に外にあるファイルとしてではなく、認証情報のファイルとして拒まれる。
3. **拒否は `path_not_allowed`** とし、どの規則に当たったかを `details.reason` で
   示す（`outside_work_dir` か `sensitive_path`）。ファイルが無い場合とディレクトリの
   場合は、これまでどおり `invalid_arguments`。
4. `os.Root.MkdirAll` を使うため、`go.mod` を Go 1.25 に上げる。依存は引き続き
   標準ライブラリだけ。

## Consequences

- **互換を崩す。** `work_dir` の無い `upload_file` は `work_dir_required` で拒まれる。
  どこからでもアップロードしていた呼び出し側は、先にファイルを作業ディレクトリへ
  コピーする必要がある。`work_dir` の外を指す `screencast_start` は、開始の時点で
  拒まれる。すべての呼び出しに `_meta["jp.nlink/work_dir"]` を付けるランタイムは、
  `upload_file` の呼び出しを変えなくてよい。
- `upload_file` は `uploaded` にファイルの実体のパス、つまり Chrome に渡したパスを
  返すようになった。
- `internal/workdir` は変えておらず、フリートのコピーとバイト単位で同一のまま。
  規則は `internal/tools/confine.go` に置く。

## References

- 組織 ADR-021 §7、[ADR-0005](0005-work-dir-contract.ja.md)、
  [ADR-0004](0004-per-call-workspace-root.ja.md)
- slack-mcp-extender の `internal/containment` — アップロードの例外に最初に
  当てはまったサーバー。検査の順（解決、一覧、内側か、通常のファイルか）はこれに倣った
