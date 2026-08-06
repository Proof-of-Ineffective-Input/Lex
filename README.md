# Lex

A Go-based MCP server providing DuckDuckGo search and web scraping — a zero-overhead local Exa alternative.

Unlike other DDG MCP servers that return only snippets and force a second `fetch` call, Lex embeds full-page content extraction directly into each search result via heuristic sentence re-ranking. No external API, no browser, no RAG pipeline.

## Features

- `One-shot search` — returns real page highlights, not just snippets
- `Zero-overhead extraction` — no browser, no external API, no vector DB, no RAG
- `BM25 re-ranking` — page-level scoring with tiered highlight budgets (top half full, bottom half half)
- `Content-aware` — probes `<main>` / `<article>` / `[role=main]` containers by priority
- `Robust encoding` — automatic charset detection and garbled-text repair (GBK/Big5/Shift-JIS, etc.)
- `YouTube bypass` — detects video links, extracts transcripts and comments via oEmbed + yt-dlp
- `Built-in cache` — LRU 256 entries / 5-minute TTL, repeated fetches cost nothing
- `Concurrent by design` — 8-way semaphore parallelism, graceful fallback to original snippets

## Install

```bash
go mod download
go build -o lex.exe .
```

## Usage

Register in your MCP client config:

```json
{
  "mcpServers": {
    "lex": {
      "command": "./lex.exe"
    }
  }
}
```

### `web_search_lex`

Also callable as `search` / `web_search`.

```json
{ "query": "Go 1.24 Swiss Table map performance", "max_results": 10 }
```

- `max_results` clamped to `[5, 50]`, defaults to 10
- Returns `Title` / `URL` / `Snippet` / `Highlights` in markdown
- Fetches all pages concurrently → BM25 page scoring → top half gets full highlight budget (3000 tokens), bottom half gets half

### `web_fetch_lex`

Also callable as `fetch` / `web_fetch`.

```json
{ "urls": { "https://example.com/article": 16000 } }
```

- Per-URL character limits clamped to `[2000, 64000]`, rounded to nearest 1000; `0` means no truncation
- URLs sorted then fetched concurrently; failed items are annotated individually without breaking the batch

## How It Works

```mermaid
flowchart LR
  A["DDG Lite HTML"] --> B["parse results"]
  B --> C["concurrent fetch (x8)"]
  C --> D["HTML-to-Markdown"]
  D --> E["content area extraction"]
  E --> F["charset detect + garbled repair"]
  F --> G["BM25 page scoring"]
  G --> H["tiered highlight budget"]
  H --> I["structured results"]
```

The BM25 engine layers identifier expansion (snake_case / camelCase splitting), stem matching, noise-sentence penalties, and code-block preservation — pure Go, zero ML.

## Tech Stack

- Go 1.25+
- `go-sdk/mcp` — MCP Go SDK (declarative tool registry, schema inferred from jsonschema tags)
- `goquery` — HTML parsing and content extraction
- `html-to-markdown` — HTML to Markdown conversion
- `golang-lru/v2` — expirable LRU cache
- `x/net/html/charset` — charset detection and decoding

## License

AGPL-3.0
