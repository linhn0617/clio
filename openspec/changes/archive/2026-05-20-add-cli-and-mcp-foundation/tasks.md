## 0. Scaffolding + CI

- [x] 0.1 建立 `go.mod`（module path、Go 1.25）、`cmd/clio/main.go`（cobra root command）
- [x] 0.2 `internal/config`：DB 路徑解析（macOS `~/Library/Application Support/clio/`、Linux `~/.local/share/clio/`、`XDG_DATA_HOME` 覆寫）
- [x] 0.3 `internal/` 各 package 空骨架（db / model / ingest / search / mcp / watcher / lock / cli）
- [x] 0.4 [P] `.github/workflows/ci.yml`：`go build ./...` + `go vet ./...` + `gofmt -l` + `go test ./...` + cross-compile matrix（darwin/linux × amd64/arm64）
- [x] 0.5 [P] `.gitignore`（DB、binary）、`README.md` 骨架
- [x] 0.6 驗收：`go build ./...` 過、`clio --help` 列出所有 subcommand

## 1. Data layer + ingest core

- [x] 1.1 `internal/db`：migrations SQL（`go:embed`）— sessions / messages / messages_fts(trigram) / tool_calls / ingest_state（含 last_byte_offset、tail_fingerprint、role 欄）
- [x] 1.2 `internal/db`：開 DB 設 `journal_mode=WAL`、`busy_timeout=3000`、`synchronous=NORMAL`、DB 檔 `0600`；migration runner（idempotent，可重跑）
- [x] 1.3 `internal/model`：`Event`（raw jsonl）、`Session`、`Message`、`ToolCall` 型別
- [x] 1.4 `internal/ingest/projectpath.go`：從第一個含 `cwd` 的 event 取 project_path（不 decode 目錄名；目錄名僅 fallback）
- [x] 1.5 `internal/ingest/redact.go`：secret-pattern redactor（AWS/GCP key、`Bearer <token>`、PRIVATE KEY header、`.env` KEY=value）→ `[REDACTED:type]`
- [x] 1.6 `internal/ingest/parser.go`：逐行 event → Message；tool_use 摘要（`Edit src/x.go`）；自我污染排除（跳過 clio 自己的 MCP tool_use/tool_result）；per-message >64KB FTS 截斷（raw_json 留完整）；套用 redact
- [x] 1.7 `internal/ingest/incremental.go`：`(size,mtime)` 快篩 + 尾段 fingerprint 校驗；從 last-complete-newline offset seek，未完成尾巴留下次；size 變小 → 整檔重做
- [x] 1.8 `internal/ingest/walker.go`：scan `~/.claude/projects/**/*.jsonl`
- [x] 1.9 `internal/ingest/ingest.go`：one-file-per-transaction（messages + FTS + ingest_state 同 tx，commit 後才算完）
- [x] 1.10 `internal/cli/index.go`：`clio index [--full]`（進度顯示）
- [x] 1.11 [P] Tests unit：parser golden（tool_use / image / summary / torn-line / 壞 JSON）
- [x] 1.12 [P] Tests unit：incremental（半行補齊、same-size rewrite 偵測、fingerprint 校驗、size 縮小重做）
- [x] 1.13 [P] Tests unit：cwd extraction（含 `-` / `_` 的真實路徑）、redactor pattern、自我污染排除、64KB 截斷
- [x] 1.14 Tests integration（**on-disk SQLite**）：tempdir 假 `.claude/projects/` → full ingest → 驗證 row + WAL 並發 reader 不 block writer + tx rollback on parse error
- [x] 1.15 驗收：對真實 `~/.claude/projects/` 跑 `clio index`，DB 正確、secrets redact、無 clio 自身 MCP traffic

## 2. Search

- [x] 2.1 `internal/search/query.go`：FTS5 MATCH passthrough + date/project/role filter SQL builder
- [x] 2.2 `internal/search/rank.go`：bm25 + role weight + recency prior 的 Go 端 post-rank
- [x] 2.3 `internal/search/snippet.go`：`snippet()` highlight（`【】` 包匹配）
- [x] 2.4 `internal/cli/search.go`：`clio search <query>`（`--since` / `--project` / `--role` / `--limit` / `--json` / `--include-tool-output`；預設排除 tool output）
- [x] 2.5 `internal/cli/list.go`：`clio list`（`--since` / `--project` / `--min-turns` / `--limit`）
- [x] 2.6 `internal/cli/show.go`：`clio show <uuid-prefix>`（`--format markdown|json|raw`、`--no-tool-output`、uuid 前綴匹配）
- [x] 2.7 [P] Tests：query builder flag 組合、ranking（對話 > tool output）、empty query error
- [x] 2.8 E2E：**CJK + 程式碼 trigram 搜尋**（搜「驗證」找到「資料驗證」、搜 camelCase）
- [x] 2.9 驗收：`clio search "<中文>"` 第一頁是真實對話非 log 雜訊；`clio show` 印完整 session

## 3. MCP server

- [x] 3.1 `internal/lock`：MCP-sole-writer lock file（pid，CLI 讀來判斷退讓 read-only；stale lock 偵測）
- [x] 3.2 `internal/mcp/schema.go`：四個 tool 的 input schema；limit clamp（default 10 / max 50）；read_session pagination 參數
- [x] 3.3 `internal/mcp/tools.go`：`search` / `list_sessions` / `activity_summary`（GROUP BY day/project）/ `read_session`（預設不含 tool output、分頁）handler
- [x] 3.4 `internal/mcp/server.go`：mark3labs/mcp-go stdio transport；所有 log/error 走 stderr、回 MCP 標準 error response
- [x] 3.5 `internal/cli/mcp.go`：`clio mcp`（啟動先跑一次增量補抓、寫 lock file、進 serve loop）
- [x] 3.6 [P] Tests：MCP golden response、limit clamp、pagination
- [x] 3.7 E2E：**stdout 純 JSON-RPC**（stderr/log 不污染 stdout）
- [x] 3.8 驗收：in-process MCP client 呼叫四個 tool 都回正確結果

## 4. Watcher + installer + doctor

- [x] 4.1 `internal/watcher`：fsnotify + 既有子目錄 walk-add + 新目錄 Create handler + debounce 500ms + 60s 保底全量 walk
- [x] 4.2 watcher 整合進 `clio mcp`：只在 MCP 模式啟用、寫 lock file、batch incremental ingest
- [x] 4.3 `internal/cli/install_mcp.go`：two-phase（Phase 1 full ingest 進度條 → Phase 2 atomic write + `.bak` backup 注入 `~/.claude.json` mcpServers）；驗證寫後可 parse 才刪 backup
- [x] 4.4 `internal/cli/install_mcp.go`：`clio uninstall-mcp`（atomic 移除 entry）
- [x] 4.5 `internal/cli/doctor.go`：路徑檢查、DB 完整性（`PRAGMA integrity_check`）、ingest 落差、孤兒 session、FTS 健康、**source-of-truth 對帳**（DB session event count vs `.jsonl`）、DB size warning
- [x] 4.6 [P] Tests E2E：**install-mcp**（`~/.claude.json` 既有 server 不被蓋、Phase 1 失敗不碰 config、中斷可從 `.bak` 復原）
- [x] 4.7 [P] Tests E2E：**watcher tailing 成長中 jsonl**（append → 自動 ingest、新目錄出現 → watch）
- [x] 4.8 [P] Tests：doctor 對帳（餵截斷 jsonl → 報警）
- [ ] 4.9 驗收：`clio install-mcp` 裝進真實 Claude Code、重開 session 問「上週做了什麼」MCP 回得出來（需使用者授權寫入真實 `~/.claude.json`）

## 5. Distribution + docs

- [x] 5.1 `.github/workflows/release.yml`：tag 觸發、cross-platform binary（darwin/linux × amd64/arm64）+ checksums + GitHub Release
- [x] 5.2 驗證 `go install <module>/cmd/clio@latest` 路徑可裝
- [x] 5.3 `README.md`：安裝、`clio install-mcp` 引導、四個 MCP tool 說明、隱私說明（redaction、本機 only、read-only）
- [x] 5.4 `openspec validate add-cli-and-mcp-foundation` 通過
- [ ] 5.5 完成後 `openspec archive add-cli-and-mcp-foundation`（landed 後再執行）
