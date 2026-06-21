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

### 운영 구조 (Jail)

pfortainer는 FreeBSD Jail(`zdata/jails/pfortainer`) 안에서 실행됩니다.

```
FreeBSD 호스트
├── /run/podman/podman.sock          ← podman_api rc 서비스가 관리
├── /zdata/tools/pfortainer-freebsd/ ← 바이너리 및 설정
│   ├── pfortainer                   ← 실행 바이너리 (배포 대상)
│   ├── rc.d/pfortainer              ← Jail 내부 rc.d 원본
│   ├── metrics.db                   ← 메트릭 SQLite DB (영구 보존)
│   └── .env                         ← 환경변수 설정
│
└── Jail: pfortainer (192.168.10.111)
    ├── /app        → nullfs ro mount (/zdata/tools/pfortainer-freebsd/)
    │   └── 바이너리, rc.d, metrics.db, .env 접근
    ├── /run/podman → nullfs rw mount (호스트 Podman 소켓)
    │   └── podman.sock (Podman API 통신)
    ├── /var/log/pfortainer.log      ← Jail 내부 로그 (ZFS에 영구 보존)
    └── :11000 서비스
```

**데이터 보존:** Jail을 중지해도 ZFS 데이터셋(`zdata/jails/pfortainer`)과 호스트의 `metrics.db`는 그대로 유지됩니다.

**rc 서비스 의존 순서:** `podman_api` → `pfortainer (Jail 내부)`

### 초기 설치

1. Jail 생성:
   ```sh
   zfs create zdata/jails
   zfs create zdata/jails/pfortainer
   fetch https://download.freebsd.org/releases/amd64/15.1-RELEASE/base.txz -o /tmp/base.txz
   tar -xf /tmp/base.txz -C /zdata/jails/pfortainer
   cp /etc/resolv.conf /zdata/jails/pfortainer/etc/
   chroot /zdata/jails/pfortainer /bin/sh -c 'ln -sf /usr/share/zoneinfo/Asia/Seoul /etc/localtime'
   ```

2. Jail 설정 파일 배포:

   `/etc/jail.conf.d/pfortainer.conf`:
   ```
   pfortainer {
       path = /zdata/jails/pfortainer;
       host.hostname = pfortainer;

       ip4.addr = igc3|192.168.10.111/24;
       ip4.addr += lo0|127.0.0.2/8;

       exec.start = "/bin/sh /etc/rc";
       exec.stop  = "/bin/sh /etc/rc.shutdown";
       exec.poststart += "zfs jail pfortainer zdata";
       exec.poststart += "zfs jail pfortainer zboot";
       exec.clean;
       mount.devfs;
       mount.fstab = /etc/fstab.jails/pfortainer;

       allow.mount;
       allow.mount.zfs;
       enforce_statfs = 1;
   }
   ```

   `/etc/fstab.jails/pfortainer`:
   ```
   /run/podman                      /zdata/jails/pfortainer/run/podman  nullfs  rw,late  0  0
   /zdata/tools/pfortainer-freebsd  /zdata/jails/pfortainer/app         nullfs  ro,late  0  0
   /zdata                           /zdata/jails/pfortainer/zdata        nullfs  rw,late  0  0
   /zboot                           /zdata/jails/pfortainer/zboot        nullfs  ro,late  0  0
   ```

   마운트 포인트 생성:
   ```sh
   mkdir -p /zdata/jails/pfortainer/run/podman \
            /zdata/jails/pfortainer/app \
            /zdata/jails/pfortainer/zdata \
            /zdata/jails/pfortainer/zboot
   ```

3. ZFS 권한 위임 (파일시스템/파일매니저에서 ZFS 조회 가능):
   ```sh
   zfs allow -u root mount,create,destroy,snapshot,rollback zdata
   zfs allow -u root mount,create,destroy,snapshot,rollback zboot
   ```
   `jail.conf.d/pfortainer.conf`에 `exec.poststart`로 `zfs jail`을 추가해야 재부팅 후에도 유지됩니다 (아래 설정 파일 참고). 새 ZFS 풀 추가 시 `zfs allow`와 `exec.poststart` 줄을 동일하게 추가하세요.

4. 호스트 rc 서비스 설치:
   ```sh
   cp deploy/freebsd/rc.d/podman_api /usr/local/etc/rc.d/
   chmod +x /usr/local/etc/rc.d/podman_api
   sysrc podman_api_enable=YES
   sysrc jail_enable=YES
   sysrc jail_list=pfortainer
   ```

5. Jail 내부 rc 서비스 설치:
   ```sh
   mkdir -p /zdata/jails/pfortainer/usr/local/etc/rc.d
   cp deploy/freebsd/rc.d/pfortainer /zdata/jails/pfortainer/usr/local/etc/rc.d/
   chmod +x /zdata/jails/pfortainer/usr/local/etc/rc.d/pfortainer
   ```
   Jail 내부 `/etc/rc.conf`:
   ```sh
   pfortainer_enable="YES"
   pfortainer_dir="/app"
   pfortainer_logfile="/var/log/pfortainer.log"
   ```

6. `.env` 생성: `/zdata/tools/pfortainer-freebsd/.env` (`.env.example` 참고)

7. `pf.conf`에 Tailscale 포트 포워딩 추가:
   ```sh
   # /etc/pf.conf (rdr-anchor 위에 추가)
   ts_if=tailscale0
   rdr pass on $ts_if proto tcp from any to TAILSCALE_IP port 11000 -> 192.168.10.111 port 11000
   ```
   ```sh
   pfctl -f /etc/pf.conf
   ```

8. 서비스 시작:
   ```sh
   service podman_api start
   service jail start pfortainer
   ```

### 바이너리 업데이트

```sh
# 1. 로컬에서 크로스 컴파일
GOOS=freebsd GOARCH=amd64 go build -o pfortainer-freebsd .

# 2. Jail 내부 pfortainer 중지 (실행 중 바이너리 덮어쓰기 방지)
ssh -t fbnas "sudo jexec pfortainer service pfortainer stop"

# 3. 바이너리 전송
scp pfortainer-freebsd fbnas:/zdata/tools/pfortainer-freebsd/pfortainer

# 4. pfortainer 재시작
ssh -t fbnas "sudo jexec pfortainer service pfortainer start"
```

### 로그 확인

```sh
# Jail 내부 pfortainer 로그
ssh fbnas "sudo jexec pfortainer tail -f /var/log/pfortainer.log"

# Jail 상태
ssh fbnas "jls -v"
```

## 문제 해결

**부팅 직후 "Podman 소켓에 연결할 수 없습니다" 페이지**

pfortainer는 소켓 없이도 즉시 기동되며, `podman_api` 서비스가 소켓을 생성하면 자동으로 연결됩니다. 페이지의 새로고침 버튼을 누르거나 잠시 후 재접속하면 정상 동작합니다.

**컨테이너 목록/상세에서 nil pointer panic**

Podman 5.8.1/FreeBSD의 Docker 호환 API(`/containers/json`, `/containers/{id}/json`)가 panic을 일으키는 버그가 있다. 이를 피하기 위해 목록·상세 조회는 네이티브 libpod API(`/v5.0.0/libpod/...`)를 사용한다. 상세 조회에서 5xx가 발생하면 목록 데이터로 대체 렌더링하며 페이지 상단에 경고 배너를 표시한다.

Podman 버전 업그레이드 후 같은 증상이 재발하면 libpod API 응답 형식 변경 여부를 확인:
```sh
curl --unix-socket /run/podman/podman.sock \
  http://d/v5.0.0/libpod/containers/json?all=true
```
