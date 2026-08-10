package extract

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/korean"
	"golang.org/x/text/transform"
)

// PlainExtractor 는 텍스트 계열 파일을 처리한다.
//
// 한국 사무실 파일에는 UTF-8 뿐 아니라 CP949(EUC-KR)로 저장된 메모가 흔하다.
// UTF-8 로 해석되지 않으면 CP949 로 재시도한다.
type PlainExtractor struct{}

func (PlainExtractor) Extensions() []string {
	return []string{".txt", ".md", ".markdown", ".csv", ".log", ".json", ".xml", ".html", ".htm"}
}

// plainMaxBytes 는 텍스트 파일 1개의 읽기 상한(32MB).
// 실수로 거대한 로그를 인덱싱해 메모리를 날리는 걸 막는다.
const plainMaxBytes = 32 << 20

func (PlainExtractor) Extract(ctx context.Context, path string, opt Options) (*Doc, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("파일 열기 실패: %w", err)
	}
	defer f.Close()

	var buf bytes.Buffer
	// LimitReader 로 상한을 걸면 파일이 더 짧아도 에러가 나지 않는다(io.CopyN 과 다른 점).
	if _, err := buf.ReadFrom(io.LimitReader(f, plainMaxBytes)); err != nil {
		return nil, fmt.Errorf("파일 읽기 실패: %w", err)
	}

	doc := &Doc{Kind: KindNative}
	raw := buf.Bytes()

	text, enc, err := decodeText(raw)
	if err != nil {
		return nil, err
	}
	if enc != "UTF-8" {
		doc.Warnings = append(doc.Warnings, "인코딩 "+enc+" 로 해석했습니다")
	}
	doc.Text = text

	if strings.HasSuffix(strings.ToLower(path), ".md") ||
		strings.HasSuffix(strings.ToLower(path), ".markdown") {
		doc.Outline = markdownOutline(text)
		if len(doc.Outline) > 0 && doc.Outline[0].Level <= 1 {
			doc.Title = doc.Outline[0].Text
		}
	}
	if strings.TrimSpace(doc.Text) == "" {
		doc.Kind = KindEmpty
	}
	return doc, nil
}

// decodeText 는 UTF-8 → CP949 순으로 해석을 시도한다.
func decodeText(raw []byte) (text, encoding string, err error) {
	raw = bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF}) // UTF-8 BOM

	if utf8.Valid(raw) {
		return string(raw), "UTF-8", nil
	}
	dec := korean.EUCKR.NewDecoder()
	out, _, derr := transform.Bytes(dec, raw)
	if derr == nil && utf8.Valid(out) {
		return string(out), "CP949", nil
	}
	// 어느 쪽으로도 안 되면 깨진 바이트를 U+FFFD 로 바꿔서라도 살린다.
	return strings.ToValidUTF8(string(raw), "�"), "unknown", nil
}

// markdownOutline 은 # 헤딩을 목차로 뽑는다.
func markdownOutline(text string) []Heading {
	var out []Heading
	inFence := false
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence || !strings.HasPrefix(trimmed, "#") {
			continue
		}
		level := 0
		for level < len(trimmed) && trimmed[level] == '#' {
			level++
		}
		if level > 6 || level >= len(trimmed) || trimmed[level] != ' ' {
			continue
		}
		if t := strings.TrimSpace(trimmed[level:]); t != "" {
			out = append(out, Heading{Level: level, Text: t})
		}
	}
	return out
}
