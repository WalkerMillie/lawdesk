package extract

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// OCREngine 은 PDF 한 페이지를 이미지로 렌더해 글자를 읽어낸다.
type OCREngine interface {
	Page(ctx context.Context, pdfPath string, page int) (OCRResult, error)
}

type OCRResult struct {
	Text   string
	Millis int
}

// TesseractOCR 은 mutool 로 페이지를 PNG 로 굽고 tesseract 에 넘긴다.
//
// 파라미터는 합성 스캔본(등기부등본/계약서 사본)으로 실측해 정한 값이다.
//   - PSM 6 : tesseract 기본값인 PSM 3(자동 페이지 분할)은 표와 세로 정렬이 많은
//     한국 법률 서식에서 오작동해 정확도가 37%까지 떨어졌다. PSM 6(단일 텍스트 블록)
//     으로 바꾸면 같은 문서가 96%대로 올라간다.
//   - 150 DPI : 300 DPI 는 정확도 이득 없이 시간·메모리만 3배 쓴다. 일부 문서는
//     오히려 나빠졌다.
//
// 실제 스캔본(접힌 자국, 복사 얼룩, 겹친 도장, 손글씨)은 합성본보다 조건이 나쁘므로
// 현장 정확도는 이보다 낮게 잡아야 한다.
type TesseractOCR struct {
	Tools *Tools

	// Lang 은 tesseract 언어 코드. 빈 값이면 DefaultOCRLang.
	Lang string
	// DPI 는 렌더 해상도. 0이면 DefaultOCRDPI.
	DPI int
	// PSM 은 페이지 분할 모드. 빈 값이면 DefaultOCRPSM.
	PSM string
}

const (
	// DefaultOCRLang 은 "kor" 단독이다.
	//
	// 영문 계약서를 고려해 "kor+eng" 를 써 봤으나 실측에서 오히려 나빠졌다
	// (등기부등본 문자 정확도 98.1% → 93.1%). 영문 모델이 노이즈가 있는
	// 한글 구간에서 경합해 도장 근처 글자가 라틴 문자로 오인된다.
	// 검색어 재현율은 양쪽 100%였으므로, 영문 스캔본이 많은 곳에서는
	// 설정에서 "kor+eng" 로 바꿀 수 있게 열어 둔다.
	DefaultOCRLang = "kor"
	DefaultOCRDPI  = 150
	DefaultOCRPSM  = "6"
)

func NewTesseractOCR(tools *Tools) *TesseractOCR {
	return &TesseractOCR{Tools: tools, Lang: DefaultOCRLang, DPI: DefaultOCRDPI, PSM: DefaultOCRPSM}
}

// Available 은 OCR 을 실제로 수행할 수 있는 상태인지 알려준다.
func (o *TesseractOCR) Available() bool {
	return o != nil && o.Tools != nil && o.Tools.Tesseract != "" && o.Tools.MuTool != ""
}

func (o *TesseractOCR) Page(ctx context.Context, pdfPath string, page int) (OCRResult, error) {
	if !o.Available() {
		return OCRResult{}, fmt.Errorf("OCR 도구를 사용할 수 없습니다")
	}
	start := time.Now()

	dir, err := os.MkdirTemp("", "lawdesk-ocr-")
	if err != nil {
		return OCRResult{}, fmt.Errorf("임시 폴더 생성 실패: %w", err)
	}
	defer os.RemoveAll(dir)

	png := filepath.Join(dir, "page.png")
	if _, err := o.Tools.run(ctx, o.Tools.MuTool,
		"draw", "-F", "png", "-r", strconv.Itoa(o.dpi()),
		"-o", png, pdfPath, strconv.Itoa(page)); err != nil {
		return OCRResult{}, fmt.Errorf("페이지 렌더 실패: %w", err)
	}

	// tesseract 의 출력 인자 "stdout" 은 파일명이 아니라 특수 키워드다.
	out, err := o.Tools.run(ctx, o.Tools.Tesseract,
		png, "stdout", "-l", o.lang(), "--psm", o.psm())
	if err != nil {
		return OCRResult{}, err
	}

	return OCRResult{
		Text:   strings.TrimSpace(string(out)),
		Millis: int(time.Since(start).Milliseconds()),
	}, nil
}

func (o *TesseractOCR) lang() string {
	if o.Lang != "" {
		return o.Lang
	}
	return DefaultOCRLang
}

func (o *TesseractOCR) dpi() int {
	if o.DPI > 0 {
		return o.DPI
	}
	return DefaultOCRDPI
}

func (o *TesseractOCR) psm() string {
	if o.PSM != "" {
		return o.PSM
	}
	return DefaultOCRPSM
}
