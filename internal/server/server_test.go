package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/WalkerMillie/lawdesk/internal/extract"
	"github.com/WalkerMillie/lawdesk/internal/index"
	"github.com/WalkerMillie/lawdesk/internal/scan"
)

func newTestServer(t *testing.T) (http.Handler, *index.DB) {
	t.Helper()
	db, err := index.Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	reg := extract.NewRegistry(extract.PlainExtractor{})
	ix := scan.NewIndexer(db, reg, extract.Options{})
	tools := &extract.Tools{}
	return New(db, ix, tools, false, "test").Handler(), db
}

// do 는 루프백에서 온 것처럼 요청을 만든다(localOnly 통과용).
func do(t *testing.T, h http.Handler, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, target, nil)
	} else {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	r.RemoteAddr = "127.0.0.1:54321"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestLocalOnlyBlocksRemote(t *testing.T) {
	h, _ := newTestServer(t)

	r := httptest.NewRequest("GET", "/api/status", nil)
	r.RemoteAddr = "192.168.1.50:44444" // 사내망 다른 PC
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusForbidden {
		t.Errorf("외부 IP 접근이 %d 로 허용됐습니다 — 403 이어야 합니다", w.Code)
	}
	// 문서 목록이 새어나가면 안 된다
	if strings.Contains(w.Body.String(), "docs") {
		t.Error("차단 응답에 데이터가 섞여 있습니다")
	}
}

func TestNoStoreHeader(t *testing.T) {
	h, _ := newTestServer(t)
	w := do(t, h, "GET", "/api/status", "")
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store (문서 내용이 디스크 캐시에 남으면 안 됩니다)", got)
	}
}

func TestStatusShape(t *testing.T) {
	h, _ := newTestServer(t)
	w := do(t, h, "GET", "/api/status", "")
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	var st statusResp
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatalf("응답 파싱 실패: %v — %s", err, w.Body.String())
	}
	if st.Version != "test" {
		t.Errorf("Version = %q", st.Version)
	}
	if st.Stats == nil {
		t.Error("Stats 가 없습니다")
	}
	// 도구가 없는 구성이므로 안내가 나와야 한다
	if len(st.MissingTools) == 0 {
		t.Error("MissingTools 가 비어 있습니다 — 사용자에게 기능 제한을 알려야 합니다")
	}
}

func TestSearchEndpoint(t *testing.T) {
	h, db := newTestServer(t)
	if err := db.Put("/docs", &extract.Doc{
		Path: "/docs/계약서.txt", Ext: ".txt", Kind: extract.KindNative,
		Title: "용역계약서", Text: "제7조 손해배상 책임을 진다",
	}); err != nil {
		t.Fatal(err)
	}

	w := do(t, h, "GET", "/api/search?q=%EC%86%90%ED%95%B4%EB%B0%B0%EC%83%81", "")
	if w.Code != 200 {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var res index.SearchResult
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.Total != 1 || res.Hits[0].Title != "용역계약서" {
		t.Errorf("검색 결과가 예상과 다릅니다: %+v", res)
	}
}

func TestSearchEmptyQueryReturnsEmptyArray(t *testing.T) {
	h, _ := newTestServer(t)
	w := do(t, h, "GET", "/api/search?q=", "")

	// hits 가 null 이면 프런트엔드의 .filter() 가 터진다. 반드시 [] 여야 한다.
	if !strings.Contains(w.Body.String(), `"hits":[]`) {
		t.Errorf("빈 질의 응답에 hits:[] 가 없습니다: %s", w.Body.String())
	}
}

func TestDocNotFound(t *testing.T) {
	h, _ := newTestServer(t)
	if w := do(t, h, "GET", "/api/doc/9999", ""); w.Code != http.StatusNotFound {
		t.Errorf("없는 문서 조회 = %d, want 404", w.Code)
	}
	if w := do(t, h, "GET", "/api/doc/abc", ""); w.Code != http.StatusBadRequest {
		t.Errorf("잘못된 id = %d, want 400", w.Code)
	}
}

func TestIndexStartValidation(t *testing.T) {
	h, _ := newTestServer(t)

	if w := do(t, h, "POST", "/api/index/start", `{"root":""}`); w.Code != http.StatusBadRequest {
		t.Errorf("빈 경로 = %d, want 400", w.Code)
	}
	if w := do(t, h, "POST", "/api/index/start", `not json`); w.Code != http.StatusBadRequest {
		t.Errorf("잘못된 JSON = %d, want 400", w.Code)
	}
	if w := do(t, h, "POST", "/api/index/start", `{"root":"/없는/폴더/xyz"}`); w.Code != http.StatusBadRequest {
		t.Errorf("없는 폴더 = %d, want 400", w.Code)
	}
}

func TestUIIsServed(t *testing.T) {
	h, _ := newTestServer(t)
	for _, path := range []string{"/", "/app.js", "/app.css"} {
		w := do(t, h, "GET", path, "")
		if w.Code != 200 {
			t.Errorf("%s = %d, want 200 (UI 자산이 임베드되지 않았습니다)", path, w.Code)
		}
		if w.Body.Len() == 0 {
			t.Errorf("%s 응답이 비어 있습니다", path)
		}
	}
}

func TestIsLoopback(t *testing.T) {
	for _, h := range []string{"127.0.0.1", "::1", "localhost"} {
		if !isLoopback(h) {
			t.Errorf("isLoopback(%q) = false", h)
		}
	}
	for _, h := range []string{"192.168.1.1", "10.0.0.5", "8.8.8.8", ""} {
		if isLoopback(h) {
			t.Errorf("isLoopback(%q) = true — 외부 주소입니다", h)
		}
	}
}
