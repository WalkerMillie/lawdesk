package extract

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Tools 는 외부 실행 바이너리 경로를 담는다.
//
// 배포판(윈도우 exe)에서는 mutool/tesseract 를 exe 안에 임베드해 두고
// 최초 실행 시 임시 폴더에 풀어 그 경로를 여기에 채운다(bootstrap_windows.go).
// 개발 중(리눅스)에는 PATH 에서 찾는다.
type Tools struct {
	MuTool    string // PDF 텍스트 추출 / 페이지 렌더
	Tesseract string // OCR
	TessData  string // 학습데이터 디렉터리 (TESSDATA_PREFIX)

	// ExecTimeout 은 외부 프로세스 1회 호출의 상한. 0이면 DefaultExecTimeout.
	ExecTimeout time.Duration
}

// DefaultExecTimeout 은 손상된 PDF 등으로 외부 도구가 멈췄을 때
// 인덱싱 전체가 영원히 대기하는 것을 막는다.
const DefaultExecTimeout = 90 * time.Second

// DiscoverTools 는 PATH 와 실행파일 옆 bin/ 디렉터리에서 도구를 찾는다.
func DiscoverTools() *Tools {
	t := &Tools{}
	t.MuTool = findBinary("mutool")
	t.Tesseract = findBinary("tesseract")
	return t
}

func findBinary(name string) string {
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	// 1) 실행파일 옆 bin/ — 배포판 레이아웃
	if self, err := os.Executable(); err == nil {
		cand := filepath.Join(filepath.Dir(self), "bin", name)
		if isExecutable(cand) {
			return cand
		}
	}
	// 2) PATH
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return ""
}

func isExecutable(path string) bool {
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return false
	}
	if runtime.GOOS == "windows" {
		return true
	}
	return fi.Mode()&0o111 != 0
}

func (t *Tools) timeout() time.Duration {
	if t.ExecTimeout > 0 {
		return t.ExecTimeout
	}
	return DefaultExecTimeout
}

// run 은 외부 도구를 호출하고 stdout 을 돌려준다.
// stderr 는 에러 메시지에만 쓰고 결과에 섞지 않는다(추출 텍스트 오염 방지).
func (t *Tools) run(ctx context.Context, bin string, args ...string) ([]byte, error) {
	if bin == "" {
		return nil, errors.New("도구 경로가 설정되지 않았습니다")
	}
	ctx, cancel := context.WithTimeout(ctx, t.timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, args...)
	if t.TessData != "" {
		cmd.Env = append(os.Environ(), "TESSDATA_PREFIX="+t.TessData)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if ctx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("%s 실행 시간 초과(%s)", filepath.Base(bin), t.timeout())
	}
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 300 {
			msg = msg[:300] + "…"
		}
		return nil, fmt.Errorf("%s 실행 실패: %w (%s)", filepath.Base(bin), err, msg)
	}
	return out, nil
}

// Missing 은 없어서 기능이 제한되는 도구 이름을 돌려준다(사용자 안내용).
func (t *Tools) Missing() []string {
	var miss []string
	if t.MuTool == "" {
		miss = append(miss, "mutool (PDF 처리)")
	}
	if t.Tesseract == "" {
		miss = append(miss, "tesseract (스캔 문서 OCR)")
	}
	return miss
}
