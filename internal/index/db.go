// Package index 는 추출된 문서를 SQLite 에 저장하고 전문검색을 제공한다.
//
// 순수 Go 드라이버(modernc.org/sqlite)를 쓰므로 cgo 없이 윈도우 크로스컴파일이 된다.
// DB 는 파일 하나라서 설치·서버·계정이 필요 없고, 백업은 복사, 초기화는 삭제다.
package index

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/WalkerMillie/lawdesk/internal/extract"
	_ "modernc.org/sqlite"
)

// DB 는 인덱스 데이터베이스 핸들.
type DB struct {
	sql  *sql.DB
	path string
}

// Open 은 인덱스 DB 를 열고, 없으면 만든다.
// 스키마 버전이 다르면 인덱스를 폐기하고 새로 만든다(원본 문서는 무관).
func Open(path string) (*DB, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("DB 폴더 생성 실패: %w", err)
		}
	}

	db, err := openAt(path)
	if err != nil {
		return nil, err
	}

	ver, err := db.metaInt("schema_version")
	if err != nil {
		db.Close()
		return nil, err
	}
	if ver != 0 && ver != schemaVersion {
		// 스키마가 바뀌었다 — 인덱스는 언제든 재생성 가능한 파생 데이터이므로 버린다.
		db.Close()
		if err := removeDBFiles(path); err != nil {
			return nil, fmt.Errorf("구버전 인덱스 삭제 실패: %w", err)
		}
		if db, err = openAt(path); err != nil {
			return nil, err
		}
	}
	if err := db.setMeta("schema_version", fmt.Sprint(schemaVersion)); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func openAt(path string) (*DB, error) {
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)"
	sdb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("DB 열기 실패: %w", err)
	}
	// 순수 Go 드라이버는 다중 쓰기 연결에서 SQLITE_BUSY 를 내기 쉽다.
	// 쓰기는 인덱서 한 곳에서만 하므로 연결을 1개로 묶는 편이 안전하고 빠르다.
	sdb.SetMaxOpenConns(1)

	if err := sdb.Ping(); err != nil {
		sdb.Close()
		return nil, fmt.Errorf("DB 연결 실패: %w", err)
	}
	if _, err := sdb.Exec(schemaSQL); err != nil {
		sdb.Close()
		return nil, fmt.Errorf("스키마 생성 실패: %w", err)
	}
	return &DB{sql: sdb, path: path}, nil
}

// removeDBFiles 는 WAL/SHM 까지 함께 지운다.
func removeDBFiles(path string) error {
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(path + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (d *DB) Close() error { return d.sql.Close() }
func (d *DB) Path() string { return d.path }

// ---------------------------------------------------------------- meta

func (d *DB) setMeta(key, value string) error {
	_, err := d.sql.Exec(
		`INSERT INTO meta(key,value) VALUES(?,?)
		 ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

func (d *DB) getMeta(key string) (string, error) {
	var v string
	err := d.sql.QueryRow(`SELECT value FROM meta WHERE key=?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (d *DB) metaInt(key string) (int, error) {
	v, err := d.getMeta(key)
	if err != nil || v == "" {
		return 0, err
	}
	var n int
	_, _ = fmt.Sscanf(v, "%d", &n)
	return n, nil
}

// SetRoot 기록해 두면 다음 실행에서 같은 폴더를 자동으로 연다.
func (d *DB) SetRoot(root string) error { return d.setMeta("root", root) }
func (d *DB) Root() (string, error)     { return d.getMeta("root") }

// ---------------------------------------------------------------- 쓰기

// Stored 는 이미 색인된 파일의 상태(변경 감지용).
type Stored struct {
	ID    int64
	Size  int64
	MTime int64
}

// Existing 은 색인된 모든 파일의 경로 → 상태 맵을 돌려준다.
// 증분 인덱싱에서 "변경된 것만 다시 읽기" 와 "삭제된 것 지우기" 판단에 쓴다.
func (d *DB) Existing() (map[string]Stored, error) {
	rows, err := d.sql.Query(`SELECT id, path, size, mtime FROM docs`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]Stored)
	for rows.Next() {
		var s Stored
		var p string
		if err := rows.Scan(&s.ID, &p, &s.Size, &s.MTime); err != nil {
			return nil, err
		}
		out[p] = s
	}
	return out, rows.Err()
}

// Put 은 문서를 저장한다(있으면 교체).
func (d *DB) Put(root string, doc *extract.Doc) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // 성공 시 Commit 이 먼저 실행된다

	// 기존 행 제거 — FTS 는 외부 콘텐츠가 아니므로 rowid 로 직접 지운다.
	var oldID sql.NullInt64
	err = tx.QueryRow(`SELECT id FROM docs WHERE path=?`, doc.Path).Scan(&oldID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if oldID.Valid {
		if _, err := tx.Exec(`DELETE FROM docs_fts WHERE rowid=?`, oldID.Int64); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM docs WHERE id=?`, oldID.Int64); err != nil {
			return err
		}
	}

	rel, err := filepath.Rel(root, doc.Path)
	if err != nil || strings.HasPrefix(rel, "..") {
		rel = doc.Path // 루트 밖이면 절대경로 그대로
	}
	rel = filepath.ToSlash(rel)
	dir := filepath.ToSlash(filepath.Dir(rel))
	if dir == "." {
		dir = ""
	}

	outline, _ := json.Marshal(nonNilOutline(doc.Outline))
	warns, _ := json.Marshal(nonNilStrings(doc.Warnings))

	res, err := tx.Exec(`
		INSERT INTO docs(path, rel_path, dir, name, ext, size, mtime, kind,
		                 title, summary, outline, warnings, pages, ocr_pages,
		                 text_len, indexed_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		doc.Path, rel, dir, filepath.Base(doc.Path), doc.Ext, doc.Size, doc.ModTime,
		string(doc.Kind), doc.Title, doc.Summary(300), string(outline), string(warns),
		doc.Pages, doc.OCRPages, len([]rune(doc.Text)), time.Now().Unix())
	if err != nil {
		return fmt.Errorf("docs 삽입 실패: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}

	// 파일명도 본문에 함께 색인한다. "임대차" 로 검색했을 때 본문에 그 단어가
	// 없더라도 파일명이 맞으면 찾아져야 하기 때문이다.
	body := rel + "\n" + doc.Title + "\n" + doc.Text
	if _, err := tx.Exec(
		`INSERT INTO docs_fts(rowid, title, body) VALUES(?,?,?)`,
		id, doc.Title, body); err != nil {
		return fmt.Errorf("검색 인덱스 삽입 실패: %w", err)
	}
	return tx.Commit()
}

// DeleteByPath 는 사라진 파일을 인덱스에서 제거한다.
func (d *DB) DeleteByPath(path string) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	var id sql.NullInt64
	err = tx.QueryRow(`SELECT id FROM docs WHERE path=?`, path).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM docs_fts WHERE rowid=?`, id.Int64); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM docs WHERE id=?`, id.Int64); err != nil {
		return err
	}
	return tx.Commit()
}

// Clear 는 인덱스를 비운다(루트 폴더를 바꿀 때).
func (d *DB) Clear() error {
	_, err := d.sql.Exec(`DELETE FROM docs_fts; DELETE FROM docs;`)
	return err
}

// Optimize 는 인덱싱 완료 후 FTS 인덱스를 정리해 검색을 빠르게 만든다.
func (d *DB) Optimize() error {
	_, err := d.sql.Exec(`INSERT INTO docs_fts(docs_fts) VALUES('optimize')`)
	return err
}

func nonNilOutline(v []extract.Heading) []extract.Heading {
	if v == nil {
		return []extract.Heading{}
	}
	return v
}

func nonNilStrings(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}
