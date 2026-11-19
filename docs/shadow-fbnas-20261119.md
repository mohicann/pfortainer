# fbnas 섀도 검증 (2026-11-19 15:20~15:40)

운영 무영향으로 진행: 운영 앱(jail :11000)·운영 hostd·운영 DB는 건드리지 않음. 끝나고 섀도 프로세스·임시 파일 모두 정리.

## 방법
- Go 바이너리: Mac에서 `GOOS=freebsd GOARCH=amd64 CGO_ENABLED=0`으로 교차 빌드(e60d160)해 jail `/tmp/pfshadow`에 임시로 둠
  (섀도 전용. 실제 전환은 계획대로 git pull → jail 빌드, deploy key·jail go 설치 필요).
- 앱: jail 안에서 운영 config.db·metrics.db 사본을 각자 가진 섀도 Rust(:11101, 운영 바이너리 46302bb)와 Go(:11102).
  `APP_ENV=test`로 스케줄러 끔(스냅샷 등 쓰기 방지), 별도 SESSION_SECRET. 둘 다 운영 Rust hostd·Podman 소켓을 GET만으로 사용.
- hostd: 호스트에 Go hostd를 별도 소켓(`/tmp/pfshadow-host/go.sock`)으로 띄워 운영 Rust hostd와 읽기 전용 엔드포인트 비교.
- `cmd/shadowdiff`가 같은 경로를 두 쪽에 동시에 보내고 정규화(HTML 공백, 라벨·디스크 순서, JSON 키) 후 비교.

## 결과
| 대상 | 요청 | 차이 | 비고 |
|---|---|---|---|
| 앱 (전 GET 화면·API 71경로 × 3회, 컨테이너·jail 상세 포함) | 213 | **0** | [shadowdiff-fbnas-20261119-app.txt](shadowdiff-fbnas-20261119-app.txt) |
| hostd (읽기 50경로 × 3회) | 150 | 12 | 전부 `/smart?dev=ada3·ada4`의 실시간 SMART 원시값(Seek_Error_Rate, Head_Flying_Hours) 변동. Rust hostd 대 Rust hostd 대조에서도 같은 차이 → 구현 차이 아님 |
| `/metrics/host` | 수동 | 같음 | net 11개·tcp_estab·pools 동일 |
| 메트릭 수집기(sysctl, 섀도 2개 모두 수집 켬) | 수동 | 같음 | load·ARC·풀·TCP 같은 값, CPU·NIC·디스크 초당값은 측정 시점 차이 범위. cputemp는 둘 다 0(jail에서 안 보임) |

지연: 앱 p50 Rust 1.4ms / Go 1.7ms, p95 657ms / 654ms(느린 쪽은 hostd·appjail 명령 시간). hostd p95 Rust 973ms / Go 915ms.
메모리(RSS): 앱 Rust 20.8MB / Go 17.1MB, hostd Rust 29.5MB(운영) / Go 19.8MB.

## 남은 확인 (전환 후 관찰)
- 실제 SMTP 발송(이메일 알림 미사용 중), 스케줄러·알림 루프의 장시간 동작, 변경 작업(POST)은 계약 테스트로만 확인
