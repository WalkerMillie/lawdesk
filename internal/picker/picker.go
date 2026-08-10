// Package picker 는 OS 기본 폴더 선택 대화상자를 띄운다.
//
// 왜 백엔드에서 띄우는가:
// 브라우저의 <input type="file" webkitdirectory> 는 보안상 실제 경로를 주지 않는다.
// 상대경로 목록만 오고 파일 내용을 브라우저 메모리로 올려버리므로, 수천 건 문서를
// 제자리에서 색인해야 하는 이 앱에는 쓸 수 없다. 앱이 로컬에서 돌기 때문에
// 백엔드가 직접 네이티브 창을 띄우는 쪽이 정확하고 사용자에게도 익숙하다.
package picker

import "errors"

// ErrUnsupported 는 이 플랫폼에 네이티브 대화상자가 없을 때.
// UI 는 이 경우 경로 직접 입력란으로 대체한다.
var ErrUnsupported = errors.New("이 환경에서는 폴더 선택 창을 열 수 없습니다. 경로를 직접 입력해 주세요")

// ErrCancelled 는 사용자가 창을 닫았을 때.
var ErrCancelled = errors.New("폴더 선택이 취소되었습니다")

// PickFolder 는 폴더 선택 창을 띄우고 선택된 절대경로를 돌려준다.
func PickFolder(title string) (string, error) { return pickFolder(title) }

// Available 은 네이티브 대화상자를 쓸 수 있는지 알려준다(UI 분기용).
func Available() bool { return available() }
