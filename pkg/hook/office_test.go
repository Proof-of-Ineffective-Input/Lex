package hook

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// markitdownTestdata 定位 markitdown 模块缓存中的 testdata 目录。
func markitdownTestdata(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	candidates := []string{
		filepath.Join(home, "go", "pkg", "mod", "github.com", "conductor-oss", "markitdown@v0.0.1", "testdata"),
	}
	if gmc := os.Getenv("GOMODCACHE"); gmc != "" {
		candidates = append([]string{filepath.Join(gmc, "github.com", "conductor-oss", "markitdown@v0.0.1", "testdata")}, candidates...)
	}
	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && fi.IsDir() {
			return c
		}
	}
	t.Fatal("markitdown testdata not found; run 'go mod download github.com/conductor-oss/markitdown' first")
	return ""
}

func TestConvertOfficeDOCXFromMarkitdownTestdata(t *testing.T) {
	dir := markitdownTestdata(t)
	data, err := os.ReadFile(filepath.Join(dir, "test.docx"))
	if err != nil {
		t.Fatalf("read test.docx: %v", err)
	}
	out, err := ConvertOffice(data, "application/vnd.openxmlformats-officedocument.wordprocessingml.document", "test.docx")
	if err != nil {
		t.Fatalf("ConvertOffice docx: %v", err)
	}
	if strings.TrimSpace(out) == "" {
		t.Fatal("docx convert produced empty output")
	}
}

func TestConvertOfficePDFFromMarkitdownTestdata(t *testing.T) {
	dir := markitdownTestdata(t)
	data, err := os.ReadFile(filepath.Join(dir, "test.pdf"))
	if err != nil {
		t.Fatalf("read test.pdf: %v", err)
	}
	out, err := ConvertOffice(data, "application/pdf", "test.pdf")
	if err != nil {
		t.Fatalf("ConvertOffice pdf: %v", err)
	}
	if strings.TrimSpace(out) == "" {
		t.Fatal("pdf convert produced empty output")
	}
}

func TestConvertOfficeNoTextLayerHint(t *testing.T) {
	// 最小无文本层 PDF：仅含对象头与页面壳，无内容流文本
	minimal := []byte("%PDF-1.4\n1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] >>\nendobj\nxref\n0 4\n0000000000 65535 f \n0000000009 00000 n \n0000000058 00000 n \n0000000115 00000 n \ntrailer\n<< /Size 4 /Root 1 0 R >>\nstartxref\n190\n%%EOF\n")
	out, err := ConvertOffice(minimal, "application/pdf", "no-text.pdf")
	if err == nil {
		t.Fatalf("expected no-text-layer error, got: %q", out)
	}
	if !strings.Contains(err.Error(), "无文本层") {
		t.Fatalf("expected 无文本层 hint, got: %v", err)
	}
}

func TestIsOfficeMIME(t *testing.T) {
	cases := map[string]bool{
		"application/pdf":                true,
		"application/pdf; charset=utf-8": true,
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document": true,
		"text/html":                false,
		"application/octet-stream": false,
	}
	for ct, want := range cases {
		if got := IsOfficeMIME(ct); got != want {
			t.Errorf("IsOfficeMIME(%q) = %v, want %v", ct, got, want)
		}
	}
}
