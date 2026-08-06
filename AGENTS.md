# AGENT.md

## Lex

A Go-based MCP server providing DuckDuckGo search and web scraping — a zero-overhead local Exa alternative.

Unlike other DDG MCP servers that return only snippets and force a second `fetch` call, Lex embeds full-page content extraction directly into each search result via heuristic sentence re-ranking. No external API, no browser, no RAG pipeline.

## Build

```powershell
go build -o lex.exe .
```

## Non-obvious facts

- Module name: `mcp-search-duckduckgo` — import path uses this, not `lex`
- Go version: `1.25.5` — very recent, ensure your environment matches
- No linter config — no `.golangci.yml`, style follows Go conventions
- Tests only live in `pkg/hook` and `pkg/rerank` subpackages (`_test.go`); CI does not run tests
- Core helpers live in [`pkg/`](pkg):
  - [`pkg.UA`](pkg/clawer.go:21) — shared User-Agent constant
  - [`pkg.ResolveDDGURL()`](pkg/clawer.go:47) — resolves DDG redirect links (`uddg=` decode, `//` protocol completion)
  - [`pkg.NormalizeLimit()`](pkg/clawer.go:67) — clamps char limits to `[2000, 64000]`, rounds to nearest 1000
  - [`pkg.FetchSingle()`](pkg/clawer.go:140) — core fetch: LRU cache (256 entries, 5-min TTL) → YouTube bypass → charset decode → content-area extraction → HTML→Markdown
  - [`pkg.ScorePage()`](pkg/highlights.go:8) — page-level BM25 scoring for search result tiering (thin wrapper, impl in rerank)
  - [`pkg.ExtractHighlights()`](pkg/highlights.go:13) — BM25 sentence re-ranking under a token budget (thin wrapper, impl in rerank)
- Subpackages:
  - [`pkg/rerank`](pkg/rerank/rerank.go) — standalone BM25 sentence re-ranking engine, sunk out of `pkg` to avoid circular imports. Includes identifier expansion (snake_case/camelCase), stem matching, noise penalties, code-block preservation, garbled-text repair, token estimation
  - [`pkg/hook`](pkg/hook/ytb.go) — bypass hook for `FetchSingle`. `ytb.go` is the YouTube bypass: oEmbed for basic metadata (zero deps), yt-dlp for transcripts and comments (degrades to metadata + hint on failure)
- MCP tool names: [`web_search_lex`](main.go:57) / [`web_fetch_lex`](main.go:64) — Exa-style naming, aliased as `search`/`web_search` and `fetch`/`web_fetch` respectively
- Tool registration: declarative registry [`tools`](main.go:55) — adding a tool is one more `toolSpec` entry; schemas are inferred by the SDK from jsonschema tags
- Search tiering: fetch all → BM25 page-score sort → top half (rounded up) gets full 3000-token budget, bottom half gets half (1500)
- Concurrency: semaphore `sem(8)` governs parallel fetches
- CI cross-compiles: GitHub Actions builds Windows/Linux/macOS (amd64+arm64) with `-ldflags="-s -w"`
