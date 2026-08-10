// Command lawdesk 는 로컬 문서 검색 서버를 띄우고 브라우저를 연다.
//
// 전 과정 오프라인으로 동작한다. 색인 대상이 계약서·판결문 같은 비밀유지의무
// 대상 문서이므로 어떤 데이터도 외부로 전송하지 않는다.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/WalkerMillie/lawdesk/internal/bundle"
	"github.com/WalkerMillie/lawdesk/internal/extract"
	"github.com/WalkerMillie/lawdesk/internal/index"
	"github.com/WalkerMillie/lawdesk/internal/openfile"
	"github.com/WalkerMillie/lawdesk/internal/scan"
	"github.com/WalkerMillie/lawdesk/internal/server"
)

// version 은 릴리스 빌드 시 -ldflags 로 주입한다.
var version = "dev"

func main() {
	var (
		addr    = flag.String("addr", "127.0.0.1:7777", "서버 주소 (루프백만 허용)")
		dbPath  = flag.String("db", "", "인덱스 DB 경로 (기본: 사용자 데이터 폴더)")
		root    = flag.String("root", "", "시작 시 색인할 폴더")
		noOCR   = flag.Bool("no-ocr", false, "스캔 문서 OCR 비활성화")
		noOpen  = flag.Bool("no-browser", false, "브라우저 자동 실행 안 함")
		showVer = flag.Bool("version", false, "버전 출력 후 종료")
	)
	flag.Parse()

	if *showVer {
		fmt.Printf("lawdesk %s (%s/%s)\n", version, runtime.GOOS, runtime.GOARCH)
		return
	}

	if err := run(*addr, *dbPath, *root, *noOCR, *noOpen); err != nil {
		log.Fatalf("오류: %v", err)
	}
}

func run(addr, dbPath, root string, noOCR, noBrowser bool) error {
	if dbPath == "" {
		var err error
		if dbPath, err = defaultDBPath(); err != nil {
			return err
		}
	}

	db, err := index.Open(dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	// 실행파일에 도구가 임베드돼 있으면 먼저 풀어서 그것을 쓴다.
	// 사용자 PC에 설치된 다른 버전보다 우선해, 검증된 조합으로 동작하게 한다.
	tools := extract.DiscoverTools()
	if b, err := bundle.Extract(); err != nil {
		fmt.Println("  경고: 내장 도구를 풀지 못했습니다 —", err)
	} else if b.Dir != "" {
		if b.MuTool != "" {
			tools.MuTool = b.MuTool
		}
		if b.Tesseract != "" {
			tools.Tesseract = b.Tesseract
		}
		tools.TessData = b.TessData
	}

	opt := extract.Options{MaxTextBytes: 16 << 20}

	ocrOn := false
	if !noOCR {
		if ocr := extract.NewTesseractOCR(tools); ocr.Available() {
			opt.OCR = ocr
			ocrOn = true
		}
	}

	reg := extract.DefaultRegistry(tools)
	ix := scan.NewIndexer(db, reg, opt)
	srv := server.New(db, ix, tools, ocrOn, version)

	// 루프백 강제. 외부 인터페이스 바인딩은 문서 유출 위험이 있어 막는다.
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("주소 형식 오류(host:port): %w", err)
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("보안상 루프백 주소만 사용할 수 있습니다 (127.0.0.1). 입력값: %s", host)
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("포트를 열 수 없습니다(%s): %w", addr, err)
	}

	httpSrv := &http.Server{
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	url := "http://" + ln.Addr().String() + "/"
	fmt.Println("lawdesk", version, "—", url)
	fmt.Println("  인덱스:", dbPath)
	if miss := tools.Missing(); len(miss) > 0 {
		fmt.Println("  경고: 사용할 수 없는 기능 —", miss)
	}
	if !ocrOn {
		fmt.Println("  OCR: 꺼짐 (스캔 문서는 본문 검색이 되지 않습니다)")
	}
	fmt.Println("  종료하려면 Ctrl+C")

	if root != "" {
		if abs, err := filepath.Abs(root); err == nil {
			if err := ix.Start(abs); err != nil {
				fmt.Println("  색인 시작 실패:", err)
			}
		}
	}
	if !noBrowser {
		// 서버가 실제로 응답할 준비가 된 뒤에 연다.
		go func() {
			time.Sleep(250 * time.Millisecond)
			_ = openfile.Open(url)
		}()
	}

	// Ctrl+C 로 깔끔히 종료 — 인덱싱 중이면 취소하고 DB 를 정상 닫는다.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() { errCh <- httpSrv.Serve(ln) }()

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		fmt.Println("\n종료 중…")
		ix.Cancel()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
	}
	return nil
}

// defaultDBPath 는 OS 별 사용자 데이터 폴더 아래에 인덱스를 둔다.
// 문서 폴더를 더럽히지 않기 위해서다(원본 폴더는 읽기만 한다).
func defaultDBPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		// 홈 디렉터리조차 없으면 현재 폴더로 폴백
		return "lawdesk.db", nil //nolint:nilerr
	}
	return filepath.Join(dir, "lawdesk", "index.db"), nil
}
