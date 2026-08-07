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
	out := RerankByChars(content, "capital gains tax land value", 120)
	if out == "" {
		t.Fatal("expected non-empty output")
	}
	if !strings.Contains(out, "capital gains tax") {
		t.Errorf("query-relevant sentence should be kept, got: %q", out)
	}
	if !strings.Contains(out, "Land value") {
		t.Errorf("query-relevant sentence should be kept, got: %q", out)
	}
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

func TestDedupeExactDuplicate(t *testing.T) {
	in := []string{"The capital gains tax is a major policy debate.", "The capital gains tax is a major policy debate."}
	out := dedupeSentences(in)
	if len(out) != 1 {
		t.Errorf("exact duplicate should collapse to 1, got %d: %v", len(out), out)
	}
}

func TestDedupeNearDuplicate(t *testing.T) {
	in := []string{
		"The capital gains tax is a major policy debate.",
		"The capital gains tax is a major policy debate!",
	}
	out := dedupeSentences(in)
	if len(out) != 1 {
		t.Errorf("near-duplicate (punctuation diff) should collapse to 1, got %d: %v", len(out), out)
	}
}

func TestDedupeKeepsDistinct(t *testing.T) {
	in := []string{
		"The capital gains tax is a major policy debate.",
		"Land value taxation affects property owners.",
	}
	out := dedupeSentences(in)
	if len(out) != 2 {
		t.Errorf("distinct sentences should be kept, got %d: %v", len(out), out)
	}
}

func TestDedupeKeepsFirstOrder(t *testing.T) {
	in := []string{
		"First unique sentence about taxes.",
		"Second unique sentence about land.",
		"First unique sentence about taxes.",
	}
	out := dedupeSentences(in)
	if len(out) != 2 {
		t.Fatalf("expected 2 after dedupe, got %d: %v", len(out), out)
	}
	if out[0] != in[0] || out[1] != in[1] {
		t.Errorf("order should preserve first occurrences, got: %v", out)
	}
}

func TestDedupeRerankIntegration(t *testing.T) {
	content := "The capital gains tax is a major policy debate. " +
		"The capital gains tax is a major policy debate. " +
		"Land value taxation affects property owners."
	out := RerankByChars(content, "capital gains tax land value", 200)
	if strings.Count(out, "capital gains tax is a major policy debate") != 1 {
		t.Errorf("duplicate sentence should appear once in rerank output, got: %q", out)
	}
}
