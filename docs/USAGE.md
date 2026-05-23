# clio Usage Guide / 使用教學

Complete usage guide for clio — bilingual (English / 繁體中文). Commands and flags
are shared; prose is given in both languages.

完整使用教學，中英對照。指令與參數共用，說明文字提供雙語。

> **What is clio?** It indexes Claude Code's session history
> (`~/.claude/projects/*.jsonl`) into a local SQLite + FTS5 database and exposes it
> two ways: a terminal **CLI** for searching/reading past conversations, and an
> **MCP server** that lets Claude Code query its own history mid-conversation.
> Local-first, read-only against your `.claude` data, secrets redacted at ingest.
>
> **clio 是什麼？** 它把 Claude Code 的對話歷史（`~/.claude/projects/*.jsonl`）索引進
> 本機 SQLite + FTS5，提供兩種用法：終端機 **CLI** 搜尋/閱讀過去對話，以及 **MCP
> server** 讓 Claude Code 在對話中反查自己的歷史。本機優先、對 `.claude` 唯讀、
> ingest 時自動遮蔽機密。

---

## 1. Install / 安裝

**Prebuilt (recommended)** — download the asset for your platform from the
[latest release](https://github.com/linhn0617/clio/releases/latest)
(macOS/Linux `amd64`+`arm64`, Windows `amd64`), put it on your `PATH`, verify
against `SHASUMS256.txt`. This repo is private, so download while signed in to GitHub.

**預編譯（推薦）** —— 從 [latest release](https://github.com/linhn0617/clio/releases/latest)
下載你平台的檔案（macOS/Linux `amd64`+`arm64`、Windows `amd64`），放進 `PATH`，
用 `SHASUMS256.txt` 驗證。此 repo 為 private，請在登入 GitHub 狀態下下載。

**Or via `go install`** / **或用 `go install`：**

```bash
export GOPRIVATE=github.com/linhn0617/*   # private repo: set once / 設定一次
go install github.com/linhn0617/clio/cmd/clio@latest
```

Make sure `$(go env GOPATH)/bin` is on your `PATH`, and your git/GitHub auth can
clone the private repo. Verify / 確認：

```bash
clio --version
clio --help
```

---

## 2. Quick start / 快速開始

```bash
clio install-mcp
```

EN: Builds the full index from `~/.claude/projects/` (with progress), then — only
if indexing succeeds — registers clio in `~/.claude.json` atomically (with a `.bak`
backup, preserving your other MCP servers). Then **restart Claude Code**.

中文：從 `~/.claude/projects/` 建立完整索引（顯示進度），**只有索引成功**才把 clio
atomic 寫進 `~/.claude.json`（留 `.bak` 備份、保留你其他的 MCP server）。接著
**重開 Claude Code**。

---

## 3. Inside Claude Code (MCP) / 在 Claude Code 裡使用

After restarting, just ask in natural language — Claude calls clio over MCP:

重開後直接用自然語言問，Claude 會透過 MCP 呼叫 clio：

> "What did we work on last week?" / 「上週我們做了什麼？」
> "How did we fix that auth bug?" / 「之前那個 auth bug 怎麼修的？」
> "Find where we discussed the DB migration." / 「找一下我們討論資料庫遷移的地方。」

The MCP server exposes four tools / MCP server 提供四個工具：

| Tool | English | 中文 |
|------|---------|------|
| `search` | Full-text search across all conversations (tool output excluded by default) | 跨所有對話全文搜尋（預設排除 tool output） |
| `list_sessions` | List sessions with date/project/turn filters | 依日期/專案/turn 數列出 session |
| `activity_summary` | Counts grouped by day or project | 依天或專案統計活動量 |
| `read_session` | Read one session in full, paginated | 分頁讀取單一 session 全文 |

While Claude Code runs, clio's MCP server watches `~/.claude/projects/` and keeps
the index current automatically — you never run anything manually.

Claude Code 開著時，clio 的 MCP server 會即時監看 `~/.claude/projects/`，自動保持
索引最新，你完全不用手動跑。

---

## 4. CLI reference / CLI 指令參考

### `clio search <query>` — full-text search / 全文搜尋

| Flag | English | 中文 | Default |
|------|---------|------|---------|
| `--since` | Only results since this time (`7d`, `12h`, `30m`, `YYYY-MM-DD`) | 只看這時間之後 | — |
| `--project` | Filter by project path prefix | 依專案路徑前綴過濾 | — |
| `--role` | Filter by role (`user`\|`assistant`) | 依角色過濾 | — |
| `--limit` | Maximum number of results | 結果上限 | `20` |
| `--include-tool-output` | Include tool output in results | 連 tool 輸出一起搜 | `false` |
| `--json` | Output JSON | JSON 輸出 | `false` |

```bash
clio search "資料驗證"                      # CJK works / 中文可
clio search "auth bug" --since 7d
clio search "panic" --project myapp
clio search "TODO" --role user --limit 50
clio search "race" --include-tool-output
clio search "lease" --since 7d --json       # machine-readable / 機器可讀
```

Example output / 範例輸出:

```
2f4d1a81  2026-05-23 12:27  clio  [assistant]
    …#2、[flock] 鎖死 …
```

```json
[
  {
    "MessageID": 59379,
    "SessionUUID": "2f4d1a81-71c4-40c7-90fd-a4fb7c95223d",
    "ProjectPath": "/Users/lin/Herd/clio",
    "Role": "assistant",
    "TS": 1779519278,
    "Snippet": "… Re[lease]。幫你釐…",
    "Score": 6.88
  }
]
```

### `clio list` — browse sessions / 瀏覽 session

| Flag | English | 中文 | Default |
|------|---------|------|---------|
| `--since` | Only sessions since this time | 只看這時間之後的 session | — |
| `--project` | Filter by project path prefix | 依專案路徑前綴過濾 | — |
| `--min-turns` | Only sessions with at least this many turns | 至少這麼多 turn 的 session | `0` |
| `--limit` | Maximum number of sessions | session 上限 | `50` |
| `--json` | Output JSON | JSON 輸出 | `false` |

```bash
clio list --since 14d --limit 8
clio list --project myapp --min-turns 10
clio list --json
```

Example output / 範例輸出:

```
2f4d1a81  2026-05-23 19:48   36 turns  clio  claude --continue
agent-a3  2026-05-23 15:01    1 turns  twin3-personal-agent-main  You are a MAINTAINABILITY specialist…
```

### `clio show <session-uuid-or-prefix>` — read a full session / 讀完整對話

| Flag | English | 中文 | Default |
|------|---------|------|---------|
| `--format` | Output format (`markdown`\|`json`\|`raw`) | 輸出格式 | `markdown` |
| `--no-tool-output` | Omit tool output | 省略 tool 輸出 | `false` |

EN: The argument accepts a full session UUID or an unambiguous prefix (e.g. the
short id shown by `list`/`search`).
中文：參數接受完整 session UUID 或不會混淆的前綴（例如 `list`/`search` 顯示的短 id）。

```bash
clio show 2f4d1a81                           # markdown (default)
clio show 2f4d1a81 --format json
clio show 2f4d1a81 --format raw              # original JSONL events / 原始事件
clio show 2f4d1a81 --no-tool-output
```

### `clio index` — (re)build the index / 建立索引

| Flag | English | 中文 |
|------|---------|------|
| `--full` | Force a full re-index instead of incremental | 強制從頭重建（而非增量） |

```bash
clio index            # incremental / 增量
clio index --full     # rebuild from scratch / 從頭重建
```

### Other commands / 其他指令

```bash
clio install-mcp      # index + register MCP in ~/.claude.json / 索引並註冊 MCP
clio uninstall-mcp    # remove clio from ~/.claude.json (keeps your data) / 移除整合（不刪資料）
clio doctor           # diagnose paths, DB integrity, ingest health / 健康檢查
clio mcp              # run the stdio MCP server (Claude Code launches this) / 跑 MCP server（通常由 Claude Code 啟動）
```

`clio doctor` example / 範例:

```
[ok  ] claude projects dir    /Users/lin/.claude/projects
[ok  ] db integrity           ok
[ok  ] fts index              32057 messages / 32057 fts rows
[ok  ] ingest coverage        301 files on disk, 301 tracked
[ok  ] db size                db 342.0 MB, ~2.5x source
```

---

## 5. How indexing stays current / 索引如何保持最新

EN:
- **MCP server running** (Claude Code open): a file watcher live-ingests new
  activity; nothing to run manually.
- **MCP server not running**: each CLI command does a quick incremental catch-up
  before querying. Run `clio index` to force one; `clio index --full` rebuilds.
- Short queries (1–2 chars, e.g. most CJK words) fall back to a substring scan,
  since the trigram FTS index needs 3+ characters.

中文：
- **MCP server 執行中**（Claude Code 開著）：file watcher 即時 ingest 新活動，不用手動跑。
- **MCP server 沒在跑**：每個 CLI 指令查詢前會做快速增量補抓。可用 `clio index` 強制；
  `clio index --full` 從頭重建。
- 短查詢（1–2 字，例如多數中文詞）會 fallback 到子字串掃描，因為 trigram FTS 索引
  需要 3 字以上。

---

## 6. Multiple sessions at once / 多個 session 同時開

EN: You can open multiple Claude Code windows/tabs at the same time; each runs its
own `clio mcp` and all work. One instance is elected leader (via an OS-`flock`
lease) and runs the watcher; the others share the same index and serve reads from
a read-only connection. If the leader exits, another takes over automatically. No
action needed.

中文：你可以同時開多個 Claude Code 視窗/分頁，每個都跑自己的 `clio mcp` 且全部正常。
其中一個會被選為 leader（用 OS-`flock` lease）負責 watcher，其餘共享同一份索引、
以唯讀連線服務查詢。leader 關掉時別人自動接手。你不用做任何事。

---

## 7. Data & privacy / 資料與隱私

EN:
- DB at `~/Library/Application Support/clio/db.sqlite` (macOS) or
  `~/.local/share/clio/db.sqlite` (Linux), `0600` permissions.
- Read-only against `~/.claude/projects/`; your original files are never modified.
- Secret patterns (API keys, tokens, private keys, `.env` lines) are redacted at
  ingest, in both searchable text and the stored raw event.
- All local; no telemetry, no cloud sync.

中文：
- DB 位於 `~/Library/Application Support/clio/db.sqlite`（macOS）或
  `~/.local/share/clio/db.sqlite`（Linux），權限 `0600`。
- 對 `~/.claude/projects/` 唯讀；絕不修改你的原始檔。
- ingest 時遮蔽機密（API key、token、private key、`.env` 行），可搜尋文字與儲存的
  原始事件都會處理。
- 全部本機；無遙測、無雲端同步。

---

## 8. Troubleshooting / 疑難排解

| Symptom / 症狀 | Fix / 解法 |
|---|---|
| CJK/short query finds nothing / 中文或短詞搜不到 | 1–2 chars fall back to substring scan; add more characters or quote a phrase. / 1–2 字會走子字串掃描，多打幾字或用引號。 |
| clio tools not in Claude Code / Claude Code 裡沒有 clio 工具 | Run `clio install-mcp` and **restart** Claude Code; check with `/mcp`; run `clio doctor`. / 跑 `clio install-mcp` 並**重開**；用 `/mcp` 確認；`clio doctor` 檢查。 |
| `go install` fails (private repo) / `go install` 失敗 | Set `GOPRIVATE=github.com/linhn0617/*` and ensure git/GitHub auth can clone. / 設 `GOPRIVATE` 且 git 能認證 clone。 |
| Index looks stale / wrong / 索引看起來舊或怪 | `clio index` (incremental) or `clio index --full` (rebuild); `clio doctor` to diagnose. / `clio index` 或 `clio index --full`；`clio doctor` 診斷。 |

---

## 9. Cheat sheet / 速查表

```bash
clio install-mcp                                  # set up (index + register MCP)
clio search "<query>" [--since 7d] [--project X] [--role user|assistant] [--limit N] [--json]
clio list [--since 7d] [--project X] [--min-turns N] [--limit N] [--json]
clio show <uuid-or-prefix> [--format markdown|json|raw] [--no-tool-output]
clio index [--full]                              # (re)index manually
clio doctor                                      # health check
clio uninstall-mcp                               # remove MCP integration
```
