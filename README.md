# DuckDuckGo Search MCP Server

A Go-based MCP (Model Context Protocol) server providing DuckDuckGo search and web scraping capabilities.

## Features

| Tool | Description |
|------|-------------|
| `search` | Search DuckDuckGo Lite and return results in Markdown format |
| `fetch` | Fetch URL content via Jina AI or direct HTML-to-Markdown conversion |

## Installation

```bash
go mod download
go build -o ddg-search.exe main.go
```

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

**search** - Search for keywords:

```json
{
  "query": "Model Context Protocol"
}
```

**fetch** - Fetch web page content:

```json
{
  "urls": ["https://modelcontextprotocol.io/introduction"]
}
```

## Tech Stack

- Go 1.25+
- [go-sdk/mcp](https://github.com/modelcontextprotocol/go-sdk) - MCP Go SDK
- [goquery](https://github.com/PuerkitoBio/goquery) - HTML parsing
- [html-to-markdown](https://github.com/JohannesKaufmann/html-to-markdown) - HTML to Markdown conversion

## Implementation Details

- Uses DuckDuckGo Lite (`lite.duckduckgo.com`) for search results
- `fetch` prioritizes Jina AI service (`r.jina.ai`), falling back to direct scraping with HTML conversion
- Concurrent URL processing with a maximum concurrency limit of 5

## License

AGPL-3.0
