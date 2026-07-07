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
against `SHASUMS256.txt`.

**預編譯（推薦）** —— 從 [latest release](https://github.com/linhn0617/clio/releases/latest)
下載你平台的檔案（macOS/Linux `amd64`+`arm64`、Windows `amd64`），放進 `PATH`，
用 `SHASUMS256.txt` 驗證。

**Or via `go install`** / **或用 `go install`：**

```bash
go install github.com/linhn0617/clio/cmd/clio@latest
```

Make sure `$(go env GOPATH)/bin` is on your `PATH`. Verify / 確認：

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
if indexing succeeds — registers clio in `~/.claude.json` atomically (preserving
your other MCP servers, and leaving a `.bak` of the previous version for manual
recovery). Then **restart Claude Code**.

中文：從 `~/.claude/projects/` 建立完整索引（顯示進度），**只有索引成功**才把 clio
atomic 寫進 `~/.claude.json`（保留你其他的 MCP server，並留下改寫前版本的 `.bak`
供手動復原）。接著**重開 Claude Code**。

---

## 3. Inside Claude Code (MCP) / 在 Claude Code 裡使用

After restarting, just ask in natural language — Claude calls clio over MCP:

重開後直接用自然語言問，Claude 會透過 MCP 呼叫 clio：

> "What did we work on last week?" / 「上週我們做了什麼？」
> "How did we fix that auth bug?" / 「之前那個 auth bug 怎麼修的？」
> "Find where we discussed the DB migration." / 「找一下我們討論資料庫遷移的地方。」

The MCP server exposes five tools / MCP server 提供五個工具：

| Tool | English | 中文 |
|------|---------|------|
| `search` | Full-text search across all conversations (tool output excluded by default) | 跨所有對話全文搜尋（預設排除 tool output） |
| `ask` | Cited evidence bundle answering a question (windowed excerpts grouped by session) for Claude to synthesize from | 回答問題的帶引用證據包（依 session 分組的加窗片段），交給 Claude 合成 |
| `list_sessions` | List sessions by date/project/turn, or file touched / tool used / command run (subagent children excluded by default; `include_subagents` includes them) | 依日期/專案/turn，或動過的檔／用過的工具／跑過的指令列出 session（子代理子項預設不列入；`include_subagents` 會列入） |
| `activity_summary` | Counts by day/project, or most-used files/commands/tools/patterns/URLs | 依天/專案，或最常動的檔/指令/工具/pattern/URL 統計 |
| `read_session` | Read one session in full, paginated; reports a parent's subagents (`include_subagents` inlines their messages) | 分頁讀取單一 session 全文；回報母體的子代理（`include_subagents` 連訊息一起內嵌） |

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

### `clio ask <question>` — answer from history / 從歷史回答

Retrieval-only: clio finds the most relevant excerpts (windowed in their
surrounding turns, grouped by session, cited) and prints them — it does not
generate an answer. Over MCP, Claude synthesizes from the bundle.

只檢索：clio 找出最相關的片段（含前後 turn、依 session 分組、附引用）印出來，本身
不生成答案；透過 MCP 時由 Claude 從證據包合成。

| Flag | English | 中文 | Default |
|------|---------|------|---------|
| `--project` | Limit to a project path prefix | 限定專案路徑前綴 | all / 全部 |
| `--since` | Only consider sessions since this time | 只考慮此時間後的 session | — |
| `--limit` | Max sessions in the bundle | 最多幾個 session | `6` |
| `--window` | Dialogue turns each side of a match | 命中前後各幾個對話 turn | `2` |
| `--json` | Output the bundle as JSON | 以 JSON 輸出 | `false` |

```bash
clio ask "how did we fix the auth bug"
clio ask "資料庫遷移的設計" --since 30d --window 3
clio ask "rate limiter" --project myapp --json
```

### `clio list` — browse sessions / 瀏覽 session

| Flag | English | 中文 | Default |
|------|---------|------|---------|
| `--since` | Only sessions since this time | 只看這時間之後的 session | — |
| `--project` | Filter by project path prefix | 依專案路徑前綴過濾 | — |
| `--min-turns` | Only sessions with at least this many turns | 至少這麼多 turn 的 session | `0` |
| `--limit` | Maximum number of sessions | session 上限 | `50` |
| `--include-subagents` | Also list subagent child sessions (hidden by default; see [Subagents](#subagents--子代理)) | 連子代理子 session 一起列（預設隱藏；見 [子代理](#subagents--子代理)） | `false` |
| `--json` | Output JSON | JSON 輸出 | `false` |

```bash
clio list --since 14d --limit 8
clio list --project myapp --min-turns 10
clio list --include-subagents             # show the agent-… child sessions too
clio list --json
```

Example output / 範例輸出 (subagents are hidden; a parent shows its count / 子代理隱藏，母體顯示數量):

```
2f4d1a81  2026-05-23 19:48   36 turns  clio  claude --continue  (+42 subagents)
ffe5096c  2026-06-17 23:29    6 turns  lin  幫我找一下 obsidian-vault 的資料夾在哪
```

### `clio show <session-uuid-or-prefix>` — read a full session / 讀完整對話

| Flag | English | 中文 | Default |
|------|---------|------|---------|
| `--format` | Output format (`markdown`\|`json`\|`raw`) | 輸出格式 | `markdown` |
| `--no-tool-output` | Omit tool output | 省略 tool 輸出 | `false` |
| `--include-subagents` | Also inline this session's subagent transcripts | 連同此 session 的子代理逐字稿一起內嵌 | `false` |

EN: The argument accepts a full session UUID or an unambiguous prefix (e.g. the
short id shown by `list`/`search`).
中文：參數接受完整 session UUID 或不會混淆的前綴（例如 `list`/`search` 顯示的短 id）。

```bash
clio show 2f4d1a81                           # markdown (default)
clio show 2f4d1a81 --format json
clio show 2f4d1a81 --format raw              # original JSONL events / 原始事件
clio show 2f4d1a81 --no-tool-output
```

### `clio tui` — interactive dashboard / 互動式儀表板

A full-screen terminal dashboard over the same index, **read-only**. Four tabs,
each with a list on the left and a detail pane on the right (what the pane shows
depends on the tab — see below).

全螢幕的終端機儀表板，唯讀地跑在同一個索引上。四個分頁，左邊清單、右邊是對應的細節窗格
（窗格內容依分頁而定，見下）。

| Tab / 分頁 | English | 中文 |
|------|---------|------|
| **Search** | Live full-text search (debounced); the selected hit is highlighted in its surrounding turns | 即時全文搜尋（防抖）；選中的命中在前後 turn 中高亮 |
| **Browse** | Recent sessions; a parent that spawned subagents shows `▸+N` and expands to a message preview of the selection | 最近的 session；有子代理的母 session 顯示 `▸+N`，可展開；右邊預覽選取的訊息 |
| **Activity** | Top files / commands / tools; drill into the sessions behind each entry | 最常動的檔／指令／工具；鑽進每項背後的 session |
| **Ask** | A question's cited evidence groups with their windowed excerpts | 問題的帶引用證據組與加窗片段 |

| Key / 按鍵 | Action / 動作 |
|-----|--------|
| `Tab` / `Shift-Tab`, or `1`-`4` | Switch tabs (digits work on Browse/Activity) / 切換分頁（數字鍵在 Browse/Activity 有效） |
| `↑` `↓` | Move the selection, on any tab / 移動選取（任一分頁） |
| `j` / `k` | Move the selection on Browse/Activity (query text on Search/Ask) / 在 Browse/Activity 移動選取（在 Search/Ask 是查詢文字） |
| `Enter` | On Browse, expand/collapse a parent's subagents / 在 Browse 展開或收合子代理 |
| `Esc` / `Ctrl-C` | Quit, from any tab / 離開（任一分頁） |
| `q` | Quit on Browse/Activity (it is query text on Search/Ask) / 在 Browse/Activity 離開（在 Search/Ask 是查詢文字） |

`clio tui` takes no flags and opens like `search`: a quick incremental catch-up
first (or it defers to a running MCP server), and if no index exists yet it exits
with a hint to run `clio index`.

`clio tui` 不吃旗標，開啟方式跟 `search` 一樣：先做一次增量補進度（若有在跑的 MCP
server 就交給它），若還沒有索引會提示你先跑 `clio index`。

```bash
clio tui
```

### Subagents / 子代理

Claude Code's Task tool spawns **subagents** (e.g. `general-purpose`, `Explore`),
each with its own transcript file. clio links a subagent transcript to the
conversation that spawned it and hides it from listings by default, so your
history stays uncluttered while the subagent's work stays searchable.

Claude Code 的 Task 工具會派出**子代理**（例如 `general-purpose`、`Explore`），每個
都有自己的逐字稿。clio 把子代理的逐字稿連到「派出它的那場對話」底下，並在清單中預設
隱藏，所以歷史清單不被洗版，子代理做的事也照樣搜得到。

- **`clio list`** hides subagents by default and tags a parent with `(+N subagents)`; `--include-subagents` lists them too. (A subagent whose parent isn't in the current listing — filtered out by `--since`/`--project` or past the `--limit` — is still shown, so recent work is never lost.)
- **`clio show <parent>`** lists the parent's subagents (id · type · title); `--include-subagents` inlines their transcripts (subject to `--limit`). `clio show <agent-id>` reads one subagent, with a header naming its parent and type.
- **`clio search`** still finds subagent content and labels such hits with `↳<type>`.
- **TUI Browse** nests a parent's subagents beneath it (`Enter` to expand/collapse); a subagent whose parent isn't on the current page appears as its own row.
- **MCP** mirrors this: `list_sessions` and `read_session` take an `include_subagents` parameter, and `search` results carry `parent_session` / `agent_type`.

- **`clio list`**：預設隱藏子代理，並在母 session 標 `(+N subagents)`；`--include-subagents` 連子代理一起列。（若某子代理的母體不在當前清單裡——被 `--since`/`--project` 濾掉或超出 `--limit`——它仍會顯示，近期工作不會遺漏。）
- **`clio show <母體>`**：列出該母體的子代理（id · 類型 · 標題）；`--include-subagents` 內嵌它們的逐字稿（受 `--limit` 限制）。`clio show <agent-id>` 讀單一子代理，並在開頭標明其母體與類型。
- **`clio search`**：照樣搜得到子代理內容，並把這類命中標上 `↳<類型>`。
- **TUI Browse**：把子代理巢狀在母體底下（`Enter` 展開／收合）；母體不在當頁的子代理會自成一列。
- **MCP**：同步支援 —— `list_sessions`、`read_session` 接受 `include_subagents` 參數，`search` 結果帶 `parent_session` / `agent_type`。

### `clio activity` — what you worked on / 你做了什麼

Summarize indexed activity. `clio search` and `clio list` also accept
`--touched <path>`, `--tool <name>`, and `--ran <substring>` to filter by it.

統整索引到的活動。`clio search` 與 `clio list` 也可用 `--touched <路徑>`、
`--tool <名稱>`、`--ran <子字串>` 依活動過濾。

| Flag | English | 中文 | Default |
|------|---------|------|---------|
| `--by` | Group by `file`\|`command`\|`tool`\|`pattern`\|`url` | 依類別分組 | `file` |
| `--since` | Period start | 期間起點 | — |
| `--project` | Filter by project path prefix | 依專案前綴過濾 | — |

```bash
clio activity --by file --since 7d        # files touched most / 最常動的檔
clio activity --by command --since 30d    # commands run most / 最常跑的指令
clio list --touched src/auth.ts           # sessions that edited a file / 編輯過某檔的 session
clio search "panic" --ran "go test"       # matches from sessions that ran a command
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
clio recall           # recent-activity digest for the current project / 目前專案的近況摘要
clio install-hook     # opt in: inject the recall digest at each session start / 啟用：每個 session 開始注入近況摘要
clio uninstall-hook   # remove the recall SessionStart hook / 移除近況 SessionStart hook
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
| Index looks stale / wrong / 索引看起來舊或怪 | `clio index` (incremental) or `clio index --full` (rebuild); `clio doctor` to diagnose. / `clio index` 或 `clio index --full`；`clio doctor` 診斷。 |

---

## 9. Cheat sheet / 速查表

```bash
clio install-mcp                                  # set up (index + register MCP)
clio search "<query>" [--since 7d] [--project X] [--role user|assistant] [--touched P] [--tool T] [--ran S] [--limit N] [--json]
clio ask "<question>" [--since 7d] [--project X] [--limit N] [--window N] [--json]
clio list [--since 7d] [--project X] [--min-turns N] [--touched P] [--tool T] [--ran S] [--limit N] [--json]
clio show <uuid-or-prefix> [--format markdown|json|raw] [--no-tool-output]
clio activity --by file|command|tool|pattern|url [--since 7d] [--project X]
clio index [--full]                              # (re)index manually
clio recall                                      # recent-activity digest (current project)
clio doctor                                      # health check
clio uninstall-mcp                               # remove MCP integration
clio install-hook / uninstall-hook               # opt in/out of the session-start recall digest
```
