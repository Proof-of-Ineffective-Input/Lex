# AGENT.md

## Lex

A Go-based MCP server providing a local-first Exa-like alternative (with Exa search support) and web scraping.

Unlike other search MCP servers that return only snippets and force a second `fetch` call, Lex embeds full-page content extraction directly into each search result via heuristic sentence re-ranking. No external API, no browser, no RAG pipeline.

## Build

```powershell
go build -ldflags="-s -w" -o lex.exe .
```

## Non-obvious facts

- Module name: `lex` — import path uses this
- Go version: `1.25.5` — very recent, ensure your environment matches
- No linter config — no `.golangci.yml`, style follows Go conventions
- Tests only live in `pkg/hook`, `pkg/rerank`, and `pkg/search` subpackages (`_test.go`); CI does not run tests
- Core helpers live in [`pkg/`](pkg):
  - [`pkg.UA`](pkg/clawer.go:18) — shared User-Agent constant
  - [`pkg.ResolveDDGURL()`](pkg/clawer.go:89) — resolves DDG redirect links (`uddg=` decode, `//` protocol completion)
  - [`pkg.NormalizeLimit()`](pkg/clawer.go:109) — clamps char limits to `[2000, 64000]`, rounds to nearest 1000
  - [`pkg.FetchAll()`](pkg/clawer.go:135) — concurrent batch fetch: semaphore(8) + per-host limiter, order-preserving
  - [`pkg.FetchSingle()`](pkg/clawer.go:164) — core fetch: LRU cache (1024 entries, 5-min TTL) → hook match → charset decode → content-area extraction → HTML→Markdown
  - [`pkg.AnalyzePage()`](pkg/highlights.go:14) — page-level BM25 scoring for search result tiering (thin wrapper, impl in rerank)
  - [`pkg.ExtractHighlightsFromAnalysis()`](pkg/highlights.go:23) — BM25 sentence re-ranking under a token budget (thin wrapper, impl in rerank)
  - [`pkg.RerankByChars()`](pkg/highlights.go:32) — order-preserving semantic re-rank under a char budget for `web_fetch` (thin wrapper, impl in rerank)
- Subpackages:
  - [`pkg/search`](pkg/search/search.go) — pluggable search engine chain via the [`Searcher`](pkg/search/search.go:20) interface. [`Execute()`](pkg/search/search.go:38) tries each in order, first non-empty wins. Registration in [`exa.go`](pkg/search/exa.go:261) `init()`: [`ExaSearcher`](pkg/search/exa.go:20) first (Exa MCP RPC), [`DDGSearcher`](pkg/search/ddg.go:23) fallback (DDG Lite). Single natural-language `query` drives both engines
  - [`pkg/rerank`](pkg/rerank/rerank.go) — standalone BM25 sentence re-ranking engine, sunk out of `pkg` to avoid circular imports. Includes identifier expansion (snake_case/camelCase), stem matching, noise penalties, code-block preservation, garbled-text repair, token estimation
  - [`pkg/hook`](pkg/hook/registry.go) — declarative hook registry for `FetchSingle`. [`Hook`](pkg/hook/registry.go:10) interface with `Match`/`Fetch`; [`Register()`](pkg/hook/registry.go:24) appends, [`Match()`](pkg/hook/registry.go:29) returns first match else `HTMLHook` fallback. Concrete hooks:
    - [`HTMLHook`](pkg/hook/html.go:35) — built-in fallback (`Match` always true): HTTP GET → charset decode → main-content extraction → HTML→Markdown
    - [`OfficeHook`](pkg/hook/office.go:34) — `.pdf`/`.docx`/`.xlsx`/`.xls`/`.pptx` direct-to-Markdown via markitdown
    - [`YTHook`](pkg/hook/ytb.go) — YouTube bypass: oEmbed for basic metadata (zero deps), yt-dlp for transcripts and comments (degrades to metadata + hint on failure)
- MCP tool names: [`web_search`](main.go:48) / [`web_fetch`](main.go:55) — generic names to maximize model willingness to call them
- Tool registration: declarative registry [`tools`](main.go:46) — adding a tool is one more `toolSpec` entry; schemas are inferred by the SDK from jsonschema tags
- Tool args:
  - [`SearchArgs`](main.go:21) — `query` (natural language) + `max_results` clamped to `[5, 50]`, default 10
  - [`FetchArgs`](main.go:27) — `urls` list + `char` (string|int: `"full"` → no truncation/no rerank, else number clamped to `[2000, 64000]`) + optional `query` for semantic re-ranking
- Search tiering: fetch all → BM25 page-score sort → top half (rounded up) gets full 3000-token budget, bottom half gets half (1500)
- Concurrency: semaphore `sem(8)` governs parallel fetches; per-host limiter caps each host at 2 concurrent
- Search cache: in-memory 30-min TTL keyed by query ([`searchCache`](main.go:64))
- CI cross-compiles: GitHub Actions builds Windows/Linux/macOS (amd64+arm64) with `-ldflags="-s -w"`
