package index

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/WalkerMillie/lawdesk/internal/extract"
)

func openTemp(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func put(t *testing.T, db *DB, root, path, title, body string) {
	t.Helper()
	doc := &extract.Doc{
		Path: path, Ext: filepath.Ext(path), Kind: extract.KindNative,
		Title: title, Text: body, Size: int64(len(body)), ModTime: 1,
	}
	if err := db.Put(root, doc); err != nil {
		t.Fatalf("Put(%s): %v", path, err)
	}
}

const root = "/docs"

func seed(t *testing.T, db *DB) {
	t.Helper()
	put(t, db, root, "/docs/계약서/용역계약서.docx", "소프트웨어 개발 용역계약서",
		"제7조 (손해배상) 을의 귀책사유로 손해가 발생한 경우 배상책임을 진다. 총 계약금액을 한도로 한다.")
	put(t, db, root, "/docs/판례/2021다12345.pdf", "대법원 판결",
		"대법원 2021. 5. 13. 선고 2021다12345 판결. 채무불이행으로 인한 손해배상 책임이 인정된다.")
	put(t, db, root, "/docs/계약서/NDA.docx", "비밀유지계약서",
		"This Non-Disclosure Agreement shall be governed by the laws of the Republic of Korea.")
}

func TestSearchScenarios(t *testing.T) {
	db := openTemp(t)
	seed(t, db)

	cases := []struct {
		name     string
		query    string
		wantHits int
		wantIn   string // 결과 rel_path 에 포함돼야 할 문자열
		wantMode string
	}{
		{"일반 단어", "손해배상", 2, "", "match"},
		{"어절 중간 부분일치", "배상책임", 1, "용역계약서", "match"},
		{"사건번호 정확검색", "2021다12345", 1, "2021다12345", "match"},
		{"조문 번호", "제7조", 1, "용역계약서", "match"},
		{"구문 검색", "채무불이행으로 인한", 1, "2021다12345", "match"},
		{"하이픈 포함(FTS5 연산자)", "Non-Disclosure", 1, "NDA", "match"},
		{"2글자 → LIKE 폴백", "판결", 1, "", "like"},
		{"파일명으로 검색", "NDA", 1, "NDA", "match"},
		{"결과 없음", "존재하지않는문구", 0, "", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := db.Search(tc.query, 50)
			if err != nil {
				t.Fatalf("Search(%q): %v", tc.query, err)
			}
			if res.Total != tc.wantHits {
				var got []string
				for _, h := range res.Hits {
					got = append(got, h.RelPath)
				}
				t.Fatalf("Search(%q) = %d건, want %d. 결과: %v", tc.query, res.Total, tc.wantHits, got)
			}
			if tc.wantMode != "" && res.Mode != tc.wantMode {
				t.Errorf("mode = %q, want %q", res.Mode, tc.wantMode)
			}
			if tc.wantIn != "" {
				found := false
				for _, h := range res.Hits {
					if strings.Contains(h.RelPath, tc.wantIn) {
						found = true
					}
				}
				if !found {
					t.Errorf("결과에 %q 를 포함하는 항목이 없습니다", tc.wantIn)
				}
			}
		})
	}
}

// FTS5 연산자 문자가 들어와도 크래시하지 않아야 한다.
// 사용자는 검색창에 무엇이든 칠 수 있다.
func TestSearchNeverCrashesOnOperators(t *testing.T) {
	db := openTemp(t)
	seed(t, db)

	for _, q := range []string{
		`(주)가나`, `"인용부호"`, `A OR B`, `NEAR(a b)`, `*`, `-손해`, `a:b`,
		`^시작`, `{중괄호}`, `back\slash`, `100%`, `under_score`, `'`, `""`,
	} {
		if _, err := db.Search(q, 10); err != nil {
			t.Errorf("Search(%q) 가 에러를 냈습니다: %v", q, err)
		}
	}
}

func TestIncrementalReplace(t *testing.T) {
	db := openTemp(t)
	path := "/docs/메모.txt"

	put(t, db, root, path, "메모", "원래 내용은 임대차 관련")
	if res, _ := db.Search("임대차", 10); res.Total != 1 {
		t.Fatalf("초기 색인 실패")
	}

	// 같은 경로를 다시 Put → 교체되어야 하고 중복 행이 남으면 안 된다
	put(t, db, root, path, "메모", "바뀐 내용은 손해배상 관련")

	if res, _ := db.Search("임대차", 10); res.Total != 0 {
		t.Errorf("옛 내용이 검색됩니다 — FTS 인덱스가 갱신되지 않았습니다")
	}
	res, _ := db.Search("손해배상", 10)
	if res.Total != 1 {
		t.Errorf("새 내용 검색 = %d건, want 1", res.Total)
	}

	if st, _ := db.Stats(); st.Docs != 1 {
		t.Errorf("문서 수 = %d, want 1 (중복 행이 남았습니다)", st.Docs)
	}
}

func TestDeleteByPath(t *testing.T) {
	db := openTemp(t)
	seed(t, db)

	if err := db.DeleteByPath("/docs/계약서/NDA.docx"); err != nil {
		t.Fatal(err)
	}
	if res, _ := db.Search("Non-Disclosure", 10); res.Total != 0 {
		t.Errorf("삭제된 문서가 검색됩니다")
	}
	if st, _ := db.Stats(); st.Docs != 2 {
		t.Errorf("문서 수 = %d, want 2", st.Docs)
	}
	// 없는 경로 삭제는 에러가 아니어야 한다(증분 스캔에서 흔한 상황)
	if err := db.DeleteByPath("/docs/없는파일.txt"); err != nil {
		t.Errorf("존재하지 않는 경로 삭제가 에러를 냈습니다: %v", err)
	}
}

func TestExistingTracksSizeAndMTime(t *testing.T) {
	db := openTemp(t)
	seed(t, db)

	ex, err := db.Existing()
	if err != nil {
		t.Fatal(err)
	}
	if len(ex) != 3 {
		t.Fatalf("Existing() = %d건, want 3", len(ex))
	}
	got, ok := ex["/docs/계약서/NDA.docx"]
	if !ok {
		t.Fatal("NDA 경로가 없습니다")
	}
	if got.MTime != 1 || got.Size == 0 {
		t.Errorf("메타데이터가 잘못 저장됐습니다: %+v", got)
	}
}

func TestRelPathAndDir(t *testing.T) {
	db := openTemp(t)
	put(t, db, root, "/docs/계약서/하위/문서.docx", "문서", "내용")

	docs, err := db.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 {
		t.Fatalf("List() = %d건", len(docs))
	}
	if docs[0].RelPath != "계약서/하위/문서.docx" {
		t.Errorf("RelPath = %q", docs[0].RelPath)
	}
	if docs[0].Dir != "계약서/하위" {
		t.Errorf("Dir = %q", docs[0].Dir)
	}
}

func TestSearchLimitAndComplete(t *testing.T) {
	db := openTemp(t)
	for i := 0; i < 12; i++ {
		put(t, db, root, filepath.Join(root, "문서", string(rune('a'+i))+".txt"),
			"문서", "공통어구 손해배상 조항")
	}
	res, err := db.Search("손해배상", 5)
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 5 {
		t.Errorf("limit=5 인데 %d건 반환", res.Total)
	}
	if res.Complete {
		t.Error("결과가 잘렸으므로 Complete 는 false 여야 합니다")
	}
}

func TestStats(t *testing.T) {
	db := openTemp(t)
	seed(t, db)
	if err := db.Put(root, &extract.Doc{
		Path: "/docs/스캔.pdf", Ext: ".pdf", Kind: extract.KindScanned,
		Title: "스캔본", Text: "등기사항전부증명서", Pages: 3, OCRPages: 3,
	}); err != nil {
		t.Fatal(err)
	}

	st, err := db.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if st.Docs != 4 {
		t.Errorf("Docs = %d, want 4", st.Docs)
	}
	if st.Scanned != 1 {
		t.Errorf("Scanned = %d, want 1", st.Scanned)
	}
	if st.OCRPages != 3 {
		t.Errorf("OCRPages = %d, want 3", st.OCRPages)
	}
	if st.ByExt[".docx"] != 2 {
		t.Errorf("ByExt[.docx] = %d, want 2", st.ByExt[".docx"])
	}
}

func TestEmptyQuery(t *testing.T) {
	db := openTemp(t)
	seed(t, db)
	res, err := db.Search("   ", 10)
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 0 || res.Hits == nil {
		t.Errorf("빈 질의는 빈 슬라이스를 돌려줘야 합니다(널 아님): %+v", res)
	}
}

func TestRootPersistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "index.db")

	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetRoot(`D:\법무팀\문서`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	db2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()

	got, err := db2.Root()
	if err != nil {
		t.Fatal(err)
	}
	if got != `D:\법무팀\문서` {
		t.Errorf("Root() = %q — 재시작 후 루트가 유지되지 않습니다", got)
	}
}
