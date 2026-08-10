package scan

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/WalkerMillie/lawdesk/internal/extract"
	"github.com/WalkerMillie/lawdesk/internal/index"
)

func TestSkipDir(t *testing.T) {
	for _, name := range []string{".git", ".svn", "node_modules", "$RECYCLE.BIN",
		"System Volume Information", "__pycache__", ".venv"} {
		if !skipDir(name) {
			t.Errorf("skipDir(%q) = false, 건너뛰어야 합니다", name)
		}
	}
	for _, name := range []string{"계약서", "판례", "2023년", "docs"} {
		if skipDir(name) {
			t.Errorf("skipDir(%q) = true, 색인해야 합니다", name)
		}
	}
}

func TestSkipFile(t *testing.T) {
	// "~$" 는 워드가 문서를 열어둔 동안 만드는 잠금 파일이다.
	// 실제 사무실 폴더에 흔하고, 열어봐야 본문이 없다.
	for _, name := range []string{"~$계약서.docx", ".DS_Store", "Thumbs.db",
		"desktop.ini", "임시.tmp"} {
		if !skipFile(name) {
			t.Errorf("skipFile(%q) = false, 건너뛰어야 합니다", name)
		}
	}
	for _, name := range []string{"계약서.docx", "판결문.pdf", "메모.txt"} {
		if skipFile(name) {
			t.Errorf("skipFile(%q) = true, 색인해야 합니다", name)
		}
	}
}

// newIndexer 는 텍스트 파일만 다루는 가벼운 인덱서를 만든다(외부 도구 불필요).
func newIndexer(t *testing.T) (*Indexer, *index.DB) {
	t.Helper()
	db, err := index.Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	reg := extract.NewRegistry(extract.PlainExtractor{})
	return NewIndexer(db, reg, extract.Options{}), db
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// waitDone 은 인덱싱이 끝날 때까지 기다린다.
func waitDone(t *testing.T, ix *Indexer) Progress {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		p := ix.Progress()
		if !p.Running {
			return p
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("인덱싱이 제한시간 안에 끝나지 않았습니다")
	return Progress{}
}

func TestIndexAndIncremental(t *testing.T) {
	ix, db := newIndexer(t)
	root := t.TempDir()

	write(t, filepath.Join(root, "계약서", "용역.txt"), "제7조 손해배상 조항이 있다")
	write(t, filepath.Join(root, "판례", "대법원.txt"), "2021다12345 채무불이행 사건")
	write(t, filepath.Join(root, "메모.txt"), "임대차 보증금 오억원")
	// 건너뛰어야 하는 것들
	write(t, filepath.Join(root, "~$열린문서.txt"), "잠금 파일")
	write(t, filepath.Join(root, ".git", "config"), "무시 대상")

	if err := ix.Start(root); err != nil {
		t.Fatal(err)
	}
	p := waitDone(t, ix)

	if p.Phase != "done" {
		t.Fatalf("phase = %q, error = %q", p.Phase, p.Error)
	}
	if p.Total != 3 {
		t.Errorf("Total = %d, want 3 (잠금파일/.git 은 제외돼야 함)", p.Total)
	}
	if p.Indexed != 3 || p.Failed != 0 {
		t.Errorf("indexed=%d failed=%d, want 3/0", p.Indexed, p.Failed)
	}

	if res, _ := db.Search("손해배상", 10); res.Total != 1 {
		t.Errorf("색인된 내용이 검색되지 않습니다")
	}
	if res, _ := db.Search("잠금 파일", 10); res.Total != 0 {
		t.Errorf("~$ 잠금 파일이 색인됐습니다")
	}

	// 2회차: 변경이 없으므로 전부 skip
	if err := ix.Start(root); err != nil {
		t.Fatal(err)
	}
	p2 := waitDone(t, ix)
	if p2.Skipped != 3 || p2.Indexed != 0 {
		t.Errorf("증분 2회차: skipped=%d indexed=%d, want 3/0", p2.Skipped, p2.Indexed)
	}
}

func TestIndexDetectsAddAndRemove(t *testing.T) {
	ix, db := newIndexer(t)
	root := t.TempDir()

	write(t, filepath.Join(root, "a.txt"), "가나다 원본 문서")
	write(t, filepath.Join(root, "b.txt"), "라마바 삭제될 문서")

	if err := ix.Start(root); err != nil {
		t.Fatal(err)
	}
	waitDone(t, ix)

	if err := os.Remove(filepath.Join(root, "b.txt")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "c.txt"), "사아자 새로 추가된 문서")

	if err := ix.Start(root); err != nil {
		t.Fatal(err)
	}
	p := waitDone(t, ix)

	if p.Indexed != 1 {
		t.Errorf("indexed = %d, want 1 (새 파일만)", p.Indexed)
	}
	if p.Removed != 1 {
		t.Errorf("removed = %d, want 1 (삭제된 파일)", p.Removed)
	}
	if res, _ := db.Search("삭제될 문서", 10); res.Total != 0 {
		t.Errorf("삭제된 파일이 아직 검색됩니다")
	}
	if res, _ := db.Search("새로 추가된", 10); res.Total != 1 {
		t.Errorf("새 파일이 검색되지 않습니다")
	}
}

func TestIndexDetectsModification(t *testing.T) {
	ix, db := newIndexer(t)
	root := t.TempDir()
	path := filepath.Join(root, "메모.txt")

	write(t, path, "처음에는 임대차 내용")
	if err := ix.Start(root); err != nil {
		t.Fatal(err)
	}
	waitDone(t, ix)

	// 크기와 수정시각이 모두 같으면 건너뛰므로, 내용 길이를 바꿔 확실히 감지시킨다.
	write(t, path, "나중에는 손해배상 내용으로 훨씬 길게 바뀌었다")
	if err := os.Chtimes(path, time.Now(), time.Now().Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}

	if err := ix.Start(root); err != nil {
		t.Fatal(err)
	}
	p := waitDone(t, ix)
	if p.Indexed != 1 {
		t.Errorf("indexed = %d, want 1 (수정 감지 실패)", p.Indexed)
	}
	if res, _ := db.Search("임대차", 10); res.Total != 0 {
		t.Errorf("옛 내용이 남아 있습니다")
	}
	if res, _ := db.Search("손해배상", 10); res.Total != 1 {
		t.Errorf("바뀐 내용이 색인되지 않았습니다")
	}
}

func TestStartRejectsBadRoot(t *testing.T) {
	ix, _ := newIndexer(t)

	if err := ix.Start(filepath.Join(t.TempDir(), "없는폴더")); err == nil {
		t.Error("존재하지 않는 폴더인데 에러가 나지 않았습니다")
	}

	file := filepath.Join(t.TempDir(), "파일.txt")
	write(t, file, "내용")
	if err := ix.Start(file); err == nil {
		t.Error("폴더가 아닌 경로인데 에러가 나지 않았습니다")
	}
}

func TestConcurrentStartIsRejected(t *testing.T) {
	ix, _ := newIndexer(t)
	root := t.TempDir()
	for i := 0; i < 60; i++ {
		write(t, filepath.Join(root, string(rune('a'+i%26))+string(rune('a'+i/26))+".txt"),
			"충분한 분량의 본문 내용을 넣어 인덱싱이 즉시 끝나지 않게 한다")
	}

	if err := ix.Start(root); err != nil {
		t.Fatal(err)
	}
	// 첫 작업이 진행 중일 때 두 번째 요청은 ErrBusy 여야 한다.
	// (이미 끝났다면 판정할 수 없으므로 그 경우는 건너뛴다)
	if ix.Progress().Running {
		if err := ix.Start(root); err == nil {
			t.Error("동시 인덱싱이 허용됐습니다 — ErrBusy 여야 합니다")
		}
	}
	waitDone(t, ix)
}
