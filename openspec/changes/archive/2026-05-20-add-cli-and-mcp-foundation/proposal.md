## Why

Claude Code 把每個對話寫成 `~/.claude/projects/<encoded-cwd>/<uuid>.jsonl`，內建的 `/resume` picker 只能列當前 cwd 的 session、不支援跨專案搜尋、找不到「兩週前在另一個專案討論過的某段對話」。第三方商業工具 Histo 補上了這塊，但 closed-source、需要付費才能用全部歷史、且不能控制資料路徑。

`clio` 是同樣定位的開源 Go 工具，提供兩條介面：CLI 給人直接查、MCP server 給 Claude Code session 內反查自己的長期記憶。第一版聚焦「索引 + 搜尋 + 跨專案瀏覽」這個最小可用核心，不做 GUI、不做雲端同步。

## What Changes

- **新增** 單一 Go binary `clio`，包含 6 個 subcommand：`index` / `search` / `list` / `show` / `mcp` / `doctor` / `install-mcp`
- **新增** SQLite 持久層（`modernc.org/sqlite`，純 Go、免 cgo），DB 位於 `~/Library/Application Support/clio/db.sqlite`（macOS）/ `~/.local/share/clio/db.sqlite`（Linux）
- **新增** FTS5 全文索引，採用 `trigram` tokenizer 以支援中文與程式碼搜尋
- **新增** `.jsonl` 增量 ingest 機制：基於 `(file path, size, mtime, last_byte_offset)` 的 append-aware 解析，避免重 parse 整檔
- **新增** stdio MCP server，暴露 4 個 tool：`search` / `list_sessions` / `activity_summary` / `read_session`
- **新增** fsnotify 檔案監聽（僅 `clio mcp` 模式啟動），debounce 500ms 後批次增量 ingest，含新建子目錄的 Create 事件處理 + 60s 保底全量 walk
- **新增** `clio install-mcp` 子指令：atomic write + `.bak` backup 寫入 `~/.claude.json`，並同步跑初始 full ingest（含進度條）
- **新增** project path 取得：從 `.jsonl` 第一個含 `cwd` 的 event 讀出（非目錄名 decode，因 Claude Code 把 `/` 與 `_` 都換成 `-`，目錄名不可逆）
- **新增** secret redaction：ingest 時對 tool output 套用 secret-pattern 過濾（AWS/GCP key、Bearer token、private key header、`.env` 行），content 與 raw_json 皆 redact
- **新增** 自我污染排除：ingest 時跳過 clio 自己的 MCP `tool_use` / `tool_result`
- **新增** OpenSpec 專案治理：`openspec/` 結構、`AGENTS.md` / `CLAUDE.md` 整合

## Capabilities

### New Capabilities

- `session-ingest`：scan `~/.claude/projects/`、解析 `.jsonl` event 流、increment-aware（last-complete-newline offset + 尾段 fingerprint 校驗）寫入 SQLite，含 cwd 取得、tool_use 摘要、parent-session 連結、secret redaction、自我污染排除、per-message 64KB FTS 截斷、one-file-per-transaction
- `fts-search`：FTS5 trigram tokenizer 索引、bm25 + role 加權 + recency prior 的 post-rank、snippet/highlight、date/project/role filter、預設排除 tool output（`--include-tool-output` 才納入）
- `cli-surface`：cobra 框架下 6 個 subcommand 的 flag/輸出規格（文字 + JSON 模式），MCP 在跑時 CLI 退讓為 read-only
- `mcp-server`：mark3labs/mcp-go stdio transport、4 個 tool 的 input schema 與 handler、limit/pagination 防止 context overflow、所有錯誤走 stderr / MCP error response（stdout 純 JSON-RPC）
- `file-watcher`：fsnotify 監聽（含子目錄 watch 管理 + 新目錄 Create 處理 + 60s 保底 walk）、debounce、batch ingest、僅在 MCP 模式啟用、MCP 為唯一 writer（lock file 標記）
- `mcp-installer`：atomic write + `.bak` backup 注入/移除 `~/.claude.json` server entry、idempotent、two-phase（先 ingest 再寫 config，失敗語義明確）
- `diagnostics`：`clio doctor` 檢查路徑、DB 完整性、ingest 落差、孤兒 session、FTS index 健康、source-of-truth 對帳（偵測缺尾段 / 重複 ingest / 自我污染）、DB size warning
