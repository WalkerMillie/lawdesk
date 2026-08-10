// Package server 는 로컬 HTTP 서버와 임베드된 웹 UI 를 제공한다.
//
// 보안 전제: 이 서버는 127.0.0.1 에만 바인딩된다. 문서 원문을 다루므로
// 외부 인터페이스에 절대 노출하지 않는다(변호사 비밀유지의무).
package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/WalkerMillie/lawdesk/internal/extract"
	"github.com/WalkerMillie/lawdesk/internal/index"
	"github.com/WalkerMillie/lawdesk/internal/openfile"
	"github.com/WalkerMillie/lawdesk/internal/picker"
	"github.com/WalkerMillie/lawdesk/internal/scan"
	"github.com/WalkerMillie/lawdesk/web"
)

// Server 는 API 핸들러 묶음.
type Server struct {
	db      *index.DB
	ix      *scan.Indexer
	tools   *extract.Tools
	ocrOn   bool
	version string
}

func New(db *index.DB, ix *scan.Indexer, tools *extract.Tools, ocrOn bool, version string) *Server {
	return &Server{db: db, ix: ix, tools: tools, ocrOn: ocrOn, version: version}
}

// Handler 는 라우팅된 http.Handler 를 돌려준다.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("GET /api/search", s.handleSearch)
	mux.HandleFunc("GET /api/tree", s.handleTree)
	mux.HandleFunc("GET /api/doc/{id}", s.handleDoc)
	mux.HandleFunc("GET /api/index/progress", s.handleProgress)
	mux.HandleFunc("POST /api/index/start", s.handleIndexStart)
	mux.HandleFunc("POST /api/index/cancel", s.handleIndexCancel)
	mux.HandleFunc("POST /api/pick-folder", s.handlePickFolder)
	mux.HandleFunc("POST /api/open", s.handleOpen)

	ui, err := fs.Sub(web.Assets, "dist")
	if err != nil {
		panic("웹 자산 마운트 실패: " + err.Error())
	}
	mux.Handle("/", http.FileServer(http.FS(ui)))

	return localOnly(noStore(mux))
}

// localOnly 는 루프백 이외의 접속을 거부한다.
// 바인딩도 127.0.0.1 로 하지만, 실수로 설정이 바뀌어도 문서가 새어나가지 않도록
// 요청 단계에서 한 번 더 막는 이중 안전장치다.
func localOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := splitHostPort(r.RemoteAddr)
		if err != nil || !isLoopback(host) {
			http.Error(w, "로컬에서만 접근할 수 있습니다", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 문서 내용이 디스크 캐시에 남지 않게 한다.
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// ---------------------------------------------------------------- 핸들러

type statusResp struct {
	Version      string        `json:"version"`
	Root         string        `json:"root"`
	OCREnabled   bool          `json:"ocr_enabled"`
	PickerNative bool          `json:"picker_native"`
	MissingTools []string      `json:"missing_tools"`
	Extensions   []string      `json:"extensions"`
	Stats        *index.Stats  `json:"stats"`
	Progress     scan.Progress `json:"progress"`
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	root, _ := s.db.Root()
	stats, err := s.db.Stats()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, statusResp{
		Version:      s.version,
		Root:         root,
		OCREnabled:   s.ocrOn,
		PickerNative: picker.Available(),
		MissingTools: s.tools.Missing(),
		Stats:        stats,
		Progress:     s.ix.Progress(),
	})
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	res, err := s.db.Search(q, limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, res)
}

func (s *Server) handleTree(w http.ResponseWriter, r *http.Request) {
	docs, err := s.db.List()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, map[string]any{"docs": docs, "total": len(docs)})
}

func (s *Server) handleDoc(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("잘못된 id"))
		return
	}
	doc, err := s.db.Get(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, errors.New("문서를 찾을 수 없습니다"))
		return
	}
	writeJSON(w, doc)
}

func (s *Server) handleProgress(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.ix.Progress())
}

func (s *Server) handleIndexStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Root string `json:"root"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("요청 형식 오류"))
		return
	}
	root := strings.TrimSpace(req.Root)
	if root == "" {
		writeErr(w, http.StatusBadRequest, errors.New("폴더 경로가 비어 있습니다"))
		return
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.ix.Start(abs); err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, scan.ErrBusy) {
			code = http.StatusConflict
		}
		writeErr(w, code, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "root": abs})
}

func (s *Server) handleIndexCancel(w http.ResponseWriter, r *http.Request) {
	s.ix.Cancel()
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handlePickFolder(w http.ResponseWriter, r *http.Request) {
	path, err := picker.PickFolder("색인할 문서 폴더를 선택하세요")
	switch {
	case errors.Is(err, picker.ErrCancelled):
		writeJSON(w, map[string]any{"cancelled": true})
	case errors.Is(err, picker.ErrUnsupported):
		writeErr(w, http.StatusNotImplemented, err)
	case err != nil:
		writeErr(w, http.StatusInternalServerError, err)
	default:
		writeJSON(w, map[string]any{"path": path})
	}
}

func (s *Server) handleOpen(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID     int64 `json:"id"`
		Reveal bool  `json:"reveal"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("요청 형식 오류"))
		return
	}
	doc, err := s.db.Get(req.ID)
	if err != nil {
		writeErr(w, http.StatusNotFound, errors.New("문서를 찾을 수 없습니다"))
		return
	}
	// 인덱스에 남아 있어도 파일이 이동·삭제됐을 수 있다.
	if _, err := os.Stat(doc.Path); err != nil {
		writeErr(w, http.StatusNotFound,
			fmt.Errorf("파일이 원래 위치에 없습니다: %s", doc.Path))
		return
	}
	if req.Reveal {
		err = openfile.Reveal(doc.Path)
	} else {
		err = openfile.Open(doc.Path)
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// ---------------------------------------------------------------- 유틸

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		// 헤더를 이미 보냈으므로 상태코드를 바꿀 수 없다. 로그만 남긴다.
		fmt.Fprintln(os.Stderr, "응답 인코딩 실패:", err)
	}
}

func writeErr(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func splitHostPort(addr string) (string, string, error) {
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		return addr, "", nil
	}
	host := strings.Trim(addr[:i], "[]")
	return host, addr[i+1:], nil
}

func isLoopback(host string) bool {
	return host == "127.0.0.1" || host == "::1" || host == "localhost"
}
