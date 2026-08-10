// Package extract 는 로컬 문서 파일에서 검색 가능한 텍스트와 구조 정보를 뽑아낸다.
//
// 설계 원칙
//   - 네트워크를 절대 사용하지 않는다. 변호사 비밀유지의무 대상 문서를 다루므로
//     추출 단계는 전 과정 오프라인이어야 한다.
//   - 한 파일이 실패해도 전체 인덱싱은 계속된다. 실패는 Doc.Err 로 담아 반환한다.
package extract

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// Kind 는 문서에서 텍스트를 어떻게 얻었는지를 나타낸다.
type Kind string

const (
	KindNative  Kind = "native"  // 파일에 텍스트가 그대로 들어있음
	KindScanned Kind = "scanned" // 전부 이미지 → OCR로 얻음
	KindMixed   Kind = "mixed"   // 일부 페이지만 이미지
	KindEmpty   Kind = "empty"   // 텍스트를 얻지 못함
)

// Heading 은 문서 내부의 제목 구조. 목차 표시와 "구조 요약"에 쓰인다.
type Heading struct {
	Level int    `json:"level"`
	Text  string `json:"text"`
}

// Doc 은 파일 하나에서 뽑아낸 결과.
type Doc struct {
	Path    string    `json:"path"`
	Ext     string    `json:"ext"`
	Size    int64     `json:"size"`
	ModTime int64     `json:"mod_time"`
	Kind    Kind      `json:"kind"`
	Title   string    `json:"title"`
	Outline []Heading `json:"outline,omitempty"`
	Text    string    `json:"-"`

	Pages     int `json:"pages,omitempty"`
	OCRPages  int `json:"ocr_pages,omitempty"`
	OCRMillis int `json:"ocr_millis,omitempty"`

	Warnings []string `json:"warnings,omitempty"`
	Err      error    `json:"-"`
}

// Summary 는 LLM 없이 만드는 구조 기반 요약.
// 법률 문서는 사람이 이미 첫머리에 요지를 써 두므로 이것만으로도 식별이 된다.
func (d *Doc) Summary(maxRunes int) string {
	var b strings.Builder
	if len(d.Outline) > 0 {
		parts := make([]string, 0, 4)
		for _, h := range d.Outline {
			if len(parts) == 4 {
				break
			}
			parts = append(parts, h.Text)
		}
		b.WriteString(strings.Join(parts, " · "))
		b.WriteString("\n")
	}
	body := strings.TrimSpace(collapseSpace(d.Text))
	b.WriteString(body)
	return truncateRunes(strings.TrimSpace(b.String()), maxRunes)
}

// Extractor 는 확장자별 추출 구현.
type Extractor interface {
	// Extensions 는 이 추출기가 담당하는 소문자 확장자 목록(점 포함).
	Extensions() []string
	// Extract 는 파일을 읽어 Doc 을 채운다.
	Extract(ctx context.Context, path string, opt Options) (*Doc, error)
}

// Options 는 추출 동작 조절값.
type Options struct {
	// OCR 이 nil 이면 스캔 문서를 만나도 OCR 하지 않고 KindScanned 로만 표시한다.
	OCR OCREngine
	// MaxTextBytes 는 한 문서에서 보관할 텍스트 상한. 0이면 무제한.
	MaxTextBytes int
}

// Registry 는 확장자 → 추출기 매핑.
type Registry struct {
	byExt map[string]Extractor
}

func NewRegistry(exs ...Extractor) *Registry {
	r := &Registry{byExt: map[string]Extractor{}}
	for _, e := range exs {
		for _, ext := range e.Extensions() {
			r.byExt[strings.ToLower(ext)] = e
		}
	}
	return r
}

// DefaultRegistry 는 실사용 구성. tools 가 nil 이면 PDF 는 등록되지 않는다.
func DefaultRegistry(tools *Tools) *Registry {
	exs := []Extractor{PlainExtractor{}, DocxExtractor{}}
	if tools != nil && tools.MuTool != "" {
		exs = append(exs, PDFExtractor{Tools: tools})
	}
	return NewRegistry(exs...)
}

// Supports 는 해당 경로를 처리할 수 있는지 알려준다.
func (r *Registry) Supports(path string) bool {
	_, ok := r.byExt[strings.ToLower(filepath.Ext(path))]
	return ok
}

// SupportedExtensions 는 등록된 확장자 목록(정렬 없음).
func (r *Registry) SupportedExtensions() []string {
	out := make([]string, 0, len(r.byExt))
	for ext := range r.byExt {
		out = append(out, ext)
	}
	return out
}

// Extract 는 경로를 적절한 추출기로 넘긴다.
// 추출기가 에러를 내더라도 Doc 은 항상 non-nil 로 반환해 인덱싱이 계속되게 한다.
func (r *Registry) Extract(ctx context.Context, path string, opt Options) *Doc {
	ext := strings.ToLower(filepath.Ext(path))
	doc := &Doc{Path: path, Ext: ext, Kind: KindEmpty}

	if fi, err := os.Stat(path); err == nil {
		doc.Size = fi.Size()
		doc.ModTime = fi.ModTime().Unix()
	}
	// 확장자가 없어도 파일명은 검색 대상이므로 제목은 미리 채워둔다.
	doc.Title = titleFromPath(path)

	ex, ok := r.byExt[ext]
	if !ok {
		doc.Err = fmt.Errorf("지원하지 않는 확장자: %s", ext)
		return doc
	}

	got, err := ex.Extract(ctx, path, opt)
	if err != nil {
		doc.Err = err
		return doc
	}

	// 추출기가 채우지 않은 공통 필드를 보정한다.
	got.Path, got.Ext = path, ext
	got.Size, got.ModTime = doc.Size, doc.ModTime
	if strings.TrimSpace(got.Title) == "" {
		got.Title = doc.Title
	}
	if opt.MaxTextBytes > 0 && len(got.Text) > opt.MaxTextBytes {
		got.Text = safeTruncateBytes(got.Text, opt.MaxTextBytes)
		got.Warnings = append(got.Warnings,
			fmt.Sprintf("텍스트가 %d바이트를 넘어 잘렸습니다", opt.MaxTextBytes))
	}
	if strings.TrimSpace(got.Text) == "" && got.Kind == "" {
		got.Kind = KindEmpty
	}
	return got
}

// ---------------------------------------------------------------- 유틸

func titleFromPath(path string) string {
	base := filepath.Base(path)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// collapseSpace 는 연속 공백/개행을 하나로 줄인다(요약 표시용).
func collapseSpace(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	space := false
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == ' ' {
			space = true
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
	}
	return b.String()
}

func truncateRunes(s string, max int) string {
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	n := 0
	for i := range s {
		if n == max {
			return strings.TrimSpace(s[:i]) + "…"
		}
		n++
	}
	return s
}

// safeTruncateBytes 는 UTF-8 문자 중간에서 자르지 않는다.
func safeTruncateBytes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max]
}
