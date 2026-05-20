# clio

在終端機搜尋、瀏覽你的 Claude Code 對話歷史 —— 也讓 Claude 自己能查。

[English](./README.md) | 繁體中文

clio 把 Claude Code 的 session 檔（`~/.claude/projects/*.jsonl`）索引進本機的 SQLite + FTS5 資料庫，提供兩條介面：

- **CLI** —— `clio search`、`clio list`、`clio show`，跨所有專案搜尋與閱讀過去的對話
- **MCP server** —— 讓 Claude Code 在 session 內反查自己的歷史（「上週我們討論了什麼？」、「那個 bug 之前怎麼修的？」）

本機優先（local-first）、對 `.claude` 資料唯讀，絕不寫入你原始的 session 檔。

## 開始使用

**1. 安裝執行檔**

```
go install github.com/linhn0617/clio/cmd/clio@latest
```

這會把 `clio` 放進 `$(go env GOPATH)/bin`，確認該目錄在你的 `PATH` 裡。
（或從 [Releases](https://github.com/linhn0617/clio/releases) 下載預編譯 binary：macOS/Linux/Windows、amd64/arm64。）

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
clio search "驗證 流程"          # 全文搜尋（中文 + 程式碼）
clio search "bug" --since 7d --project myapp --json
clio list --since 7d            # 瀏覽最近的 session
clio show <session-id>          # 印出完整對話（markdown|json|raw）
clio doctor                     # 健康檢查
```

之後要移除整合：`clio uninstall-mcp`。

## 索引如何保持最新

- **MCP server 執行中**（Claude Code 開著）：file watcher 即時 ingest 新活動，你完全不用手動跑。
- **MCP server 沒在跑**：每個 CLI 指令查詢前會做一次快速增量補抓。隨時可 `clio index` 強制執行；`clio index --full` 從頭重建。

短查詢（1-2 個字，例如大多數中文詞）會自動 fallback 到子字串掃描，因為 trigram 索引需要 3 個字以上才能匹配。

## MCP tools

透過 `clio install-mcp` 註冊後，Claude Code 可呼叫：

| Tool | 用途 |
|------|------|
| `search` | 跨所有對話全文搜尋（預設排除 tool output） |
| `list_sessions` | 依日期/專案/turn 數過濾列出 session |
| `activity_summary` | 依天或專案統計（「我上週做了什麼？」） |
| `read_session` | 分頁讀取單一 session 的完整內容 |

## 隱私

- 對 `~/.claude/projects/` 唯讀；絕不修改你的原始檔。
- ingest 時會 redact secret pattern（API key、token、private key、`.env` 行），可搜尋文字與儲存的原始事件都會處理。
- 所有資料留在本機；無遙測、無雲端同步。資料庫位於 `~/Library/Application Support/clio/db.sqlite`（macOS）或 `~/.local/share/clio/db.sqlite`（Linux），權限 `0600`。

## 授權

[MIT](./LICENSE)
