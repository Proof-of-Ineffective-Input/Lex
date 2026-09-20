package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"lex/pkg"
	"lex/pkg/search"
)

const (
	basePort     = 8931
	portAttempts = 10
	cacheTTL     = 30 * time.Minute
)

type cachedSearch struct {
	results   []search.SearchResult
	expiresAt time.Time
}

var cache = struct {
	mu      sync.Mutex
	entries map[string]cachedSearch
}{
	entries: make(map[string]cachedSearch),
}

// GetCached 读取未过期的搜索缓存，供 MCP 与 HTTP 两条路径共享。
func GetCached(query string) ([]search.SearchResult, bool) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	c, ok := cache.entries[query]
	if !ok {
		return nil, false
	}
	if time.Now().After(c.expiresAt) {
		delete(cache.entries, query)
		return nil, false
	}
	return c.results, true
}

// SetCached 写入搜索缓存。
func SetCached(query string, results []search.SearchResult) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.entries[query] = cachedSearch{
		results:   results,
		expiresAt: time.Now().Add(cacheTTL),
	}
}

func handleSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req exaRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, exaResponse{Results: []exaResult{}})
		return
	}

	query := req.Query
	if query == "" {
		writeJSON(w, exaResponse{Results: []exaResult{}})
		return
	}

	maxResults := req.NumResults
	if maxResults <= 0 {
		maxResults = 10
	}
	maxResults = min(max(maxResults, 5), 50)

	if cached, ok := GetCached(query); ok {
		writeJSON(w, toExaResponse(cached))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 18*time.Second)
	defer cancel()

	results, _, err := search.Execute(ctx, pkg.SharedClient, query, maxResults)
	if err != nil || len(results) == 0 {
		writeJSON(w, exaResponse{Results: []exaResult{}})
		return
	}

	SetCached(query, results)
	writeJSON(w, toExaResponse(results))
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// Start 在后台监听本地 HTTP 端口，作为 Kelivo 内置 Exa search 的数据通道。
// 端口被占用时顺序探测；全部失败则静默放弃，不阻塞 MCP 主流程。
func Start() {
	ln, port := listen()
	if ln == nil {
		fmt.Fprintln(os.Stderr, "httpapi: no available port, HTTP endpoint disabled")
		return
	}

	writePortFile(port)

	mux := http.NewServeMux()
	mux.HandleFunc("/search", handleSearch)

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	fmt.Fprintf(os.Stderr, "httpapi: listening on 127.0.0.1:%d\n", port)
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "httpapi: serve error: %v\n", err)
		}
	}()
}

func listen() (net.Listener, int) {
	for i := 0; i < portAttempts; i++ {
		port := basePort + i
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			return ln, port
		}
	}
	return nil, 0
}

func writePortFile(port int) {
	path := portFilePath()
	if path == "" {
		return
	}
	_ = os.WriteFile(path, []byte(fmt.Sprintf("%d", port)), 0o644)
}

func portFilePath() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return dir + string(os.PathSeparator) + "lex-http-port"
}
