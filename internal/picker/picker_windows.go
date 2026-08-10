//go:build windows

package picker

import (
	"runtime"
	"syscall"
	"unsafe"
)

var (
	shell32 = syscall.NewLazyDLL("shell32.dll")
	ole32   = syscall.NewLazyDLL("ole32.dll")

	procSHBrowseForFolderW  = shell32.NewProc("SHBrowseForFolderW")
	procSHGetPathFromIDList = shell32.NewProc("SHGetPathFromIDListW")
	procCoInitializeEx      = ole32.NewProc("CoInitializeEx")
	procCoUninitialize      = ole32.NewProc("CoUninitialize")
	procCoTaskMemFree       = ole32.NewProc("CoTaskMemFree")
)

// browseInfoW 는 win32 BROWSEINFOW 와 메모리 배치가 같아야 한다.
type browseInfoW struct {
	hwndOwner      uintptr
	pidlRoot       uintptr
	pszDisplayName *uint16
	lpszTitle      *uint16
	ulFlags        uint32
	lpfn           uintptr
	lParam         uintptr
	iImage         int32
}

const (
	bifReturnOnlyFSDirs = 0x00000001 // 파일시스템 폴더만 (제어판 같은 가상 항목 제외)
	bifNewDialogStyle   = 0x00000040 // 크기 조절·새 폴더 만들기 가능한 최신 스타일
	bifEditBox          = 0x00000010 // 경로 직접 입력 가능

	coinitApartmentThreaded = 0x2
	coinitDisableOLE1DDE    = 0x4

	maxPathW = 32768 // 유니코드 확장 경로 대응
)

func available() bool { return true }

func pickFolder(title string) (string, error) {
	// 셸 대화상자는 STA(단일 스레드 아파트)를 요구한다. 고루틴이 OS 스레드를
	// 옮겨 다니면 COM 상태가 깨지므로 이 함수가 끝날 때까지 스레드를 고정한다.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	// S_OK(0) 와 S_FALSE(1) 는 성공. RPC_E_CHANGED_MODE(0x80010106) 는
	// 이미 다른 모드로 초기화된 경우인데, 그때는 Uninitialize 를 부르면 안 된다.
	hr, _, _ := procCoInitializeEx.Call(0, coinitApartmentThreaded|coinitDisableOLE1DDE)
	if hr == 0 || hr == 1 {
		defer procCoUninitialize.Call()
	}

	titlePtr, err := syscall.UTF16PtrFromString(title)
	if err != nil {
		return "", err
	}
	display := make([]uint16, maxPathW)

	bi := browseInfoW{
		pszDisplayName: &display[0],
		lpszTitle:      titlePtr,
		ulFlags:        bifReturnOnlyFSDirs | bifNewDialogStyle | bifEditBox,
	}

	pidl, _, _ := procSHBrowseForFolderW.Call(uintptr(unsafe.Pointer(&bi)))
	if pidl == 0 {
		return "", ErrCancelled
	}
	// PIDL 은 셸 할당자가 준 메모리다. 반드시 CoTaskMemFree 로 돌려줘야 한다.
	defer procCoTaskMemFree.Call(pidl)

	buf := make([]uint16, maxPathW)
	ok, _, _ := procSHGetPathFromIDList.Call(pidl, uintptr(unsafe.Pointer(&buf[0])))
	if ok == 0 {
		return "", ErrCancelled
	}
	path := syscall.UTF16ToString(buf)
	if path == "" {
		return "", ErrCancelled
	}
	// bi 가 콜백 호출 전에 GC 되지 않도록 여기까지 살려 둔다.
	runtime.KeepAlive(bi)
	runtime.KeepAlive(display)
	return path, nil
}
