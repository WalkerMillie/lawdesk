// Package openfile 은 문서를 OS 기본 프로그램으로 연다.
// 검색으로 찾은 뒤 실제 원본(한글·워드·PDF 뷰어)으로 넘어가는 마지막 단계다.
package openfile

import (
	"fmt"
	"os/exec"
	"runtime"
)

// Open 은 파일 또는 폴더를 기본 연결 프로그램으로 연다.
func Open(path string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		// rundll32 을 쓰는 이유: `cmd /c start` 는 콘솔 창이 잠깐 뜨고,
		// 경로에 &, ^ 같은 문자가 있으면 cmd 가 해석해 버린다.
		cmd = exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", path)
	case "darwin":
		cmd = exec.Command("open", path)
	default:
		cmd = exec.Command("xdg-open", path)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("파일 열기 실패: %w", err)
	}
	// 뷰어가 떠 있는 동안 좀비 프로세스가 남지 않도록 회수만 하고 기다리지 않는다.
	go func() { _ = cmd.Wait() }()
	return nil
}

// Reveal 은 파일이 들어있는 폴더를 열고 해당 파일을 선택한다.
func Reveal(path string) error {
	switch runtime.GOOS {
	case "windows":
		cmd := exec.Command("explorer.exe", "/select,"+path)
		// explorer 는 성공해도 exit code 1 을 내는 경우가 있어 에러를 무시한다.
		_ = cmd.Start()
		go func() { _ = cmd.Wait() }()
		return nil
	case "darwin":
		cmd := exec.Command("open", "-R", path)
		if err := cmd.Start(); err != nil {
			return err
		}
		go func() { _ = cmd.Wait() }()
		return nil
	default:
		return Open(path)
	}
}
