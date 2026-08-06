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

// TestFetchDegradesWithoutYtDlp 验证无 yt-dlp 时降级：返回元数据 + 提示，不报错。
// 该测试依赖网络访问 oEmbed；若离线则跳过。
func TestFetchDegradesWithoutYtDlp(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	// 用假 PATH 确保找不到 yt-dlp，验证降级路径
	t.Setenv("PATH", "")
	client := &http.Client{}
	out, err := Fetch(context.Background(), client, "https://www.youtube.com/watch?v=MWx0KFdWg7A", 0)
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

// TestFetchInvalidURL 验证非 YouTube URL 返回错误。
func TestFetchInvalidURL(t *testing.T) {
	client := &http.Client{}
	_, err := Fetch(context.Background(), client, "https://example.com", 0)
	if err == nil {
		t.Fatal("expected error for non-YouTube URL")
	}
}

// TestCookiesPathEnv 验证环境变量 LEX_YT_COOKIES 优先于默认路径。
func TestCookiesPathEnv(t *testing.T) {
	t.Setenv("LEX_YT_COOKIES", "C:/custom/cookies.txt")
	if got := cookiesPath(); got != "C:/custom/cookies.txt" {
		t.Errorf("cookiesPath with env = %q, want C:/custom/cookies.txt", got)
	}
}

// TestCookiesPathDefault 验证无环境变量时回退到 {userprofile}/.lex/cookies.txt。
func TestCookiesPathDefault(t *testing.T) {
	t.Setenv("LEX_YT_COOKIES", "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home dir: %v", err)
	}
	want := filepath.Join(home, ".lex", "cookies.txt")
	if got := cookiesPath(); got != want {
		t.Errorf("cookiesPath default = %q, want %q", got, want)
	}
}
