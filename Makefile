VERSION  ?= 0.1.0
LDFLAGS   = -s -w -X main.version=$(VERSION)
BUILDOPTS = -trimpath -ldflags "$(LDFLAGS)"
DIST      = dist

# cgo 를 끄면 순수 Go 로만 링크되어 리눅스에서 윈도우 exe 를 그대로 만들 수 있다.
export CGO_ENABLED = 0

.PHONY: all build test vet fmt run corpus package-windows package-all clean tools-status

all: vet test build

build:
	go build $(BUILDOPTS) -o $(DIST)/lawdesk ./cmd/lawdesk

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

## run: 테스트 코퍼스를 색인하며 개발 서버 실행
run:
	go run ./cmd/lawdesk -root testdata/corpus -no-browser

## corpus: 합성 테스트 문서 재생성 (한글 폰트·reportlab·python-docx 필요)
corpus:
	python3 testdata/gen_corpus.py

## tools-status: 임베드 예정 자산 확인
tools-status:
	@echo "internal/bundle/assets:"
	@find internal/bundle/assets -type f ! -name README.md -printf '  %-44p %10s bytes\n' 2>/dev/null \
		| sed 's|internal/bundle/assets/||' || true
	@test -n "$$(find internal/bundle/assets -type f ! -name README.md 2>/dev/null)" \
		|| echo "  (비어 있음 — 빌드는 되지만 PDF/OCR 은 시스템 PATH 의 도구를 씁니다)"

## package-windows: 윈도우 배포용 단일 exe 생성
package-windows: tools-status
	GOOS=windows GOARCH=amd64 go build $(BUILDOPTS) -o $(DIST)/lawdesk-$(VERSION)-windows-amd64.exe ./cmd/lawdesk
	@ls -lh $(DIST)/lawdesk-$(VERSION)-windows-amd64.exe

package-all: package-windows
	GOOS=windows GOARCH=arm64 go build $(BUILDOPTS) -o $(DIST)/lawdesk-$(VERSION)-windows-arm64.exe ./cmd/lawdesk
	GOOS=linux   GOARCH=amd64 go build $(BUILDOPTS) -o $(DIST)/lawdesk-$(VERSION)-linux-amd64      ./cmd/lawdesk
	GOOS=darwin  GOARCH=arm64 go build $(BUILDOPTS) -o $(DIST)/lawdesk-$(VERSION)-darwin-arm64     ./cmd/lawdesk
	@ls -lh $(DIST)/

clean:
	rm -rf $(DIST)
