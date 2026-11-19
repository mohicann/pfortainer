# fbnas pfortainer (Go, FreeBSD 15.1 + AppJail)

2026-11-19 Rust(pfortainer-rs)에서 Go로 옮겼다(계약 테스트·섀도 검증 뒤 일괄 전환, `docs/cutover-plan.md`).

| 항목 | 값 |
|---|---|
| 웹앱 | AppJail jail `pfortainer` (thick, ajnet `10.0.0.13`), 포트 11000 |
| 접속 | 호스트 `192.168.0.110:11000`, `100.70.120.35:11000` → jail `11000` |
| hostd | 호스트에서 root로 실행(`rc.d/pfortainer_hostd`), 소켓 `/var/run/pfortainer-rs/host.sock` |
| Podman API | 호스트 `podman_service` → `/var/run/podman/podman.sock` |
| 체크아웃 | `/zdata/apps/pfortainer` → jail `/opt/pfortainer` (읽기 전용 deploy key, SSH 별칭 `github.com-pfortainer-go`) |
| 데이터 | `/zdata/apps/pfortainer/data/{config.db,metrics.db}` (`zdata/apps` 스냅샷·zbak 복제에 포함, git 무시) |
| 설정 | `/zdata/apps/pfortainer/.env` (600, `env.example`) |

빌드는 jail 안에서 한다(`go`, 약 30초). hostd는 그 결과물 `bin/pfortainer`를 호스트에서 그대로 실행한다.

## 배포 (코드 변경 후)

```sh
cd /zdata/apps/pfortainer && git pull                           # 호스트, mohicann
sudo appjail cmd jexec pfortainer sh -c "cd /opt/pfortainer && env GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=safe.directory GIT_CONFIG_VALUE_0='*' go build -o bin/pfortainer ./cmd/pfortainer"
sudo appjail service jail pfortainer pfortainer restart
sudo service pfortainer_hostd restart                           # hostd 코드가 바뀐 경우
```

배포 전에 `go test ./contract/...`(Mac)으로 계약 테스트를 돌린다. 전환 뒤에는 Go가 원본이라 골든을 Go로 다시 기록해도 된다.

## 처음 설치

```sh
git clone -b main git@github.com-pfortainer-go:mohicann/pfortainer.git /zdata/apps/pfortainer
install -d -m 700 /zdata/apps/pfortainer/data        # .env는 env.example대로(600)
sysrc podman_service_enable=YES && service podman_service start
pkg install -y smartmontools
install -d -m 750 /var/run/pfortainer-rs
cd /zdata/apps/pfortainer/deploy/appjail && appjail makejail -j pfortainer -f Makejail
install -m 755 rc.d/pfortainer_hostd /usr/local/etc/rc.d/pfortainer_hostd
sysrc pfortainer_hostd_enable=YES && service pfortainer_hostd start
```

## 알려진 제한
- ZFS 복제·스케줄 기능은 있지만 쓰지 않는다(백업은 sanoid+syncoid).
- jail 생성·삭제는 화면에 없다(Makejail로 만든다).
