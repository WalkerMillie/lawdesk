package extract

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
)

// PDFExtractor 는 mutool 로 PDF 텍스트를 뽑고, 텍스트가 없는 페이지는 OCR 로 넘긴다.
//
// 순수 Go PDF 라이브러리 대신 mutool(MuPDF)을 쓰는 이유:
// 한글 CID 폰트 매핑 처리가 검증돼 있어서 판결문/계약서에서 글자가 깨지지 않는다.
// 별도 프로세스로 호출하므로 라이선스(AGPL) 전이 문제도 없다.
type PDFExtractor struct {
	Tools *Tools
}

func (PDFExtractor) Extensions() []string { return []string{".pdf"} }

// scannedPageThreshold 는 "이 페이지는 이미지다"라고 판정하는 글자 수 기준.
//
// 스캔 페이지도 머리말/페이지번호가 텍스트로 박혀 몇 글자 나오는 경우가 있어
// 0 이 아니라 여유를 둔다. 반대로 표지처럼 원래 글자가 적은 페이지를
// 오탐할 수 있으나, OCR 을 한 번 더 돌릴 뿐 결과가 틀리지는 않는다.
const scannedPageThreshold = 40

func (e PDFExtractor) Extract(ctx context.Context, path string, opt Options) (*Doc, error) {
	if e.Tools == nil || e.Tools.MuTool == "" {
		return nil, fmt.Errorf("mutool 이 없어 PDF 를 처리할 수 없습니다")
	}

	pages, err := e.pageCount(ctx, path)
	if err != nil {
		return nil, err
	}
	if pages == 0 {
		return &Doc{Kind: KindEmpty, Warnings: []string{"페이지가 없는 PDF"}}, nil
	}

	doc := &Doc{Pages: pages}
	var (
		body      strings.Builder
		needOCR   []int
		nativeCnt int
	)

	for p := 1; p <= pages; p++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		text, err := e.pageText(ctx, path, p)
		if err != nil {
			doc.Warnings = append(doc.Warnings,
				fmt.Sprintf("%d쪽 텍스트 추출 실패: %v", p, err))
			needOCR = append(needOCR, p)
			continue
		}
		if countMeaningful(text) < scannedPageThreshold {
			needOCR = append(needOCR, p)
			continue
		}
		nativeCnt++
		body.WriteString(text)
		body.WriteString("\n")
	}

	// 스캔 페이지 OCR
	if len(needOCR) > 0 && opt.OCR != nil {
		for _, p := range needOCR {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
			}
			res, err := opt.OCR.Page(ctx, path, p)
			if err != nil {
				doc.Warnings = append(doc.Warnings,
					fmt.Sprintf("%d쪽 OCR 실패: %v", p, err))
				continue
			}
			doc.OCRPages++
			doc.OCRMillis += res.Millis
			body.WriteString(res.Text)
			body.WriteString("\n")
		}
	} else if len(needOCR) > 0 {
		doc.Warnings = append(doc.Warnings,
			fmt.Sprintf("이미지 페이지 %d쪽이 OCR 없이 건너뛰어졌습니다", len(needOCR)))
	}

	doc.Text = body.String()

	switch {
	case strings.TrimSpace(doc.Text) == "":
		doc.Kind = KindEmpty
	case len(needOCR) == 0:
		doc.Kind = KindNative
	case nativeCnt == 0:
		doc.Kind = KindScanned
	default:
		doc.Kind = KindMixed
	}

	doc.Title = firstMeaningfulLine(doc.Text)
	return doc, nil
}

// pageCount 는 `mutool info` 출력에서 "Pages: N" 을 읽는다.
func (e PDFExtractor) pageCount(ctx context.Context, path string) (int, error) {
	out, err := e.Tools.run(ctx, e.Tools.MuTool, "info", path)
	if err != nil {
		return 0, fmt.Errorf("PDF 정보 읽기 실패(암호가 걸렸거나 손상된 파일일 수 있음): %w", err)
	}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if rest, ok := strings.CutPrefix(line, "Pages:"); ok {
			n, err := strconv.Atoi(strings.TrimSpace(rest))
			if err != nil {
				return 0, fmt.Errorf("페이지 수 해석 실패: %q", line)
			}
			return n, nil
		}
	}
	return 0, fmt.Errorf("PDF 에서 페이지 수를 찾지 못했습니다")
}

// pageText 는 특정 페이지의 텍스트 레이어를 뽑는다.
func (e PDFExtractor) pageText(ctx context.Context, path string, page int) (string, error) {
	out, err := e.Tools.run(ctx, e.Tools.MuTool,
		"draw", "-F", "txt", "-o", "-", path, strconv.Itoa(page))
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// countMeaningful 은 공백을 뺀 글자 수를 센다(스캔 여부 판정용).
func countMeaningful(s string) int {
	n := 0
	for _, r := range s {
		switch r {
		case ' ', '\t', '\n', '\r', '\f', '\v', ' ':
		default:
			n++
		}
	}
	return n
}

// firstMeaningfulLine 은 앞부분 줄들 중 제목으로 쓸 만한 첫 줄을 고른다.
//
// 무조건 첫 줄을 쓰면 안 된다. 스캔 문서에서는 상단의 직인·머리말·얼룩이
// OCR 노이즈("ro} 번즈" 같은 것)로 먼저 나와 제목 자리를 차지한다.
// 그래서 글자 구성을 보고 노이즈를 걸러낸 뒤, 쓸 만한 줄이 없으면
// 빈 문자열을 돌려줘 호출측이 파일명을 제목으로 쓰게 한다.
func firstMeaningfulLine(text string) string {
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for n := 0; sc.Scan() && n < titleScanLines; n++ {
		line := collapseSpace(strings.TrimSpace(sc.Text()))
		if looksLikeTitle(line) {
			return truncateRunes(line, 80)
		}
	}
	return ""
}

// titleScanLines 는 제목 후보를 찾을 때 살펴볼 앞쪽 줄 수.
const titleScanLines = 15

// looksLikeTitle 은 줄이 사람이 쓴 표제처럼 보이는지 판정한다.
func looksLikeTitle(line string) bool {
	var total, valid, syllable, latin int
	for _, r := range line {
		if r == ' ' {
			continue
		}
		total++
		switch {
		case r >= 0xAC00 && r <= 0xD7A3: // 한글 완성형 음절
			syllable++
			valid++
		case r >= 0x1100 && r <= 0x11FF, // 한글 자모
			r >= 0x3130 && r <= 0x318F: // 호환 자모
			// 낱자 자모는 정상 표제에 거의 나오지 않는다.
			// OCR 이 뭉개진 글자를 자모로 뱉는 경우가 많으므로
			// 유효 문자로만 세고 제목 판정 근거로는 삼지 않는다.
			valid++
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z':
			latin++
			valid++
		case r >= '0' && r <= '9':
			valid++
		case strings.ContainsRune("()[]【】<>·.,-–—/:'\"", r):
			valid++ // 표제에 흔히 쓰이는 문장부호는 감점하지 않는다
		}
	}
	if total < 3 || total > 80 {
		return false
	}
	// 기호 쓰레기가 섞인 줄 배제
	if float64(valid)/float64(total) < 0.8 {
		return false
	}
	// 짧은 한글 조각(OCR 노이즈)이나 의미 없는 라틴 몇 글자를 배제
	return syllable >= 3 || latin >= 5
}
