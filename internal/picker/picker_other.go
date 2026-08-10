//go:build !windows

package picker

// 리눅스/맥에서는 네이티브 폴더 창을 띄우지 않는다.
// 이 앱의 배포 대상은 윈도우이고, 개발 중에는 UI 의 경로 직접 입력란으로 충분하다.
func available() bool { return false }

func pickFolder(string) (string, error) { return "", ErrUnsupported }
