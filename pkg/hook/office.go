package hook

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"

	markitdown "github.com/conductor-oss/markitdown"
)

var md = markitdown.New()

var officeExts = map[string]bool{
	".pdf":  true,
	".docx": true,
	".xlsx": true,
	".xls":  true,
	".pptx": true,
}

var officeMimes = []string{
	"application/pdf",
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	"application/vnd.ms-excel",
	"application/vnd.openxmlformats-officedocument.presentationml.presentation",
}

// OfficeHook 处理文档 URL（.pdf/.docx/.xlsx/.xls/.pptx）直转 Markdown。
type OfficeHook struct{}

// Name 实现 Hook。
func (OfficeHook) Name() string { return "office" }

// Match 实现 Hook：URL 后缀命中文档类型。
func (OfficeHook) Match(target string) bool {
	return officeExtFromURL(target) != ""
}

// Fetch 实现 Hook：抓取文档字节并直转 Markdown。
func (OfficeHook) Fetch(ctx context.Context, client *http.Client, target string, limit int) (string, error) {
	data, contentType, err := readURL(ctx, client, target)
	if err != nil {
		return "", err
	}
	mdText, err := ConvertOffice(data, contentType, target)
	if err != nil {
		return "", err
	}
	return Truncate(mdText, limit), nil
}

// IsOfficeURL 判断 URL 是否指向支持直转的文档（file url hook）。
func IsOfficeURL(target string) bool {
	return officeExtFromURL(target) != ""
}

// officeExtFromURL 从 URL 路径提取文档扩展名（小写，含点）。
func officeExtFromURL(target string) string {
	p := target
	if i := strings.IndexByte(p, '?'); i >= 0 {
		p = p[:i]
	}
	if i := strings.IndexByte(p, '#'); i >= 0 {
		p = p[:i]
	}
	ext := strings.ToLower(path.Ext(p))
	if officeExts[ext] {
		return ext
	}
	return ""
}

// IsOfficeMIME 判断 Content-Type 是否为支持的文档类型。
func IsOfficeMIME(contentType string) bool {
	ct := strings.ToLower(strings.TrimSpace(contentType))
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	for _, m := range officeMimes {
		if ct == m {
			return true
		}
	}
	return false
}

// ConvertOffice 将文档字节直转为 Markdown。
func ConvertOffice(data []byte, contentType, target string) (string, error) {
	ext := officeExtFromURL(target)
	info := markitdown.StreamInfo{
		Extension: ext,
		MIMEType:  contentType,
	}
	res, err := md.ConvertReader(bytes.NewReader(data), info)
	if err != nil {
		var unsupported *markitdown.UnsupportedFormatError
		if errors.As(err, &unsupported) {
			return "", fmt.Errorf("unsupported document format")
		}
		return "", err
	}
	text := res.Markdown
	if strings.TrimSpace(text) == "" || text == "[No readable text content found in PDF]" {
		return "", fmt.Errorf("PDF has no text layer; OCR required, which is beyond current capabilities")
	}
	return text, nil
}

func init() {
	Register(OfficeHook{})
}
