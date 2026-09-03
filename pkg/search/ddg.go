package search

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"

	"github.com/PuerkitoBio/goquery"

	"lex/pkg"
)

const (
	defaultFetchLimit    = 2000
	highlightsFullBudget = 3000
)

// DDGSearcher 实现 Searcher 接口（DuckDuckGo Lite 兜底搜索）。
type DDGSearcher struct{}

func (DDGSearcher) Name() string { return "ddg" }

func (DDGSearcher) Search(ctx context.Context, client *http.Client, query string, maxResults int) ([]SearchResult, error) {
	u := fmt.Sprintf("https://lite.duckduckgo.com/lite/?q=%s", url.QueryEscape(query))

	hReq, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	hReq.Header.Set("User-Agent", pkg.UA)
	hReq.Header.Set("Referer", "https://html.duckduckgo.com/")
	hReq.Header.Set("Sec-Fetch-Site", "same-origin")

	resp, err := client.Do(hReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("DDG returned status code %d", resp.StatusCode)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, err
	}

	var results []SearchResult
	doc.Find("table").Last().Find("tr").Each(func(i int, s *goquery.Selection) {
		link := s.Find("a.result-link")
		if link.Length() == 0 {
			return
		}
		title := strings.TrimSpace(link.Text())
		href, _ := link.Attr("href")
		snippet := strings.TrimSpace(s.Next().Text())
		if snippet == "" {
			snippet = "(no description available)"
		}
		results = append(results, SearchResult{
			Title:   title,
			URL:     pkg.ResolveDDGURL(href),
			Snippet: snippet,
		})
	})

	if len(results) == 0 {
		return nil, fmt.Errorf("no results found on DDG")
	}

	if len(results) > maxResults {
		results = results[:maxResults]
	}

	urls := make([]string, len(results))
	for i, r := range results {
		urls[i] = r.URL
	}

	fetched := pkg.FetchAll(ctx, client, urls, []int{defaultFetchLimit})

	analyses := make([]*pkg.PageAnalysis, len(results))
	for i := range results {
		if fetched[i].Err == nil {
			analyses[i] = pkg.AnalyzePage(fetched[i].Content, query)
			results[i].Score = analyses[i].Score
		}
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	n := len(results)
	topN := (n + 1) / 2
	var hlWg sync.WaitGroup
	hlSem := make(chan struct{}, 8)
	for i := range results {
		if fetched[i].Err != nil {
			continue
		}
		hlWg.Add(1)
		go func(idx int) {
			defer hlWg.Done()
			hlSem <- struct{}{}
			defer func() { <-hlSem }()
			budget := highlightsFullBudget
			if idx >= topN {
				budget = highlightsFullBudget / 2
			}
			hl := pkg.ExtractHighlightsFromAnalysis(analyses[idx], query, budget)
			if hl != "" {
				results[idx].Highlights = hl
			}
		}(i)
	}
	hlWg.Wait()

	return results, nil
}
