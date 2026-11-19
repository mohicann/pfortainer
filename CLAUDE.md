# CLAUDE.md

pfortainer — FreeBSD + Podman 관리 웹 UI(Go). 운영 중인 Rust 구현(`../pfortainer-rs`)을 동작 그대로 옮기는 중.
계획: [docs/go-migration-plan.md](docs/go-migration-plan.md), 계약 테스트: [docs/contract-tests.md](docs/contract-tests.md).

## 빌드·테스트

```sh
go build -o bin/pfortainer ./cmd/pfortainer          # 앱, `bin/pfortainer -hostd`는 root host agent
go test ./internal/...                                 # 단위 테스트(템플릿이 Rust 사본인지 포함)
SUT_BIN=$PWD/bin/pfortainer OTHER_BIN=../pfortainer-rs/target/release/pfortainer go test ./contract/...
go test ./contract/... -update                         # 골든 기록 — Go(bin/pfortainer)로만 (2026-11-19 전환 뒤 Go가 원본)
```

## 구조

- `cmd/pfortainer`: main
- `internal/askama`: Rust askama 템플릿을 그대로 해석(이스케이프·공백 동일). `internal/pf/templates`는 Rust 사본
- `internal/httpx`: axum 라우팅·추출기 거절 문구, serde_json 파싱 오류·직렬화 흉내
- `internal/pf`: 앱 핸들러, Podman/hostd 클라이언트, hostd(`hostd.go`), 메트릭·알림·스케줄러
- `contract/`: 블랙박스 계약 테스트(Rust로 기록한 골든)

## 규칙

- 동작은 Rust와 바이트 단위로 같게. 차이를 없애려 정규화를 늘리는 건 사용자와 결정.
- 전환(2026-11-19) 뒤에는 Go가 원본. Rust 저장소는 보관됨 — 템플릿은 여기서만 고친다.
- 오류 문구는 Rust 표기(`exit status: N`, `(os error N)`, serde 문구)를 따른다 — `rustio.go`, `httpx/serde*.go`.
