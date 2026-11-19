# 계약 테스트 픽스처

## capture/
fbnas 운영 hostd(`/var/run/pfortainer-rs/host.sock`)와 Podman(`/var/run/podman/podman.sock`)에
**읽기 전용 GET만** 보내 받은 응답 (2026-11-19). `index.txt`는 `종류 상태코드 URI`.
파일 이름은 `종류__URI의 영숫자 외 문자를 _로`.body.

수집 직후 비밀값 치환: `POSTGRES_PASSWORD=redacted-for-fixture`.

다시 수집할 때는 상태를 바꾸는 엔드포인트(POST)를 절대 넣지 않는다.

## cmd/
hostd 단독 테스트(P6)의 가짜 명령(shim) 출력. fbnas에서 읽기 전용 명령으로 수집.
