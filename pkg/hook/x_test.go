package hook

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestXMatch(t *testing.T) {
	cases := []struct {
		url  string
		want bool
	}{
		{"https://x.com/elonmusk/status/2096844931520225342", true},
		{"https://twitter.com/elonmusk/status/2096844931520225342", true},
		{"https://x.com/elonmusk", true},
		{"https://twitter.com/elonmusk", true},
		{"https://x.com/search?q=golang", true},
		{"https://x.com/search?q=golang&src=typed_query", true},
		{"https://example.com/article", false},
		{"https://www.youtube.com/watch?v=abc", false},
		{"https://api.fxtwitter.com/2/status/123", false},
	}
	for _, c := range cases {
		if got := (XHook{}).Match(c.url); got != c.want {
			t.Errorf("Match(%q) = %v, want %v", c.url, got, c.want)
		}
	}
}

func TestClassifyXURL(t *testing.T) {
	cases := []struct {
		url    string
		kind   xURLKind
		id     string
		handle string
		query  string
	}{
		{"https://x.com/elonmusk/status/2096844931520225342", xStatusURL, "2096844931520225342", "elonmusk", ""},
		{"https://twitter.com/elonmusk/status/2096844931520225342", xStatusURL, "2096844931520225342", "elonmusk", ""},
		{"https://x.com/elonmusk", xProfileURL, "", "elonmusk", ""},
		{"https://x.com/search?q=golang", xSearchURL, "", "", "golang"},
		{"https://x.com/search?q=golang&src=typed_query", xSearchURL, "", "", "golang"},
	}
	for _, c := range cases {
		got := classifyXURL(c.url)
		if got.kind != c.kind || got.id != c.id || got.handle != c.handle || got.query != c.query {
			t.Errorf("classifyXURL(%q) = %+v, want kind=%v id=%q handle=%q query=%q", c.url, got, c.kind, c.id, c.handle, c.query)
		}
	}
}

const xConvJSON = `{
	"status": {
		"id": "2096844931520225342",
		"url": "https://x.com/elonmusk/status/2096844931520225342",
		"text": "Illuminating",
		"created_at": "Mon Sep 07 06:16:03 +0000 2026",
		"created_timestamp": 1788761763,
		"likes": 2680,
		"reposts": 452,
		"quotes": 31,
		"replies": 523,
		"views": 987858,
		"bookmarks": 301,
		"author": {
			"screen_name": "elonmusk",
			"name": "Elon Musk",
			"id": "44196397",
			"followers": 241592257,
			"following": 1405,
			"statuses": 108219,
			"description": "http://Terafab.AI",
			"verification": {"verified": true}
		},
		"lang": "en",
		"source": "Twitter for iPhone",
		"is_note_tweet": false
	},
	"replies": [
		{
			"id": "2096865840025022719",
			"url": "https://x.com/grok/status/2096865840025022719",
			"text": "Precisely. Global unitarity holds.",
			"created_at": "Mon Sep 07 07:39:08 +0000 2026",
			"likes": 0,
			"reposts": 0,
			"quotes": 0,
			"replies": 1,
			"bookmarks": 0,
			"author": {
				"screen_name": "grok",
				"name": "Grok",
				"id": "1720665183188922368",
				"followers": 9042361,
				"following": 4,
				"statuses": 139286284,
				"description": "@grok it",
				"verification": {"verified": true}
			},
			"lang": "en",
			"source": "Twitter Web App",
			"is_note_tweet": true
		}
	],
	"author": {
		"screen_name": "elonmusk",
		"name": "Elon Musk",
		"id": "44196397",
		"followers": 241592257,
		"following": 1405,
		"statuses": 108219,
		"description": "http://Terafab.AI",
		"verification": {"verified": true}
	},
	"cursor": {"bottom": "abc"},
	"code": 200
}`

const xSearchJSON = `{
	"code": 200,
	"results": [
		{
			"id": "2096870374021472473",
			"url": "https://x.com/Dipak_Prasad1/status/2096870374021472473",
			"text": "@golang Agree — AI as a teammate only works with review gates.",
			"created_at": "Mon Sep 07 07:57:09 +0000 2026",
			"likes": 0,
			"reposts": 0,
			"quotes": 0,
			"replies": 0,
			"bookmarks": 0,
			"author": {
				"screen_name": "Dipak_Prasad1",
				"name": "Dipak Kr Prasad",
				"id": "123456",
				"followers": 417,
				"following": 124,
				"statuses": 521,
				"description": "flutter developer",
				"verification": {"verified": false}
			},
			"lang": "en",
			"source": "Twitter Web App",
			"is_note_tweet": false
		}
	],
	"cursor": {"top": "abc", "bottom": "def"}
}`

func TestXFetchStatus(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/2/conversation/2096844931520225342" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(xConvJSON))
	}))
	defer ts.Close()
	orig := fxtwitterBase
	fxtwitterBase = ts.URL
	defer func() { fxtwitterBase = orig }()

	out, err := (XHook{}).Fetch(context.Background(), &http.Client{}, "https://x.com/elonmusk/status/2096844931520225342", 0)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !strings.Contains(out, "Illuminating") {
		t.Errorf("expected tweet text, got: %q", out)
	}
	if !strings.Contains(out, "Elon Musk") {
		t.Errorf("expected author name, got: %q", out)
	}
	if !strings.Contains(out, "Precisely") {
		t.Errorf("expected reply text, got: %q", out)
	}
}

func TestXFetchSearch(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/2/search" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if r.URL.Query().Get("q") != "golang" {
			t.Errorf("unexpected query q=%q", r.URL.Query().Get("q"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(xSearchJSON))
	}))
	defer ts.Close()
	orig := fxtwitterBase
	fxtwitterBase = ts.URL
	defer func() { fxtwitterBase = orig }()

	out, err := (XHook{}).Fetch(context.Background(), &http.Client{}, "https://x.com/search?q=golang", 0)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !strings.Contains(out, "Dipak Kr Prasad") {
		t.Errorf("expected author name, got: %q", out)
	}
	if !strings.Contains(out, "review gates") {
		t.Errorf("expected search result text, got: %q", out)
	}
}

func TestXFetchProfile(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/2/profile/elonmusk/statuses" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(xSearchJSON))
	}))
	defer ts.Close()
	orig := fxtwitterBase
	fxtwitterBase = ts.URL
	defer func() { fxtwitterBase = orig }()

	out, err := (XHook{}).Fetch(context.Background(), &http.Client{}, "https://x.com/elonmusk", 0)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !strings.Contains(out, "Dipak Kr Prasad") {
		t.Errorf("expected timeline result, got: %q", out)
	}
}

func TestXFetchFallback(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"code":500}`))
	}))
	defer ts.Close()
	orig := fxtwitterBase
	fxtwitterBase = ts.URL
	defer func() { fxtwitterBase = orig }()

	origDirect := xDirectFetcher
	xDirectFetcher = func(ctx context.Context, client *http.Client, target string, limit int) (string, error) {
		return "fallback content", nil
	}
	defer func() { xDirectFetcher = origDirect }()

	out, err := (XHook{}).Fetch(context.Background(), &http.Client{}, "https://x.com/elonmusk/status/2096844931520225342", 0)
	if err != nil {
		t.Fatalf("Fetch should not error on fallback: %v", err)
	}
	if !strings.Contains(out, "fallback content") {
		t.Errorf("expected fallback content, got: %q", out)
	}
}

func TestXFetch404Empty(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"code":404}`))
	}))
	defer ts.Close()
	orig := fxtwitterBase
	fxtwitterBase = ts.URL
	defer func() { fxtwitterBase = orig }()

	// search 404 视为空结果，不降级
	out, err := (XHook{}).Fetch(context.Background(), &http.Client{}, "https://x.com/search?q=unknown", 0)
	if err != nil {
		t.Fatalf("Fetch should not error on 404 empty: %v", err)
	}
	if out != "" {
		t.Errorf("expected empty result, got: %q", out)
	}
}

func TestXFetchTruncate(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(xConvJSON))
	}))
	defer ts.Close()
	orig := fxtwitterBase
	fxtwitterBase = ts.URL
	defer func() { fxtwitterBase = orig }()

	out, err := (XHook{}).Fetch(context.Background(), &http.Client{}, "https://x.com/elonmusk/status/2096844931520225342", 20)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !strings.Contains(out, "truncated") {
		t.Errorf("expected truncation marker, got: %q", out)
	}
}
