package search

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"lex/pkg"
	"lex/pkg/hook"
)

const ExaMCPURL = "https://mcp.exa.ai/mcp"

// ExaSearcher 实现 Searcher 接口（首选 Exa MCP 搜索）。
type ExaSearcher struct{}

func (ExaSearcher) Name() string { return "exa" }

type exaRPCRequest struct {
	JSONRPC string       `json:"jsonrpc"`
	ID      int          `json:"id"`
	Method  string       `json:"method"`
	Params  exaRPCParams `json:"params"`
}

type exaRPCParams struct {
	Name      string `json:"name"`
	Arguments any    `json:"arguments"`
}

type exaSearchArgs struct {
	Query      string `json:"query"`
	NumResults int    `json:"numResults,omitempty"`
}

type exaFetchArgs struct {
	URLs          []string `json:"urls"`
	MaxCharacters int      `json:"maxCharacters,omitempty"`
}

type exaRPCResponse struct {
	Result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"result"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (ExaSearcher) Search(ctx context.Context, client *http.Client, query, description string, maxResults int) ([]SearchResult, error) {
	if maxResults <= 0 {
		maxResults = 10
	}

	searchQ := query
	if description != "" {
		searchQ = query + " " + description
	}

	reqBody := exaRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "tools/call",
		Params: exaRPCParams{
			Name: "web_search_exa",
			Arguments: exaSearchArgs{
				Query:      searchQ,
				NumResults: maxResults,
			},
		},
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	// 限制搜索超时为 15 秒，避免卡死
	searchCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(searchCtx, "POST", ExaMCPURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")
	httpReq.Header.Set("User-Agent", pkg.UA)

	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Exa MCP returned status %d", resp.StatusCode)
	}

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	rawText, err := parseSSEResponse(respBytes)
	if err != nil {
		return nil, err
	}

	results := parseExaText(rawText)
	if len(results) == 0 {
		return nil, fmt.Errorf("no valid results parsed from Exa response")
	}

	return results, nil
}

// FetchExaURL 调用 Exa MCP 的 web_fetch_exa 作为抓取的隐式兜底
func FetchExaURL(ctx context.Context, client *http.Client, targetURL string, limit int) (string, error) {
	if limit <= 0 {
		limit = 16000
	}

	reqBody := exaRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "tools/call",
		Params: exaRPCParams{
			Name: "web_fetch_exa",
			Arguments: exaFetchArgs{
				URLs:          []string{targetURL},
				MaxCharacters: limit,
			},
		},
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	fetchCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(fetchCtx, "POST", ExaMCPURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")
	httpReq.Header.Set("User-Agent", pkg.UA)

	resp, err := client.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Exa MCP Fetch returned status %d", resp.StatusCode)
	}

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	return parseSSEResponse(respBytes)
}

func parseSSEResponse(data []byte) (string, error) {
	lines := strings.Split(string(data), "\n")
	var jsonData string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "data:") {
			jsonData = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			break
		}
	}

	if jsonData == "" {
		// 尝试作为普通 JSON 解析
		jsonData = string(data)
	}

	var rpcResp exaRPCResponse
	if err := json.Unmarshal([]byte(jsonData), &rpcResp); err != nil {
		return "", fmt.Errorf("failed to unmarshal Exa RPC response: %w", err)
	}

	if rpcResp.Error != nil {
		return "", fmt.Errorf("Exa RPC error [%d]: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}

	if len(rpcResp.Result.Content) == 0 {
		return "", fmt.Errorf("empty content in Exa RPC response")
	}

	return rpcResp.Result.Content[0].Text, nil
}

func parseExaText(text string) []SearchResult {
	// Exa 结果通常分割为 \n\n---\n\n 或 \n---\n
	rawBlocks := strings.Split(text, "---\n")
	var results []SearchResult
	for _, block := range rawBlocks {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		lines := strings.Split(block, "\n")
		var r SearchResult
		var highlightLines []string
		inHighlights := false

		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if inHighlights {
				highlightLines = append(highlightLines, line)
				continue
			}
			if strings.HasPrefix(trimmed, "Title: ") {
				r.Title = strings.TrimPrefix(trimmed, "Title: ")
			} else if strings.HasPrefix(trimmed, "URL: ") {
				r.URL = strings.TrimPrefix(trimmed, "URL: ")
			} else if strings.HasPrefix(trimmed, "Snippet: ") {
				r.Snippet = strings.TrimPrefix(trimmed, "Snippet: ")
			} else if strings.HasPrefix(trimmed, "Highlights:") {
				inHighlights = true
			} else if strings.HasPrefix(trimmed, "Text: ") {
				r.Snippet = strings.TrimPrefix(trimmed, "Text: ")
			}
		}

		if len(highlightLines) > 0 {
			r.Highlights = strings.TrimSpace(strings.Join(highlightLines, "\n"))
		}
		if r.Snippet == "" && r.Highlights != "" {
			r.Snippet = r.Highlights
			if len(r.Snippet) > 200 {
				r.Snippet = r.Snippet[:200] + "..."
			}
		}
		if r.Title != "" || r.URL != "" {
			results = append(results, r)
		}
	}
	return results
}

func init() {
	hook.ExaFetcher = FetchExaURL
	Register(ExaSearcher{})
	Register(DDGSearcher{})
}
