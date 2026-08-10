// Package bundle 은 외부 도구(mutool, tesseract)를 실행파일 안에 담고,
// 실행 시 사용자 캐시 폴더에 풀어 쓴다.
//
// 왜 이렇게 하는가:
// 최종 사용자는 비개발자다. "zip 풀고 폴더 구조 유지해서 실행하세요" 는
// 현장에서 반드시 깨진다(exe만 바탕화면에 복사하는 일이 생긴다).
// 파일 하나로 끝나야 안전하다.
//
// 임베드할 바이너리는 저장소에 커밋하지 않는다(용량·라이선스 재배포 문제).
// scripts/fetch-tools.sh 가 assets/ 에 내려받은 뒤 빌드하면 그때 포함된다.
// 아무것도 없으면 이 패키지는 조용히 비활성화되고, 앱은 PATH 에서 도구를 찾는다.
package bundle

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

//go:embed assets
var assets embed.FS

// Result 는 풀어놓은 도구의 위치.
type Result struct {
	Dir       string // 도구가 풀린 폴더 ("" 이면 임베드된 것이 없음)
	MuTool    string
	Tesseract string
	TessData  string
}

// Bundled 는 실행파일에 도구가 포함돼 있는지 알려준다.
func Bundled() bool {
	names, err := listAssets()
	return err == nil && len(names) > 0
}

// Extract 는 임베드된 도구를 캐시 폴더에 풀고 경로를 돌려준다.
//
// 같은 내용이 이미 풀려 있으면 다시 쓰지 않는다(두 번째 실행부터 즉시 시작).
// 임베드된 것이 없으면 빈 Result 를 돌려주며, 이는 에러가 아니다.
func Extract() (Result, error) {
	names, err := listAssets()
	if err != nil || len(names) == 0 {
		return Result{}, nil //nolint:nilerr // 번들 없음은 정상 상태다
	}

	// 내용 해시를 폴더명에 넣어, 앱을 업데이트하면 새 폴더에 풀리게 한다.
	// 구버전 도구가 남아 잘못 실행되는 사고를 막는다.
	sum, err := assetsHash(names)
	if err != nil {
		return Result{}, err
	}

	base, err := os.UserCacheDir()
	if err != nil {
		base = os.TempDir()
	}
	dir := filepath.Join(base, "lawdesk", "tools-"+sum[:12])

	stamp := filepath.Join(dir, ".complete")
	if _, err := os.Stat(stamp); err != nil {
		if err := extractAll(names, dir); err != nil {
			return Result{}, err
		}
		if err := os.WriteFile(stamp, []byte(sum), 0o644); err != nil {
			return Result{}, fmt.Errorf("완료 표시 기록 실패: %w", err)
		}
	}

	res := Result{Dir: dir}
	if p := filepath.Join(dir, exeName("mutool")); exists(p) {
		res.MuTool = p
	}
	if p := filepath.Join(dir, exeName("tesseract")); exists(p) {
		res.Tesseract = p
	}
	if p := filepath.Join(dir, "tessdata"); exists(p) {
		res.TessData = p
	}
	return res, nil
}

// listAssets 는 자리표시자(README 등)를 뺀 실제 자산 목록을 돌려준다.
func listAssets() ([]string, error) {
	var out []string
	err := fs.WalkDir(assets, "assets", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		name := d.Name()
		if name == "README.md" || name == ".gitkeep" {
			return nil
		}
		out = append(out, p)
		return nil
	})
	return out, err
}

func assetsHash(names []string) (string, error) {
	h := sha256.New()
	for _, n := range names {
		b, err := assets.ReadFile(n)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s:%d:", n, len(b))
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func extractAll(names []string, dir string) error {
	// 이전 시도가 중간에 죽었을 수 있으니 통째로 지우고 다시 푼다.
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("캐시 폴더 정리 실패: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("캐시 폴더 생성 실패: %w", err)
	}

	for _, name := range names {
		rel := strings.TrimPrefix(name, "assets/")
		dst := filepath.Join(dir, filepath.FromSlash(rel))

		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		src, err := assets.Open(name)
		if err != nil {
			return err
		}
		// 실행 권한을 붙여 둔다(리눅스/맥에서 번들을 쓸 경우 필요).
		f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			src.Close()
			return err
		}
		_, cerr := io.Copy(f, src)
		src.Close()
		if err := f.Close(); err != nil && cerr == nil {
			cerr = err
		}
		if cerr != nil {
			return fmt.Errorf("%s 풀기 실패: %w", rel, cerr)
		}
	}
	return nil
}

func exeName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
