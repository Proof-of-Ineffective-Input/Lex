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

	"lex/pkg/hook"
)

const UA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/[IP] Safari/537.36"

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
	return hook.Truncate(content, limit)
}

// FetchResult 一次抓取的输出：内容或错误。
type FetchResult struct {
	Content string
	Err     error
}

// FetchAll 并发抓取多个 URL，返回与输入顺序一致的结果切片。
// 统一 semaphore(8) + per-host 限流；每个 URL 独立槽位，无共享可变状态。
// limit 为 0 时表示对全部 URL 使用同一 limit；否则按 targets 逐一对齐 limits。
func FetchAll(ctx context.Context, client *http.Client, targets []string, limits []int) []FetchResult {
	results := make([]FetchResult, len(targets))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i, target := range targets {
		limit := 0
		if len(limits) == 1 {
			limit = limits[0]
		} else if i < len(limits) {
			limit = limits[i]
		}
		wg.Add(1)
		go func(idx int, t string, l int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			release := AcquireHost(HostOf(t))
			defer release()
			content, err := FetchSingle(ctx, client, t, l)
			results[idx] = FetchResult{Content: content, Err: err}
		}(i, target, limit)
	}
	wg.Wait()
	return results
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
