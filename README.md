# pfortainer

FreeBSD + Podman 환경을 위한 경량 Portainer 대체 웹 UI. Go로 작성된 단일 바이너리이며,
Podman REST API(유닉스 소켓)에 직접 연결해 컨테이너/이미지를 조회하고 제어합니다.

## 주요 기능

- **Dashboard**: 전체/실행 중/정지 컨테이너 수, 이미지 수, 실행 중인 컨테이너 목록
- **Containers**
  - 목록 조회 (이름, 상태, 이미지, 생성일, 포트, ID)
  - 검색(이름/상태/이미지/포트/ID 텍스트 필터)
  - 체크박스 다중 선택 + 상단 작업 툴바: 시작 / 정지 / Kill / 재시작 / 일시정지(pause) / 재개(resume) / 삭제
  - 실행 중인 컨테이너를 삭제하려고 하면 "실행 중인 컨테이너는 삭제할 수 없습니다" 안내
- **Images**
  - 목록 조회 (태그, Image ID, 크기, 생성일)
  - 검색(태그/ID/크기/생성일 텍스트 필터)
  - 체크박스 다중 선택 + 삭제 / 강제 삭제(force remove, 드롭다운)
- **인증**: 단일 관리자 비밀번호 + HMAC 서명된 세션 쿠키 (8시간 유지)

## 설정

`.env` 파일 또는 환경변수로 설정합니다 (`.env.example` 참고).

| 변수 | 기본값 | 설명 |
|---|---|---|
| `PODMAN_SOCKET` | `/run/podman/podman.sock` | Podman API 유닉스 소켓 경로 |
| `ADMIN_PASSWORD` | `changeme` | 로그인 비밀번호 |
| `SESSION_SECRET` | (기본값 변경 필요) | 세션 쿠키 서명용 시크릿 |
| `HOST` | `0.0.0.0` | 바인드 호스트 |
| `PORT` | `11000` | 바인드 포트 |

## 로컬 실행

```sh
cp .env.example .env   # 값 수정 후
go run .
```

## 빌드

```sh
# 현재 플랫폼
go build -o pfortainer .

# FreeBSD amd64 (배포용)
GOOS=freebsd GOARCH=amd64 go build -o pfortainer-freebsd .
```

## FreeBSD 배포

배포 위치: `/zdata/tools/pfortainer-freebsd/`

1. 빌드한 `pfortainer-freebsd` 바이너리를 서버로 전송 후 `pfortainer`로 교체
   (실행 중인 바이너리는 `service stop` 후 교체해야 "Text file busy" 오류를 피할 수 있음)
2. `deploy/freebsd/rc.d/pfortainer`를 `/usr/local/etc/rc.d/pfortainer`에 설치하고
   `sysrc pfortainer_enable=YES` 설정
3. `service pfortainer start|stop|restart|status`로 관리
   - 시작 시 Podman API 소켓(`/run/podman/podman.sock`)이 없으면 자동으로
     `podman system service`를 백그라운드로 띄움

수동 실행이 필요하면 `deploy/freebsd/start.sh` 참고.

## 구조

- `main.go` — 라우팅, 인증 미들웨어
- `handlers.go` — 페이지 렌더링 및 컨테이너/이미지 액션 핸들러
- `podman.go` — Podman REST API 클라이언트 (조회 + 시작/정지/재시작/kill/pause/resume/remove, 이미지 remove)
- `session.go` — 쿠키 기반 인증
- `config.go` — 환경변수/`.env` 로딩
- `templates/` — HTML 템플릿 (Bootstrap 5 + Bootstrap Icons)
