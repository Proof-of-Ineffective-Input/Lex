# 提案：为 lex 接入 Twitter/X 数据流（XHook）

> 状态：待评审
> 目标：让 lex 的 `web_fetch` 直接抓取 X 推文、评论区与最新消息，作为"小道消息"数据源。
> 落地范围（用户已确认）：本次只落地 FxTwitter 层 + 直接 fetch 兜底；gobird 仅记录为未来扩展，暂不实现。

## 一、背景与目标

X 官方免费 API 已不可读（读取需 $200/月，免费层只写不读）。lex 需要一条无需官方 API 的路径抓取 X 评论区与最新推文。

本提案为 lex 新增一个 `XHook`，复用现有 [`Hook`](pkg/hook/registry.go:10) 声明式注册机制，使 `web_fetch` 传入 `x.com` / `twitter.com` URL 时自动走 X 专用抓取逻辑，无需新增 MCP 工具。

### 方案优先级（本次落地）

1. FxTwitter API（`api.fxtwitter.com`）—— 零认证、零依赖，取单推文 / 线程 / 评论区 / 搜索 / 用户时间线。本次主链路。
2. Syndication 端点 —— 不使用（用户确认，仅返回计数不返回评论正文，价值低）。
3. 直接 fetch —— FxTwitter 失败时，直接 HTTP GET `x.com` 页面并解析，作为最后兜底。

### 未来扩展（仅记录，不实现）

gobird（`github.com/mudrii/gobird`）—— 真实账号 GraphQL 客户端，取完整评论 / 时间线 / 搜索。当 FxTwitter 对超大 viral 线程静默截断、或需完整评论时，再评估接入。详见[附录 A](#附录-a-gobird-调研记录)。

## 二、复用现有架构

### 2.1 Hook 注册机制

[`Hook`](pkg/hook/registry.go:10) 接口：`Name()` / `Match()` / `Fetch(ctx, client, target, limit)`。
注册：`init()` 中调用 [`Register()`](pkg/hook/registry.go:24)。
匹配：`FetchSingle` 在 [`clawer.go`](pkg/clawer.go:164) 中按 URL 调用 [`Match()`](pkg/hook/registry.go:29)，未命中回退 [`HTMLHook`](pkg/hook/html.go:35)。
参考：`YTHook` 的注册与降级模式（[`ytb.go`](pkg/hook/ytb.go:63)）。

### 2.2 可复用基础设施

- [`pkg.UA`](pkg/clawer.go:18) —— 共享 User-Agent。
- [`pkg.SharedClient`](pkg/clawer.go:34) —— 带连接池的全局 http.Client。
- [`pkg.AcquireHost()`](pkg/clawer.go:75) —— per-host 并发限流（上限 2）。
- [`hook.Truncate`](pkg/hook/ytb.go:579) —— 字符预算截断。
- [`pkg.RerankByChars`](pkg/highlights.go:32) —— 语义重排（按相关性排序评论）。

### 2.3 MCP 工具

无需新增工具。`web_fetch` 的 [`FetchArgs`](main.go:27) 已支持 `urls` 列表 + `char` 预算 + 可选 `query` 语义重排。传入 X URL 即自动命中 `XHook`。

## 三、FxTwitter API v2 精确端点

Base URL：`https://api.fxtwitter.com`。所有端点均返回 JSON 对象，内含 `code` 字段镜像 HTTP 状态码（`200`/`400`/`401`/`404`/`500`）。列表端点返回 `cursor` 对象（`top`/`bottom`），传 `cursor.bottom` 作为 `cursor` 查询参数取下一页。

### 3.1 端点表

- `GET /2/status/{id}` —— 单条推文，触发条件为 URL 是 `/status/<id>`，返回 `SocialThread`（`status`+`thread`+`author`）。
- `GET /2/thread/{id}` —— 作者线程链，需展开完整链时用，返回 `SocialThread`。
- `GET /2/conversation/{id}` —— 评论区（核心），需抓回复时用，返回 `SocialConversation`（`status`+`thread`+`replies`+`cursor`）。
- `GET /2/search` —— 关键词/话题搜索，URL 为 `x.com/search?q=...` 时用，返回 `APISearchResults`（`results`+`cursor`）。
- `GET /2/profile/{handle}/statuses` —— 用户时间线，URL 为 `x.com/<user>` 时用，返回 `APISearchResults` 或 `APIGroupedSearchResults`。
- `GET /2/profile/{handle}` —— 用户 profile，需账号元数据时用，返回 `UserAPIResponse`（`user`）。

### 3.2 关键参数

- `ranking_mode` —— 仅 `conversation` 用，`likes`（默认）或 `recency`，决定 `replies` 排序。
- `cursor` —— 所有列表端点用，传上一页 `cursor.bottom` 分页。
- `count` —— 列表端点用，页大小 `1–100`，默认 `20`（search 默认 `30`）。
- `feed` —— 仅 `search` 用，`latest`（默认）或 `top` 或 `media`。
- `lang` —— 全部端点用，ISO 639-1/639-5，请求 X 内联翻译（可选）。
- `with_replies` / `groupthreads` —— 仅 `profile/{handle}/statuses` 用，包含回复 / 按会话分组。

### 3.3 字段映射（`APITwitterStatus`）

抓取后需提取的字段：

- `id`、`url`、`text`、`created_at`、`created_timestamp`。
- `likes`、`reposts`、`quotes`、`replies`、`views`、`bookmarks`。
- `author`（`APIUser`：`id`、`name`、`screen_name`、`description`、`followers`、`following`、`statuses`、`verified`）。
- `raw_text`、`lang`、`possibly_sensitive`、`replying_to`、`source`、`is_note_tweet`。

### 3.4 错误与边界处理

- HTTP 非 200 或 body 内 `code != 200`（如 `401` 私有、`404` 未找到、`500` 上游失败）→ 判定失败，降级到直接 fetch。
- `404` 双义性：`search` / `profile/{handle}/statuses` 的 `404` 可能是空结果而非错误（未知 handle 与空 timeline 不区分）。应把 `404` 且 body 正常视为空结果返回，而非降级。
- 完整性标注：`conversation` 对超大 viral 线程可能静默截断尾部回复（[FxEmbed#2087](https://github.com/FxEmbed/FxEmbed/issues/2087)），结果应标记为 `partial` 而非 `complete`。
- 分页策略：单页内尽取，不强制多页（避免 `cursor` 404 风险）。

## 四、降级链设计

`XHook.Fetch()` 按以下顺序尝试，首个成功者返回：

1. FxTwitter API —— 成功则返回（含评论区/线程/搜索/时间线）。
2. 直接 fetch x.com 页面 —— FxTwitter 失败时兜底。

### 4.1 第 1 层：FxTwitter API

依据 URL 形态选择端点（见 3.1 端点表）。请求 `GET {base}/{endpoint}`，带 `pkg.UA`，走 `pkg.SharedClient`，经 `pkg.AcquireHost("api.fxtwitter.com")` 限流。解析 JSON → 提取 `text`/`author`/`replies`/`cursor` → 组装 Markdown。

失败条件：HTTP 非 200、body 为空、`code != 200`、JSON 解析失败 → 降级第 2 层。

### 4.2 第 2 层：直接 fetch（兜底）

FxTwitter 失败时，直接 HTTP GET `x.com/<user>/status/<id>` 页面，用现有 HTML 解析（`ExtractMainContent`）提取可见文本。

复用 [`pkg/hook/html.go`](pkg/hook/html.go:35) 的 `HTMLHook` 逻辑（charset 解码 → 主内容提取 → HTML→Markdown）。局限：X 页面大量内容靠 JS 渲染，直接 fetch 通常只能拿到首屏骨架，作为最后兜底。

## 五、实现清单

### 5.1 新增文件

- `pkg/hook/x.go` —— `XHook` 实现：
  - `Match()`：正则匹配 `x.com|twitter.com/<user>/status/<id>`、`x.com/<user>`、`x.com/search?q=...`。
  - `Fetch()`：按降级链调用 FxTwitter → 直接 fetch。
  - `init()`：`Register(XHook{})`。
- `pkg/hook/x_test.go` —— 单元测试（Match 匹配、降级链、FxTwitter 解析）。

### 5.2 修改文件

- `README.md` —— 在 Features 中补充 X/Twitter 支持说明。

### 5.3 依赖

本次不新增任何 Go 依赖（FxTwitter 走标准库 `net/http` + `encoding/json`）。gobird 依赖见[附录 A](#附录-a-gobird-调研记录)，仅记录。

## 六、关键风险与应对

- FxTwitter conversation 静默截断尾部回复 → 结果标记 `partial`，不宣称完整。
- FxTwitter 分页 cursor 404 → 单页内尽取，不强制多页。
- FxTwitter `404` 双义（空结果 vs 错误）→ `404` 且 body 正常视为空结果返回。
- FxTwitter 上游不可用 / rate limit → 复用 `AcquireHost` 限流；失败降级直接 fetch。
- 直接 fetch 拿不到 JS 渲染内容 → 仅作兜底，标注局限。
- FxTwitter 为第三方公共索引 → 结果标注非官方、非穷尽，不宣称完整 X 覆盖。

## 七、验收标准

1. `web_fetch` 传入 `https://x.com/<user>/status/<id>` 时，返回推文文本 + 作者 + 元数据。
2. 传入线程 / 评论区 URL 时，返回 `replies[]` 评论列表（FxTwitter `conversation` 层）。
3. FxTwitter 失败时自动降级到直接 fetch。
4. 结果在 `char` 预算内截断，支持 `query` 语义重排。
5. 无新增 MCP 工具、无新增 Go 依赖，`web_fetch` 行为向后兼容。

## 附录 A：gobird 调研记录（仅记录，暂不实现）

决策：本次不实现。当 FxTwitter 对超大 viral 线程静默截断、或需完整评论时，再评估接入。

### A.1 定位

`github.com/mudrii/gobird` —— Twitter/X 非官方 GraphQL 客户端（Go CLI + 可导入库）。用浏览器会话 cookies 认证，绕过官方 API 限制。

### A.2 Go 库 API（`pkg/bird`）

```go
import "github.com/mudrii/gobird/pkg/bird"

// 认证
client, err := bird.NewWithTokens(authToken, ct0, nil)   // 或 bird.New(creds, nil)
creds, err := bird.ResolveCredentials(bird.ResolveOptions{}) // 自动从浏览器/env/flag 解析

// 读取方法
tweet, err := client.GetTweet(ctx, "12345", nil)                 // 单推文
thread, err := client.GetThread(ctx, "12345", &bird.ThreadOptions{}) // 线程链
replies, err := client.GetReplies(ctx, "12345", &bird.ThreadOptions{}) // 评论
page := client.Search(ctx, "golang", &bird.SearchOptions{Product: "Latest"}) // 搜索
result, err := client.GetUserTweets(ctx, "12345", &bird.UserTweetsOptions{}) // 时间线
```

凭据：`auth_token`（40 hex）+ `ct0`（32–160 alnum）；env `AUTH_TOKEN`/`CT0`（或 `TWITTER_AUTH_TOKEN`/`TWITTER_CT0`）。浏览器 cookie 提取：Safari/Chrome/Firefox，仅 macOS/Linux（Windows 不支持）。

### A.3 版本与兼容性

go.mod 声明 `go 1.26.0`（toolchain `go1.26.2`）。用户已升级 Go 1.27，无碍。依赖：`cobra`、`modernc.org/sqlite`、`tailscale/hujson`、`golang.org/x/sync` 等。

### A.4 风险（重要）

- 违反 X ToS：使用逆向工程私有 API。X ToS 含 `$15,000/百万帖` liquidated damages 条款。
- 账号封禁/停用风险：X 可随时因使用非官方客户端封号。
- 不稳定：GraphQL query IDs 频繁轮换，上游变更可随时破坏行为。
- 若接入，需以 `X_AUTH_TOKEN` / `X_CT0` 环境变量提供凭据，并复用 `AcquireHost` 限流。
