# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## 빌드 및 실행

```sh
# 로컬 실행 (개발)
go run .

# 현재 플랫폼 빌드
go build -o pfortainer .

# FreeBSD amd64 배포용 빌드 (커밋 시 항상 같이 빌드)
GOOS=freebsd GOARCH=amd64 go build -o pfortainer-freebsd .
```

테스트 없음. 컴파일 검증은 `go build ./...`로 충분.

## 아키텍처

단일 Go 패키지(`package main`), 5개 소스 파일로 구성된 서버사이드 렌더링 웹앱.

```
요청 → main.go(라우팅+auth 미들웨어)
         → handlers.go(핸들러 → VM 변환 → render)
              → podman.go(Podman REST API 호출)
              → templates/*.html(Go template, base.html 상속)
```

**핵심 흐름:**
- 모든 페이지 핸들러는 Podman API → API 구조체 → VM(View Model) → 템플릿 순으로 처리
- `render(w, "page-name", data)` 호출 시 `templates.go`가 `base.html` + 해당 템플릿을 합성해 렌더링
- 로그인 페이지만 예외적으로 단독 렌더링(base.html 미사용)

**Podman API 클라이언트 (`podman.go`):**
- Unix 소켓(`/run/podman/podman.sock`)에 HTTP 연결, 호스트명은 `podman` (더미)
- 컨테이너 목록/상세 조회는 반드시 **libpod 네이티브 API** (`/v5.0.0/libpod/...`) 사용
  - Docker 호환 API(`/containers/json`)는 Podman 5.8.1/FreeBSD에서 nil pointer panic 발생
- 이미지/컨테이너 액션(start/stop/kill/pause/resume/remove)은 Docker 호환 API 사용
- `PodmanError` 타입으로 API 에러 구분, 핸들러에서 `StatusCode`로 분기 처리

**인증 (`session.go`):**
- HMAC-SHA256 서명된 쿠키(`pfsession`), 8시간 유지
- `isAuthenticated()` → auth 미들웨어 → 미인증 시 `/login` 리디렉트

**템플릿 (`templates/`):**
- `//go:embed templates/*`로 바이너리에 내장
- `base.html`이 공통 레이아웃(사이드바, 상단바) 제공, 나머지 페이지는 `{{template "base" .}}`로 상속
- 사이드바 메뉴 활성화: 각 핸들러가 `"ActivePage"` 키를 data에 포함시키고, base.html에서 `{{if eq .ActivePage "xxx"}}active{{end}}`로 처리
- UI: Bootstrap 5.3 + Bootstrap Icons 1.11 (CDN), 한국어

## FreeBSD 배포

배포 경로: `/zdata/tools/pfortainer-freebsd/`

```sh
# fbnas에서 바이너리 교체 절차
sudo service pfortainer stop
# pfortainer-freebsd 바이너리 scp 전송
sudo service pfortainer start
```

rc.d 스크립트(`deploy/freebsd/rc.d/pfortainer`)는 시작 시 Podman API 소켓이 없으면 자동으로 `podman system service`를 띄운다. Podman 소켓 자체도 별도 rc 서비스(`podman_api`)로 관리 중.

## 주요 구현 주의사항

- **컨테이너 상세 페이지 fallback**: `InspectContainer` API가 FreeBSD에서 5xx 오류를 내면 컨테이너 목록 데이터로 대체 렌더링 (`InspectFailed: true` → 템플릿에서 경고 배너 표시)
- **Entrypoint 타입**: Podman이 string 또는 `[]string` 둘 다 반환 가능 → `entrypointField` 커스텀 언마샬러로 처리
- **이미지 태그 필터**: `<none>:<none>` 태그는 VM 변환 시 제거
