# 전환 절차 (fbnas, pfortainer Rust → Go)

단계 A = 앱(jail `pfortainer`), 단계 B = hostd(호스트 root). 한 번에 하나만 바꾼다. 모두 실행 전에 사용자 승인.
피할 시간대: 00:00 전후(zfs-backup cron, sanoid/syncoid), 23:00 전후(audio backup-sync).

## 준비 상태 (2026-11-19 15:30 완료, 운영 무변화)
- Go 체크아웃: fbnas `/zdata/apps/pfortainer` (mohicann/pfortainer go2, 읽기 전용 deploy key `github.com-pfortainer-go`, id 170407130)
- jail 마운트: `/zdata/apps/pfortainer → /opt/pfortainer` (appjail fstab #3 + 지금 수동 nullfs)
- jail에 go 1.26.11 설치, `/opt/pfortainer/bin/pfortainer` 빌드됨(99f6d5d, CGO=1, 약 28초)
  - 빌드 명령: `appjail cmd jexec pfortainer sh -c "cd /opt/pfortainer && env GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=safe.directory GIT_CONFIG_VALUE_0='*' go build -o bin/pfortainer ./cmd/pfortainer"`
- jail rc.d 스위치 설치(pfortainer-rs 74e24b2): `/usr/local/etc/pfortainer.bin`이 없으면 Rust. 이전 rc.d는 fbnas `/tmp/pfortainer.rc.pre-go`
- jail에서 빌드한 Go 바이너리로 섀도 재확인: 71경로 차이 0

## 단계 A — 앱 전환
1. 기준선
   - `zfs snapshot zdata/apps@pre-go-pf-YYYYMMDD` (config.db·metrics.db 포함)
   - Rust 바이너리 보관: `cp -p /zdata/apps/pfortainer-rs/target/release/pfortainer /zdata/apps/pfortainer-rs/target/release/pfortainer.rust-pre-go`
     (hostd도 같은 파일을 쓰므로 단계 B 전까지 Rust 쪽 빌드는 하지 않는다)
2. 전환
   - `J="appjail cmd jexec pfortainer"; $J sh -c 'echo /opt/pfortainer/bin/pfortainer > /usr/local/etc/pfortainer.bin && service pfortainer restart'`
3. 확인
   - `ps`로 실행 바이너리가 `/opt/pfortainer/bin/pfortainer`인지, `/var/log/pfortainer.log`에 ERROR 없는지
   - 다른 기기에서 `/login` 200, 익명 `/api/files/list` 302
   - 사용자 확인: 로그인, 데스크탑, AppJail 목록·상세, 스토리지, 파일 관리자, 메트릭 그래프, 알림 설정 화면
   - 메트릭 수집이 끊기지 않는지(5초 간격 metrics.db 기록), hostd 연결 정상
4. 관찰 3일: 5분 알림 점검, 스케줄러(쓰는 스케줄 없음), RSS, healthcheck 재시작 여부(`appjail healthcheck` 로그)
- **롤백**: `$J sh -c 'rm /usr/local/etc/pfortainer.bin && service pfortainer restart'` (DB 스키마 변화 없음, Rust가 그대로 읽음 — P7)

## 단계 B — hostd 전환 (A 관찰 뒤, 별도 승인)
- 호스트 rc.d `pfortainer_hostd`에도 같은 스위치를 둔다(`pfortainer_hostd_bin`을 `/zdata/apps/pfortainer/bin/pfortainer`로).
  부팅 경로(BEFORE: appjail)라 재부팅 영향 명시 후 동의받기. 재부팅 검증은 사용자 동의 시.
- 롤백: 스위치 제거 + `service pfortainer_hostd restart`.

## 정리 (B 뒤 2주, 확인받고)
- Rust 바이너리·`.rust-pre-go`·`/tmp/pfortainer.rc.pre-go` 삭제, jail에서 rust 패키지 제거 여부, pfortainer-rs 저장소 archive, 옛 deploy key 정리
- go2를 main으로 병합

## 단계 A 실행 기록
- **2026-11-19 15:43 fbnas 앱이 Go(99f6d5d, `/opt/pfortainer/bin/pfortainer`)로 운영 중.**
- 기준선: 스냅샷 `zdata/apps@pre-go-pf-20261119-1543`, Rust 바이너리 `target/release/pfortainer.rust-pre-go`(sha256 4e92835b…, 원본과 같음)
- 확인: 재시작 직후 RSS 18.8MB(Rust 22.8MB), 로그 ERROR 0(로그의 WARN 3줄은 11-17 Rust 때 것), 메트릭 WAL 5초마다 갱신,
  외부(Mac)에서 `/login` 200·익명 `/api/files/list` 302·`/nope` 404, appjail healthcheck healthy
- 롤백: `appjail cmd jexec pfortainer sh -c 'rm /usr/local/etc/pfortainer.bin && service pfortainer restart'`

| 시각 | RSS | ERROR | 메모 |
|---|---|---|---|
| 11-19 15:44 | 18.8MB | 0 | 전환 직후 |
| 11-19 사용자 확인 | — | 0 | 로그인한 화면 정상(사용자 브라우저 확인) |

## 단계 B 실행 기록 (관찰 생략 — 사용자 결정: 회원 서비스가 아니라서 바로 전환)
- **2026-11-19 15:48 fbnas hostd가 Go(`/zdata/apps/pfortainer/bin/pfortainer -hostd`)로 운영 중.** 앱·hostd 모두 Go.
- 호스트 rc.d `pfortainer_hostd`에 스위치(`/usr/local/etc/pfortainer_hostd.bin`, pfortainer-rs dca2621), 이전 rc.d `/tmp/pfortainer_hostd.rc.pre-go`
- 확인: RSS 17.8MB(Rust 29.5MB), 소켓 재생성·jail에서 보임, zpool-list·appjail/list·smb/status·fs/stat·metrics/host 200,
  앱 로그인 200·앱 ERROR 0·메트릭 5초 기록 계속
- 롤백: `rm /usr/local/etc/pfortainer_hostd.bin && service pfortainer_hostd restart` (Rust 바이너리 `.rust-pre-go`도 보관)
- 재부팅 검증은 하지 않음(부팅 경로: 스위치 파일이 가리키는 Go 바이너리가 없으면 prestart가 실패해 hostd만 안 뜸 — 부팅 자체엔 영향 없음)

## 정리 실행 기록 (2026-11-19 15:50, 사용자 지시로 즉시)
- 배포 구성을 이 저장소 `deploy/appjail/`로 옮김(Go가 기본, 스위치 제거) — fe4f378. fbnas 체크아웃은 main.
- 데이터·.env를 `/zdata/apps/pfortainer/{data,.env}`로 이동(같은 데이터셋 안 mv), .env 경로를 `/opt/pfortainer/data/…`로
- jail·호스트 rc.d를 Go 기본판으로 교체, 스위치 파일 두 개 삭제, 앱·hostd 재시작
- jail fstab에서 `/zdata/apps/pfortainer-rs` 제거·umount, `/zdata/apps/pfortainer-rs`(Rust 체크아웃·바이너리·`.rust-pre-go`) 삭제,
  jail에서 rust 패키지·`/root/.cargo` 삭제, `/tmp` rc.d 백업 삭제
- GitHub: mohicann/pfortainer-rs README에 보관 안내 후 **archive**, deploy key 2개 삭제(bastille·appjail), fbnas의 옛 키·SSH 별칭 삭제
- 확인: 외부 `/login` 200·익명 API 302, 앱 ERROR 0, 메트릭 기록 계속, healthcheck healthy, RSS 앱 24.3MB·hostd 22.0MB
- 남긴 것: 스냅샷 `zdata/apps@pre-go-pf-20261119-1543`(Rust 체크아웃·데이터 그대로 들어 있음 — 되돌릴 유일한 경로), `data/config.db.bak-20261118-112255`(사용자가 나중에 지우기로 한 것)

## 재부팅 검증 (2026-11-19 16:02, 사용자 요청)
- `shutdown -r now` 16:02:55 → SSH 16:04:20(약 85초) → pfortainer `/login` 16:04:48 응답
- hostd(Go)가 jail보다 먼저 떠 소켓 재생성(16:04:23), jail 안에서 hostd·podman 소켓 보임, 앱(Go) 메트릭 360개 복원·기록 재개, ERROR 0
- LAN·tailnet 11000 200, 익명 API 302, appjail healthcheck healthy(16:07, start_period 뒤). audio 8080·cloudreve 3040·immich 2283 모두 200, 풀 3개 ONLINE
