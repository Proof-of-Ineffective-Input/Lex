# DuckDuckGo Search MCP Server

A Go-based MCP (Model Context Protocol) server providing DuckDuckGo search and web scraping capabilities — **reimagined as a zero-overhead local Exa alternative**.

Unlike every other DuckDuckGo MCP server that returns only search snippets and forces a second `fetch` call to read pages, `ddg-search` **embeds full-page content extraction directly into the search result**. Each result comes with query-relevant highlights extracted via heuristic sentence re-ranking — no external API, no browser, no RAG pipeline.

## Features

| Tool | Description |
|------|-------------|
| `search` | Search DuckDuckGo Lite, **concurrently fetch each result page**, extract query-relevant highlights via keyword heuristics, and return structured results with markdown rendering |
| `fetch` | Fetch URL content via direct HTML-to-Markdown conversion with configurable character limits |

### What makes this different

**One-shot search** — `search` returns not just snippets, but real page highlights. No need to call `fetch` separately for every result.

**Zero-overhead page extraction** — no browser (Puppeteer/Playwright), no external API (Jina/Exa), no vector database, no RAG pipeline. Just Go, a lightweight HTML parser, and a heuristic sentence re-ranker that runs in microseconds.

**Content-aware extraction** — automatically identifies `<main>`, `<article>`, `[role="main"]` and other content containers to filter out navigation, headers, and footers before highlight extraction.

**Concurrent by design** — up to 5 parallel fetches with channel-based semaphore, 15s timeout per page, graceful degradation (falls back to snippet on fetch failure).

## Comparison

| Dimension | `ddg-search` (ours) | `nickclyde/duckduckgo-mcp-server` | Exa MCP | Jina MCP |
|-----------|---------------------|------------------------------------|---------|----------|
| Language | Go (single binary) | Python (needs runtime) | SaaS | SaaS |
| Search + content in one call | **✅ Yes** | ❌ Separate tools | ❌ Separate | ❌ Separate |
| Query-relevant highlights | **✅ Built-in** | ❌ Raw truncation only | ✅ (paid) | ✅ (paid) |
| Content area extraction | **✅ `<main>`/`<article>` aware** | ❌ Raw HTML | ✅ | ✅ |
| API key required | **❌ No** | ❌ No | ✅ Yes | ✅ Yes |
| External dependencies | **❌ None** | `httpx` + optional `curl` | — | — |
| Browser required | **❌ No** | ❌ No (but TLS fingerprint issues) | — | — |
| Binary size | **~12 MB** | N/A (Python) | — | — |

## Installation

```bash
go mod download
go build -o ddg-search.exe .
```

Or download a pre-built binary from [Releases](https://github.com/mindires/duckduck-go-mcp/releases).

## Usage

### MCP Configuration

Add to your MCP client configuration:

```json
{
  "mcpServers": {
    "ddg-search": {
      "command": "./ddg-search.exe"
    }
  }
}
```

### Tool Call Examples

**search** — Search and get highlights in one shot:

```json
{
  "query": "Go 1.24 Swiss Table map performance"
}
```

Returns:

```
Title: Faster Go maps with Swiss Tables - The Go Programming Language
URL: https://go.dev/blog/swisstable
Snippet: Go 1.24 includes a completely new implementation of the built-in map type...
Highlights:
Go 1.24 includes a completely new implementation of the built-in map type,
based on the Swiss Table design.
In this blog post we'll look at how Swiss Tables improve upon traditional
hash tables, and at some of the unique challenges in bringing the Swiss
Table design to Go's maps.
```

**fetch** — Fetch web page content with configurable limits:

```json
{
  "urls": {
    "https://example.com/article": 16000
  }
}
```

## How Highlights Work

```
DDG Lite HTML → parse results → concurrent full-page fetch (×5)
→ HTML-to-Markdown → content area extraction (<main>/<article>/...)
→ sentence splitting → tokenize + stop-word filter
→ keyword scoring (hit rate × 0.5 + hit count × 0.3 + position × 0.2)
→ top-sentence selection → original-order reassembly → highlights
```

The heuristic re-ranker scores every sentence against the query using:

- **Unigram hits** — individual keyword matches
- **Bigram hits** — phrase-level matches (e.g. "Swiss Table")
- **Coverage** — what fraction of query tokens appear in the sentence
- **Position bonus** — earlier sentences have a slight advantage

All in ~100 lines of pure Go, zero ML dependencies.

## Tech Stack

- Go 1.25+
- [go-sdk/mcp](https://github.com/modelcontextprotocol/go-sdk) — MCP Go SDK
- [goquery](https://github.com/PuerkitoBio/goquery) — HTML parsing & content extraction
- [html-to-markdown](https://github.com/JohannesKaufmann/html-to-markdown) — HTML to Markdown conversion

## Implementation Details

- Uses DuckDuckGo Lite (`lite.duckduckgo.com`) — no API key, no rate limit worries
- `search` concurrently fetches each result page (concurrency: 5, timeout: 15s)
- Content area extraction via ordered selectors: `<main>` → `<article>` → `[role="main"]` → `.post-content` → `.content` → `#content`
- Highlights budget: 4000 characters per page, whole-sentence boundaries
- Graceful degradation: if a page fetch fails, the result still shows with its original snippet
- `fetch` supports per-URL character limits, clamped to [2000, 64000], rounded to nearest 1000

## License

AGPL-3.0
