// Command extractdump 은 폴더를 훑어 추출 결과를 표로 찍는다.
// 개발/검증용 도구다. 최종 배포 바이너리에는 포함되지 않는다.
package main

import (
	"context"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/WalkerMillie/lawdesk/internal/extract"
)

func main() {
	var (
		root    = flag.String("root", "testdata/corpus", "훑을 폴더")
		noOCR   = flag.Bool("no-ocr", false, "OCR 비활성화")
		verbose = flag.Bool("v", false, "문서별 본문 앞부분과 목차 출력")
	)
	flag.Parse()

	tools := extract.DiscoverTools()
	if miss := tools.Missing(); len(miss) > 0 {
		fmt.Fprintf(os.Stderr, "경고: 사용할 수 없는 도구 → %s\n\n", strings.Join(miss, ", "))
	}

	opt := extract.Options{MaxTextBytes: 8 << 20}
	if !*noOCR {
		if ocr := extract.NewTesseractOCR(tools); ocr.Available() {
			opt.OCR = ocr
		}
	}

	reg := extract.DefaultRegistry(tools)

	var paths []string
	err := filepath.WalkDir(*root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !reg.Supports(p) {
			return nil
		}
		paths = append(paths, p)
		return nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "폴더 탐색 실패:", err)
		os.Exit(1)
	}
	sort.Strings(paths)

	ctx := context.Background()
	fmt.Printf("%-46s %-8s %6s %7s %6s  %s\n", "파일", "종류", "글자", "OCR쪽", "ms", "제목")
	fmt.Println(strings.Repeat("-", 128))

	var totalRunes, totalMs int
	var failed int

	for _, p := range paths {
		start := time.Now()
		doc := reg.Extract(ctx, p, opt)
		elapsed := int(time.Since(start).Milliseconds())
		totalMs += elapsed

		rel, _ := filepath.Rel(*root, p)
		if doc.Err != nil {
			failed++
			fmt.Printf("%-46s  ✗ %v\n", trunc(rel, 44), doc.Err)
			continue
		}
		n := len([]rune(doc.Text))
		totalRunes += n

		fmt.Printf("%-46s %-8s %6d %7d %6d  %s\n",
			trunc(rel, 44), doc.Kind, n, doc.OCRPages, elapsed, trunc(doc.Title, 40))

		for _, w := range doc.Warnings {
			fmt.Printf("%48s ⚠ %s\n", "", w)
		}
		if *verbose {
			for _, h := range doc.Outline {
				fmt.Printf("%48s %s• %s\n", "", strings.Repeat("  ", max(0, h.Level-1)), h.Text)
			}
			fmt.Printf("%48s │ %s\n", "", trunc(doc.Summary(140), 100))
		}
	}

	fmt.Println(strings.Repeat("-", 128))
	fmt.Printf("파일 %d개 · 실패 %d개 · 총 %d자 · %dms\n",
		len(paths), failed, totalRunes, totalMs)
}

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
