#!/bin/sh
# 빌드 → 전송 → 재시작 한 번에
# 사용: ./deploy.sh [fbnas-호스트명]  (기본값: fbnas)
set -e

HOST="${1:-fbnas}"
REMOTE_DIR="/zdata/tools/pfortainer-freebsd"
BIN="pfortainer-freebsd"

echo "==> FreeBSD 바이너리 빌드 중..."
GOOS=freebsd GOARCH=amd64 go build -o "$BIN" .

echo "==> $HOST 에 전송 중..."
scp "$BIN" "$HOST:/tmp/pfortainer-new"
ssh "$HOST" "sudo install -o root -g wheel -m 755 /tmp/pfortainer-new $REMOTE_DIR/pfortainer && rm /tmp/pfortainer-new"

echo "==> 서비스 재시작 중..."
ssh "$HOST" "sudo service pfortainer_hostd onerestart && sudo jexec pfortainer service pfortainer onerestart"

echo "==> 완료"
