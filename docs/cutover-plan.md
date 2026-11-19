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
