# AGENT.md

## Lex

A Go-based MCP server providing DuckDuckGo search and web scraping — a zero-overhead local Exa alternative.

Unlike other DDG MCP servers that return only snippets and force a second `fetch` call, `Lex` embeds full-page content extraction directly into each search result via heuristic sentence re-ranking. No external API, no browser, no RAG pipeline.

## Build

编译可执行文件：

```powershell
go build -o lex.exe .
```

## 非显而易见的信息

- 模块名: `mcp-search-duckduckgo` — 导入路径用此名，非 `lex`
- Go 版本: `1.25.5` — 极新版本，确保环境匹配
- 无测试文件 — 整个项目无 `_test.go`，CI 也不跑测试
- 无 Linter 配置 — 无 `.golangci.yml`，代码风格靠 Go 标准
- 核心工具函数 在 [`pkg/`](pkg) 包中：
  - [`pkg.UA`](pkg/clawer.go:19) — 共享 User-Agent 常量
  - [`pkg.ResolveDDGURL()`](pkg/clawer.go:45) — 解析 DDG 跳转链接
  - [`pkg.FetchSingle()`](pkg/clawer.go:138) — 核心抓取，内置 LRU 缓存（256 条目，5 分钟 TTL）
  - [`pkg.ExtractHighlights()`](pkg/highlights.go:28) — BM25 句子重排序
  - [`pkg.ScorePage()`](pkg/highlights.go:28) — 页面级 BM25 评分，用于 search 结果分级
  - [`pkg.NormalizeLimit()`](pkg/clawer.go:65) — 字符限制钳位 `[2000, 64000]`，四舍五入到千位
- MCP 工具名: [`web_search_lex`](main.go:42) / [`web_fetch_lex`](main.go:60) — 模仿 Exa 命名风格，避免与 `search`/`fetch` 冲突
- search 分级策略: 全部抓取 → BM25 页面评分 → 前一半结果拿 full highlights budget，后一半拿 half budget
- 并发模型: 信号量 `sem(8)` 控制并行抓取数
- CI 交叉编译: GitHub Actions 编译 Windows/Linux/macOS(amd64+arm64)，使用 `-ldflags="-s -w"` 剥离调试符号
