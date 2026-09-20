package httpapi

import (
	"strings"

	"lex/pkg/search"
)

type exaRequest struct {
	Query      string `json:"query"`
	NumResults int    `json:"numResults"`
	Contents   struct {
		Text bool `json:"text"`
	} `json:"contents"`
}

type exaResult struct {
	Title string `json:"title"`
	URL   string `json:"url"`
	Text  string `json:"text"`
}

type exaResponse struct {
	Results []exaResult `json:"results"`
}

func toExaResponse(results []search.SearchResult) exaResponse {
	out := exaResponse{Results: make([]exaResult, 0, len(results))}
	for _, r := range results {
		if strings.TrimSpace(r.URL) == "" {
			continue
		}
		text := r.Snippet
		if r.Highlights != "" {
			if text != "" {
				text += "\n\n"
			}
			text += r.Highlights
		}
		out.Results = append(out.Results, exaResult{
			Title: r.Title,
			URL:   r.URL,
			Text:  text,
		})
	}
	return out
}
