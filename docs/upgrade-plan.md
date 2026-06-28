# pfortainer → FreeBSD Total NAS 솔루션 업그레이드 계획

> 작성일: 2026-06-28
> 범위: xigmanas(FreeBSD/PHP) · TrueNAS(CORE/SCALE) 분석 기반 pfortainer 기능 포팅 로드맵

---

## 1. 요약 (Executive Summary)

pfortainer는 현재 **Podman 컨테이너 관리 + ZFS 데이터셋 + 파일매니저 + 메트릭**을 제공하는
경량 Go SSR 웹앱이다(소스 ~3,400 LOC, 단일 패키지). 목표는 이를 xigmanas/TrueNAS 수준의
**FreeBSD 기반 종합 NAS 솔루션**으로 확장하는 것이다.

핵심 결론은 세 가지다.

1. **가장 큰 제약은 기능이 아니라 권한 경계다.** pfortainer는 지금 unprivileged Jail 안에서
   돌아가며, 호스트에는 `podman.sock`(rw)과 `/app`(ro)만 마운트되어 있다. NAS 핵심 기능
   (ZFS 풀/디스크/SMART, 네트워크 인터페이스, 호스트 서비스 daemon, 사용자 계정)은 대부분
   **호스트 root 권한**을 요구한다. 따라서 단순히 핸들러를 추가하는 문제가 아니라
   **권한 아키텍처를 먼저 설계**해야 한다.
2. **권장 아키텍처는 "Jail UI + 호스트 특권 에이전트" 분리 모델**이다. TrueNAS의 middleware
   구조와 동일한 발상으로, UI는 Jail에 두고 특권 작업은 호스트의 작은 Go 데몬(`pfortainer-agent`)이
   로컬 소켓으로 대행한다. 이는 최근 커밋(`호스트 rc.conf에서 pfortainer 직접 실행 비활성화`)으로
   확정한 Jail 격리 원칙을 깨지 않으면서 전 기능을 가능하게 한다.
3. **pfortainer는 "전체 config 소유자"가 되어선 안 된다.** xigmanas/TrueNAS는 `config.xml` /
   middleware DB가 OS 설정 전체를 소유하고 `rc.conf`·`smb4.conf` 등을 통째로 재생성한다.
   이는 강력하지만, 사용자가 수동 관리하는 기존 FreeBSD 호스트에는 침습적이고 부팅 홀딩
   리스크가 크다. pfortainer는 **drop-in 조각 설정**(`rc.conf.d/`, `*.conf.d/`) 방식으로
   기존 운영과 공존하는 "관리 패널"로 남는 것을 권장한다.

---

## 2. 현재 pfortainer 역량 평가

| 영역 | 현재 구현 | 파일 | 비고 |
|---|---|---|---|
| 컨테이너 | 목록/상세/start·stop·kill·pause·resume·remove | `podman.go`, `handlers.go` | libpod 네이티브 API |
| 이미지 | 목록/상세/remove | `handlers.go` | Docker 호환 API |
| ZFS | 풀/데이터셋 조회, 데이터셋 생성/삭제 | `zfs.go` | `zfs` 바이너리 호출 (Jail 위임) |
| 파일매니저 | 전체 CRUD, 업로드/다운로드, chmod, clip/paste, 편집 | `filemanager.go` | path traversal 방어 있음 |
| 서비스 | listening 소켓 ↔ Jail/컨테이너 매핑 조회 | `services.go` | **읽기 전용** |
| 메트릭 | CPU/MEM/Load/Net/Disk/ARC/TCP/Pool, SQLite 보존, netdata 스타일 차트 | `metrics.go` | 자체 수집기 |
| 인증 | HMAC-SHA256 서명 쿠키, 8h | `session.go` | 단일 사용자, RBAC 없음 |

**강점:** 단일 바이너리, 가볍고 빠름, Podman-네이티브, FreeBSD 친화적 메트릭.
**한계:** 단일 사용자, 쓰기 가능한 시스템 제어 거의 없음, 호스트 권한 부재.

---

## 3. xigmanas / TrueNAS 분석

### 3.1 xigmanas (FreeBSD, PHP, ~283 페이지)

xigmanas는 `config.xml` 단일 소스를 중심으로 PHP가 OS 설정 파일을 재생성하는 구조다.
기능 영역(실제 `www/*.php` 인벤토리 기준):

- **disks (59):** ZFS 풀/데이터셋/볼륨(zvol)/스냅샷/클론, 스크럽·스냅샷 스케줄러,
  SMART 관리, 디스크 init/format/mount, GEOM RAID(mirror/stripe/raid5/concat/vinum),
  geli 암호화, ZFS 설정 동기화(`zfs_config_sync`)
- **services (87):** SMB/CIFS(samba), Samba AD, NFS, FTP, rsync(daemon/client/local),
  iSCSI(ctld/ctl + istgt), AFP, WebDAV, SSHd, TFTP, SNMP, UPS(NUT), Dynamic DNS(inadyn),
  miniDLNA, DAAP, BitTorrent, Syncthing, Unison, MariaDB, LCDproc, HAST, WSD
- **interfaces (13):** LAN/OPT 할당, VLAN, LAGG, bridge, CARP(고가용성), wireless
- **access (9):** 로컬 users/groups, AD, LDAP, SSH 공개키
- **system (43):** 일반/고급 설정, users 비밀번호, email + 리포트, cron, rc.conf 편집,
  loader.conf, sysctl 튜너블, swap, 방화벽, 부트환경(BE), 펌웨어 업데이트,
  config 백업/복원, syslog, 프록시, 예약 재부팅
- **diag (26):** 로그, SMART/디스크/마운트/netstat/socket/space/swap/UPS/AD 정보, arp, ping, traceroute
- **status (22):** 시스템/서비스/디스크/프로세스/인터페이스 상태, RRD 그래프(CPU/메모리/네트워크/load/ZFS ARC·L2ARC/UPS/uptime/latency)
- **vm (4):** VirtualBox / Xen 게스트

### 3.2 TrueNAS (CORE = FreeBSD, SCALE = Linux)

- **아키텍처 핵심:** `middleware` (Python asyncio 데몬)가 모든 특권 작업을 수행하고,
  **REST + WebSocket(JSON-RPC) API**를 노출한다. 웹 UI는 그 API의 얇은 클라이언트.
  설정은 SQLite DB(Django ORM)에 저장, 서비스 설정 파일을 렌더링.
- **CORE(FreeBSD) 기능:** ZFS 전반, 공유(SMB/NFS/iSCSI/WebDAV/FTP/rsync/S3),
  주기 스냅샷 태스크 + **ZFS replication**(로컬/원격, SSH+netcat), **cloud sync(rclone)**,
  SMART 테스트 스케줄, scrub 태스크, snapshot 보존정책, plugins(iocage jail), VM(bhyve),
  사용자/그룹 + 디렉터리 서비스(AD/LDAP/Kerberos/NIS), 알림(alert) 시스템,
  리포팅(netdata), 2FA, API 키, 감사 로그.
- **pfortainer 관점에서 가장 가치 있는 패턴:**
  1. **middleware = 특권 에이전트 분리** (→ 우리의 host agent 모델 근거)
  2. **선언적 태스크 스케줄러** (스냅샷/스크럽/SMART/replication/cloud sync를 동일한 "task" 추상화로)
  3. **알림 프레임워크** (디스크 degrade/SMART 실패/풀 용량/스크럽 결과 → email/webhook)
  4. **ZFS replication & cloud sync** — 백업이 NAS의 핵심 가치

### 3.3 두 시스템의 공통 교훈

- 둘 다 **"설정 DB → 파일 렌더링 → 서비스 reload"** 파이프라인을 갖는다.
- 둘 다 NAS 가치의 핵심은 **(a) ZFS 무결성/스냅샷/복제 (b) 파일 공유 (c) 모니터링·알림**이다.
  화려한 부가 서비스(BitTorrent, DAAP 등)는 후순위이며, pfortainer는 이를 **컨테이너로** 제공해
  호스트를 깨끗하게 유지할 수 있다(차별점).

---

## 4. 기능 격차 매트릭스

| 기능 | xigmanas | TrueNAS | pfortainer 현재 | 권한 필요 | 우선순위 |
|---|---|---|---|---|---|
| ZFS 풀/vdev 관리 (생성·확장·교체) | ✅ | ✅ | ❌ | 호스트 | **P1** |
| ZFS 스냅샷/롤백/클론 | ✅ | ✅ | ❌ | 위임 가능 | **P1** |
| 스냅샷/스크럽 스케줄러 | ✅ | ✅ | ❌ | 호스트 | **P1** |
| ZFS replication (백업) | △ | ✅ | ❌ | 호스트 | **P2** |
| Cloud sync (rclone) | △ | ✅ | ❌ | 컨테이너화 가능 | P2 |
| SMART 모니터링/테스트 | ✅ | ✅ | ❌ | 호스트 | **P1** |
| 디스크 관리/인벤토리 | ✅ | ✅ | 부분(메트릭) | 호스트 | **P1** |
| SMB 공유 | ✅ | ✅ | ❌ | 호스트 or 컨테이너 | **P1** |
| NFS 공유 | ✅ | ✅ | ❌ | 호스트 | **P1** |
| iSCSI 타깃 | ✅ | ✅ | ❌ | 호스트 | P3 |
| FTP/rsync/WebDAV | ✅ | ✅ | ❌ | 컨테이너화 가능 | P2 |
| 로컬 users/groups | ✅ | ✅ | ❌(단일 admin) | 호스트(공유용) | **P1** |
| AD/LDAP 연동 | ✅ | ✅ | ❌ | 호스트 | P3 |
| 네트워크 인터페이스/VLAN/LAGG/bridge | ✅ | ✅ | ❌ | 호스트 | P2 |
| 방화벽 | ✅ | △ | ❌ | 호스트 | P3 |
| 알림(email/webhook) | ✅ | ✅ | ❌ | Jail 가능 | **P1** |
| config 백업/복원 | ✅ | ✅ | 부분(.env) | Jail 가능 | P2 |
| 예약 작업(cron) UI | ✅ | ✅ | ❌ | 호스트 | P2 |
| 시스템 튜너블/loader.conf | ✅ | ✅ | ❌ | 호스트 | P3 |
| 로그 뷰어/진단 | ✅ | ✅ | ❌ | Jail 부분 | P2 |
| VM(bhyve) | ✅ | ✅ | ❌ | 호스트 | P3 |
| 컨테이너/앱 | ❌ | ✅(SCALE) | ✅ | 보유 | ✅ |
| RBAC/다중 사용자/2FA | △ | ✅ | ❌ | Jail 가능 | P2 |
| REST/WS API | ❌ | ✅ | 부분(내부) | Jail 가능 | P2 |

凡例: ✅완비 △부분 ❌없음

---

## 5. 핵심 아키텍처 결정 (가장 중요)

### 결정 1 — 권한 모델: **Jail UI + 호스트 특권 에이전트 분리**

```
FreeBSD 호스트 (root)
├── pfortainer-agent (신규, Go, 작은 특권 데몬)
│     - /var/run/pfortainer-agent.sock (unix, root 소유, 0600)
│     - 화이트리스트된 특권 작업만 수행:
│         zpool/zfs, smartctl, ifconfig, service(8),
│         pw(8) 사용자, smb4.conf.d 렌더 + service samba_server reload …
│     - 모든 작업을 JSON 요청 → 구조화 응답 + audit 로그
│
└── Jail: pfortainer (기존)
      ├── /app (ro)           기존
      ├── /run/podman (rw)    기존
      └── /run/pfortainer-agent.sock (rw nullfs)  ← 신규 마운트
            └── pfortainer UI가 특권 작업을 에이전트에 위임
```

근거:
- TrueNAS middleware와 동일한 검증된 패턴(얇은 UI + 특권 데몬).
- 최근 확정한 **Jail 격리 + 호스트 직접 실행 금지** 원칙([[project_jail_migration]])과 충돌하지 않음.
- 공격 표면을 **화이트리스트 명령 집합**으로 한정 → unrestricted shell보다 안전.
- 에이전트는 단일 정적 Go 바이너리로 배포(빌드는 로컬에서, 호스트엔 바이너리만 — [[feedback_dev_prod_separation]]).

대안 비교:
- **(B) 호스트에서 직접 실행:** 가장 단순하나 Jail 격리 폐기 → 최근 작업과 정면 배치, 기각.
- **(C) Jail 위임만 확장:** ZFS는 위임 가능하나 네트워크/호스트 서비스/SMART는 불가 → 부분만 가능, 보조 수단.

### 결정 2 — 설정 소유권: **fragment(drop-in) 방식, 전체 재생성 금지**

- `rc.conf` 통째 재작성 ❌ → `/etc/rc.conf.d/<service>` 조각만 관리 ✅
- `smb4.conf`를 소유 ❌ → `include` 디렉터리(`smb4.conf.d/`)에 공유별 조각 생성 ✅
- 이유: 부팅 홀딩/서비스 의존성 사고 방지([[feedback_system_ops]]), 수동 관리와 공존, 롤백 단순.
- 모든 변경 전 **자동 백업 + diff 미리보기 + 적용/롤백** UX 제공(xigmanas `changes.php` 패턴).

### 결정 3 — 서비스 제공 방식: **호스트 vs 컨테이너 이원화**

- **호스트 필수**(커널/디스크/네트워크 밀착): ZFS, SMART, SMB(커널 zfs ACL 통합), NFS, iSCSI, 네트워크.
- **컨테이너 권장**(pfortainer 강점 활용, 호스트 청결 유지): WebDAV, FTP, rsync, S3(MinIO),
  miniDLNA, Syncthing, cloud sync(rclone). → "앱 카탈로그"로 원클릭 배포.
- 이 이원화가 xigmanas/TrueNAS 대비 pfortainer의 **차별점**(호스트에 서비스 난립 없음).

### 결정 4 — 데이터 모델: 경량 설정 스토어 도입

- 현재 `.env` + Podman/ZFS 실시간 조회만으로는 "공유/스케줄/알림" 같은 선언적 상태를 담을 수 없음.
- TrueNAS처럼 무거운 ORM 대신 **단일 SQLite**(이미 metrics.db로 SQLite 사용 중) 또는
  버전 관리되는 단일 `config.json`을 도입. 스키마: shares, schedules, alerts, users, datasets-meta.
- config 백업/복원 = 이 파일 export/import (xigmanas `system_backup` 대응).

---

## 6. 단계별 로드맵

### Phase 0 — 기반 작업 (선행 필수)
- [x] **호스트 특권 에이전트** — 이미 존재했음(`hostd.go`, `pfortainer -hostd`,
      `/run/pfortainer/host.sock`). `/sockstat`·`/jls`·`/ps`·`/compose-up` 제공,
      `hostGet()` 소켓-or-exec 폴백 패턴. → 별도 `pfortainer-agent` 신설 불필요, 이 위에 확장.
- [x] **특권 읽기 경로 PoC 완료** (2026-06-28) — agent에 `/zpool-status`·`/zpool-list`·
      `/smart-scan`·`/smart?dev=` 읽기 엔드포인트 추가(`hostd.go`), audit 로그(`auditLog`) +
      디바이스 화이트리스트(`smartDevRe`) 적용. 데이터층 `storage.go`(zpool status/SMART 파서),
      `/storage` "스토리지 상태" 페이지(풀 health + 디스크 SMART) 추가. 파서 4종 유닛검증 통과,
      로컬 end-to-end 렌더링 확인(local-exec 폴백 시 경고 배너 정상). → **보고서가 지목한
      "agent 경유 zpool status/smartctl 왕복" PoC 기준 충족.**
- [ ] 설정 스토어(SQLite 또는 config.json) + 백업/복원/롤백 프레임워크  ← 다음
- [ ] RBAC 토대: 사용자 테이블, 역할(admin/operator/viewer), 세션 확장(2FA는 P2)  ← 다음

**Phase 0 운영/보안 후속 메모:**
- 호스트에 **smartmontools 설치 필요**(`pkg install smartmontools`)해야 `/storage`의 SMART 표시.
  `zpool`은 base 포함. `pfortainer_hostd` rc 서비스가 떠 있어야 Jail에서 호출 가능.
- **소켓 권한 점검 권고:** 현재 `hostdSockPath`를 `0666`(world-writable)으로 chmod 중
  (`hostd.go`). 이 소켓은 호스트 root 권한 통로이므로 호출자 식별 후 `0660`(root:사용그룹)으로
  좁히는 것을 권장. 단, 현재 배포의 Jail 호출 주체 권한 확인 전 변경 시 기능 중단 위험 →
  배포 환경에서 검증 후 적용(이번 변경에는 미포함).

### Phase 1 — NAS 코어 (ZFS · 디스크 · 공유 · 알림)
스토리지 무결성과 파일 공유 — NAS의 본질.
- [ ] **ZFS 확장:** 풀 상태/health, vdev 트리, 스냅샷 생성/롤백/삭제/클론, 데이터셋 속성 편집(compression/quota/recordsize), zvol
- [ ] **스케줄러 추상화:** 스냅샷 자동(주기+보존정책), scrub 예약 → cron.d 조각 또는 agent 내부 타이머
- [ ] **SMART:** 디스크 인벤토리(`camcontrol`/`smartctl -a`), 상태 배지, 단기/장기 테스트 실행·예약
- [ ] **SMB 공유:** `smb4.conf.d` 조각 생성, 공유 CRUD, 사용자/권한 매핑, reload
- [ ] **NFS export:** `/etc/exports` 조각 또는 exports.d, 공유 CRUD
- [ ] **로컬 users/groups:** 공유 인증용 `pw` 사용자 관리(agent 경유)
- [ ] **알림 프레임워크:** 풀 degrade/SMART 실패/스크럽 결과/용량 임계 → email(SMTP)/webhook

### Phase 2 — 백업 · 네트워크 · 운영성
- [ ] **ZFS replication:** 로컬/원격(SSH) send/recv 태스크, 증분, 보존
- [ ] **Cloud sync:** rclone 컨테이너 + 잡 정의 UI
- [ ] **컨테이너화 공유 서비스:** WebDAV/FTP/rsync/MinIO 앱 카탈로그(원클릭)
- [ ] **네트워크:** 인터페이스/VLAN/LAGG/bridge 조회 + 편집(rc.conf.d 조각, 신중 적용)
- [ ] **예약 작업 UI(cron):** agent 경유 crontab.d 관리
- [ ] **로그 뷰어/진단:** 시스템 로그 tail, netstat/arp/mount/space 진단 페이지
- [ ] **2FA(TOTP) + API 키**, REST/WS API 정식화

### Phase 3 — 고급/선택
- [ ] iSCSI 타깃(ctld), AFP, WebDAV(호스트)
- [ ] AD/LDAP 디렉터리 서비스 연동
- [ ] 방화벽(pf) 규칙, sysctl/loader.conf 튜너블 편집(부트환경 백업 연계)
- [ ] bhyve VM 관리
- [ ] 부트환경(BE) 관리, 펌웨어/OS 업데이트 패널

---

## 7. 구현 노트 (기존 코드 재사용 관점)

- **라우팅:** 현재 Go 1.22 `mux.Handle("GET /...")` 패턴 그대로 확장. 사이드바는 `base.html`의
  `ActivePage` 규약에 메뉴 그룹(스토리지/공유/네트워크/시스템/앱) 추가.
- **VM 패턴 유지:** API → 구조체 → VM → 템플릿 흐름을 신규 페이지에도 동일 적용.
- **zfs.go 확장:** 현재 `zfs` 바이너리 직접 호출 → 특권 작업은 agent 경유로 리팩터. 조회는 위임으로 Jail 내 직접 유지 가능.
- **metrics.go 활용:** 알림 임계 판정에 기존 메트릭 수집기 재사용(풀 용량/ARC 등 이미 수집 중).
- **services.go 진화:** 현재 읽기 전용 소켓 매핑 → "관리되는 서비스" 상태/제어 패널로 확장.
- **filemanager.go 보안 패턴:** 이미 구현된 path-traversal 방어(`safePath`)를 공유 경로 선택 UI에 재사용.

## 8. 리스크 및 운영 고려사항

- **부팅 홀딩:** 네트워크/rc.conf 변경은 잘못되면 부팅·원격접속 차단. → 모든 적용은 백업+롤백 타이머
  (적용 후 N분 내 confirm 없으면 자동 복원) 패턴 권장.
- **권한 상승 표면:** agent는 NAS 전체의 root 권한 통로. 화이트리스트·인자 검증·audit 로그·
  소켓 권한(0600) 필수. 임의 셸/임의 경로 금지.
- **dev/prod 분리:** agent도 로컬 크로스컴파일 → 바이너리만 fbnas 배포([[feedback_dev_prod_separation]]).
- **서비스 의존성:** `podman_api` → `pfortainer-agent` → Jail `pfortainer` 순서 정의 필요.
- **데이터 안전:** ZFS 파괴적 작업(풀 삭제/디스크 교체)은 확인 단계·드라이런 우선.
- **Samba/ZFS ACL:** SMB는 호스트 ZFS와 밀착(ACL/VFS) → 컨테이너보다 호스트 권장(결정 3).

## 9. 권고 (Next Steps)

1. **Phase 0의 `pfortainer-agent` PoC**를 먼저 만든다 — 이것이 전체 로드맵의 병목이자 전제.
   최소 기능: 소켓 + `zpool status`/`smartctl -a` 읽기 작업 2개만 agent 경유로 왕복 검증.
2. PoC 성공 시 **Phase 1을 "ZFS 스냅샷 스케줄러 + SMART + SMB 공유 + 알림"** 4종으로 잡아
   "백업되는 파일서버"라는 NAS 최소 가치를 완성한다.
3. 부가 서비스는 **앱 카탈로그(컨테이너)**로 흡수하여 호스트를 깨끗하게 유지한다(차별화).

> 한 줄 요약: **"middleware(=agent) 분리 + fragment 설정 + 컨테이너 앱 카탈로그"** 세 축으로
> Jail 격리를 유지한 채 TrueNAS급 NAS 기능을 점진 포팅한다.
