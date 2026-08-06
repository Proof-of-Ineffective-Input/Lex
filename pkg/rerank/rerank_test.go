package rerank

import (
	"strings"
	"testing"
)

func TestRerankByCharsNoQuery(t *testing.T) {
	content := "This is the first sentence about taxes. " +
		"This is the second sentence about land. " +
		"This is the third sentence about capital gains."
	out := RerankByChars(content, "", 40)
	if out == "" {
		t.Fatal("expected non-empty output")
	}
	if !strings.Contains(out, "first sentence") {
		t.Errorf("no-query rerank should keep leading sentences, got: %q", out)
	}
}

func TestRerankByCharsWithQuery(t *testing.T) {
	content := "Welcome back to the channel and thanks for watching. " +
		"The capital gains tax is a major policy debate. " +
		"Please subscribe for more videos. " +
		"Land value taxation affects property owners."
	// 预算收紧到只能容纳 2 个短句，验证 rerank 优先保留 query 相关句子
	out := RerankByChars(content, "capital gains tax land value", 120)
	if out == "" {
		t.Fatal("expected non-empty output")
	}
	// query 相关句子应被优先保留
	if !strings.Contains(out, "capital gains tax") {
		t.Errorf("query-relevant sentence should be kept, got: %q", out)
	}
	if !strings.Contains(out, "Land value") {
		t.Errorf("query-relevant sentence should be kept, got: %q", out)
	}
	// 客套话（welcome/subscribe）在预算紧张时应被过滤
	if strings.Contains(out, "Welcome back") {
		t.Errorf("greeting should be filtered under tight budget, got: %q", out)
	}
	if strings.Contains(out, "subscribe") {
		t.Errorf("subscribe call-to-action should be filtered under tight budget, got: %q", out)
	}
}

func TestRerankByCharsBudget(t *testing.T) {
	content := strings.Repeat("This is a sentence about taxes. ", 50)
	out := RerankByChars(content, "tax", 100)
	if len([]rune(out)) > 100 {
		t.Errorf("output %d chars exceeds budget 100", len([]rune(out)))
	}
	if out == "" {
		t.Fatal("expected non-empty output")
	}
}

func TestScorePage(t *testing.T) {
	if ScorePage("", "query") != 0 {
		t.Error("empty content should score 0")
	}
	if ScorePage("content", "") != 0 {
		t.Error("empty query should score 0")
	}
	high := ScorePage("tax land value capital gains debate", "tax land")
	low := ScorePage("cooking recipes pasta tomato", "tax land")
	if high <= low {
		t.Errorf("relevant page should score higher: high=%v low=%v", high, low)
	}
}
