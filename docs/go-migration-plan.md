# pfortainer-rs → Go 마이그레이션 개발계획 (2026-11-19, 결정 D1~D5 확정)

> audio-server-rs → Go 재마이그레이션(2026-11-18~19, `~/git/audio-server-go`)에서 쓴 방식을
> 그대로 따른다. 계약 테스트를 먼저 만들고, 같은 바이너리 자리에 넣어 섀도 검증한 뒤 일괄 전환하며,
> Rust는 보관해 롤백 경로를 남긴다.

## 0. 현황 요약

| 항목 | 값 |
|---|---|
| 코드 | Rust 약 9.5k줄(`hostd.rs` 1.9k, `configdb.rs` 0.8k, `metrics.rs` 0.8k, `podman.rs` 0.7k) + askama 템플릿 28개 약 7.5k줄 |
| 라우트 | 앱 81개 경로(GET 페이지·JSON API·POST 폼, `require_operator`/`require_admin` 2계층) + hostd 약 50개(Unix 소켓 HTTP) |
| 저장소 | `config.db`(users, meta, schedules, alert_history, desktop_prefs, replication_tasks), `metrics.db`(metrics) |
| 백그라운드 | 메트릭 수집 스레드, 스케줄러(60초 스냅샷 스케줄·5분 알림·10분 복제) |
| 외부 의존 | Podman libpod API(소켓), hostd(소켓), Discord/SMTP 알림 |
| 테스트 | **없음** (README: "cargo build로 컴파일 검증") |
| 운영 | fbnas 단일 호스트. jail `pfortainer` 10.0.0.13:11000(앱) + 호스트 rc.d `pfortainer_hostd`(root) |
| 역사 | 원래 Go(`~/git/pfortainer`, `mohicann/pfortainer`)였고 2026-07-19~20에 Rust로 옮김. Go 코드는 07-19(a0243c2)에서 멈춤. 그 뒤 Rust에 기능·수정 커밋 약 30개(AppJail, 웹 데스크탑, 메트릭 오프셋, SMART, Discord embed, 복제 last_snapshot 수정 등) |

audio-server와 다른 점:
1. **HTML 서버 렌더링**이 중심이다. JSON API만 비교하면 되는 audio-server와 달리 페이지 출력을 비교해야 한다.
2. **OS 명령에 크게 의존**한다(zfs/zpool/appjail/smartctl/pw/smbpasswd/sysctl…). 대부분 root로 실행되고, 상태를 바꾸는 명령도 많다.
3. **프로세스가 두 개**다(앱 + hostd). 둘 사이의 소켓 HTTP가 내부 계약이므로 따로따로 전환할 수 있다.
4. 테스트가 전혀 없으니 계약 테스트가 첫 명세가 된다.
5. 운영 호스트가 fbnas 하나뿐이다(ugnas 대상 아님). 사용자는 관리자 본인 한 명이다.

## 1. 원칙 (audio-server에서 그대로 가져옴)

- **동작 100% 동일.** "Rust에서 녹색인 테스트를 Go에서도 녹색으로." 전환 중에 버그를 발견하면 KNOWN-BUG로 기록만 하고, 고치는 건 전환 뒤에 한다.
- **골든은 Rust 빌드로만 기록**한다(`-update`는 `SUT_BIN==RUST_BIN`일 때만).
- **스키마 원본은 Rust**다. `configdb.rs` 마이그레이션(role TEXT→INTEGER 등)을 포함해 Go가 같은 DB를 만들고 읽는지 일치 검사한다.
- Rust 쪽 변경은 **테스트 훅만** 넣는다(`APP_ENV=test` 전용, 운영 동작 무변화).
- 라우트 단위 strangler는 쓰지 않는다(세션·백그라운드 잡이 하나뿐). 대신 **앱과 hostd를 각각 일괄 전환**한다.
- Rust 바이너리를 보관하고 wrapper 스위치를 둬서 롤백은 파일 하나 지우고 재시작하면 되게 한다.
- 배포는 git push → fbnas에서 pull, 그리고 **매번 승인**을 받는다. hostd·rc.d 변경은 부팅·root에 영향을 주므로 위험을 명시하고 동의를 받는다.

## 2. 결정 사항 (2026-11-19 사용자 확정: 모두 권장안)

| # | 질문 | 권장안 | 대안 |
|---|---|---|---|
| D1 | 저장소 | **원조 `mohicann/pfortainer`(`~/git/pfortainer`)를 되살린다.** 07-19 상태에 `legacy-2026-07` 태그를 달고 `go2` 브랜치에서 `internal/` 구조로 재편한 뒤 main에 병합한다. 바이너리 이름(`pfortainer`)·Go 모듈명이 이미 맞고, 옛 Go 코드가 Rust의 조상이라 podman·zfs 파싱·hostd 상당 부분을 재사용할 수 있다 | audio-server처럼 새 저장소 `pfortainer-go`를 만들고 옛 것은 `-legacy`로 바꾼다 |
| D2 | 템플릿 | **`html/template`**. askama 템플릿이 원래 Go 템플릿에서 옮겨 온 것이라 역변환이 쉽다 | templ(코드 생성) — 빌드 단계가 늘고 원본과 멀어짐 |
| D3 | SQLite 드라이버 | **modernc(순수 Go)**. DB가 작고, cgo가 없으면 jail 빌드가 단순하다. `metrics.db` 조회 지연은 섀도에서 확인한다 | mattn(cgo) |
| D4 | 안 쓰는 ZFS 복제·스냅샷 스케줄 기능(sanoid와 중복) | **동일하게 이식한다**(원칙 우선, 이미 고친 last_snapshot 로직 포함). 제거는 전환 뒤 별도 작업으로 한다 | 전환과 함께 메뉴·잡을 제거한다. 일은 줄지만 동작이 달라진다 |
| D5 | 전환 순서 | **앱 먼저(jail, 롤백 쉬움) → 3일 관찰 → hostd(호스트 root)** | 동시에 전환 |

## 3. 계약 테스트 설계 (`contract/`, Go 저장소 안)

> **2026-11-19 P0~P7 완료** — 실제 구현·결과·KNOWN-BUG는 [contract-tests.md](contract-tests.md). 아래는 처음 설계.

### 3.1 하네스
```
contract/harness/
  server.go     # 앱 바이너리 기동(임시 dir, 빈 포트, .env 대신 env 주입), hostd 바이너리 기동(-hostd, 임시 소켓)
  fakehostd.go  # 앱 테스트용 가짜 hostd: Unix 소켓 HTTP, 요청(메서드·경로·바디) 기록, 픽스처 응답
  fakepodman.go # 가짜 libpod API(/v5.0.0/libpod/...), 요청 기록, 빌드 스트리밍 응답 재현
  shims/        # hostd 테스트용 가짜 명령(zfs, zpool, appjail, smartctl, pw, service, sockstat, ps, netstat …)
                #   PATH 앞에 둠. 호출 인자를 JSONL로 기록하고 fixtures/cmd/ 아래 캡처 출력을 돌려줌
  stubs.go      # Discord 웹후크·SMTP 스텁(요청 기록)
  auth.go       # 로그인 헬퍼(역할별), TOTP 코드 생성, 위조/만료 쿠키
  html.go       # HTML 정규화(공백 접기, CSRF/시각/버전 마스킹) + 골든 비교
  dbcheck.go    # config.db/metrics.db 행 스냅샷
  golden.go, normalize.go
contract/fixtures/
  cmd/          # fbnas에서 실제로 캡처한 명령 출력(읽기 전용 명령만 실행해 수집): zfs list -Hp, zpool status, appjail jail list -eHIp, smartctl -a/-j, netstat -ibn, sockstat -l, ps -J …
  podman/       # 실제 libpod 응답 캡처(containers/json, inspect, images …)
```

### 3.2 무엇을 비교하나
- **앱**: 상태코드·리다이렉트(Location)·Set-Cookie(이름·속성·서명 형식), JSON API 바디, HTML(정규화 골든), **앱이 hostd/Podman에 보낸 요청 순서와 내용**(핵심 부수효과), config.db 행 변화, 알림 스텁 수신.
- **hostd**: 소켓 HTTP 응답(JSON), **shim이 기록한 명령과 인자**(명령 인젝션·경로 검증 회귀 방지), 파일 작업 결과(임시 루트 안에서), 동시 변경 잠금(Mutex) 동작.
- 제외: 실제 OS 상태 변경, FreeBSD sysctl 바이너리 구조체(devstat·vm.loadavg·온도)는 §3.4에서 따로 다룬다.

### 3.3 Rust 테스트 훅 (최소)
- `APP_ENV=test`일 때만 적용: 고정 시계(`TEST_FIXED_NOW`, 메트릭·알림·스케줄 타임스탬프용), 스케줄러 틱 수동 트리거(`POST /__test/tick`처럼 test에서만 열리는 경로, 또는 `TEST_SCHED_INTERVAL_MS`), 메트릭 수집 주기 단축 또는 끄기.
- hostd 명령은 이미 이름으로(PATH 검색) 실행된다. 절대경로를 쓰는 곳(`/usr/local/bin/appjail`, `/etc/passwd`, `/usr/local/etc/smb4.conf`, `/var/log/*`)은 `TEST_ROOT` 접두어 훅으로 임시 루트에 돌린다.
- 소켓 경로(`HOSTD_SOCKET`, `PODMAN_SOCKET`)·DB 경로는 이미 env라 훅이 필요 없다.

### 3.4 FreeBSD 전용 부분
- sysctl 파싱(devstat 오프셋 72/80, struct loadavg, CPU 온도)은 **캡처한 원시 바이트를 입력으로 받는 순수 함수**로 분리하고, 양쪽 구현에 같은 바이트 픽스처를 넣어 비교한다(Rust 쪽은 훅 CLI `pfortainer -dump-sysctl-parse <file>` 같은 test 전용 서브커맨드).
- 실제 값은 fbnas 섀도 검증(§5)에서 `/metrics/host` 응답을 Rust와 Go로 비교해 확인한다.

### 3.5 단계 (P = 계약 테스트, Rust로 녹색)
| 단계 | 범위 |
|---|---|
| P0 | 하네스(앱·hostd 기동, fakehostd, fakepodman, shim) + 로그인 페이지 200 |
| P1 | 라우트 인벤토리(`main.rs`·hostd 라우터와 일치 검사) + 권한표(익명·각 role × 81 라우트) + 라우터 동작(404/405/끝 슬래시, `/`→`/appjail`, `/bastille`→`/appjail`, 정적 파일) |
| P2 | 인증: 로그인·TOTP 2단계·로그아웃·쿠키 서명(HMAC)·프로필 비밀번호/2FA·관리자 사용자 관리·초기 admin 생성·role 마이그레이션 |
| P3 | 컨테이너·이미지·compose·시스템: Podman 요청 기록, inspect 5xx 폴백, 빌드 스트리밍(multipart 입력 → 청크 출력) |
| P4 | 스토리지·ZFS·스냅샷·SMART·파일매니저·공유(SMB/NFS)·로컬 사용자/그룹·네트워크·서비스·AppJail: 앱→hostd 요청 + 렌더링 |
| P5 | 메트릭 페이지/데이터 API, 알림 설정·테스트·이력(Discord embed·SMTP), 진단, 데스크탑 prefs, 스케줄러·복제 잡(고정 시계 + 틱) |
| P6 | hostd 단독: ~50개 엔드포인트 × shim 기록. 파일 작업 경로 탈출 케이스, 잠금, replication_run의 last_snapshot 복구 분기, sysctl 바이트 픽스처 |
| P7 | 호환: Rust 앱↔Go hostd, Go 앱↔Rust hostd 교차 실행, 같은 config.db/metrics.db를 번갈아 열기, 쿠키 상호 인정(전환 시 재로그인 불필요) |

## 4. Go 구현 (G = P와 1:1, 해당 계약이 Go로 녹색이면 완료)

| 단계 | 내용 |
|---|---|
| G0 | 골격: config(Rust와 같은 env·기본값·.env 로딩 규칙), testhooks, db(같은 pragma·마이그레이션), 라우터(net/http 1.22+ 패턴 또는 audio-server-go의 httpx 재사용), `-hostd` 서브커맨드 |
| G1 | 전 라우트 등록(미구현 501) + require_operator/require_admin + 인증 전체 |
| G2 | (G1에 합침, audio-server와 같은 이유) |
| G3 | Podman 클라이언트(libpod 전용. Docker 호환 API는 FreeBSD Podman에서 panic) + 컨테이너 페이지 |
| G4 | 스토리지 계열 페이지 + hostc(앱 쪽 hostd 클라이언트) |
| G5 | 메트릭 수집기·알림·스케줄러·데스크탑 |
| G6 | hostd 전체(옛 Go `hostd.go`를 바탕으로 Rust 이후 수정분을 반영) — `ps -o` 필드 분리, `netstat <Link#>`, appjail 탭 출력 파싱 같은 함정 포함 |
| G7 | 교차 호환 + `-race` + 반복 3회 녹색 |

옛 Go 코드는 참고만 한다. **기준은 Rust 동작과 골든이다.** 07-19 이후 Rust에서 바뀐 커밋은 `git log 5a62b7d..` 목록으로 체크리스트를 만들어 하나씩 확인한다.

## 5. 섀도 검증 (fbnas, 운영 영향 없음)

- jail 안에서 DB 사본(`config.db`·`metrics.db`) 하나에 **섀도 Rust 11101 + 섀도 Go 11102**를 함께 띄운다(운영 11000과는 비교하지 않음. DB가 계속 바뀌어 가짜 차이가 생기므로 — audio-server 교훈).
- 두 섀도는 **진짜 hostd·Podman 소켓을 쓰되 GET만** 보낸다(POST·상태 변경은 계약 테스트에서만). 메트릭 수집·스케줄러는 섀도에서 끈다(훅 또는 env).
- `cmd/shadowdiff`(audio-server-go에서 가져와 HTML 정규화 추가): 모든 GET 페이지·API를 관리자 로그인으로 순회하고 차이와 지연(p50/p95)을 보고한다.
- hostd는 호스트에 **섀도 Go hostd를 다른 소켓**(`/var/run/pfortainer-go/host.sock`)으로 띄우고, 읽기 전용 엔드포인트만 Rust hostd와 응답을 비교한다(`/metrics/host`, `/zfs-list`, `/zpool-status`, `/appjail/list`, `/smart` …).

## 6. 전환 단계

### 단계 A — 앱(jail)
1. 기준선: `zdata/apps@pre-go-pf-YYYYMMDD` 스냅샷, config.db 백업, Rust 바이너리를 `pfortainer.rust-pre-go`로 복사.
2. jail rc.d에 wrapper 스위치를 둔다(`/usr/local/etc/pfortainer.bin`이 있으면 그 경로를 실행, 없으면 Rust). 이 rc.d 교체는 사전 준비로 미리 해 둔다(동작 무변화).
3. jail 안에서 `go build`(jail에 `go` pkg 설치, `GIT_CONFIG` safe.directory env 필요 — audio-server 함정) → 스위치 파일 기록 → `appjail service jail pfortainer pfortainer restart`.
4. 확인: shadowdiff 스크립트를 운영 포트에 GET만으로 1회 실행, 사용자가 데스크탑·주요 메뉴 확인, ERROR 로그 0, rss 관찰.
5. 3일 관찰: 알림 5분 잡, SMART 알림, 메트릭 그래프 연속성(수집 공백 없음).
- 롤백: 스위치 파일 삭제 + 재시작.

### 단계 B — hostd(호스트, root)
1. rc.d `pfortainer_hostd`에도 같은 스위치를 둔다(승인 필요. 부팅 경로이므로 재부팅 영향을 명시하고 동의를 받는다. BEFORE: appjail 순서 유지).
2. Go hostd 빌드 산출물은 jail 빌드 결과를 호스트가 실행하는 지금 구조를 유지한다.
3. 전환 → 메뉴 전체 GET 확인 → 상태 변경 작업 1개씩 사용자 입회하에 확인(예: 테스트 데이터셋 스냅샷 생성/삭제, jail restart 1회).
4. 재부팅 검증 1회(사용자 동의 시) — 소켓 생성 순서, jail 안 nullfs 노출.
- 롤백: 스위치 삭제 + `service pfortainer_hostd restart`.

### 롤백 판단
ERROR 로그 증가, 메트릭 수집 공백, 알림 미발송/중복, hostd 명령 실패, 사용자 보고 중 하나라도 있으면 즉시 롤백하고 원인을 분석한다.

## 7. 전환 후 정리 (2주 뒤, 확인받고)
- Rust 바이너리·스위치 제거, `pfortainer-rs` 저장소 archive(README에 이동 안내), fbnas deploy key 정리(pfortainer-rs 키 삭제).
- KNOWN-BUG 수정, D4에서 미룬 복제/스케줄 기능 제거 여부 결정.
- 메모리·README의 stale 문구 갱신.

## 8. 위험과 대응

| 위험 | 대응 |
|---|---|
| askama와 html/template의 이스케이프 차이(`'`→`&#x27;` vs `&#39;`, 속성·JS 컨텍스트 자동 이스케이프) | 골든에서 바이트 차이로 드러남. 정규화에서 엔티티 디코드 후 비교 + 차이 목록을 따로 검토(무해 판정은 사용자 확인) |
| 쿠키 서명 형식 불일치 → 전환 시 전원 로그아웃 | P2/P7에서 상호 인정 테스트. 실패해도 영향은 재로그인뿐이지만 동일하게 맞춘다 |
| hostd 경로 검증 회귀(root 파일 작업) | P6에 경로 탈출·심볼릭 링크 케이스를 명시적으로 넣는다 |
| 빌드 스트리밍·multipart 차이 | P3에서 청크 단위 기록 비교 |
| sysctl 구조체 파싱 | §3.4 바이트 픽스처 + 섀도 `/metrics/host` 비교 |
| Go GC로 rss 증가(audio-server 42→140MB) | 관찰 항목. 필요하면 `GOMEMLIMIT` |
| modernc 지연(metrics.db 범위 조회) | 섀도 p95 기준: 페이지 200ms 이하 |
| JSON 직렬화 차이(float `12.0`, 키 순서, 시각 형식) | 계약에서 바이트 비교, Go 쪽 헬퍼로 Rust 형식 맞춤 |
| 운영 호스트가 하나뿐이라 연습 노드가 없음 | 섀도 검증 비중을 키우고, 단계 A·B를 분리해 한 번에 하나만 바꾼다 |

## 9. 예상 작업량 (참고)
audio-server(라우트 80개, 기존 테스트 없음)는 계약 P0~P7과 구현 G0~G6에 약 2일, 섀도·전환 준비에 1일이 걸렸다.
pfortainer는 라우트 수가 비슷하지만 hostd·shim·HTML 골든 때문에 하네스가 더 크다. 계약 2~3일, 구현 2일, 섀도·전환 1~2일 정도로 본다.
