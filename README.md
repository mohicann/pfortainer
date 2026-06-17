# pfortainer

FreeBSD + Podman 환경을 위한 경량 Portainer 대체 웹 UI. Go로 작성된 단일 바이너리이며,
Podman REST API(유닉스 소켓)에 직접 연결해 컨테이너/이미지를 조회하고 제어합니다.

## 주요 기능

**pfortainer 메뉴**
- **Dashboard**: 전체/실행 중/정지 컨테이너 수, 이미지 수, 실행 중인 컨테이너 목록
- **Containers**: 목록 조회·검색, 상세 보기, 다중 선택 작업(시작/정지/Kill/재시작/일시정지/재개/삭제)
- **Images**: 목록 조회·검색, 다중 선택 삭제/강제 삭제

**시스템 메뉴**
- **시스템 정보**: Podman `/libpod/info` API로 호스트 정보(hostname·OS·커널·아키텍처·업타임), CPU 사용률, 메모리/스왑, Podman 버전, 컨테이너·이미지 수, 스토리지 경로 표시

**공통**
- 단일 관리자 비밀번호 + HMAC-SHA256 서명된 세션 쿠키 (8시간 유지)

## 설정

`.env` 파일 또는 환경변수로 설정합니다 (`.env.example` 참고).

| 변수 | 기본값 | 설명 |
|---|---|---|
| `PODMAN_SOCKET` | `/run/podman/podman.sock` | Podman API 유닉스 소켓 경로 |
| `ADMIN_PASSWORD` | (필수) | 로그인 비밀번호 |
| `SESSION_SECRET` | (필수) | 세션 쿠키 서명용 시크릿 |
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

**초기 설치:**
1. `deploy/freebsd/rc.d/pfortainer`를 `/usr/local/etc/rc.d/pfortainer`에 복사
2. `sysrc pfortainer_enable=YES`
3. `/zdata/tools/pfortainer-freebsd/.env` 생성 (`.env.example` 참고)
4. `service pfortainer start`

**바이너리 업데이트:**
```sh
sudo service pfortainer stop
# pfortainer-freebsd 전송 후 배포 경로에 교체
sudo service pfortainer start
```

> 실행 중인 바이너리를 바로 덮어쓰면 "Text file busy" 오류가 발생하므로 반드시 stop 후 교체.

rc.d 스크립트는 시작 시 Podman API 소켓(`/run/podman/podman.sock`)이 없으면 자동으로 `podman system service`를 백그라운드로 띄운다. 소켓 자체는 별도 rc 서비스(`podman_api`)로도 관리 가능.

## 문제 해결

**컨테이너 목록/상세에서 nil pointer panic**

Podman 5.8.1/FreeBSD의 Docker 호환 API(`/containers/json`, `/containers/{id}/json`)가 panic을 일으키는 버그가 있다. 이를 피하기 위해 목록·상세 조회는 네이티브 libpod API(`/v5.0.0/libpod/...`)를 사용한다. 상세 조회에서 5xx가 발생하면 목록 데이터로 대체 렌더링하며 페이지 상단에 경고 배너를 표시한다.

Podman 버전 업그레이드 후 같은 증상이 재발하면 libpod API 응답 형식 변경 여부를 확인:
```sh
curl --unix-socket /run/podman/podman.sock \
  http://d/v5.0.0/libpod/containers/json?all=true
```
