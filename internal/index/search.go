package index

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/WalkerMillie/lawdesk/internal/extract"
)

// Hit 은 검색 결과 한 건.
type Hit struct {
	ID      int64             `json:"id"`
	RelPath string            `json:"rel_path"`
	Dir     string            `json:"dir"`
	Name    string            `json:"name"`
	Ext     string            `json:"ext"`
	Kind    string            `json:"kind"`
	Title   string            `json:"title"`
	Summary string            `json:"summary"`
	Snippet string            `json:"snippet"`
	Outline []extract.Heading `json:"outline,omitempty"`
	Size    int64             `json:"size"`
	Pages   int               `json:"pages"`
}

// SearchResult 는 검색 응답 전체.
type SearchResult struct {
	Query    string `json:"query"`
	Hits     []Hit  `json:"hits"`
	Total    int    `json:"total"`
	Mode     string `json:"mode"` // "match" | "like"
	Note     string `json:"note"` // 사용자에게 보여줄 안내(있을 때만)
	Millis   int    `json:"millis"`
	Complete bool   `json:"complete"` // false 면 limit 때문에 잘렸다는 뜻
}

// minTrigramRunes 는 trigram 색인이 동작하는 최소 글자 수.
// 이보다 짧은 질의는 인덱스를 쓸 수 없어 LIKE 전체 스캔으로 우회한다.
const minTrigramRunes = 3

// Search 는 전문검색을 수행한다.
func (d *DB) Search(query string, limit int) (*SearchResult, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return &SearchResult{Query: query, Hits: []Hit{}, Mode: "match"}, nil
	}
	if limit <= 0 {
		limit = 100
	}

	if utf8.RuneCountInString(q) < minTrigramRunes {
		return d.searchLike(q, limit)
	}
	res, err := d.searchFTS(q, limit)
	if err != nil {
		return nil, err
	}
	// trigram 은 색인되지만 결과가 없을 수 있다. 그럴 땐 사용자를 빈손으로
	// 보내기보다 LIKE 로 한 번 더 확인한다(느리지만 정확하다).
	if len(res.Hits) == 0 {
		if alt, err2 := d.searchLike(q, limit); err2 == nil && len(alt.Hits) > 0 {
			alt.Note = "부분일치 검색으로 찾았습니다"
			return alt, nil
		}
	}
	return res, nil
}

const hitColumns = `d.id, d.rel_path, d.dir, d.name, d.ext, d.kind,
                    d.title, d.summary, d.outline, d.size, d.pages`

func (d *DB) searchFTS(q string, limit int) (*SearchResult, error) {
	rows, err := d.sql.Query(fmt.Sprintf(`
		SELECT %s, snippet(docs_fts, 1, '<mark>', '</mark>', '…', 14)
		FROM docs_fts
		JOIN docs d ON d.id = docs_fts.rowid
		WHERE docs_fts MATCH ?
		ORDER BY rank
		LIMIT ?`, hitColumns), quoteFTS(q), limit+1)
	if err != nil {
		return nil, fmt.Errorf("검색 실패: %w", err)
	}
	defer rows.Close()

	res := &SearchResult{Query: q, Mode: "match", Hits: []Hit{}}
	if err := scanHits(rows, res, limit, true); err != nil {
		return nil, err
	}
	return res, nil
}

func (d *DB) searchLike(q string, limit int) (*SearchResult, error) {
	pattern := "%" + escapeLike(q) + "%"
	rows, err := d.sql.Query(fmt.Sprintf(`
		SELECT %s, ''
		FROM docs d
		JOIN docs_fts f ON f.rowid = d.id
		WHERE f.body LIKE ? ESCAPE '\'
		ORDER BY d.rel_path
		LIMIT ?`, hitColumns), pattern, limit+1)
	if err != nil {
		return nil, fmt.Errorf("검색 실패: %w", err)
	}
	defer rows.Close()

	res := &SearchResult{Query: q, Mode: "like", Hits: []Hit{}}
	if utf8.RuneCountInString(q) < minTrigramRunes {
		res.Note = "3글자 미만은 인덱스를 쓸 수 없어 느릴 수 있습니다"
	}
	if err := scanHits(rows, res, limit, false); err != nil {
		return nil, err
	}
	return res, nil
}

func scanHits(rows *sql.Rows, res *SearchResult, limit int, hasSnippet bool) error {
	res.Complete = true
	for rows.Next() {
		if len(res.Hits) == limit {
			// limit+1 건을 요청했으므로 여기 도달하면 더 있다는 뜻
			res.Complete = false
			break
		}
		var h Hit
		var outline, snippet string
		if err := rows.Scan(&h.ID, &h.RelPath, &h.Dir, &h.Name, &h.Ext, &h.Kind,
			&h.Title, &h.Summary, &outline, &h.Size, &h.Pages, &snippet); err != nil {
			return err
		}
		_ = json.Unmarshal([]byte(outline), &h.Outline)
		if hasSnippet {
			h.Snippet = snippet
		} else {
			h.Snippet = h.Summary
		}
		res.Hits = append(res.Hits, h)
	}
	res.Total = len(res.Hits)
	return rows.Err()
}

// quoteFTS 는 사용자 입력을 FTS5 MATCH 에 안전하게 넘긴다.
//
// FTS5 는 -, ", *, (, ), : 등을 질의 연산자로 해석한다. 사용자가 친
// "Non-Disclosure" 나 "(주)가나" 가 문법 오류를 내지 않도록 전체를
// 큰따옴표로 감싸고 내부 큰따옴표는 두 번 반복해 이스케이프한다.
// 결과적으로 질의는 항상 "구문 검색"이 되는데, 법률 문서 검색에서는
// 오히려 이쪽이 기대에 맞는다(조문·사건번호를 원문 그대로 찾는다).
func quoteFTS(q string) string {
	return `"` + strings.ReplaceAll(q, `"`, `""`) + `"`
}

// escapeLike 는 LIKE 패턴의 와일드카드를 무력화한다.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// ---------------------------------------------------------------- 조회

// Doc 은 상세 보기용 단건 조회 결과.
type Doc struct {
	Hit
	Path     string   `json:"path"`
	Warnings []string `json:"warnings"`
	OCRPages int      `json:"ocr_pages"`
	TextLen  int      `json:"text_len"`
}

// Get 은 문서 1건을 가져온다.
func (d *DB) Get(id int64) (*Doc, error) {
	var doc Doc
	var outline, warnings string
	err := d.sql.QueryRow(`
		SELECT id, rel_path, dir, name, ext, kind, title, summary, outline,
		       size, pages, path, warnings, ocr_pages, text_len
		FROM docs WHERE id=?`, id).
		Scan(&doc.ID, &doc.RelPath, &doc.Dir, &doc.Name, &doc.Ext, &doc.Kind,
			&doc.Title, &doc.Summary, &outline, &doc.Size, &doc.Pages,
			&doc.Path, &warnings, &doc.OCRPages, &doc.TextLen)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(outline), &doc.Outline)
	_ = json.Unmarshal([]byte(warnings), &doc.Warnings)
	doc.Snippet = doc.Summary
	return &doc, nil
}

// List 는 트리 표시용으로 모든 문서의 메타데이터를 경로순으로 돌려준다.
func (d *DB) List() ([]Hit, error) {
	rows, err := d.sql.Query(fmt.Sprintf(
		`SELECT %s, '' FROM docs d ORDER BY d.rel_path`, hitColumns))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	res := &SearchResult{Hits: []Hit{}}
	if err := scanHits(rows, res, 1<<30, false); err != nil {
		return nil, err
	}
	return res.Hits, nil
}

// Stats 는 인덱스 요약 통계.
type Stats struct {
	Docs     int            `json:"docs"`
	Scanned  int            `json:"scanned"`
	OCRPages int            `json:"ocr_pages"`
	ByExt    map[string]int `json:"by_ext"`
	DBBytes  int64          `json:"db_bytes"`
}

func (d *DB) Stats() (*Stats, error) {
	st := &Stats{ByExt: map[string]int{}}

	if err := d.sql.QueryRow(`
		SELECT COUNT(*),
		       COALESCE(SUM(CASE WHEN kind IN ('scanned','mixed') THEN 1 ELSE 0 END),0),
		       COALESCE(SUM(ocr_pages),0)
		FROM docs`).Scan(&st.Docs, &st.Scanned, &st.OCRPages); err != nil {
		return nil, err
	}

	rows, err := d.sql.Query(`SELECT ext, COUNT(*) FROM docs GROUP BY ext ORDER BY 2 DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var ext string
		var n int
		if err := rows.Scan(&ext, &n); err != nil {
			return nil, err
		}
		st.ByExt[ext] = n
	}
	return st, rows.Err()
}
