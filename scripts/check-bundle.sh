#!/usr/bin/env bash
# 번들에 넣은 윈도우 바이너리의 DLL 의존성이 실제로 전부 충족되는지 검사한다.
#
# 왜 필요한가:
# mutool.exe 는 Visual C++ 재배포 런타임(MSVCP140.dll 등)을 요구한다.
# 개발자 PC 와 CI 러너, Wine 에는 이 DLL 이 이미 있어서 테스트가 전부 통과하지만,
# 깨끗한 윈도우에 설치한 최종 사용자에게서는 mutool 이 아예 실행되지 않는다.
# 그 결과 PDF 가 텍스트든 스캔이든 통째로 안 읽힌다. 실제로 그렇게 나갔다.
#
# 실행 검사로는 못 잡는 종류의 결함이라, import 테이블을 정적으로 확인한다.
# 필요한 DLL 은 (1) 번들에 있거나 (2) 윈도우 기본 탑재여야 한다. 둘 다 아니면 실패.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ASSETS="${1:-$REPO_ROOT/internal/bundle/assets}"

command -v objdump >/dev/null || { echo "objdump 가 필요합니다 (apt install binutils)" >&2; exit 1; }

if ! ls "$ASSETS"/*.exe >/dev/null 2>&1; then
  echo "번들 자산이 없습니다: $ASSETS"
  echo "(도구를 임베드하지 않는 빌드라면 정상입니다)"
  exit 0
fi

ASSETS="$ASSETS" python3 - <<'PY'
import os, re, subprocess, sys

assets = os.environ['ASSETS']

# 윈도우 10/11 에 기본 탑재되는 DLL. 여기 있으면 번들하지 않아도 된다.
# api-ms-win-crt-* 는 UCRT 로, 윈도우 10 부터 OS 구성요소다.
INBOX_PREFIX = ('api-ms-win-',)
INBOX = {
    'kernel32.dll', 'kernelbase.dll', 'ntdll.dll', 'user32.dll', 'gdi32.dll',
    'advapi32.dll', 'shell32.dll', 'shlwapi.dll', 'ole32.dll', 'oleaut32.dll',
    'comdlg32.dll', 'comctl32.dll', 'version.dll', 'ws2_32.dll', 'crypt32.dll',
    'bcrypt.dll', 'ncrypt.dll', 'secur32.dll', 'rpcrt4.dll', 'psapi.dll',
    'iphlpapi.dll', 'userenv.dll', 'setupapi.dll', 'winmm.dll', 'imm32.dll',
    'uxtheme.dll', 'dwmapi.dll', 'oleacc.dll', 'mpr.dll', 'netapi32.dll',
    'wldap32.dll', 'powrprof.dll', 'dbghelp.dll', 'msvcrt.dll', 'ucrtbase.dll',
    'wintrust.dll', 'winhttp.dll', 'wininet.dll', 'normaliz.dll', 'dnsapi.dll',
}

bundled = {f.lower(): f for f in os.listdir(assets)
           if f.lower().endswith(('.dll', '.exe'))}

def imports(path):
    out = subprocess.run(['objdump', '-p', path], capture_output=True, text=True).stdout
    return sorted(set(re.findall(r'DLL Name:\s*(\S+)', out)))

def inbox(name):
    n = name.lower()
    return n in INBOX or n.startswith(INBOX_PREFIX)

roots = sorted(f for f in bundled.values() if f.lower().endswith('.exe'))
missing, seen, order = {}, set(), []
stack = list(roots)
while stack:
    cur = stack.pop()
    if cur.lower() in seen:
        continue
    seen.add(cur.lower())
    order.append(cur)
    p = os.path.join(assets, bundled.get(cur.lower(), cur))
    if not os.path.exists(p):
        continue
    for dep in imports(p):
        if dep.lower() in bundled:
            stack.append(dep)
        elif not inbox(dep):
            missing.setdefault(dep, []).append(cur)

print(f'검사 대상: {assets}')
print(f'  실행파일 {len(roots)}개에서 출발해 DLL {len(seen) - len(roots)}개 추적\n')

for f in roots:
    ext = [d for d in imports(os.path.join(assets, f)) if not inbox(d)]
    ok = [d for d in ext if d.lower() in bundled]
    bad = [d for d in ext if d.lower() not in bundled]
    mark = '❌' if bad else '✅'
    print(f'  {mark} {f}  — 번들 의존 {len(ok)}개' + (f', 누락 {len(bad)}개' if bad else ''))

if missing:
    print('\n❌ 번들에도 없고 윈도우 기본 탑재도 아닌 DLL:\n')
    for dll, users in sorted(missing.items()):
        print(f'  {dll}')
        print(f'      필요로 하는 곳: {", ".join(sorted(set(users)))}')
    print('\n이 상태로 배포하면 해당 도구가 깨끗한 윈도우에서 실행되지 않습니다.')
    print('scripts/fetch-tools.sh 를 확인하세요.')
    sys.exit(1)

print('\n✅ 모든 의존성이 번들에 있거나 윈도우 기본 탑재입니다.')
PY
