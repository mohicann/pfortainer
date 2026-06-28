# pfortainer

FreeBSD + Podman 환경을 위한 경량 Portainer 대체 웹 UI. Go로 작성된 단일 바이너리이며,
Podman REST API(유닉스 소켓)에 직접 연결해 컨테이너/이미지를 조회하고 제어합니다.

## 주요 기능

### 인증 / 보안
- **RBAC 다중 사용자**: viewer / operator / admin 3단계 역할. 관리자 UI(`/admin/users`)에서 사용자 추가·역할 변경·비밀번호 변경·삭제
- **2FA TOTP**: Google Authenticator 등 TOTP 앱으로 2단계 인증. `/profile`에서 QR 코드로 활성화·비활성화. 로그인 시 비밀번호 → `/login/totp` 코드 입력 2단계 진행
- **세션**: HMAC-SHA256 서명 쿠키(`pfsession`), 8시간 유지

### 컨테이너
- **Dashboard**: 전체/실행 중/정지 컨테이너 수, 이미지 수, 실행 중인 컨테이너 목록
- **Containers**: 목록·검색, 상세 보기, 다중 선택 작업(시작/정지/Kill/재시작/일시정지/재개/삭제), Dockerfile 빌드, docker-compose.yml 스택 실행
- **Images**: 목록·검색, 다중 선택 삭제/강제 삭제
- **앱 카탈로그**: FileBrowser·MinIO·Syncthing·Jellyfin·Vaultwarden·Uptime Kuma·Gitea 원클릭 배포. `pf-<id>` 이름으로 컨테이너 생성, 포트·볼륨·환경변수 입력 모달

### 스토리지 / ZFS
- **스토리지 상태** (`/storage`): ZFS 풀 health (vdev 트리), 디스크 SMART 상태·단기/장기 테스트 실행
- **ZFS 데이터셋** (`/filesystem`): ZFS 풀/데이터셋 조회, 데이터셋 생성·삭제, 사용량 트리 보기 (접기/펼치기)
- **스케줄 / 스냅샷** (`/snapshots`): 자동 스케줄 관리(상단), ZFS 스냅샷 생성·삭제·롤백·클론. 스냅샷 목록은 데이터셋별 최신 5개만 표시, "더 보기"로 전체 확인. Podman 컨테이너 레이어 스냅샷은 자동 필터링
- **자동 스케줄**: 스냅샷/스크럽/SMART 자동 실행. 매시간·매일·매주·매월, 보존 개수 설정
- **ZFS 복제** (`/replications`): `zfs send | zfs receive` 파이프라인. 로컬 or SSH 원격 대상, 증분/전체 자동 판단. 스케줄 설정 (수동/매시간/매일/매주)

### 파일 공유
- **SMB 공유**: `/usr/local/etc/smb4.conf.d/` drop-in 방식. 공유 추가·삭제, samba 재시작/상태 조회
- **NFS export**: `/etc/exports.d/` drop-in 방식. export 추가·삭제, mountd 리로드
- **로컬 사용자/그룹**: FreeBSD OS 사용자 생성·삭제, 그룹 관리, SMB 비밀번호 설정 (pw + smbpasswd via host agent)

### 시스템 / 모니터링
- **시스템 정보** (`/system`): 호스트 CPU·메모리·업타임, Podman 버전·스토리지 경로 실시간 표시
- **주요 메트릭** (`/metrics`): 자체 수집 시계열 차트 (CPU·메모리·네트워크·디스크I/O·ZFS ARC·Load Average 등). SQLite 저장, 10일 보존
- **네트워크** (`/network`): 인터페이스 목록 (IP·MAC·상태·미디어), 라우팅 테이블, DNS 서버
- **서비스** (`/services`): 리스닝 포트 목록 + 컨테이너/Jail/네이티브 자동 분류
- **진단/로그** (`/diagnostics`): pfortainer.log·messages·security·dmesg 실시간 tail, df/mount/ifconfig/netstat/sockstat/ps/dmesg 명령 실행 (AJAX, 자동 새로고침)
- **파일매니저** (`/files`): 서버 파일시스템 탐색·편집·업로드·다운로드

### 알림
- Email(SMTP) + Webhook. ZFS 풀 상태 악화·SMART 오류·디스크 용량·스크럽 오류 감지. 쿨다운 설정, 테스트 발송 버튼

## 사이드바 메뉴 구조

```
앱
├── Dashboard
├── Containers
├── Images
└── 앱 카탈로그

시스템
├── 시스템
├── 주요 메트릭
├── 스토리지
├── ZFS 데이터셋
├── 스케줄 / 스냅샷
├── 복제
├── 파일매니저
├── 네트워크
└── 진단/로그

공유
└── 공유 (SMB·NFS·로컬 사용자)

관리
├── 알림
└── 사용자 관리
```

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
| 컨테이너 감지 | FreeBSD Podman은 컨테이너를 VNET Jail로 실행. `jls`로 Jail 목록을 가져와 Podman 컨테이너 ID와 매핑 |
| Jail 실행 시 host agent 사용 | pfortainer가 Jail 안에서 실행되면 `sockstat`/`jls`가 호스트 포트를 볼 수 없음. `pfortainer_hostd` rc 서비스가 호스트에서 Unix 소켓(`/run/pfortainer/host.sock`)을 통해 해당 명령을 대리 실행. 소켓 없으면 직접 exec 폴백(로컬 개발 환경) |
| 필터 탭 | 전체/컨테이너/Jail/네이티브 탭으로 원클릭 필터링 |

## 주요 메트릭

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
| `SESSION_SECRET` | (필수) | 세션/TOTP pre-auth 쿠키 서명용 시크릿 |
| `HOST` | `0.0.0.0` | 바인드 호스트 |
| `PORT` | `11000` | 바인드 포트 |
| `METRICS_DB` | `./metrics.db` | 메트릭 SQLite DB 파일 경로 |
| `METRICS_RETENTION_DAYS` | `10` | 메트릭 보존 기간 (일) |
| `SMTP_HOST` | (선택) | SMTP 서버 (알림 이메일용) |
| `SMTP_PORT` | `587` | SMTP 포트 |
| `SMTP_USER` | (선택) | SMTP 인증 사용자 |
| `SMTP_PASS` | (선택) | SMTP 인증 비밀번호 |
| `ALERT_FROM` | (선택) | 알림 발신자 이메일 |

> **참고:** `ADMIN_PASSWORD`는 더 이상 사용하지 않습니다. 최초 실행 시 `admin` 계정이 자동 생성됩니다. 비밀번호는 `/admin/users`에서 변경하세요.

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
├── /run/pfortainer/host.sock        ← pfortainer_hostd rc 서비스가 관리
├── /zdata/tools/pfortainer-freebsd/ ← 바이너리 및 설정
│   ├── pfortainer                   ← 실행 바이너리 (배포 대상, hostd 겸용)
│   ├── metrics.db                   ← 메트릭 SQLite DB (영구 보존)
│   └── .env                         ← 환경변수 설정
│
└── Jail: pfortainer (192.168.10.111)
    ├── /app        → nullfs ro mount (/zdata/tools/pfortainer-freebsd/)
    ├── /run/podman → nullfs rw mount (호스트 Podman 소켓)
    ├── /var/log/pfortainer.log      ← Jail 내부 로그 (ZFS에 영구 보존)
    └── :11000 서비스
```

**데이터 보존:** Jail을 중지해도 ZFS 데이터셋(`zdata/jails/pfortainer`)과 호스트의 `metrics.db`는 그대로 유지됩니다.

**rc 서비스 기동 순서:** `podman_api` + `pfortainer_hostd` (호스트) → `jail` → `pfortainer` (Jail 내부)

**host agent 구조:** `pfortainer -hostd` 모드로 실행되는 `pfortainer_hostd` 서비스가 호스트에서 루트 권한으로 동작. Jail 내부의 pfortainer는 `/run/pfortainer/host.sock` 소켓을 통해 `ifconfig`, `sockstat`, `jls`, `zfs`, `zpool`, `smartctl` 등 권한이 필요한 명령을 위임 실행합니다.

### 바이너리 업데이트 (deploy.sh)

```sh
./deploy.sh          # 빌드 → 전송 → pfortainer_hostd + pfortainer 재시작
./deploy.sh myhostname  # 다른 호스트 지정
```

`deploy.sh` 한 번으로 끝납니다. 내부적으로:
1. FreeBSD amd64 크로스 컴파일
2. `scp`로 전송 후 `/zdata/tools/pfortainer-freebsd/pfortainer` 교체
3. 호스트의 `pfortainer_hostd` 재시작
4. Jail 내부의 `pfortainer` 재시작

> **주의:** `pfortainer_hostd`와 `pfortainer`는 동일한 바이너리를 공유하므로 반드시 함께 재시작해야 새 버전이 반영됩니다.

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

   ```sh
   sudo mkdir -p /etc/jail.conf.d /etc/fstab.jails
   sudo cp deploy/freebsd/jail.conf.d/pfortainer.conf /etc/jail.conf.d/
   sudo cp deploy/freebsd/fstab.jails/pfortainer /etc/fstab.jails/
   ```

   `/etc/jail.conf.d/pfortainer.conf` 내용:
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
            /zdata/jails/pfortainer/run/pfortainer \
            /zdata/jails/pfortainer/app \
            /zdata/jails/pfortainer/zdata \
            /zdata/jails/pfortainer/zboot
   ```

3. ZFS 권한 위임 (파일시스템/파일매니저에서 ZFS 조회 가능):
   ```sh
   zfs allow -u root mount,create,destroy,snapshot,rollback zdata
   zfs allow -u root mount,create,destroy,snapshot,rollback zboot
   ```
   `jail.conf.d/pfortainer.conf`에 `exec.poststart`로 `zfs jail`을 추가해야 재부팅 후에도 유지됩니다. 새 ZFS 풀 추가 시 `zfs allow`와 `exec.poststart` 줄을 동일하게 추가하세요.

4. 호스트 rc 서비스 설치 및 rc.conf 설정:
   ```sh
   cp deploy/freebsd/rc.d/podman_api /usr/local/etc/rc.d/
   cp deploy/freebsd/rc.d/pfortainer_hostd /usr/local/etc/rc.d/
   chmod +x /usr/local/etc/rc.d/podman_api
   chmod +x /usr/local/etc/rc.d/pfortainer_hostd
   sysrc podman_api_enable=YES
   sysrc pfortainer_hostd_enable=YES
   sysrc jail_enable=YES
   sysrc jail_list=pfortainer
   sysrc pfortainer_enable=NO   # 호스트 직접 실행 비활성화 (Jail에서 실행)
   ```

   `pfortainer_hostd`는 Jail보다 먼저 기동(`BEFORE: jail`)되어 `/run/pfortainer/host.sock`을 생성합니다. Jail 내부의 pfortainer는 이 소켓을 통해 호스트의 권한 명령을 위임 실행합니다.

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

7. `pf.conf`에 포트 포워딩 추가:

   pfortainer는 Jail(`192.168.10.111`)에서 실행되므로, 호스트 IP 및 Tailscale IP로 접근하려면 pf 포워딩이 필요합니다.

   ```sh
   # /etc/pf.conf (rdr-anchor 위에 추가)
   ts_if=tailscale0
   ext_if=igc3
   # Tailscale → Jail  ($ts_if 인터페이스 IP를 런타임에 동적 참조)
   rdr pass on $ts_if proto tcp from any to ($ts_if) port 11000 -> 192.168.10.111 port 11000
   # 호스트 LAN IP → Jail
   rdr pass on $ext_if proto tcp from any to ($ext_if) port 11000 -> 192.168.10.111 port 11000
   ```
   ```sh
   pfctl -f /etc/pf.conf
   ```

8. 서비스 시작:
   ```sh
   service podman_api start
   service pfortainer_hostd start
   service jail start pfortainer
   ```

### 로컬에서 접속 (SSH 포트 포워딩)

pfortainer는 Jail(`192.168.10.111`)에서 실행되므로, 로컬 Mac에서 접속할 때는 Jail IP를 지정해야 합니다:

```sh
ssh -L 11000:192.168.10.111:11000 fbnas
```

그 후 브라우저에서 `http://localhost:11000` 접속.

> 호스트 IP(`$ext_if` 주소)로 포워딩하면 ERR_CONNECTION_RESET 발생 — 반드시 Jail IP를 지정할 것.

### 로그 확인

```sh
# Jail 내부 pfortainer 로그
ssh fbnas "sudo jexec pfortainer tail -f /var/log/pfortainer.log"

# host agent 로그
ssh fbnas "sudo tail -f /var/log/pfortainer_hostd.log"

# Jail 상태
ssh fbnas "jls -v"
```

## 문제 해결

**부팅 직후 "Podman 소켓에 연결할 수 없습니다" 페이지**

pfortainer는 소켓 없이도 즉시 기동되며, `podman_api` 서비스가 소켓을 생성하면 자동으로 연결됩니다. 페이지의 새로고침 버튼을 누르거나 잠시 후 재접속하면 정상 동작합니다.

**네트워크/서비스/스토리지 페이지에서 "404 page not found" 또는 "host agent unavailable"**

`pfortainer_hostd`가 구버전 바이너리로 실행 중이거나 중지된 경우. `deploy.sh`를 다시 실행하면 자동으로 재시작됩니다. 수동으로 재시작하려면:
```sh
ssh fbnas "sudo service pfortainer_hostd onerestart"
```

**컨테이너 목록/상세에서 nil pointer panic**

Podman 5.8.1/FreeBSD의 Docker 호환 API(`/containers/json`, `/containers/{id}/json`)가 panic을 일으키는 버그가 있다. 이를 피하기 위해 목록·상세 조회는 네이티브 libpod API(`/v5.0.0/libpod/...`)를 사용한다. 상세 조회에서 5xx가 발생하면 목록 데이터로 대체 렌더링하며 페이지 상단에 경고 배너를 표시한다.

Podman 버전 업그레이드 후 같은 증상이 재발하면 libpod API 응답 형식 변경 여부를 확인:
```sh
curl --unix-socket /run/podman/podman.sock \
  http://d/v5.0.0/libpod/containers/json?all=true
```
