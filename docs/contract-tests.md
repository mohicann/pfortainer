# 계약 테스트 (pfortainer-rs → Go)

> 목적: 운영 중인 Rust 구현(`~/git/pfortainer-rs`)의 **밖에서 보이는 동작**을 블랙박스 테스트로 고정하고,
> Go 구현의 완료 기준으로 쓴다. "Rust에서 녹색인 테스트를 Go에서도 녹색으로."
> 계획 전체는 [go-migration-plan.md](go-migration-plan.md).

## 실행

```sh
# Rust 빌드(테스트 훅 포함, 451a6bc 이후)
(cd ../pfortainer-rs && cargo build --release)   # 골든은 인증 수정(fix(auth)) 이후 기준

go test ./contract/...                       # SUT = Rust (기본)
go test ./contract/... -update               # 골든 다시 기록 — SUT가 Rust일 때만 허용
SUT_BIN=$PWD/bin/pfortainer go test ./contract/...                 # Go 구현 검증
SUT_BIN=$PWD/bin/pfortainer OTHER_BIN=../pfortainer-rs/target/release/pfortainer \
  go test ./contract/tests -run TestInterop                        # 교차 호환(P7)
DISCOVER=1 go test ./contract/tests -run 'TestDiscover' -v          # (도구) 앱→hostd·hostd→명령 목록
```

전체 약 25초(macOS, 라우트별 앱을 병렬로 띄우는 권한표 포함). 3회 반복 녹색, 하네스는 `-race` 녹색.

## 구조

```
contract/
  harness/   app.go(앱 기동) hostd.go(hostd 기동·shim) fakesock.go(가짜 hostd/Podman Unix 소켓, 요청 기록)
             client.go auth.go(쿠키 서명·TOTP·bcrypt 사용자) golden.go normalize.go dbcheck.go
             fixtures.go(capture/) cmdfix.go(cmd/) stubs.go(웹훅 스텁, HTTP_PROXY 겸용) shim/(가짜 명령)
  fixtures/  capture/  fbnas 운영 hostd·Podman 읽기 전용 GET 응답(2026-11-19, 비밀값 치환)
             cmd/      fbnas 읽기 전용 명령 출력 + /etc/passwd 등 호스트 파일
  testdata/golden/p0..p7/
  tests/     routes.go(라우트 목록 = 권한표 원천) p0..p7_test.go
```

- 앱은 짧은 `/tmp/pfcXXXX` 작업 디렉터리(Unix 소켓 104바이트 제한)에서 `APP_ENV=test`로 뜬다.
  가짜 hostd·Podman 소켓을 붙이고, 메트릭 수집 스레드·스케줄러는 끈다(P5 백그라운드 테스트만 켬).
- hostd는 `PATH=shim 디렉터리`로 뜬다. 모든 명령은 shim(심볼릭 링크)이 `rules.json`대로 응답하고 호출(인자·stdin)을 남긴다.
  고정 절대경로(/etc/passwd, smb4.conf, exports.d, appjail, /var/log …)는 `TEST_ROOT` 아래로 간다.
- 골든은 응답(상태·주요 헤더·바디) + 앱이 hostd/Podman에 보낸 요청(정렬) + 필요한 DB 행.

## Rust 테스트 훅 (`src/testhooks.rs`, APP_ENV=test 전용)

| env | 용도 |
|---|---|
| `TEST_ROOT` | hostd·앱의 고정 절대경로 접두어 |
| `TEST_DISABLE_METRICS=1` | 메트릭 수집 스레드 안 띄움(ring·DB 조회는 그대로) |
| `TEST_DISABLE_SCHEDULER=1` | 스케줄러 루프 안 띄움 |
| `TEST_SCHED_INTERVALS=s,a,r` | 스케줄·알림·복제 주기(초) |

Go 구현도 같은 env를 같은 뜻으로 받아야 한다.

## 비교에서 뺀 것 / 정규화 (Go도 이 범위 안에서만 달라도 됨)

| 항목 | 이유 | 처리 |
|---|---|---|
| HTML 공백 | askama ↔ html/template | 태그 사이 공백 제거, 연속 공백 1칸(`NormHTML`) |
| 컨테이너 라벨 표시 순서 | Rust HashMap 순회 | 인접 라벨 div 정렬 |
| SMART 디스크 행·옵션 순서 | `buffer_unordered` 완료 순 | 인접 행 정렬 |
| hostd `metrics/host` net 키 순서, tcp_estab 값 | HashMap, 실행 호스트 값 | JSON 재직렬화, 값 마스킹 |
| compose 출력의 stdout/stderr 줄 순서 | 동시 읽기 | log 줄 정렬 |
| 병렬 hostd/Podman 호출 순서 | 동시 요청 | 기록 요청 정렬 |
| 지금 시각에서 나온 값 | 실행 시각 | `<now>`, `next_run_time(freq, base)` 식, 화면의 `<next_run(freq)>`, `auto-<ts>`, `<rfc3339>`, `<today>`, KST "MM-DD HH:MM" 근처 값 |
| 임시 경로·포트 | 실행마다 다름 | `<ROOT>` `<DIR>` `<HOST>` `<STUB>` |
| bcrypt 해시·TOTP 비밀값·pre-auth 만료 | 무작위 | `<bcrypt $2b$12>` 등 |

그 밖은 바이트 단위로 같아야 한다 — 오류 문구(axum 추출기 거절 문구, rusqlite 오류, `exit status: N`, `(os error N)` 포함)까지.
Go에서 맞추기 어려운 문구가 나오면 그때 정규화 목록에 추가하는 것을 사용자와 결정한다.

## 단계와 결과 (P = Rust로 기록·녹색)

| 단계 | 내용 | 커밋 |
|---|---|---|
| P0 | 하네스, 로그인 페이지, 부트스트랩 admin, 쿠키 서명 일치 | e1b134a |
| P1 | 라우트 인벤토리(main.rs·hostd.rs 대조), 권한표 91라우트 × 9자격, 라우터(404/405 Allow/415/422/HEAD/쿠키 파싱) | e1b134a |
| P2 | 로그인·TOTP 흐름·프로필 2FA·사용자 관리·부트스트랩·role TEXT→INTEGER 마이그레이션·스키마 DDL | 2af11ad |
| P3 | 컨테이너 목록/상세/inspect 5xx 폴백, 액션(Docker 호환 경로), 빌드(멀티파트→tar→NDJSON), compose 릴레이, Podman 없음 | 04b94d8 |
| P4 | 파일시스템·스토리지·스냅샷·스케줄 CRUD/즉시 실행·파일 관리자(클립보드 쿠키·업로드)·공유·로컬 사용자·서비스·네트워크·AppJail·진단, hostd 없음(로컬 실행 폴백) | b1caf0c |
| P5 | 메트릭(ring·DB 버킷)·데스크탑 prefs·알림 설정/테스트(웹훅·Discord(프록시)·SMTP 오류)·백그라운드 알림 점검·스케줄/복제 틱·복제 CRUD | 4dbea5f |
| P6 | hostd 단독: 조회(실제 출력)·명령 실패·ZFS 변경·prune·복제 파이프(send→receive stdin)·last_snapshot 복구·pw/smbpasswd(stdin)·SMB/NFS 파일·AppJail 락·compose·fs·라우터 | 4e5dfa0 |
| P7 | 교차 호환: 앱↔실제 hostd(가짜 없음), DB를 한 구현으로 만들고 다른 구현으로 열기, 쿠키·pre-auth 상호 인정 | 19bcbfe |

P7은 지금 Rust↔Rust로 녹색이다. Go가 생기면 `SUT_BIN`/`OTHER_BIN`으로 양방향을 돌린다.

## KNOWN-BUG (Rust 현재 동작 — 계약에 그대로 고정, 수정은 별도 결정)

1. ~~[보안] viewer 묶음 7개 핸들러에 인증 없음~~ — **수정됨(pfortainer-rs fix(auth), 2026-11-19)**. `/api/files/list`·`download`·
   `/api/diag/*`·`/api/system/stats`·`/api/metrics/data`가 익명으로 열려 있었음(운영 확인). 이제 302 /login.
   권한표에 "공개·리다이렉트 경로 외에는 익명·위조 쿠키가 핸들러(hostd/Podman 호출)에 닿지 않음" 불변식을 넣어 회귀를 막는다.
2. 세션 쿠키가 무상태(`username:role` 서명) — DB에서 역할 변경·삭제해도 기존 쿠키 8시간 유효, DB에 없는 사용자도 통과.
3. pfpreauth 쿠키가 TOTP 성공 뒤에도 만료(5분)까지 재사용 가능.
4. 컨테이너 id에 URI 불가 문자(공백 등)가 있으면 hyperlocal panic → 응답 없이 연결 끊김.
5. scrub 스케줄이 hostd가 아닌 jail 안에서 `zpool scrub`을 직접 실행 → 항상 실패.
6. 업로드 2MB 초과 시 axum 기본 바디 제한으로 multipart 파싱 오류 500.
7. diag host-log의 `lines`가 인코딩 없이 hostd 쿼리에 붙음(`&file=` 끼워 넣기 가능).
8. hostd fs/copy로 디렉터리를 자기 안으로 복사하면 이름이 너무 길어질 때까지 재귀 생성.
9. hostd fs/write가 상대경로를 `/` 기준으로 씀.
10. hostd zfs/dataset·snapshot·clone 등은 이름 패턴 검증 없이 인자로 넘김(셸은 없음).
11. SMART 디스크 표시 순서가 매번 바뀜(UX).

## Go 구현 G0~G7 (2026-11-19 완료)

`SUT_BIN=$PWD/bin/pfortainer OTHER_BIN=../pfortainer-rs/target/release/pfortainer go test ./contract/...`이
3회 연속 녹색, `-race` 빌드도 녹색(앱·hostd 로그에 DATA RACE 있으면 실패하도록 하네스가 검사). P7은 양방향
(Go 앱↔Rust hostd, Rust 앱↔Go hostd, DB를 한쪽으로 만들고 다른 쪽으로 열기) 모두 녹색.

| 단계 | 내용 |
|---|---|
| G0 | 골격, `internal/askama`(Rust 템플릿 그대로 해석), `internal/httpx`(axum·serde 흉내), config·testhooks·configdb |
| G1·G2 | 전 라우트, require_operator/admin, 로그인·TOTP·프로필·사용자 관리 |
| G3 | Podman 클라이언트·컨테이너 화면·빌드·compose |
| G4 | 스토리지·스냅샷·스케줄·파일관리자·공유·AppJail·진단 |
| G5 | 메트릭 수집기(sysctl)·알림(웹훅·Discord·SMTP)·스케줄러·복제 |
| G6 | hostd 전체 |
| G7 | 교차 호환·race·반복 |

결정 변경: 계획의 D2(html/template) 대신 askama 해석기. html/template의 자동 이스케이프(`&#39;` 등)와
공백이 askama와 달라서, 템플릿 28개를 손으로 옮기는 것보다 같은 파일을 해석하는 편이 확실했다.

Go 쪽 정규화 추가는 하나뿐: hostd compose 임시 디렉터리(macOS Rust tempfile은 `/var/folders/…`, Go는 `/tmp`).

### 계약으로 못 덮는 것 (fbnas 섀도 검증에서 확인)
- 메트릭 수집기의 FreeBSD sysctl 값(devstat·loadavg·온도·ARC) — 파서는 단위 테스트(`metrics_test.go`)
- 실제 SMTP 발송(lettre ↔ net/smtp 오류 문구는 다를 수 있음), 실제 zfs send/receive 대용량
- 실제 사용자·그룹 이름 조회(os/user)
