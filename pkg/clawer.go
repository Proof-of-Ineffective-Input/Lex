package pkg

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/golang-lru/v2/expirable"

	"mcp-search-duckduckgo/pkg/hook"
)

var (
	fetchCache *expirable.LRU[string, string]
	cacheOnce  sync.Once
)

func getCache() *expirable.LRU[string, string] {
	cacheOnce.Do(func() {
		fetchCache = expirable.NewLRU[string, string](1024, nil, 5*time.Minute)
	})
	return fetchCache
}

// SharedClient 全局共享 http.Client，连接池跨请求复用。
// 单机 STDIO 长驻进程，全部请求共用同一连接池。
var SharedClient = &http.Client{
	Timeout: 30 * time.Second,
	Transport: &http.Transport{
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
	},
}

// hostLimiter 每 host 并发上限，避免触发反爬。
var hostLimiter = &hostSemaphore{limit: 2, chans: make(map[string]chan struct{})}

type hostSemaphore struct {
	mu    sync.Mutex
	limit int
	chans map[string]chan struct{}
}

func (h *hostSemaphore) acquire(host string) chan struct{} {
	h.mu.Lock()
	c, ok := h.chans[host]
	if !ok {
		c = make(chan struct{}, h.limit)
		h.chans[host] = c
	}
	h.mu.Unlock()
	c <- struct{}{}
	return c
}

func (h *hostSemaphore) release(c chan struct{}) {
	<-c
}

// AcquireHost 占用一个 host 并发槽位，返回释放函数。
func AcquireHost(host string) func() {
	c := hostLimiter.acquire(host)
	return func() { hostLimiter.release(c) }
}

// HostOf 提取 URL 的 host，用于限流分区。
func HostOf(target string) string {
	u, err := url.Parse(target)
	if err != nil {
		return target
	}
	return u.Host
}

func ResolveDDGURL(href string) string {
	if href == "" {
		return href
	}
	if strings.Contains(href, "uddg=") {
		u, err := url.Parse(href)
		if err == nil {
			if uddg := u.Query().Get("uddg"); uddg != "" {
				if decoded, err := url.QueryUnescape(uddg); err == nil {
					return decoded
				}
			}
		}
	}
	if strings.HasPrefix(href, "//") {
		return "https:" + href
	}
	return href
}

func NormalizeLimit(limit int) int {
	if limit <= 0 {
		return 0
	}
	if limit < 2000 {
		limit = 2000
	}
	if limit > 64000 {
		limit = 64000
	}
	return ((limit + 500) / 1000) * 1000
}

func TruncateContent(content string, limit int) string {
	if limit <= 0 || len(content) <= limit {
		return content
	}
	truncated := content[:limit]
	return fmt.Sprintf("%s\n\n---\n*Content truncated to %d characters (original: %d characters)*", truncated, limit, len(content))
}

// FetchSingle 抓取 URL 并返回文本。
// 声明式 hook 机制：查缓存 → 注册表匹配 hook → 执行 → 写缓存。
func FetchSingle(ctx context.Context, client *http.Client, target string, limit int) (string, error) {
	limit = NormalizeLimit(limit)
	// 缓存键去 limit 化：URL→完整内容，读取时按 limit 截断
	cacheKey := target

	cache := getCache()
	if cached, ok := cache.Get(cacheKey); ok {
		return TruncateContent(cached, limit), nil
	}

	h := hook.Match(target)
	if h == nil {
		return "", fmt.Errorf("no hook matched URL: %s", target)
	}
	result, err := h.Fetch(ctx, client, target, limit)
	if err != nil {
		return "", err
	}
	cache.Add(cacheKey, result)
	return result, nil
}
