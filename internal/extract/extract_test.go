package extract

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestLooksLikeTitle(t *testing.T) {
	cases := []struct {
		name string
		line string
		want bool
	}{
		// 진짜 표제
		{"판결문 표제", "대 법 원 판 결", true},
		{"등기부 표제", "등기사항전부증명서", true},
		{"계약서 표제", "물품공급계약서", true},
		{"법원명 포함", "서울중앙지방법원 판결", true},
		{"괄호 포함 표제", "소프트웨어 개발 용역계약서 (수정본)", true},
		{"영문 표제", "Non-Disclosure Agreement", true},
		{"사건번호 표제", "2021다12345 손해배상(기)", true},

		// OCR 노이즈 — 실제로 등기부등본 스캔에서 나왔던 값들
		{"직인 오인식", "ro} 번즈", false},
		{"자모 섞인 노이즈", "ㅎ 번즈", false},
		{"기호 쓰레기", "|| =_ ~~ ##", false},
		{"너무 짧음", "가", false},
		{"빈 줄", "", false},
		{"라틴 두 글자", "ro", false},

		// 경계
		{"음절 정확히 3", "계약서", true},
		{"음절 2개는 부족", "번즈", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := looksLikeTitle(tc.line); got != tc.want {
				t.Errorf("looksLikeTitle(%q) = %v, want %v", tc.line, got, tc.want)
			}
		})
	}
}

func TestFirstMeaningfulLineSkipsOCRNoise(t *testing.T) {
	// 스캔 문서 OCR 결과의 전형적 형태: 직인 노이즈가 표제보다 먼저 나온다.
	text := "ㅎ 번즈\n\n등기사항전부증명서\n[집합건물] 서울특별시 강남구 테헤란로 123\n"
	if got, want := firstMeaningfulLine(text), "등기사항전부증명서"; got != want {
		t.Errorf("firstMeaningfulLine = %q, want %q", got, want)
	}
}

func TestFirstMeaningfulLineGivesUp(t *testing.T) {
	// 쓸 만한 줄이 없으면 빈 문자열 → 호출측이 파일명을 제목으로 쓴다.
	if got := firstMeaningfulLine("~~ ||\n@@ ##\n"); got != "" {
		t.Errorf("노이즈뿐인 입력에서 %q 를 반환했습니다, 빈 문자열이어야 합니다", got)
	}
}

func TestHeadingLevel(t *testing.T) {
	cases := []struct {
		style string
		level int
		ok    bool
	}{
		{"Heading1", 1, true},
		{"heading 2", 2, true},
		{"Heading-3", 3, true},
		{"제목1", 1, true},
		{"Title", 0, true},
		{"Subtitle", 1, true},
		{"Normal", 0, false},
		{"", 0, false},
		{"Heading99", 0, false},
		{"BodyText", 0, false},
	}
	for _, tc := range cases {
		lvl, ok := headingLevel(tc.style)
		if ok != tc.ok || (ok && lvl != tc.level) {
			t.Errorf("headingLevel(%q) = (%d,%v), want (%d,%v)", tc.style, lvl, ok, tc.level, tc.ok)
		}
	}
}

func TestDecodeTextCP949(t *testing.T) {
	// "계약서" 를 CP949 로 인코딩한 바이트열
	cp949 := []byte{0xB0, 0xE8, 0xBE, 0xE0, 0xBC, 0xAD}
	text, enc, err := decodeText(cp949)
	if err != nil {
		t.Fatal(err)
	}
	if enc != "CP949" {
		t.Errorf("인코딩 판정 = %q, want CP949", enc)
	}
	if text != "계약서" {
		t.Errorf("복호 결과 = %q, want 계약서", text)
	}
}

func TestDecodeTextUTF8BOM(t *testing.T) {
	raw := append([]byte{0xEF, 0xBB, 0xBF}, []byte("손해배상")...)
	text, enc, err := decodeText(raw)
	if err != nil {
		t.Fatal(err)
	}
	if enc != "UTF-8" || text != "손해배상" {
		t.Errorf("= (%q,%q), want (손해배상, UTF-8)", text, enc)
	}
}

// TestDocxCorpus 는 실제 생성된 코퍼스로 DOCX 추출을 검증한다.
// testdata/corpus 가 없으면 건너뛴다(gen_corpus.py 로 생성).
func TestDocxCorpus(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "corpus",
		"계약서", "용역계약서_주식회사가나테크_2023.docx")

	reg := NewRegistry(DocxExtractor{})
	doc := reg.Extract(context.Background(), path, Options{})
	if doc.Err != nil {
		t.Skipf("코퍼스 없음(gen_corpus.py 실행 필요): %v", doc.Err)
	}

	if doc.Kind != KindNative {
		t.Errorf("Kind = %q, want native", doc.Kind)
	}
	if doc.Title != "소프트웨어 개발 용역계약서" {
		t.Errorf("Title = %q", doc.Title)
	}

	// 목차가 조문 단위로 잡혀야 한다 — 구조 요약의 핵심 재료
	var heads []string
	for _, h := range doc.Outline {
		heads = append(heads, h.Text)
	}
	for _, want := range []string{"제1조 (계약의 목적)", "제7조 (손해배상)", "제12조 (관할법원)"} {
		if !contains(heads, want) {
			t.Errorf("목차에 %q 가 없습니다. 실제: %v", want, heads)
		}
	}

	// 표(w:tbl) 안의 텍스트도 본문에 포함돼야 검색이 걸린다
	for _, want := range []string{"착수금", "45,000,000원", "중간 산출물 검수 완료 시"} {
		if !strings.Contains(doc.Text, want) {
			t.Errorf("표 내용 %q 가 본문에 없습니다", want)
		}
	}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
