#!/usr/bin/env bash
# 윈도우 배포판에 임베드할 외부 도구(mutool · tesseract · kor.traineddata)를 준비한다.
#
# 결과물은 internal/bundle/assets/ 에 놓이고, 이후 `go build` 하면 실행파일 안에
# 그대로 들어간다. assets/ 는 .gitignore 되어 있어 저장소에는 커밋되지 않는다.
#
# 사용법:
#   scripts/fetch-tools.sh              # 내려받아 준비 (라이선스 확인 프롬프트)
#   scripts/fetch-tools.sh -y           # 확인 없이 진행 (CI 용)
#   scripts/fetch-tools.sh --from DIR   # 이미 모아둔 폴더에서 복사
#   scripts/fetch-tools.sh --help       # 라이선스 안내와 출처
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ASSETS="$REPO_ROOT/internal/bundle/assets"

# 버전을 고정한다. 도구가 바뀌면 OCR 정확도도 바뀌므로, 올릴 때는
# testdata/measure_ocr.py 로 재측정한 뒤 올릴 것.
MUPDF_VER=1.26.0
MUPDF_URL="https://mupdf.com/downloads/archive/mupdf-${MUPDF_VER}-windows.zip"
TESS_VER=5.4.0.20240606
TESS_URL="https://github.com/UB-Mannheim/tesseract/releases/download/v${TESS_VER}/tesseract-ocr-w64-setup-${TESS_VER}.exe"
KOR_URL="https://github.com/tesseract-ocr/tessdata_fast/raw/main/kor.traineddata"

usage() {
  cat <<EOF
lawdesk 도구 번들링 스크립트

내려받는 것 (버전 고정)
  MuPDF ${MUPDF_VER}        mutool.exe            PDF 텍스트 추출 · 페이지 렌더
  Tesseract ${TESS_VER}   tesseract.exe + DLL   OCR
  tessdata_fast             kor.traineddata       한국어 학습 데이터

라이선스 — 사람이 한 번은 확인해야 한다
  MuPDF (mutool)  : AGPL-3.0. 별도 프로세스 호출은 문제없으나 바이너리를
                    재배포하면 대응 소스 제공 의무가 따라온다.
                    상용 납품 시 Artifex 상용 라이선스를 검토할 것.
                    https://artifex.com/licensing/
  Tesseract       : Apache-2.0. 재배포 시 고지 동봉.
  kor.traineddata : Apache-2.0.

  릴리스에는 THIRD-PARTY-LICENSES.txt 로 고지와 AGPL 소스 URL 을 동봉한다.

필요한 도구
  curl · python3 · 7z(p7zip-full) · objdump·strip(binutils)

용량 참고
  받는 중간 산출물 약 200MB, 최종 assets/ 약 66MB, 완성된 exe 약 80MB.
  (libtesseract-5.dll 은 디버그 심볼이 101MB 라 strip 하면 3MB 가 된다)

사용법
  scripts/fetch-tools.sh              내려받아 준비
  scripts/fetch-tools.sh -y           확인 프롬프트 생략
  scripts/fetch-tools.sh --from DIR   이미 모아둔 폴더에서 복사
  make package-windows                단일 exe 빌드
EOF
}

need() {
  local miss=()
  for c in "$@"; do command -v "$c" >/dev/null 2>&1 || miss+=("$c"); done
  if [ ${#miss[@]} -gt 0 ]; then
    echo "필요한 명령이 없습니다: ${miss[*]}" >&2
    echo "  Debian/Ubuntu: sudo apt install curl python3 p7zip-full binutils" >&2
    exit 1
  fi
}

show_assets() {
  echo
  echo "assets 구성:"
  find "$ASSETS" -type f ! -name README.md | sort | while read -r f; do
    printf '  %-44s %8.1f MB\n' "${f#"$ASSETS/"}" "$(echo "scale=1; $(stat -c%s "$f")/1048576" | bc)"
  done
  printf '  %-44s %8s\n' '합계' "$(du -sh "$ASSETS" | cut -f1)"
  echo
  echo "이제 'make package-windows' 를 실행하세요."
}

fetch() {
  need curl python3 7z objdump strip

  if [ "${1:-}" != "-y" ]; then
    echo
    usage
    echo
    read -r -p "위 라이선스 조건을 확인했습니까? 계속하려면 yes: " ans
    [ "$ans" = "yes" ] || { echo "중단합니다."; exit 1; }
  fi

  # 전역으로 둔다. local 로 두면 EXIT 트랩이 돌 때 이미 스코프를 벗어나
  # set -u 에 걸린다.
  work="$(mktemp -d)"
  trap 'rm -rf "${work:-}"' EXIT

  echo "==> MuPDF ${MUPDF_VER} 내려받는 중 (약 90MB)"
  curl -fL --progress-bar -o "$work/mupdf.zip" "$MUPDF_URL"

  echo "==> Tesseract ${TESS_VER} 내려받는 중 (약 50MB)"
  curl -fL --progress-bar -o "$work/tess.exe" "$TESS_URL"

  echo "==> kor.traineddata 내려받는 중"
  mkdir -p "$ASSETS/tessdata"
  curl -fL --progress-bar -o "$ASSETS/tessdata/kor.traineddata" "$KOR_URL"

  echo "==> mutool.exe 추출"
  MUPDF_VER="$MUPDF_VER" ASSETS="$ASSETS" WORK="$work" python3 - <<'PY'
import os, zipfile, shutil
ver, assets, work = os.environ['MUPDF_VER'], os.environ['ASSETS'], os.environ['WORK']
z = zipfile.ZipFile(os.path.join(work, 'mupdf.zip'))
for src, dst in [(f'mupdf-{ver}-windows/mutool.exe', 'mutool.exe'),
                 (f'mupdf-{ver}-windows/COPYING.txt', 'MuPDF-COPYING.txt')]:
    with z.open(src) as f, open(os.path.join(assets, dst), 'wb') as o:
        shutil.copyfileobj(f, o)
PY

  # UB-Mannheim 설치본은 NSIS 라 innoextract 로는 안 열린다. 7z 로 푼다.
  echo "==> Tesseract 설치본 해제"
  7z x -o"$work/tess" "$work/tess.exe" >/dev/null

  # tesseract.exe 가 실제로 필요로 하는 DLL 만 골라낸다. 설치본에는 학습
  # 도구용 pango/cairo/ICU 까지 들어 있어 통째로 넣으면 159MB 가 된다.
  echo "==> DLL 의존성 추적"
  ASSETS="$ASSETS" WORK="$work" python3 - <<'PY'
import os, re, shutil, subprocess
d = os.path.join(os.environ['WORK'], 'tess')
assets = os.environ['ASSETS']
avail = {f.lower(): f for f in os.listdir(d) if f.lower().endswith('.dll')}

def deps(path):
    out = subprocess.run(['objdump', '-p', path], capture_output=True, text=True).stdout
    return re.findall(r'DLL Name:\s*(\S+)', out)

seen, stack, keep = set(), ['tesseract.exe'], {'tesseract.exe'}
while stack:
    cur = stack.pop()
    if cur in seen:
        continue
    seen.add(cur)
    p = os.path.join(d, avail.get(cur.lower(), cur))
    if not os.path.exists(p):
        continue
    for dep in deps(p):
        if dep.lower() in avail:
            keep.add(avail[dep.lower()])
            stack.append(dep)

for f in sorted(keep):
    shutil.copy2(os.path.join(d, f), os.path.join(assets, f))
print(f'  tesseract.exe + DLL {len(keep) - 1}개')
PY

  # 디버그 심볼 제거. 프로그램 코드는 그대로다(libtesseract 101MB → 3MB).
  echo "==> 디버그 심볼 제거"
  for f in "$ASSETS"/*.dll "$ASSETS"/*.exe; do
    [ -e "$f" ] || continue
    x86_64-w64-mingw32-strip "$f" 2>/dev/null || strip "$f" 2>/dev/null || true
  done

  show_assets
}

case "${1:-}" in
  --from)
    SRC="${2:?--from 뒤에 폴더 경로가 필요합니다}"
    [ -d "$SRC" ] || { echo "폴더가 없습니다: $SRC" >&2; exit 1; }
    mkdir -p "$ASSETS/tessdata"
    echo "복사 중: $SRC → $ASSETS"
    # README.md 는 자리표시자이므로 덮어쓰지 않는다.
    rsync -a --exclude 'README.md' "$SRC"/ "$ASSETS"/
    show_assets
    ;;
  -h|--help)
    usage
    ;;
  -y|"")
    fetch "${1:-}"
    ;;
  *)
    echo "알 수 없는 옵션: $1" >&2
    usage
    exit 1
    ;;
esac
