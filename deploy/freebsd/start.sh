#!/bin/sh
# pfortainer 백그라운드 실행 스크립트
# 사용: sudo sh /zdata/tools/pfortainer-freebsd/start.sh

PFORTAINER_DIR="/zdata/tools/pfortainer-freebsd"
PIDFILE="/var/run/pfortainer.pid"
LOGFILE="${PFORTAINER_DIR}/pfortainer.log"
PODMAN_SOCK="/run/podman/podman.sock"

# Podman API 소켓 확인 및 시작
if [ ! -S "${PODMAN_SOCK}" ]; then
    echo "Podman API 소켓 시작 중..."
    mkdir -p /run/podman
    /usr/sbin/daemon -o /var/log/podman-api.log \
        podman system service --time=0 "unix://${PODMAN_SOCK}"
    sleep 1
fi

if [ ! -S "${PODMAN_SOCK}" ]; then
    echo "오류: Podman API 소켓을 시작할 수 없습니다."
    exit 1
fi

# pfortainer 시작
if [ -f "${PIDFILE}" ] && kill -0 "$(cat "${PIDFILE}")" 2>/dev/null; then
    echo "pfortainer is already running (pid $(cat "${PIDFILE}"))."
    exit 1
fi

/usr/sbin/daemon -P "${PIDFILE}" -o "${LOGFILE}" \
    /bin/sh -c "cd '${PFORTAINER_DIR}' && exec ./pfortainer"

echo "pfortainer started on port 11000. Log: ${LOGFILE}"
