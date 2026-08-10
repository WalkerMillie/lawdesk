# 번들 자산 폴더

이 폴더에 놓인 파일은 `go build` 시 실행파일 안에 그대로 임베드되고,
앱 실행 시 사용자 캐시 폴더(`%LOCALAPPDATA%\lawdesk\tools-<해시>\`)에 풀린다.

## 기대하는 구성 (윈도우 배포용)

```
assets/
├─ mutool.exe              MuPDF — PDF 텍스트 추출 및 페이지 렌더
├─ tesseract.exe           Tesseract OCR 실행파일 (+ 필요한 DLL)
└─ tessdata/
   └─ kor.traineddata      한국어 학습 데이터
```

## 왜 저장소에 커밋하지 않는가

1. **용량** — 합쳐서 60~80MB. git 히스토리에 넣으면 클론이 무거워진다.
2. **재배포 라이선스** — MuPDF는 AGPL, Tesseract는 Apache-2.0이다.
   별도 프로세스로 호출하는 것과 바이너리를 재배포하는 것은 의무가 다르다.
   배포 전 각 라이선스를 확인하고, 릴리스 아티팩트에 라이선스 전문을 동봉할 것.
3. **출처 추적** — 어느 빌드를 넣었는지 스크립트로 남기는 편이 명확하다.

`scripts/fetch-tools.sh --help` 를 참고해 내려받은 뒤 빌드하면 된다.

이 폴더가 비어 있어도 (이 README만 있어도) 빌드는 정상 동작한다.
그 경우 앱은 실행파일 옆 `bin/` 폴더와 시스템 `PATH` 에서 도구를 찾는다.
