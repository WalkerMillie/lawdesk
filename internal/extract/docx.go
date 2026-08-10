package extract

import (
	"archive/zip"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// DocxExtractor 는 .docx(OOXML)에서 텍스트와 제목 구조를 뽑는다.
//
// docx 는 zip 안의 word/document.xml 이 본문이다. 표준 라이브러리만으로 처리되므로
// 외부 바이너리가 필요 없다. 문단 스타일(Heading1/2/…)이 XML에 남아 있어
// 목차를 그대로 복원할 수 있는데, 이게 "LLM 없는 구조 요약"의 핵심 재료다.
type DocxExtractor struct{}

func (DocxExtractor) Extensions() []string { return []string{".docx"} }

// 본문 XML 경로. .docm 도 같은 위치를 쓴다.
const docxBodyPart = "word/document.xml"

func (DocxExtractor) Extract(ctx context.Context, path string, opt Options) (*Doc, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("docx 열기 실패: %w", err)
	}
	defer zr.Close()

	var body io.ReadCloser
	for _, f := range zr.File {
		if f.Name == docxBodyPart {
			body, err = f.Open()
			if err != nil {
				return nil, fmt.Errorf("%s 열기 실패: %w", docxBodyPart, err)
			}
			break
		}
	}
	if body == nil {
		return nil, fmt.Errorf("docx 안에 %s 가 없습니다(손상되었거나 .doc 구형 포맷일 수 있음)", docxBodyPart)
	}
	defer body.Close()

	doc := &Doc{Kind: KindNative}
	if err := parseDocxBody(ctx, body, doc); err != nil {
		return nil, err
	}
	return doc, nil
}

// parseDocxBody 는 document.xml 을 토큰 스트림으로 훑는다.
//
// DOM 으로 통째로 올리지 않는 이유: 수백 페이지 계약서도 메모리 상수로 처리하기 위함.
// 표(w:tbl) 안의 텍스트도 문서 순서대로 w:t 에 나타나므로 별도 처리 없이 포함된다.
func parseDocxBody(ctx context.Context, r io.Reader, doc *Doc) error {
	dec := xml.NewDecoder(r)

	var (
		out      strings.Builder
		para     strings.Builder
		style    string
		inPara   bool
		inText   bool
		inDelete bool // 변경내용 추적에서 '삭제된' 텍스트는 본문이 아니다
		nTok     int
	)

	flushPara := func() {
		if !inPara {
			return
		}
		text := strings.TrimSpace(para.String())
		para.Reset()
		inPara = false
		if text == "" {
			return
		}
		if lvl, ok := headingLevel(style); ok {
			doc.Outline = append(doc.Outline, Heading{Level: lvl, Text: text})
			// 첫 번째 최상위 제목을 문서 제목으로 채택
			if doc.Title == "" && lvl <= 1 {
				doc.Title = text
			}
		}
		out.WriteString(text)
		out.WriteByte('\n')
	}

	for {
		// 큰 파일에서 취소 신호를 놓치지 않도록 주기적으로 확인
		if nTok++; nTok%4096 == 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
		}

		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("document.xml 파싱 실패: %w", err)
		}

		switch t := tok.(type) {
		case xml.StartElement:
			if !isWordML(t.Name.Space) {
				continue
			}
			switch t.Name.Local {
			case "p":
				flushPara() // 중첩 문단(표 셀 안 등) 대비
				inPara, style = true, ""
			case "pStyle":
				style = attrVal(t, "val")
			case "t":
				inText = true
			case "delText":
				inDelete = true
			case "tab":
				para.WriteByte('\t')
			case "br", "cr":
				para.WriteByte('\n')
			case "tc":
				// 표 셀 경계 — 셀 내용이 서로 붙지 않게 구분자를 넣는다
				if para.Len() > 0 {
					para.WriteByte('\t')
				}
			}
		case xml.EndElement:
			if !isWordML(t.Name.Space) {
				continue
			}
			switch t.Name.Local {
			case "p":
				flushPara()
			case "t":
				inText = false
			case "delText":
				inDelete = false
			}
		case xml.CharData:
			if inText && !inDelete {
				para.Write(t)
			}
		}
	}
	flushPara()

	doc.Text = out.String()
	if strings.TrimSpace(doc.Text) == "" {
		doc.Kind = KindEmpty
		doc.Warnings = append(doc.Warnings, "본문 텍스트가 비어 있습니다")
	}
	return nil
}

// isWordML 은 WordprocessingML 네임스페이스인지 확인한다.
// 다른 네임스페이스(그림, 차트 등)의 동명 태그를 본문으로 오인하지 않기 위함.
func isWordML(space string) bool {
	return strings.Contains(space, "wordprocessingml")
}

func attrVal(e xml.StartElement, local string) string {
	for _, a := range e.Attr {
		if a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}

// headingLevel 은 문단 스타일 이름에서 제목 수준을 알아낸다.
// Word 는 로케일/버전에 따라 "Heading1", "heading 1", "1" 등으로 저장한다.
func headingLevel(style string) (int, bool) {
	if style == "" {
		return 0, false
	}
	s := strings.ToLower(strings.TrimSpace(style))
	s = strings.NewReplacer(" ", "", "-", "", "_", "").Replace(s)

	switch s {
	case "title": // 문서 제목 스타일은 최상위로 취급
		return 0, true
	case "subtitle":
		return 1, true
	}

	for _, prefix := range []string{"heading", "제목"} {
		if rest, ok := strings.CutPrefix(s, prefix); ok {
			if n, err := strconv.Atoi(rest); err == nil && n >= 1 && n <= 9 {
				return n, true
			}
		}
	}
	return 0, false
}
