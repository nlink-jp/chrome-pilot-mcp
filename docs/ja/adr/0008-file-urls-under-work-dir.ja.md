# ADR-0008: file:// は呼び出しの work_dir の中だけ開く

- Status: Accepted
- Date: 2026-09-22
- Amends: [ADR-0001](0001-host-allow-block-lists.ja.md)（`file://` の扱いと、その強制の層）

## Context

ADR-0001 は `file://` を既定で許可した（ローカル HTML の検証が主な用途）。`--block-local` を付ければ `file://` と
`data:` を拒む。その強制はツール引数の層だけだと書いた —— `Fetch` の割り込みはネットワーク要求だけを見る、という
前提だった。

2026-09-22 の独立レビューで、既定のままでは `navigate_page` に `file:///<home>/.ssh/id_rsa` を渡し、
`take_snapshot` や `evaluate_script` で中身を読めることが分かった。ADR-0006・ADR-0007 の封じ込め（アップロードは
pathguard の Outbound、書き込みは Local）の外を通る道である。

設計の前に、`file://` が Chrome に届く道を実測した（2026-09-22、ヘッドレス Chrome・使い捨てプロファイル、
スクラッチ内の偽の秘密ファイル、127.0.0.1 の HTTP サーバー）。

**読めた道:**

- `navigate_page` と `new_page` に `file://` を渡す。
- 開けた `file://` のページの JS から、別の `file://` へ移動する。
- `view-source:file://…` —— ツール引数の層は `view-source:` を知らないスキームとして通していた。
- `file://` のページから `window.open` で開いた別タブ。まだアタッチしていないタブには割り込みが効かず、
  そのタブを選んでスナップショットを取ると中身が読めた。

**Chrome 自身が止めた道:** http のページからの JS による移動・リンク・302 リダイレクト、`data:` や `about:blank`
からの移動、http のページの iframe、`file://` のページからの `fetch(file://…)`。

**割り込みについて:** `Fetch` は `file://` も見る —— ADR-0001 の前提は誤りだった。ページ本体、それが読む CSS と
画像、JS による移動、`view-source:`、iframe まで `Fetch.requestPaused` に来た。パターンを `file://*` にすると、
http の要求は一度も止まらない。

## Decision

- `file://` は、呼び出しが名指す `work_dir` の中にあり、pathguard の Local 方針を通るものだけ開く。
  `navigate_page` と `new_page` に `work_dir`（引数、無ければ `_meta`）を足し、URL が `file://` のときだけ解決する。
  `work_dir` の外は `path_not_allowed`（`outside_work_dir`）、資格情報などは pathguard の理由で拒む。場所は
  pathguard の形の最後（リンクはすべて、行き先の無いものも行き先までたどる）で決め、ファイルがあるかどうかで
  答えを変えない。
- 許可した `work_dir` は、そのページのセッションへの付与として記録する。付与はセッションごとで、積み上がる。
- `Fetch` の割り込みを常に有効にする。ホストの制限が無ければパターンは `file://*` だけ（http の閲覧に往復は入らない）、
  制限があれば従来どおり `*`。`file://` の要求は、そのセッションの付与のどれかの中にあり Local を通るときだけ
  続け、それ以外は `BlockedByClient` で止める。JS による移動・サブリソース・iframe・`view-source:` はここで止まる。
- ツール引数の層も、`view-source:` はその中の URL で判定する。
- 付与の無いローカルファイルを表示しているページ（`window.open` で開いた別タブ、利用者が開いたタブ）には、
  `navigate_page` のほかのツールを使わせない。ページを使うツールはすべて `selectedPage` を通るので、そこで毎回
  ページの現在の URL を確かめる。`navigate_page` で離れれば使える。attach モードで利用者のタブを勝手に空白には
  しない。
- 割り込みの判定は `m.mu` を取らない。`m.mu` を握ったまま CDP を呼ぶ道があり、止めた `file://` の読み込みを
  待つ呼び出しとのあいだでデッドロックになる。`protectedPlaces` は操作中のプロファイルの場所を専用のロックで読み、
  検査器は使う時点で作る（ADR-0007 のとおり）。
- `--block-local` は従来どおり `file://` と `data:` をすべて拒む。`data:` は変えない（`data:` から `file://` への
  移動は Chrome が止める）。

## Alternatives considered

| 案 | 採らなかった理由 |
|---|---|
| `file://` を既定で全面的に拒み、フラグで許可する | ADR-0001 の主な用途（ローカル HTML の検証）が既定で使えなくなり、フラグを立てると元の穴（何でも読める）に戻る |
| `work_dir` を要らず、Local 方針だけで判定する | Local はホームの中の普通のファイル（`~/Documents` など）を通す。根が無いと、開けたページの JS がどこへでも移動できる |
| `Target.setAutoAttach` と `waitForDebuggerOnStart` で、新しいタブを読み込みの前に止める | 最も強いが、オンデマンドのアタッチというページ管理を作り直すことになる。読み取りの入り口 1 か所で止められるので今回は採らず、強化案として残す |
| Outbound 方針で判定する | ページの内容はモデルに渡るが、他のサーバーの読み取り（Local）と同じ扱いにする。`file://` のページは別のファイルを読めない（`fetch` は失敗し、iframe は別オリジン）ので、外へ送れるのは自分自身の内容だけである |

## Consequences

- **挙動の変更**: `work_dir` の外の `file://` は開けなくなる。`file://` を開くには `work_dir`（引数か `_meta`）が要る。
  `view-source:file://` も同じ規則になる。
- 付与の無いローカルファイルを表示しているページは、`navigate_page` でしか触れない。
- `Fetch` は常に `file://*` で有効になる。http の閲覧には影響しない（実測）。
- 残る限界: 付与した `work_dir` の中の HTML の JS は、その HTML 自身の内容を外へ送れる（ADR-0001 の egress の
  限界と同じ）。まだアタッチしていないタブには割り込みが効かないが、読み取りの入り口で止まる。
- ADR-0001 の「`file://` / `data:` の強制はツール引数の層だけ」は、`file://` については改める。

## References

- ADR-0001（ホスト allow/block リスト）、ADR-0006（書き込みとアップロードは `work_dir` の中）、ADR-0007（pathguard）
- 組織 ADR-021（work dir 契約）、ADR-022（パスの判定を 1 つに）
