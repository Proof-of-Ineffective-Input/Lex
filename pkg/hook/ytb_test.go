package hook

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractVideoID(t *testing.T) {
	cases := []struct {
		url  string
		want string
	}{
		{"https://www.youtube.com/watch?v=MWx0KFdWg7A", "MWx0KFdWg7A"},
		{"https://youtu.be/MWx0KFdWg7A", "MWx0KFdWg7A"},
		{"https://www.youtube.com/shorts/MWx0KFdWg7A", "MWx0KFdWg7A"},
		{"https://www.youtube.com/embed/MWx0KFdWg7A", "MWx0KFdWg7A"},
		{"https://www.youtube.com/live/MWx0KFdWg7A", "MWx0KFdWg7A"},
		{"https://www.youtube.com/watch?v=MWx0KFdWg7A&t=30s", "MWx0KFdWg7A"},
		{"https://example.com/not-youtube", ""},
		{"", ""},
		{"https://www.youtube.com/watch?v=tooshort", ""},
	}
	for _, c := range cases {
		if got := ExtractVideoID(c.url); got != c.want {
			t.Errorf("ExtractVideoID(%q) = %q, want %q", c.url, got, c.want)
		}
	}
}

func TestParseSRT(t *testing.T) {
	srt := `1
00:00:00,000 --> 00:00:02,000
Hello <i>world</i>

2
00:00:02,000 --> 00:00:04,000
Hello world

3
00:00:04,000 --> 00:00:06,000
This is a test
`
	got := parseSRT(srt)
	want := "Hello world\nThis is a test"
	if got != want {
		t.Errorf("parseSRT = %q, want %q", got, want)
	}
}

func TestParseSRTWebVTT(t *testing.T) {
	vtt := `WEBVTT

00:00:00.000 --> 00:00:02.000
First line

00:00:02.000 --> 00:00:04.000
Second line
`
	got := parseSRT(vtt)
	want := "First line\nSecond line"
	if got != want {
		t.Errorf("parseSRT(webvtt) = %q, want %q", got, want)
	}
}

func TestFormatDuration(t *testing.T) {
	cases := []struct {
		sec  float64
		want string
	}{
		{0, "0:00"},
		{65, "1:05"},
		{3600, "1:00:00"},
		{3725, "1:02:05"},
	}
	for _, c := range cases {
		if got := formatDuration(c.sec); got != c.want {
			t.Errorf("formatDuration(%v) = %q, want %q", c.sec, got, c.want)
		}
	}
}

func TestTruncate(t *testing.T) {
	if got := Truncate("hello", 0); got != "hello" {
		t.Errorf("Truncate no-limit = %q, want hello", got)
	}
	if got := Truncate("hello", 10); got != "hello" {
		t.Errorf("Truncate under-limit = %q, want hello", got)
	}
	got := Truncate("hello world", 5)
	if !strings.Contains(got, "hello") || !strings.Contains(got, "truncated") {
		t.Errorf("Truncate over-limit = %q, want truncated marker", got)
	}
}

func TestSplitBudget(t *testing.T) {
	cases := []struct {
		limit, wantT, wantD int
	}{
		{0, 0, 0},
		{3000, 2000, 1000},
		{300, 200, 100},
		{2, 1, 0},
	}
	for _, c := range cases {
		gotT, gotD := splitBudget(c.limit)
		if gotT != c.wantT || gotD != c.wantD {
			t.Errorf("splitBudget(%d) = (%d,%d), want (%d,%d)", c.limit, gotT, gotD, c.wantT, c.wantD)
		}
	}
}

func TestFetchDegradesWithoutYtDlp(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	t.Setenv("PATH", "")
	client := &http.Client{}
	out, err := (YTHook{}).Fetch(context.Background(), client, "https://www.youtube.com/watch?v=MWx0KFdWg7A", 0)
	if err != nil {
		t.Fatalf("Fetch should not error even without yt-dlp: %v", err)
	}
	if !strings.Contains(out, "Title:") {
		t.Errorf("expected metadata Title, got: %q", out)
	}
	if !strings.Contains(out, "yt-dlp") {
		t.Errorf("expected yt-dlp hint for calling AI, got: %q", out)
	}
}

func TestFetchInvalidURL(t *testing.T) {
	client := &http.Client{}
	_, err := (YTHook{}).Fetch(context.Background(), client, "https://example.com", 0)
	if err == nil {
		t.Fatal("expected error for non-YouTube URL")
	}
}

func TestBrowserRetryOrder(t *testing.T) {
	t.Setenv("LEX_YT_BROWSER", "")
	order := browserRetryOrder()
	if len(order) != len(supportedBrowsers)+1 {
		t.Fatalf("browserRetryOrder len = %d, want %d", len(order), len(supportedBrowsers)+1)
	}
	if order[len(order)-1] != "" {
		t.Errorf("last element should be empty fallback, got %q", order[len(order)-1])
	}
	seen := map[string]bool{}
	for _, b := range order {
		if seen[b] {
			t.Errorf("duplicate browser in order: %q", b)
		}
		seen[b] = true
	}
}

func TestBrowserRetryOrderEnv(t *testing.T) {
	t.Setenv("LEX_YT_BROWSER", "firefox")
	order := browserRetryOrder()
	if order[0] != "firefox" {
		t.Errorf("first browser = %q, want firefox", order[0])
	}
}

func TestBrowserRetryOrderLast(t *testing.T) {
	t.Setenv("LEX_YT_BROWSER", "")
	dir := t.TempDir()
	orig := statePath
	statePath = func() string { return filepath.Join(dir, "ytb-browser.txt") }
	defer func() { statePath = orig }()
	saveLastBrowser("brave")
	order := browserRetryOrder()
	if order[0] != "brave" {
		t.Errorf("first browser = %q, want brave", order[0])
	}
}

func TestSaveLoadLastBrowser(t *testing.T) {
	dir := t.TempDir()
	orig := statePath
	statePath = func() string { return filepath.Join(dir, "ytb-browser.txt") }
	defer func() { statePath = orig }()
	saveLastBrowser("edge")
	if got := loadLastBrowser(); got != "edge" {
		t.Errorf("loadLastBrowser = %q, want edge", got)
	}
	saveLastBrowser("not-a-browser")
	if got := loadLastBrowser(); got != "edge" {
		t.Errorf("loadLastBrowser after invalid save = %q, want edge", got)
	}
}

func TestCookieFallbackChainOrder(t *testing.T) {
	t.Setenv("LEX_YT_BROWSER", "")
	dir := t.TempDir()
	origState := statePath
	statePath = func() string { return filepath.Join(dir, "ytb-browser.txt") }
	defer func() { statePath = origState }()
	origCache := defaultCookiesPath
	defaultCookiesPath = func() string { return filepath.Join(dir, "cookies.txt") }
	defer func() { defaultCookiesPath = origCache }()

	if err := os.WriteFile(filepath.Join(dir, "cookies.txt"), []byte("# HTTP Cookie File\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	chain := cookieFallbackChain()
	// 每个浏览器双参数在前，静态缓存兜底，最后无 cookie
	wantLen := len(supportedBrowsers) + 2
	if len(chain) != wantLen {
		t.Fatalf("cookieFallbackChain len = %d, want %d", len(chain), wantLen)
	}
	for i, m := range chain {
		if i < len(supportedBrowsers) {
			if m.browser == "" || m.cacheFile == "" {
				t.Errorf("browser step %d should have both browser and cache, got %+v", i, m)
			}
		} else if i == len(supportedBrowsers) {
			if m.browser != "" || m.cacheFile == "" {
				t.Errorf("cache fallback step should have only cacheFile, got %+v", m)
			}
		} else {
			if m.browser != "" || m.cacheFile != "" {
				t.Errorf("final step should have no cookies, got %+v", m)
			}
		}
	}
}

func TestCookieFallbackChainNoCache(t *testing.T) {
	t.Setenv("LEX_YT_BROWSER", "")
	dir := t.TempDir()
	origState := statePath
	statePath = func() string { return filepath.Join(dir, "ytb-browser.txt") }
	defer func() { statePath = origState }()
	origCache := defaultCookiesPath
	defaultCookiesPath = func() string { return filepath.Join(dir, "cookies.txt") }
	defer func() { defaultCookiesPath = origCache }()

	chain := cookieFallbackChain()
	// 缓存不存在时跳过缓存兜底，只剩浏览器步骤 + 无 cookie
	wantLen := len(supportedBrowsers) + 1
	if len(chain) != wantLen {
		t.Fatalf("cookieFallbackChain without cache len = %d, want %d", len(chain), wantLen)
	}
	last := chain[len(chain)-1]
	if last.browser != "" || last.cacheFile != "" {
		t.Errorf("final step should have no cookies, got %+v", last)
	}
}

func TestCookieModeArgs(t *testing.T) {
	if got := (cookieMode{}).args(); len(got) != 0 {
		t.Errorf("empty mode args = %v, want none", got)
	}
	got := (cookieMode{browser: "edge", cacheFile: "/tmp/c.txt"}).args()
	want := []string{"--cookies-from-browser", "edge", "--cookies", "/tmp/c.txt"}
	if len(got) != len(want) {
		t.Fatalf("args len = %d, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("args[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestReadTranscript(t *testing.T) {
	dir := t.TempDir()
	srt := "1\n00:00:00,000 --> 00:00:02,000\nHello world\n\n2\n00:00:02,000 --> 00:00:04,000\nSecond line\n"
	if err := os.WriteFile(filepath.Join(dir, "abc.srt"), []byte(srt), 0o600); err != nil {
		t.Fatal(err)
	}
	out, ok := readTranscript(dir)
	if !ok {
		t.Fatal("readTranscript should succeed")
	}
	if !strings.Contains(out, "Hello world") || !strings.Contains(out, "Second line") {
		t.Errorf("transcript should contain subtitle lines, got: %q", out)
	}
	if strings.Contains(out, "00:00") {
		t.Errorf("timestamps should be stripped, got: %q", out)
	}
}

func TestReadTranscriptMissing(t *testing.T) {
	dir := t.TempDir()
	if _, ok := readTranscript(dir); ok {
		t.Error("readTranscript should fail when no srt file exists")
	}
}

func TestReadComments(t *testing.T) {
	dir := t.TempDir()
	info := `{"description":"A test description","comments":[{"author":"alice","text":"first comment","like_count":5,"is_pinned":false},{"author":"bob","text":"second comment","like_count":2,"is_pinned":true}]}`
	if err := os.WriteFile(filepath.Join(dir, "abc.info.json"), []byte(info), 0o600); err != nil {
		t.Fatal(err)
	}
	comments, desc, ok := readComments(dir, "abc")
	if !ok {
		t.Fatal("readComments should succeed")
	}
	if desc != "A test description" {
		t.Errorf("description = %q, want %q", desc, "A test description")
	}
	if len(comments) != 2 {
		t.Fatalf("comments len = %d, want 2", len(comments))
	}
	if comments[0].Author != "alice" || comments[0].Text != "first comment" {
		t.Errorf("first comment = %+v", comments[0])
	}
	if comments[1].IsPinned != true {
		t.Errorf("second comment should be pinned, got %+v", comments[1])
	}
}

func TestReadCommentsMissing(t *testing.T) {
	dir := t.TempDir()
	if _, _, ok := readComments(dir, "abc"); ok {
		t.Error("readComments should fail when info.json missing")
	}
}
