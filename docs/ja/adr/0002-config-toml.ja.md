# ADR-0002: config.toml サポート (自作 TOML サブセットローダー)

- Status: Proposed
- Date: 2026-07-29
- Driver: magi
- Supersedes: RFP §2 Configuration「CLI フラグのみ」
- Generalises to: 依存ゼロ方針の他ツールでの設定ファイル対応

---

## Context

起動オプションが増え (browser 系 6 種 + workspace + ADR-0001 の
security 3 種)、MCP クライアント設定の `args` 配列に全部並べるのは
可読性・再利用性が悪い。org 標準の sectioned TOML config
([[project_config_conventions]]) に揃えたい。

ただし本プロジェクト特有の制約が 2 つある:

1. **依存ゼロ方針** — org の Go loader パターンは `BurntSushi/toml` だが、
   外部 module は追加できない (CLAUDE.md 規則 1)。
2. **config 自体が攻撃面** — `executable_path` は任意バイナリ実行に、
   `allow_hosts` は ADR-0001 の境界緩和に直結する。cwd の config を
   自動で読む設計 (data-toolbox-mcp は `./config.toml` も探索する) は、
   「クローンしてきたリポジトリを cwd にして MCP サーバーを起動」という
   よくある形で**リポジトリ内の攻撃者ファイルを設定として読む**リスクを
   持ち込む。

## Decision

### 読み込みと優先順位

- `--config <path>` — 明示指定。ファイルがなければ**起動エラー**。
- 未指定時は `~/.config/chrome-pilot-mcp/config.toml` **のみ**を探索
  (なければ組み込みデフォルトで起動)。**`./config.toml` は読まない** —
  上記 Context 2 の config 注入リスクを避けるための、org 慣行からの
  意図的逸脱。
- 優先順位: **CLI フラグ > config.toml > 組み込みデフォルト**。
  フラグの明示指定は `flag.Visit` で検出し、指定されたものだけが
  config を上書きする。
- env var レイヤーは設けない (秘密情報を扱わず、Vertex 系統一の動機で
  ある credential 運用が存在しないため)。必要になったら
  `CHROME_PILOT_<FIELD>` で追補する。

### スキーマ (sectioned TOML)

```toml
[browser]
headless        = false
channel         = "stable"     # stable | beta | dev | canary
executable_path = ""
attach          = ""           # ws://... | port | host:port (loopback のみ)
viewport        = "1280x800"
profile         = ""           # 名前付き永続プロファイル (ADR-0003)
user_data_dir   = ""           # 明示パス (ADR-0003; profile と排他)

[workspace]
root = ""                      # 空 = 一時ディレクトリ

[security]                     # ADR-0001
allow_hosts = []               # 例: ["example.com", "*.example.com"]
block_hosts = []
block_local = false
```

キーは CLI フラグと 1:1 対応 (`--executable-path` ↔ `executable_path`)。

### 自作 TOML サブセットローダー

`internal/config` に自作する。対応する構文は config に必要な最小限:

- `[section]`、`key = value`、`#` コメント
- 値: 基本文字列 `"..."` (エスケープ `\\` `\"` `\n` `\t`)、整数、真偽値、
  文字列配列 `["a", "b"]` (単一行)
- **未対応構文は黙殺せず明確なエラー** (複数行文字列、dotted key、
  inline table、日付、float が来たら「unsupported TOML syntax at line N」)
- 未知のキー・セクションもエラー (strict decode 方針
  [[feedback_strict_json_decode]] と同じ)

正式 TOML パーサの再実装はしない。「chrome-pilot-mcp の config が
書ける TOML の部分集合」と定義し、ドキュメントに対応構文を明記する。

## Alternatives considered

- **BurntSushi/toml を追加**: loader 統一の観点では最善だが、依存ゼロが
  プロジェクトの存在理由であり本末転倒。不採用。
- **JSON config**: stdlib で完結するが、org の設定ファイル規約は
  sectioned TOML であり、コメントも書けない。不採用。
- **`./config.toml` も探索**: org 慣行だが Context 2 のリスクが
  本ツールでは実害になる。不採用 (明示 `--config` で代替可能)。

## Consequences

- MCP クライアント設定は `"args": ["--config", "/path/to/config.toml"]`
  または `~/.config/chrome-pilot-mcp/config.toml` 配置だけで済む。
- フラグ運用は完全に従来互換 (config なしでも全機能が使える)。
- TOML サブセットの保守コストを負う (テーブルテストで固定する)。
  サブセット外の構文要望が出たら、その時点で対応可否を判断する。
- RFP §2 の「CLI フラグのみ・config ファイルなし」は本 ADR で更新される。
