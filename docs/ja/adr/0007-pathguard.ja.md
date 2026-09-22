# ADR-0007: パスの判定は nlink-jp/pathguard に任せる — 自前の実体比較を捨てる

- Status: Accepted
- Date: 2026-09-22
- Amends: [ADR-0006](0006-writes-and-uploads-under-work-dir.ja.md)（拒む場所の判定）

## Context

ADR-0005 で `work_dir` の検証を、ADR-0006 で書き込みとアップロードの封じ込めを入れた。拒む場所の判定は
2 層だった: 組織で共有する名前の比較（`workdir.Sensitive`、`internal/workdir` の写し）と、このサーバーが独自に
足した実体の比較（`refusedLocation`・`insideByIdentity`。守るディレクトリを `os.SameFile` で比べる）。APFS は
既定で大文字小文字を区別せず、名前の比較は 2 回続けてレビューで破られたためである。

組織はこの判定を 1 つのモジュールにまとめた（`nlink-jp/pathguard`、lib-series）。場所をファイルの実体と、ディスクと
同じやり方で同一視した名前の両方で比べ、まだ存在しない場所もその親の実体で捕まえ、一覧は gem-agent・lagent と
同じものを持つ。そして読み書き（Local）とマシンの外へ出るもの（Outbound）とで方針を分ける。

## Decision

- `github.com/nlink-jp/pathguard` v0.2.0 を依存に加える。CLAUDE.md の依存の規則は「サードパーティのモジュールを
  持たない。この組織のモジュール（同じ規則を守るもの）は可」と言い換える（運用者の判断、2026-09-22）。
- `internal/workdir` は薄いアダプタにする: `_meta` の取り出し、エラーの `toolerr` への写し、
  `NewResolverFor(places...)`（守る場所を自分で名指す）、`LocalPath` と `OutboundPath`（details.reason と文を返す）。
- 守る場所は `Manager.protectedPlaces` の 1 つの一覧（このサーバー自身のディレクトリ、操作中の Chrome の
  プロファイル、一時ディレクトリの `chrome-pilot-mcp-profile-*` すべて、利用者自身の Chrome のプロファイル）で、
  それぞれの reason（`server_dir`・`browser_profile`）と文を持つ。`Manager.resolver` が使う時点でそこから検査器を
  作る —— 場所はサーバーが動いている間に変わる（ブラウザを起動する、一時プロファイルが現れる）ので、キャッシュ
  しない。
- `work_dir` は pathguard/workdir の「作業ディレクトリにしてはならない場所」の一覧（システムのディレクトリ、ホーム
  ディレクトリそのもの、資格情報・エージェント制御の場所、このサーバーの場所。`Resolve`）で、書き込み（`outputUnder`・
  `writeUnder`）は Local の方針（`LocalPath`）で、`upload_file`（`inputUnder`）は Outbound の方針（`OutboundPath`）で
  判定する。ページは渡されたファイルをどこへでも送れるので、アップロードは
  マシンの外へ出るものとして扱う。
- `refusedLocation`・`insideByIdentity`・`protectedDir` は捨てる。`resolveExisting`（作ろうとする場所の実体。行き先に
  `..` を含む壊れたリンクは信用しない）と、`os.Root` 越しの書き込み・乱数名の一時ファイル・rename は残す ——
  これらは判定ではなく、判定したものを書き込むときの封じ込めである。

## Consequences

- **アップロードで新たに拒む**: 秘密の名前を持つファイル（`id_rsa`、`credentials.json`、`*service-account*.json`、
  `.env`）と、パスが資格情報のディレクトリ名・ファイル名（`.ssh`、`.aws`、`.npmrc`、`.netrc`、`.git-credentials`、
  `.bash_history` など）を通るファイル —— どこにあっても（`work_dir` の中でも、ホームの外でも）。
- **書き込みと `work_dir` で新たに拒む**: ランタイムと同じ一覧のうち、ホームにある本物の場所、あらゆる綴り、それらの
  ディレクトリの直下のリンクの指す先。`$HOME` がアカウントのホームと違うときは、それらの場所を両方のホームで（この
  サーバー自身の場所はこれまでどおり `$HOME` に従う）。ホームが分からなければすべて。
- **新たに通す**: `.env` のひな形（`.env.example` など）。
- `work_dir_denied` の `details` に `reason` が加わる。
- `writeUnder` の作成後の検査（ルートが実際に届いたディレクトリ）は、決定的なテストでは起こせない競合に備える
  もので、変異を加えてもテストは落ちない。v0.8.0 から同じである。

## References

- 組織 ADR-021（ファイル渡し MCP サーバーの work dir 契約）
- ADR-0005（work dir 契約）、ADR-0006（書き込みとアップロードを `work_dir` の中に限る）
- nlink-jp/pathguard の RFP（`docs/ja/pathguard-rfp.ja.md`）
