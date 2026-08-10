# lawdesk

로컬 문서 폴더를 **완전 오프라인**으로 색인해 본문까지 검색하는 데스크톱 도구.
계약서·판결문·등기부등본처럼 외부로 내보낼 수 없는 문서를 다루는 환경을 전제로 만들었다.

```
폴더 지정  →  PDF · DOCX · 텍스트 본문 추출  →  스캔본은 OCR  →  전문검색
                                                          ↑
                                              네트워크 사용 0
```

| | |
|---|---|
| 배포 | 윈도우 실행파일 1개 (설치 없음) |
| 검색 | SQLite FTS5 trigram — 한국어 부분일치·사건번호·구문 검색 |
| 스캔본 | Tesseract OCR 자동 처리 |
| 요약 | 문서 구조 기반 추출 요약 (LLM 미사용) |
| 네트워크 | **사용하지 않음.** 루프백 바인딩 강제 |

---

## 왜 오프라인인가

대상 문서가 변호사 비밀유지의무(변호사법 제26조) 대상이다.
그래서 요약에 LLM 을 쓰지 않고, 코드 어디에도 외부 HTTP 호출이 없으며,
서버는 `127.0.0.1` 에만 바인딩된다.

자세한 설계 배경과 측정 근거는 **[docs/DESIGN.md](docs/DESIGN.md)** 참고.

---

## 사용법

```
lawdesk.exe
```

브라우저가 자동으로 열린다. **[폴더 선택]** 으로 문서 폴더를 지정하면 색인이 시작된다.
두 번째 실행부터는 변경된 파일만 처리하므로 거의 즉시 끝난다.

### 옵션

```
lawdesk.exe -root "D:\법무팀\문서"   # 시작하자마자 해당 폴더 색인
            -no-ocr                  # 스캔 문서 OCR 끄기 (빠름)
            -no-browser              # 브라우저 자동 실행 안 함
            -addr 127.0.0.1:7777     # 포트 변경 (루프백만 허용)
            -db  "D:\index.db"       # 인덱스 위치 지정
```

인덱스 기본 위치는 `%APPDATA%\lawdesk\index.db` 다.
문서 폴더에는 아무것도 쓰지 않는다(읽기 전용).

### 검색 예

| 입력 | 찾는 것 |
|---|---|
| `손해배상` | 일반 단어 |
| `배상책임` | 어절 중간 부분일치 |
| `2021다12345` | 사건번호 |
| `제7조` | 조문 번호 |
| `집행을 유예` | 연속된 구문 |

3글자 미만은 인덱스를 쓸 수 없어 느리지만, 결과는 정확하게 나온다.

---

## 지원 포맷

| 포맷 | 처리 |
|---|---|
| `.docx` | 본문 + 제목 스타일에서 목차 추출 + 표 내용 |
| `.pdf` (텍스트) | MuPDF 로 페이지 단위 추출 |
| `.pdf` (스캔 이미지) | 자동 판별 후 Tesseract OCR |
| `.txt .md .csv .log .json .xml .html` | 직접 읽기 (UTF-8 / CP949 자동 판별) |

`.hwp` 는 현재 미지원 — [DESIGN.md §3.1](docs/DESIGN.md) 참고.

---

## 빌드

`CGO_ENABLED=0` 순수 Go 스택이라 리눅스/맥에서 윈도우 exe 를 그대로 만들 수 있다.

```bash
make            # vet + test + build
make test
make run        # 테스트 코퍼스를 색인하며 개발 서버 실행
make package-windows
```

Go 1.24 이상이 필요하다.

### 외부 도구

PDF 처리에 `mutool`(MuPDF), OCR 에 `tesseract` + `kor.traineddata` 를 쓴다.
개발 중에는 시스템에 설치된 것을 `PATH` 에서 찾는다.

```bash
sudo apt install mupdf-tools tesseract-ocr tesseract-ocr-kor   # Debian/Ubuntu
```

배포용 단일 exe 를 만들려면 윈도우용 바이너리를 `internal/bundle/assets/` 에 넣고
빌드하면 실행파일 안에 임베드된다. 준비 방법은 `scripts/fetch-tools.sh --help` 참고.

> 서드파티 바이너리는 용량과 재배포 라이선스(MuPDF = AGPL) 때문에
> 저장소에 커밋하지 않는다. 상용 배포 시 라이선스를 반드시 확인할 것.

### 테스트 코퍼스

실제 법률 문서를 쓸 수 없으므로 합성 코퍼스를 생성해 검증한다.
등장하는 인명·회사명·사건번호는 전부 가공의 것이다.

```bash
make corpus                        # 계약서·판결문·스캔본 9종 생성
python3 testdata/measure_ocr.py    # OCR 정확도 측정
```

`measure_ocr.py` 는 실제 문서를 확보한 뒤 정확도를 다시 재는 용도로도 쓸 수 있다.

---

## 구조

```
cmd/lawdesk/          진입점 — 서버 기동, 브라우저 실행
cmd/extractdump/      개발용 추출 검증 CLI
internal/
  extract/            포맷별 텍스트 추출 (docx · pdf · plain · ocr)
  index/              SQLite FTS5 색인과 검색
  scan/               폴더 순회, 증분 인덱싱, 병렬 워커
  server/             로컬 HTTP API
  picker/             OS 네이티브 폴더 선택 대화상자
  bundle/             외부 도구 임베드/추출
  openfile/           기본 프로그램으로 문서 열기
web/dist/             임베드되는 UI (빌드 도구 없음)
testdata/             합성 코퍼스 생성기 + OCR 측정 하네스
docs/DESIGN.md        설계 문서
```

---

## 상태

핵심 파이프라인은 동작하고 테스트로 덮여 있다.
윈도우 실기 검증(폴더 선택 대화상자, 한글 경로, 번들 도구 추출)은 아직 남아 있다.
알려진 한계는 [DESIGN.md §10](docs/DESIGN.md) 에 정리했다.

## 라이선스

MIT — [LICENSE](LICENSE) 참고.
번들하는 외부 도구는 각자의 라이선스를 따른다(MuPDF: AGPL-3.0, Tesseract: Apache-2.0).
