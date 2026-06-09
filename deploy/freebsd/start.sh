#!/bin/sh
# pfortainer 백그라운드 실행 스크립트
# 사용: sudo sh /zdata/tools/pfortainer-freebsd/start.sh

PFORTAINER_DIR="/zdata/tools/pfortainer-freebsd"
PIDFILE="/var/run/pfortainer.pid"
LOGFILE="${PFORTAINER_DIR}/pfortainer.log"

if [ -f "${PIDFILE}" ] && kill -0 "$(cat "${PIDFILE}")" 2>/dev/null; then
    echo "pfortainer is already running (pid $(cat "${PIDFILE}"))."
    exit 1
fi

/usr/sbin/daemon -P "${PIDFILE}" -o "${LOGFILE}" \
    /bin/sh -c "cd '${PFORTAINER_DIR}' && exec ./pfortainer"

echo "pfortainer started on port 11000. Log: ${LOGFILE}"
