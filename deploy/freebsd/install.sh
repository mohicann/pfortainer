#!/bin/zsh
# fbnas에서 실행: pfortainer + podman_api rc.d 설치 및 서비스 전환
# 사용: sudo zsh install.sh [fbnas-hostname]
#
# 로컬에서 원격 실행:
#   ssh fbnas "sudo zsh -s" < deploy/freebsd/install.sh

set -euo pipefail

RC_DIR="/usr/local/etc/rc.d"
SCRIPT_DIR="$(dirname "$0")"

echo "==> pfortainer 서비스 중지"
service pfortainer stop 2>/dev/null || true

echo "==> rc.d 스크립트 설치"
cp "${SCRIPT_DIR}/rc.d/podman_api" "${RC_DIR}/podman_api"
chmod +x "${RC_DIR}/podman_api"
cp "${SCRIPT_DIR}/rc.d/pfortainer" "${RC_DIR}/pfortainer"
chmod +x "${RC_DIR}/pfortainer"

echo "==> podman_api 서비스 활성화"
sysrc podman_api_enable=YES

echo "==> podman_api 서비스 시작"
service podman_api start

echo "==> pfortainer 서비스 시작"
service pfortainer start

echo "==> 상태 확인"
service podman_api status
service pfortainer status
