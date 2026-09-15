package hook

import (
	"context"
	"net/http"
)

// Hook 描述一种 URL 抓取/解析规则。
// 满足 Match 的 URL 由 Fetch 处理；未命中则交给下一个 hook。
type Hook interface {
	// Name 唯一标识，用于调试与缓存键区分。
	Name() string
	// Match 判断 URL 是否属于本 hook 负责的类型。
	Match(target string) bool
	// Fetch 执行抓取与解析，返回最终文本。
	Fetch(ctx context.Context, client *http.Client, target string, limit int) (string, error)
	// PreferRemote 声明该 URL 是否优先走远端抓取（Exa），失败再回落到本地 Fetch。
	// 专用解析型 hook（YouTube/X/非 PDF Office）返回 false，避免远端结果破坏本地解析。
	PreferRemote(target string) bool
}

// ExaFetcher 外部注入的远端抓取回调（由 search 包在 init 中注入）。
// 为 nil 时表示远端链路不可用，全部回落到本地 hook。
var ExaFetcher func(ctx context.Context, client *http.Client, target string, limit int) (string, error)

// PreferRemote 判断 URL 是否应优先走远端抓取链路。
func PreferRemote(target string) bool {
	return Match(target).PreferRemote(target)
}

// 具体 hook 注册表：按注册顺序保存，先注册者优先。
// HTMLHook 为内建兜底，不在此注册（Match 恒真，会遮蔽其他 hook）。
var registry []Hook

// Register 注册一个 hook（进程启动时调用，非并发）。
func Register(h Hook) {
	registry = append(registry, h)
}

// Match 返回首个匹配 target 的 hook；无匹配返回内建 HTMLHook 兜底。
func Match(target string) Hook {
	for _, h := range registry {
		if h.Match(target) {
			return h
		}
	}
	return HTMLHook{}
}

// FetchByHook 用匹配的 hook 抓取；返回命中的 hook（可能为 HTMLHook）。
func FetchByHook(ctx context.Context, client *http.Client, target string, limit int) (string, Hook, error) {
	h := Match(target)
	out, err := h.Fetch(ctx, client, target, limit)
	return out, h, err
}
