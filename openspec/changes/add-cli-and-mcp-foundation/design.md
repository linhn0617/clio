## Context

Claude Code 的對話歷史以 newline-delimited JSON 的形式儲存在 `~/.claude/projects/<encoded-cwd>/<session-uuid>.jsonl`。每一行是一個自包含的 event，type 包含 `user`、`assistant`、`system`、`tool_use`、`tool_result`、`summary`。檔案是 append-only：每加一個 turn 就在尾端 append 一行。

`/resume` 只能在 cwd 範圍內列 session、沒有全文搜尋、不能跨專案。當使用者想找「兩週前處理某個 bug 的對話」時，必須手動切目錄 + 滾選單，幾乎不可行。

`clio` 解這個 pain point：在背景把 `.jsonl` 灌進 SQLite + FTS5，前端用 CLI 與 MCP 兩種介面查。MCP 介面讓 Claude Code 本身也能用「我們之前討論過什麼」這種問句反查歷史，等於把過去對話變成 LLM 可用的長期記憶。

## Goals / Non-Goals

**Goals:**

- 跨專案統一索引：一次掃描整個 `~/.claude/projects/`，搜尋結果可橫跨所有 cwd
- 中文 + 程式碼搜尋：FTS5 `trigram` tokenizer 同時支援 CJK 與 camelCase / snake_case
- 全內容索引：包含 tool output（檔案讀取結果、Bash stdout、錯誤訊息），最大化召回
- 低 latency：CLI `search` < 200ms 開啟到首字輸出、MCP tool call < 100ms（DB 已開）
- 單一 binary 發布：純 Go + modernc.org/sqlite，免 cgo、跨平台 cross-compile
- 增量更新：append-only `.jsonl` 用 byte-offset 增量解析，不重 parse 整檔
- 雙介面共享 schema：CLI 與 MCP 讀同一個 SQLite，零 IPC
- 一鍵 MCP 安裝：`clio install-mcp` 直接寫入 `~/.claude.json`

**Non-Goals:**

- 任何形式的 GUI（web / TUI / native app）— 要看內容用 `clio show`，要摘要讓 Claude 透過 MCP 做
- iCloud / 跨機同步 — 每台機器各自一份 index
- Resume / Fork 整合 — 不 spawn `claude` subprocess，使用者自己 copy 內容即可
- Semantic / embedding 搜尋 — v1 只 FTS5；embedding 留給 v2
- 多使用者 / 權限模型 — 純單機單人工具
- 任何寫入 `~/.claude/projects/` 的行為 — 嚴格 read-only

## Decisions

### project path 從 .jsonl event 的 cwd 讀，不 decode 目錄名

**理由**：Claude Code 的目錄名編碼把 `/` 與 `_` 都換成 `-`（已驗證：`cli_project_COMPLETE` → `-Users-lin-Herd-cli-project-COMPLETE`），不可逆。但每個 `.jsonl` 的前幾行 event 帶 `"cwd":"/Users/lin/Herd/cli_project_COMPLETE"` 這個 ground truth。

策略：ingest 解析時，取第一個含 `cwd` field 的 event 存進 `sessions.project_path`（前 1-2 行可能是 `permission-mode` / 不含 cwd，要讀到第一個有的）。目錄名只當 fallback（舊檔 / 損壞檔）。

### SQLite tokenizer 選用 trigram 而非 unicode61

**理由**：使用者主要工作語言為繁體中文 + 程式碼。`unicode61`（FTS5 預設）對 CJK 是把整句當一個 token，搜「驗證」找不到「資料驗證流程」這種子字串。`trigram`（SQLite 3.34+ 內建）切成 3-gram，中文、英文、camelCase、snake_case 全部能 match，代價是 FTS index 大約 2-3x 大小。對「全部索引」的設計來說，召回率比磁碟成本重要。

**替代方案**：
- `unicode61 tokenchars=...` 手刻參數 → 對中文仍是 substring 失敗
- 引入第三方 jieba/cjk tokenizer → 需要 cgo、編譯麻煩、增加依賴

### 純 Go SQLite（modernc.org/sqlite），不用 mattn/go-sqlite3

**理由**：`modernc.org/sqlite` 是 SQLite 的純 Go 翻譯版（透過 SQLite 的 C-to-Go 工具），效能略遜但完全免 cgo。對發布單一 binary 至關重要：
- 直接 `go install github.com/<user>/clio@latest` 就能跑
- Cross-compile 給 Linux/Windows 不需要設定 cross C toolchain
- 不會在 CI 上踩 cgo + glibc 版本問題

效能成本：search query 可能慢 10-20%，但對單人工具負載完全足夠。

### 增量 ingest：last-complete-newline offset + 尾段 fingerprint

**理由**：`.jsonl` 是 append-only，但「append-only」不代表每次讀到的尾端都是完整 JSON line — Claude 正在寫時，從 byte offset 往後讀會吃到半行（torn line）。

策略：
1. 比對 `(size, mtime)` 做第一層快篩 — 沒變就 skip
2. 第二層校驗：存「最後一行的 fingerprint（hash）」，防 same-size rewrite / atomic replace / crash-truncate 後重寫造成的誤判
3. 從 `last_byte_offset` seek，但只 parse 到「最後一個完整 newline」為止；未完成的尾巴留到下次。記錄的 offset 永遠是 **last complete newline offset**，不是 last read byte
4. size 變小 → 檔案被改寫，整檔重做

**邊界情況**：
- Claude Code 偶爾在 session 結束補寫 summary line — append，安全
- torn line（半行）→ 不當 parse failure，等下次補齊
- 未知 event type → parser「skip + warn」而非 fail，向後相容
- 但「格式語義改變」（非新增 field，而是既有欄位語義變）是真正的脆弱點 — 見 Risks，靠 `clio doctor` 偵測 parse 失敗率 + source-of-truth 對帳

### 寫權所有權：MCP server 在跑時為唯一 writer

**理由**：`clio index`、CLI 啟動前的增量補抓、MCP watcher、60s 保底 walk 都可能想寫 DB。WAL + busy_timeout 只是「碰撞時排隊」，不是 ownership 策略。

模型：
- MCP server 啟動時建一個 lock file（`~/Library/Application Support/clio/mcp.lock`，含 pid）
- CLI 偵測到有效 lock → 跳過自己的增量補抓、純 read（資料最多落後到 watcher 上次 ingest，幾秒內）
- MCP 沒跑 → CLI 自己 ingest
- WAL + `busy_timeout=3000` + `synchronous=NORMAL` 當第二道防線（處理 lock 過期 / race 殘留）
- 單一 writer，消除常態競爭

### Secret redaction：ingest 時過濾敏感資料

**理由**：tool output 可能含 `.env` 內容、API token、private key、客戶資料。clio 全索引會把這些永久落地進未加密 SQLite、再透過 MCP 暴露給模型，與「裝任何套件前先審安全」的安全原則衝突。

策略：
- ingest 時對每個 message content 套用 secret-pattern set（AWS/GCP key、`Bearer <token>`、`-----BEGIN ... PRIVATE KEY-----`、`.env` 形式的 `KEY=value`），match 到替換為 `[REDACTED:type]`
- content（FTS）與 raw_json 都 redact，確保 MCP `read_session` 也拿不到原始 secret
- DB 檔權限設 `0600`
- pattern set 可擴充；初版用內建保守集，寧可漏 redact 也不錯殺正常文字（false-positive 會吃掉可搜性）

### 自我污染排除：跳過 clio 自己的 MCP traffic

**理由**：clio 裝上 MCP 後，Claude 每次呼叫 clio 的 `search`/`read_session`，請求與回傳會被寫回 session、下次 ingest 又被索引。久了語料庫充滿「找歷史的歷史」，搜尋品質自我退化。

策略：ingest 解析 `tool_use` / `tool_result` 時，識別 server / tool name，是 clio 自己的就不寫進 messages、不進 FTS。判斷依據：MCP tool name 前綴（`clio` server 註冊名）。

### FTS ranking：bm25 + role 加權 + recency prior

**理由**：user prompt、AI 回覆、程式碼、log、tool output 混在同一 FTS 用裸 bm25 排，常出現「log 檔提到關鍵字五十次」壓過「真正的討論段落」。能搜到但第一頁不可用。

策略：
- messages 存 `role` 欄
- 查詢用 FTS5 bm25 算基礎分，在 Go 端 post-rank：`final = bm25 × role_weight + recency_bonus`
  - role_weight：user/assistant 對話 > tool_result/tool_use
  - recency_bonus：越新的 session 輕微加分
- 預設 search 只回 user/assistant；`--include-tool-output` 才把 tool output 拉進結果（索引仍全包，只是預設不排進結果）

### Watcher 只在 MCP 模式啟動，不做 daemon

**理由**：CLI 是 one-shot；MCP server 是長期 process，watch 才有意義。避免 launchd plist 系統整合複雜度。

**fsnotify 不是遞迴的**：Go 的 fsnotify 不自動監看整棵樹，新建的 project 目錄也不會被 watch。所以實作要：
1. 啟動時 walk `~/.claude/projects/`，對每個既有子目錄 `Add` watch
2. 監聽 `~/.claude/projects/` 本身的 Create 事件 → 新目錄出現就動態 `Add` watch
3. 60s 保底全量 walk 比對 ingest_state，補抓任何漏掉的 event（fsnotify 在大量變動下會丟事件）

### install-mcp 的 two-phase 行為

**理由**：install-mcp 綁了 config 寫入 + 首次 full ingest，失敗語義必須明確，否則使用者可能拿到「寫了 config 但 DB 空」的壞 MCP，或「卡在 ingest 看似當掉」。

順序（先 ingest 再寫 config）：
1. Phase 1：跑 full ingest，顯示進度條。失敗 → 報錯、**不碰 `~/.claude.json`**（沒有半殘狀態）
2. Phase 2：ingest 成功後才 atomic write + `.bak` 注入 server entry。寫前 backup、寫後驗證可 parse 才刪 backup
3. 結果：要嘛完整裝好、要嘛完全沒動 config

### MCP 預設不回 tool output 內容，強制 limit

**理由**：Claude Code session 的 context 有限。MCP tool 回 50 個 message 但每個都是 100KB tool output 會直接打爆 context。預設行為：
- `search` 預設 `limit=10`、max 50；snippet 截 32 字
- `read_session` 預設不含 tool output、預設一次 50 messages、支援 pagination
- 由 Claude 決定要不要展開 — 提供 explicit `include_tool_output=true` 選項

### CLI 框架選 cobra；不用 urfave/cli 或 stdlib flag

**理由**：cobra 文件齊全、subcommand UX 標準、自動生成 help 與 shell completion。對未來擴充（增加 subcommand、加 flag）成本最低。

### 不引入 logger framework

stdlib `log/slog` 已足夠。CLI 模式 log 到 stderr、MCP 模式 log 到 stderr 不污染 stdio JSON-RPC。

### Project layout 採用 Go 標準 `cmd/` + `internal/`

**理由**：
- `cmd/clio/main.go` 為唯一 entry，內部模組全放 `internal/` 防止被外部 import
- 模組切分依責任：`db` / `model` / `ingest` / `search` / `mcp` / `watcher` / `cli`
- 每個 package 單一責任，方便 unit test 與後續重構

## Risks & Open Questions

- **[最大風險] 綁死私有未公開格式**：`~/.claude/projects/*.jsonl` 與 `~/.claude.json` 不是穩定 API，是整個產品的單點脆弱依賴。新增 field 可以靠寬鬆解析吸收，但「既有欄位語義改變」會讓核心價值靜默失效。對策：(1) raw_json 欄全保底；(2) `clio doctor` 偵測 parse 失敗率；(3) `clio doctor` 做 source-of-truth 對帳 — 比對 DB 內 session 的 event count / 尾段，偵測缺尾、重複 ingest、自我污染漂移。這是 design 層擋不掉、只能持續監控的風險。
- **語義漂移無法被 DB 完整性檢查抓到**：DB 完整、FTS 健康、孤兒 session 少 ≠ search 結果可信。`clio doctor` 必須包含「DB vs `.jsonl` 對帳」這類語義檢查，而非只有結構檢查。
- **FTS index 大小爆炸**：trigram + 全內容索引可能讓 DB 達原始 jsonl 的 1.5-2x。`clio doctor` 加 DB size warning，提供 `--exclude-tool-output` 重建逃生口。
- **MCP stdio purity**：SQLite 鎖死 / migration 失敗時，stdio MCP 不能在 stdout 寫錯誤（會打壞 JSON-RPC）。所有錯誤走 stderr、回 MCP 標準 error response。需 E2E 測試守住。
- **redaction 的 false-positive/negative 平衡**：太鬆漏 secret、太嚴吃掉正常文字可搜性。初版用保守內建 pattern，留 open question：是否需要 user-configurable pattern。
- **效能目標未驗證**：純 Go SQLite + trigram 的真實效能是 open question，benchmark 後可能要調 target 或加 covering index。

## Performance Targets

這些是 **warm steady-state** 目標，不是 cold-start SLA。實作後要 benchmark 真實 workload 驗證（純 Go SQLite + trigram FTS 是不確定因素）：

- 初始 full ingest（cold）：300MB `.jsonl` 目標 < 30 秒，但接受 benchmark 後調整
- 增量 ingest（單檔 append 幾 KB，warm）：< 50ms
- MCP `search` tool call（DB 已開，warm）：< 100ms
- `clio search` cold CLI（含開 DB + 可能的增量補抓）：盡力 < 500ms，不保證 200ms — 增量補抓會吃時間，故 MCP 在跑時 CLI 跳過補抓走 read-only 路徑可達 < 200ms
- DB 大小：原始 `.jsonl` 的 1.5-2x（trigram + 全內容）；`clio doctor` 發 size warning

## Testing Strategy

100% 主路徑 coverage（unit + 5 個 E2E）。**Integration test 用真實 on-disk SQLite（temp file），不用 in-memory** — 因為最危險的 failure mode（FTS5/trigram 真實可用性、WAL、檔案鎖、跨 process 競爭、tailing 成長中的 jsonl）在 in-memory 下測不到。

- **Unit**：parser golden files（tool_use / image / summary / torn line / 破壞 JSON）；incremental（append 半行 → 補齊、same-size rewrite 偵測、fingerprint 校驗）；search query builder flag 組合；cwd extraction 對含 `-`/`_` 真實路徑；secret redactor pattern；自我污染排除；64KB 截斷；role 加權 ranking
- **Integration（on-disk SQLite）**：tempdir 建假 `.claude/projects/`、full ingest → search → 驗證；WAL 並發 reader 不 block writer；tx rollback on parser error
- **E2E（5 個 critical path）**：(1) install-mcp two-phase + `.claude.json` safety；(2) CJK + 程式碼 trigram 搜尋；(3) MCP stdio purity（stdout 純 JSON-RPC）；(4) incremental ingest tailing 成長中的 jsonl；(5) cwd extraction
- **MCP**：in-process 起 server、模擬 Claude tool call、golden response、limit clamp、pagination
- **手動 smoke**：實際裝進 Claude Code 跑「上週討論了什麼」

## GSTACK REVIEW REPORT

| Review | Trigger | Why | Runs | Status | Findings |
|--------|---------|-----|------|--------|----------|
| Eng Review | `/plan-eng-review` | Architecture & tests (required) | 1 | CLEAR | 7 issues raised, all resolved |
| Outside Voice | codex | Independent 2nd opinion | 1 | issues_found | 14 raised, 6 folded as decisions, 5 as fixes |

**Decisions locked (D2-D13):** cwd-from-event (not dir decode) · install-mcp atomic+.bak · install-mcp two-phase synchronous ingest · WAL+busy_timeout · MCP-sole-writer ownership · per-message 64KB FTS truncate · one-file-per-tx ingest (last-complete-newline offset + fingerprint) · full unit+5 E2E coverage · secret redaction · self-pollution exclusion · role-weighted+recency ranking.

**CODEX folded as correctness fixes (no decision needed):** torn-line offset · second-layer integrity fingerprint · removed project_path decoder residue · on-disk SQLite for integration tests · perf targets relabeled warm/cold · fsnotify subdir watch management · doctor source-of-truth reconciliation.

**CROSS-MODEL TENSION:** Codex #12 argued for smaller scope (just index+search/show, defer watcher+MCP self-query). User chose full v1 (D1=B) — sovereignty respected. Resolved by ordering tasks.md phases so ingest+search ship and validate first; watcher + MCP self-query land in later phases (captures codex's intent without cutting scope).

**UNRESOLVED:** 0

**VERDICT:** ENG CLEARED — ready to write implementation plan (tasks.md).
