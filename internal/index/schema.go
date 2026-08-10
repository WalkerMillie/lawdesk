package index

// schemaVersion 은 스키마가 바뀔 때마다 올린다.
// 값이 기존 DB와 다르면 인덱스를 통째로 다시 만든다(원본 문서는 건드리지 않으므로 안전하다).
const schemaVersion = 1

// schemaSQL 은 최초 생성 DDL.
//
// 설계 메모
//   - docs      : 파일 하나당 한 행. 메타데이터만 담는다.
//   - docs_fts  : 전문검색 인덱스. rowid 를 docs.id 와 일치시켜 조인한다.
//     본문(body)은 여기에만 저장해 중복 보관을 피한다.
//   - tokenize=trigram : 한국어는 공백 기반 토크나이저가 잘 듣지 않는다.
//     trigram 은 어절 중간 부분일치("배상책임")와 사건번호("2021다12345")
//     검색을 모두 커버한다. 대신 3글자 미만 질의는 색인되지 않으므로
//     search.go 에서 LIKE 로 우회한다.
const schemaSQL = `
CREATE TABLE IF NOT EXISTS meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS docs (
    id          INTEGER PRIMARY KEY,
    path        TEXT    NOT NULL UNIQUE,
    rel_path    TEXT    NOT NULL,
    dir         TEXT    NOT NULL,
    name        TEXT    NOT NULL,
    ext         TEXT    NOT NULL,
    size        INTEGER NOT NULL,
    mtime       INTEGER NOT NULL,
    kind        TEXT    NOT NULL,
    title       TEXT    NOT NULL,
    summary     TEXT    NOT NULL DEFAULT '',
    outline     TEXT    NOT NULL DEFAULT '[]',
    warnings    TEXT    NOT NULL DEFAULT '[]',
    pages       INTEGER NOT NULL DEFAULT 0,
    ocr_pages   INTEGER NOT NULL DEFAULT 0,
    text_len    INTEGER NOT NULL DEFAULT 0,
    indexed_at  INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_docs_dir  ON docs(dir);
CREATE INDEX IF NOT EXISTS idx_docs_ext  ON docs(ext);
CREATE INDEX IF NOT EXISTS idx_docs_kind ON docs(kind);

CREATE VIRTUAL TABLE IF NOT EXISTS docs_fts USING fts5(
    title,
    body,
    tokenize = 'trigram'
);
`
