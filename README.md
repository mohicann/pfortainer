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
- **파일시스템**: ZFS 풀/데이터셋 조회, 데이터셋 생성·삭제, 트리 접기/펼치기
- **파일매니저**: 서버 파일시스템 탐색 및 관리 (아래 참고)
- **서비스**: 리스닝 중인 포트 목록 + container/jail/네이티브 분류 (아래 참고)
- **메트릭**: Netdata REST API 연동 실시간 모니터링 (아래 참고)

**공통**
- 단일 관리자 비밀번호 + HMAC-SHA256 서명된 세션 쿠키 (8시간 유지)

## 파일매니저

`/files` 경로에서 서버의 파일시스템을 직접 관리합니다.

| 기능 | 설명 |
|------|------|
| 디렉토리 탐색 | 브레드크럼 네비게이션, 폴더 클릭, 상위 이동 버튼 |
| 숨김파일 토글 | 스위치로 `.` 시작 파일/폴더 표시 전환 |
| 파일/폴더 생성 | 모달 입력으로 빈 파일·폴더 생성 |
| 삭제 | 다중 선택 후 확인 모달 |
| 이름 변경 | 1개 선택 시 rename 모달 |
| 복사 / 이동 | copy·cut → 대상 디렉토리에서 붙여넣기 (쿠키 기반 클립보드) |
| 권한 변경 | chmod (8진수 입력, 예: 644) |
| 파일 편집 | 텍스트 에디터 (Ctrl+S 저장, Tab 키, 1 MB 이하 텍스트 파일) |
| 다운로드 | 파일 행의 다운로드 버튼 |
| 업로드 | 드래그앤드롭 또는 파일 선택, 진행률 표시 (최대 32 MB) |

바이너리 파일·1 MB 초과 파일은 편집 불가, 다운로드만 가능합니다.

## 서비스

`/services` 경로에서 현재 시스템에서 리스닝 중인 서비스를 확인합니다.

| 기능 | 설명 |
|------|------|
| 포트 목록 | `sockstat -l` 기반으로 리스닝 중인 전체 포트 표시 (tcp4/tcp6 중복 제거) |
| 유형 분류 | **컨테이너** / **Jail** / **네이티브** 3가지로 자동 분류 |
| 컨테이너 감지 | FreeBSD Podman은 컨테이너를 VNET Jail로 실행. `jls`로 Jail 목록을 가져와 Podman 컨테이너 ID와 매핑. `ps -J <JID>`로 PID→JID→컨테이너 이름으로 연결 |
| 필터 탭 | 전체/컨테이너/Jail/네이티브 탭으로 원클릭 필터링 |
| 요약 카드 | 유형별 포트 수 요약 |

## 메트릭

`/metrics` 경로에서 시스템 메트릭을 실시간으로 확인합니다.  
Netdata 불필요 — pfortainer가 FreeBSD sysctls에서 직접 수집합니다.

| 구성 | 갱신 주기 | 내용 |
|------|-----------|------|
| 도넛 게이지 | **5초** | CPU 사용률, 메모리 사용률, zdata/zboot 풀 사용량 |
| 시계열 차트 | **30초** | 네트워크(igc0)·디스크I/O·CPU히스토리·ZFS ARC 크기·Load Average·ZFS ARC 히트율·Tailscale·TCP 활성 연결 |

수집 데이터는 SQLite(`./metrics.db`)에 저장됩니다.

| 항목 | 내용 |
|------|------|
| 수집 간격 | 5초 |
| 보존 기간 | 기본 10일, `METRICS_RETENTION_DAYS`로 변경 가능 |
| 예상 용량 | 약 50~60 MB |
| 30분 이내 조회 | 메모리 ring buffer (빠름) |
| 30분 초과 조회 | SQLite bucket 평균 쿼리 |
| 재기동 시 | DB에서 최근 30분 자동 복원 |

## 설정

`.env` 파일 또는 환경변수로 설정합니다 (`.env.example` 참고).

| 변수 | 기본값 | 설명 |
|---|---|---|
| `PODMAN_SOCKET` | `/run/podman/podman.sock` | Podman API 유닉스 소켓 경로 |
| `ADMIN_PASSWORD` | (필수) | 로그인 비밀번호 |
| `SESSION_SECRET` | (필수) | 세션 쿠키 서명용 시크릿 |
| `HOST` | `0.0.0.0` | 바인드 호스트 |
| `PORT` | `11000` | 바인드 포트 |
| `METRICS_DB` | `./metrics.db` | 메트릭 SQLite DB 파일 경로 |
| `METRICS_RETENTION_DAYS` | `10` | 메트릭 보존 기간 (일) |

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
