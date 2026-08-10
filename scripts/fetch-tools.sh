#!/usr/bin/env bash
# 윈도우 배포판에 임베드할 외부 도구를 내려받는다.
#
# 받은 파일은 internal/bundle/assets/ 에 놓이고, 이후 `go build` 하면
# 실행파일 안에 그대로 들어간다. assets/ 는 .gitignore 되어 있으므로
# 저장소에는 커밋되지 않는다(용량·재배포 라이선스 문제).
#
# 사용법:
#   scripts/fetch-tools.sh            # 대화형으로 안내만 출력
#   scripts/fetch-tools.sh --from DIR # 이미 받아둔 폴더에서 복사
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ASSETS="$REPO_ROOT/internal/bundle/assets"

usage() {
  cat <<'EOF'
lawdesk 도구 번들링 스크립트

이 스크립트는 서드파티 바이너리를 자동으로 내려받지 않는다.
배포 라이선스를 사람이 한 번은 확인해야 하기 때문이다.

  MuPDF (mutool)  : AGPL-3.0. 별도 프로세스 호출은 문제없으나
                    바이너리를 재배포하면 AGPL 의무가 따라온다.
                    상용 배포 시 Artifex 상용 라이선스를 검토할 것.
  Tesseract OCR   : Apache-2.0. 재배포 시 라이선스 전문 동봉.
  kor.traineddata : Apache-2.0.

--------------------------------------------------------------------
내려받을 곳
--------------------------------------------------------------------
  mutool.exe
    https://mupdf.com/releases            (Windows x64 빌드)

  tesseract.exe (+ 동반 DLL)
    https://github.com/UB-Mannheim/tesseract/wiki
    설치 후 설치폴더에서 tesseract.exe 와 *.dll 을 복사

  kor.traineddata
    https://github.com/tesseract-ocr/tessdata_fast/raw/main/kor.traineddata
    (fast 판은 약 5MB, 정확도 우선이면 tessdata_best 사용)

--------------------------------------------------------------------
배치 구조
--------------------------------------------------------------------
  internal/bundle/assets/
  ├─ mutool.exe
  ├─ tesseract.exe
  ├─ *.dll                (tesseract 가 요구하는 런타임 DLL)
  └─ tessdata/kor.traineddata

준비되면:
  scripts/fetch-tools.sh --from /경로/모아둔폴더
  make package-windows
EOF
}

case "${1:-}" in
  --from)
    SRC="${2:?--from 뒤에 폴더 경로가 필요합니다}"
    [ -d "$SRC" ] || { echo "폴더가 없습니다: $SRC" >&2; exit 1; }
    mkdir -p "$ASSETS/tessdata"
    echo "복사 중: $SRC → $ASSETS"
    # README.md 는 자리표시자이므로 덮어쓰지 않는다.
    rsync -a --exclude 'README.md' "$SRC"/ "$ASSETS"/
    echo
    echo "현재 assets 구성:"
    find "$ASSETS" -type f ! -name README.md -printf '  %-40p %10s bytes\n' | sed "s|$ASSETS/||"
    echo
    echo "이제 'make package-windows' 를 실행하세요."
    ;;
  -h|--help|"")
    usage
    ;;
  *)
    echo "알 수 없는 옵션: $1" >&2
    usage
    exit 1
    ;;
esac
