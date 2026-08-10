// Package web 은 빌드된 UI 자산을 실행파일 안에 담는다.
// 이 덕분에 배포물이 파일 하나로 유지되고, 실행 시 외부 리소스를 받아오지 않는다.
package web

import "embed"

//go:embed dist
var Assets embed.FS
