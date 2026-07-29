# ADR-0003: ブラウザプロファイルの指定とアイソレーション

- Status: Proposed
- Date: 2026-07-29
- Driver: magi
- Depends on: ADR-0002 (config.toml スキーマ)

---

## Context

現状の launch モードは毎回 fresh な一時 user-data-dir で起動し、終了時に
削除する (完全アイソレーション)。これは安全な既定だが、次のユースケース
に応えられない:

- **ログイン状態の保持**: 検証対象サービスに一度ログインし、以後の
  セッションでも Cookie を使い回したい (毎回 `--attach` で本物の
  ブラウザに繋ぐのは過剰で、ADR-0001 の境界も弱い)
- **用途別の分離**: 「業務検証用」「実験用」など、状態を持つ
  プロファイルを互いに混ざらないよう使い分けたい

一方で危険も増える: 永続プロファイルは認証済み状態の置き場になるため、
ユーザーの**実 Chrome プロファイルとの誤用・混同**は絶対に防ぐ必要がある。

## Decision

### 3 モード

| モード | 指定 | 挙動 |
|---|---|---|
| ephemeral (既定) | 指定なし | 一時 dir で起動、終了時に削除。現状どおり |
| named profile | `--profile <name>` | ツール管理ディレクトリ配下の永続プロファイル。なければ作成、終了時に**削除しない** |
| explicit path | `--user-data-dir <path>` | 指定パスをそのまま使用。なければ作成、終了時に削除しない |

- named profile の実体: `os.UserConfigDir()/chrome-pilot-mcp/profiles/<name>`
  (macOS: `~/Library/Application Support/chrome-pilot-mcp/profiles/<name>`)。
  作成時 perm 0700。
- `<name>` は `[a-zA-Z0-9_-]+` に制限 (パス区切り・`..` を構文的に排除)。
- config.toml (ADR-0002) には `[browser]` に `profile` / `user_data_dir`
  キーを追加。優先順位は ADR-0002 どおり flags > config。

### ガードレール

1. `--profile` と `--user-data-dir` の**同時指定はエラー**。
2. `--attach` との併用はエラー (attach 先のプロファイルは変更できない)。
3. `--user-data-dir` が**実 Chrome の既定プロファイル領域**
   (macOS `~/Library/Application Support/Google/Chrome*`、Linux
   `~/.config/google-chrome*` / `~/.config/chromium*`、Windows
   `%LOCALAPPDATA%\Google\Chrome\User Data`) を指す場合は**起動拒否**。
   実ブラウザ資産の破壊と SingletonLock 衝突を防ぐ。実プロファイルを
   使いたい正当な用途は `--attach` が担う。
4. 同一プロファイルの二重起動は Chrome の ProcessSingleton により
   失敗する。launcher はこれを検出し、「profile is in use by another
   Chrome instance」という明確な `browser_launch_failed` に変換する
   (生の「endpoint が出ずに終了」エラーのままにしない)。

### セキュリティ上の位置づけ

named profile + ADR-0001 の allow-list の組み合わせを推奨形とする:
「認証済み状態を持つが、行き先はホワイトリストで閉じた自動化環境」。
ドキュメントに、永続プロファイルには認証状態が蓄積されること・
ディレクトリのバックアップや共有はしないことを明記する。

## Alternatives considered

- **実 Chrome プロファイルの直接使用を許可**: SingletonLock 衝突、
  プロファイルバージョンの非互換、拡張機能の副作用など危険が多い。
  attach モードが既にこの用途を安全に担っており不採用。
- **プロファイルのコピー起動 (実プロファイルの clone)**: 巨大 (数 GB) で
  起動が遅く、認証状態の複製はセキュリティ的にも筋が悪い。不採用。
- **profile を workspace (ADR 外) 配下に置く**: workspace は成果物置き場
  で一時 dir が既定のため、永続データの置き場として不適。不採用。

## Consequences

- 既定挙動 (ephemeral) は変更なし — アイソレーションが標準のまま。
- プロファイル一覧・削除はスコープ外 (ディレクトリを直接見れば分かる。
  必要になったら `profiles` サブコマンドを検討)。
- 永続プロファイルは Chrome バージョンアップに追従してマイグレーション
  される (Chrome 自身の挙動)。ダウングレード非互換はドキュメントに注記。
