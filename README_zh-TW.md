# clio

在終端機搜尋、瀏覽你的 Claude Code 對話歷史 —— 也讓 Claude 自己能查。

[English](./README.md) | 繁體中文

clio 把 Claude Code 的 session 檔（`~/.claude/projects/*.jsonl`）索引進本機的 SQLite + FTS5 資料庫，提供兩條介面：

- **CLI** —— `clio search` / `clio ask` / `clio list` / `clio show`，跨所有專案搜尋、提問、閱讀過去的對話,或用 `clio tui` 開互動式儀表板
- **MCP server** —— 讓 Claude Code 在 session 內反查自己的歷史（「上週我們討論了什麼？」、「那個 bug 之前怎麼修的？」）

本機優先（local-first）、對 `.claude` 資料唯讀，絕不寫入你原始的 session 檔。

📖 **完整使用教學（中英對照）：** [docs/USAGE.md](./docs/USAGE.md)

## 為什麼不直接用 `grep`？

你當然可以 `grep ~/.claude/projects/*.jsonl` —— 但這正是 clio 想做得更好的事：

- **跨 session 的記憶。** 全新的 Claude Code session 看不到過去的對話；clio 給 Claude（和你）一份涵蓋所有專案、所有對話的可搜尋索引，不只是當下開著的那個。
- **依相關性排序。** 多數查詢依相關性（BM25）與近期程度排序，你要找的那段會浮到最前面 —— 而不是把每一行含到那個字的都照檔案順序丟出來。（1–2 字的極短查詢，例如部分中文詞，會退回依時間排序的子字串掃描。）
- **是訊號，不是雜訊。** tool output（檔案傾印、指令輸出）預設排除、snippet 也會裁剪，所以你 —— 還有 Claude 的 context —— 拿到的是相關的幾行，不是好幾 MB 的 JSONL。
- **精準命中那一個 session。** session 用 id 或不會混淆的前綴解析，你打開的就是你要的那個 —— 不是剛好含到那串字的某個檔。

## 開始使用

**1. 安裝執行檔**

**預編譯（推薦）** —— 從 [最新 release](https://github.com/linhn0617/clio/releases/latest)（目前為 [**v0.9.1**](https://github.com/linhn0617/clio/releases/tag/v0.9.1)；macOS/Linux `amd64`+`arm64`、Windows `amd64`）下載你平台的檔案，放進 `PATH`，並用 `SHASUMS256.txt` 驗證。

**或用 `go install`：**

```
go install github.com/linhn0617/clio/cmd/clio@v0.9.1   # 或用 @latest 取得最新
```

這會把 `clio` 放進 `$(go env GOPATH)/bin`，確認該目錄在 `PATH` 裡。

**2. 索引歷史並註冊 MCP server**

```
clio install-mcp
```

它依序做兩件事：
1. 從 `~/.claude/projects/` 建立完整索引（顯示進度）。
2. 只有索引成功後，才把 clio 註冊進 `~/.claude.json` —— 採 atomic 寫入、留 `.bak` 備份、保留你其他的 MCP server。

**3. 重開 Claude Code**

接著就能問它過去的工作：

> 「上週我們做了什麼？」
> 「之前那個 auth bug 是怎麼修的？」
> 「找一下我們討論資料庫遷移的地方。」

Claude 會透過 MCP 呼叫 clio 來回答。Claude Code 開著的時候，clio 的 MCP server 會監看 `~/.claude/projects/`，自動保持索引最新。

**4.（選用）直接用 CLI**

```
clio tui                        # 互動式儀表板：搜尋／瀏覽／活動／提問
clio search "驗證 流程"          # 全文搜尋（中文 + 程式碼）
clio ask "那個 auth bug 怎麼修的"        # 針對問題的帶引用證據包（不生成）
clio list --since 7d --touched auth.ts   # 瀏覽最近 session，依活動過濾
clio show <session-id>          # 印出完整對話（markdown|json|raw）
clio show <id> --include-subagents         # ...並內嵌其 Claude Code 子代理逐字稿
clio activity --by file --since 7d        # 動過的檔／跑過的指令／用過的工具
clio search "race" --source codex          # 也索引／搜尋 OpenAI Codex CLI 歷史（opt-in；預設只看 Claude Code）
clio activity --by command --source codex  # ...Codex 的指令／檔案／工具活動也能拆解
clio recall                               # 目前專案的近況摘要
clio doctor                     # 健康檢查
```

之後要移除整合：`clio uninstall-mcp`。

想讓每個新 session 一開始就帶近況摘要？用 `clio install-hook` 啟用（`clio uninstall-hook` 移除）。

## 索引如何保持最新

- **MCP server 執行中**（Claude Code 開著）：file watcher 即時 ingest 新活動，你完全不用手動跑。
- **MCP server 沒在跑**：每個 CLI 指令查詢前會做一次快速增量補抓。隨時可 `clio index` 強制執行；`clio index --full` 從頭重建。

短查詢（1-2 個字，例如大多數中文詞）會自動 fallback 到子字串掃描，因為 trigram 索引需要 3 個字以上才能匹配。

## MCP tools

透過 `clio install-mcp` 註冊後，Claude Code 可呼叫：

| Tool | 用途 |
|------|------|
| `search` | 全文搜尋、**依相關性與近期排序**（短查詢會退回子字串掃描；預設排除 tool output） |
| `ask` | 從歷史回答問題：回傳最相關片段的帶引用證據包（依 session 分組、含前後 turn），交給 Claude 合成 |
| `list_sessions` | 依日期/專案/turn 數，或依動過的檔／用過的工具／跑過的指令列出 session；子代理子項預設隱藏（母體帶 `subagent_count`），`include_subagents` 會以帶 `parent_session` / `agent_type` 的列補上 |
| `activity_summary` | 依天或專案，或你最常動的檔／指令／工具／搜尋 pattern／URL 統計（「我上週碰了什麼？」） |
| `read_session` | 分頁讀取單一 session 的完整內容；回報母體的子代理（`include_subagents` 連訊息一起內嵌） |

## 隱私

- 對 `~/.claude/projects/` 唯讀；絕不修改你的原始檔。
- ingest 時會 redact secret pattern（API key、token、private key、`.env` 行），可搜尋文字與儲存的原始事件都會處理。
- 所有資料留在本機；無遙測、無雲端同步。資料庫位於 `~/Library/Application Support/clio/db.sqlite`（macOS）或 `~/.local/share/clio/db.sqlite`（Linux），權限 `0600`。

## 授權

[MIT](./LICENSE)
