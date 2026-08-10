// Package scan 은 폴더를 훑어 문서를 추출하고 인덱스에 반영한다.
//
// 증분 동작: 크기와 수정시각이 모두 같으면 이미 색인된 것으로 보고 건너뛴다.
// 사라진 파일은 인덱스에서 지운다. 그래서 두 번째 실행부터는 거의 즉시 끝난다.
package scan

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/WalkerMillie/lawdesk/internal/extract"
	"github.com/WalkerMillie/lawdesk/internal/index"
)

// Progress 는 인덱싱 진행 상황. 화면 표시용으로 원자적으로 복사된다.
type Progress struct {
	Running   bool   `json:"running"`
	Root      string `json:"root"`
	Total     int    `json:"total"`     // 처리 대상 파일 수
	Done      int    `json:"done"`      // 처리 완료(건너뛴 것 포함)
	Skipped   int    `json:"skipped"`   // 변경 없어 재사용
	Indexed   int    `json:"indexed"`   // 새로 색인
	Removed   int    `json:"removed"`   // 사라져서 삭제
	Failed    int    `json:"failed"`    // 추출 실패
	OCRPages  int    `json:"ocr_pages"` // OCR 처리한 쪽수
	Current   string `json:"current"`   // 지금 처리 중인 파일(상대경로)
	Phase     string `json:"phase"`     // "walk" | "extract" | "optimize" | "done" | "error"
	Error     string `json:"error,omitempty"`
	StartedAt int64  `json:"started_at"`
	ElapsedMs int64  `json:"elapsed_ms"`
}

// Indexer 는 한 번에 하나의 인덱싱 작업만 수행한다.
type Indexer struct {
	db   *index.DB
	reg  *extract.Registry
	opt  extract.Options
	nWrk int

	mu       sync.Mutex
	progress Progress
	cancel   context.CancelFunc
}

func NewIndexer(db *index.DB, reg *extract.Registry, opt extract.Options) *Indexer {
	// OCR 은 CPU 를 많이 쓴다. 사용자 PC가 먹통이 되지 않도록 코어를 남긴다.
	n := runtime.NumCPU() - 1
	if n < 1 {
		n = 1
	}
	if n > 8 {
		n = 8
	}
	return &Indexer{db: db, reg: reg, opt: opt, nWrk: n}
}

// Progress 는 현재 상태의 스냅샷을 돌려준다.
func (ix *Indexer) Progress() Progress {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	p := ix.progress
	if p.Running {
		p.ElapsedMs = time.Since(time.Unix(0, p.StartedAt*int64(time.Millisecond))).Milliseconds()
	}
	return p
}

func (ix *Indexer) update(f func(*Progress)) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	f(&ix.progress)
}

// Cancel 은 진행 중인 인덱싱을 중단한다.
func (ix *Indexer) Cancel() {
	ix.mu.Lock()
	cancel := ix.cancel
	ix.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

var ErrBusy = errors.New("이미 인덱싱이 진행 중입니다")

// Start 는 백그라운드에서 인덱싱을 시작한다. 즉시 반환한다.
func (ix *Indexer) Start(root string) error {
	fi, err := os.Stat(root)
	if err != nil {
		return fmt.Errorf("폴더를 열 수 없습니다: %w", err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("폴더가 아닙니다: %s", root)
	}

	ix.mu.Lock()
	if ix.progress.Running {
		ix.mu.Unlock()
		return ErrBusy
	}
	ctx, cancel := context.WithCancel(context.Background())
	ix.cancel = cancel
	ix.progress = Progress{
		Running:   true,
		Root:      root,
		Phase:     "walk",
		StartedAt: time.Now().UnixMilli(),
	}
	ix.mu.Unlock()

	go func() {
		defer cancel()
		err := ix.run(ctx, root)
		ix.update(func(p *Progress) {
			p.Running = false
			p.Current = ""
			p.ElapsedMs = time.Now().UnixMilli() - p.StartedAt
			switch {
			case err == nil:
				p.Phase = "done"
			case errors.Is(err, context.Canceled):
				p.Phase = "done"
				p.Error = "사용자가 중단했습니다"
			default:
				p.Phase = "error"
				p.Error = err.Error()
			}
		})
	}()
	return nil
}

func (ix *Indexer) run(ctx context.Context, root string) error {
	if err := ix.db.SetRoot(root); err != nil {
		return err
	}

	existing, err := ix.db.Existing()
	if err != nil {
		return fmt.Errorf("기존 인덱스 조회 실패: %w", err)
	}

	// 1단계: 대상 파일 수집
	type job struct {
		path  string
		size  int64
		mtime int64
	}
	var jobs []job
	seen := make(map[string]bool, len(existing))

	walkErr := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// 권한 없는 폴더 하나 때문에 전체가 멈추면 안 된다.
			return nil //nolint:nilerr
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if d.IsDir() {
			if skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if skipFile(d.Name()) || !ix.reg.Supports(p) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil //nolint:nilerr
		}
		seen[p] = true
		jobs = append(jobs, job{p, info.Size(), info.ModTime().Unix()})
		return nil
	})
	if walkErr != nil {
		return walkErr
	}

	ix.update(func(p *Progress) {
		p.Total = len(jobs)
		p.Phase = "extract"
	})

	// 2단계: 사라진 파일 정리
	for path := range existing {
		if seen[path] {
			continue
		}
		if err := ix.db.DeleteByPath(path); err == nil {
			ix.update(func(p *Progress) { p.Removed++ })
		}
	}

	// 3단계: 추출 (병렬) → 저장 (직렬)
	//
	// 추출은 CPU 바운드라 워커로 나누고, 쓰기는 SQLite 연결 1개로 몰아
	// 잠금 경합을 없앤다. 채널이 그 경계다.
	jobCh := make(chan job)
	docCh := make(chan *extract.Doc, ix.nWrk)

	var wg sync.WaitGroup
	var failed int64

	for i := 0; i < ix.nWrk; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobCh {
				select {
				case <-ctx.Done():
					return
				default:
				}
				doc := ix.reg.Extract(ctx, j.path, ix.opt)
				if doc.Err != nil {
					atomic.AddInt64(&failed, 1)
					// 추출에 실패해도 파일명 검색은 되도록 최소 정보로 색인한다.
					doc.Warnings = append(doc.Warnings, "본문 추출 실패: "+doc.Err.Error())
					doc.Err = nil
				}
				select {
				case docCh <- doc:
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	go func() {
		defer close(jobCh)
		for _, j := range jobs {
			if prev, ok := existing[j.path]; ok && prev.Size == j.size && prev.MTime == j.mtime {
				ix.update(func(p *Progress) { p.Skipped++; p.Done++ })
				continue
			}
			select {
			case jobCh <- j:
			case <-ctx.Done():
				return
			}
		}
	}()

	go func() {
		wg.Wait()
		close(docCh)
	}()

	for doc := range docCh {
		rel, _ := filepath.Rel(root, doc.Path)
		ix.update(func(p *Progress) { p.Current = filepath.ToSlash(rel) })

		if err := ix.db.Put(root, doc); err != nil {
			atomic.AddInt64(&failed, 1)
			ix.update(func(p *Progress) { p.Done++ })
			continue
		}
		ocr := doc.OCRPages
		ix.update(func(p *Progress) {
			p.Done++
			p.Indexed++
			p.OCRPages += ocr
		})
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	ix.update(func(p *Progress) {
		p.Failed = int(atomic.LoadInt64(&failed))
		p.Phase = "optimize"
		p.Current = ""
	})
	if err := ix.db.Optimize(); err != nil {
		return fmt.Errorf("인덱스 최적화 실패: %w", err)
	}
	return nil
}

// skipDir 은 색인할 필요가 없는 폴더를 걸러낸다.
func skipDir(name string) bool {
	if strings.HasPrefix(name, ".") { // .git, .svn, .cache …
		return true
	}
	switch strings.ToLower(name) {
	case "node_modules", "$recycle.bin", "system volume information",
		"windows", "program files", "program files (x86)", "appdata",
		"__pycache__", "venv", ".venv":
		return true
	}
	return false
}

// skipFile 은 임시/잠금 파일을 걸러낸다.
// 특히 "~$계약서.docx" 는 워드가 파일을 열어둔 동안 만드는 잠금 파일로,
// 열어봐야 본문이 없고 에러만 난다.
func skipFile(name string) bool {
	if strings.HasPrefix(name, "~$") || strings.HasPrefix(name, ".") {
		return true
	}
	lower := strings.ToLower(name)
	return lower == "thumbs.db" || lower == "desktop.ini" || strings.HasSuffix(lower, ".tmp")
}
