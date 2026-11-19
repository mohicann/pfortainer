package tests

import (
	"os"
	"strings"
	"testing"

	h "github.com/mohicann/pfortainer/contract/harness"
)

// TestDiscoverHostd: (도구) DISCOVER=1이면 hostd 조회 엔드포인트가 실행하는 명령을 출력한다.
func TestDiscoverHostd(t *testing.T) {
	if os.Getenv("DISCOVER") == "" {
		t.Skip("DISCOVER=1 일 때만")
	}
	hd := h.StartHostd(t, nil)
	os.MkdirAll(hd.Root+"/usr/local/appjail/jails/audio/jail", 0o755)
	os.MkdirAll(hd.Root+"/usr/local/appjail/jails/audio/conf", 0o755)
	hd.Rule("appjail *", h.ShimRule{Stdout: ""})
	for _, p := range []string{"/sockstat", "/jls", "/ps?jid=5", "/zpool-status", "/zpool-list", "/metrics/host", "/zfs-list", "/zfs-list-snapshot", "/zfs-list-snapshot?dataset=zdata/apps",
		"/smart-scan", "/smart?dev=ada0", "/localusers/status", "/nfs/status", "/smb/status", "/network/status", "/appjail/list", "/appjail/detail?name=audio",
		"/diag/cmd?cmd=df", "/diag/cmd?cmd=mount", "/diag/cmd?cmd=netstat", "/diag/cmd?cmd=sockstat", "/diag/cmd?cmd=ps", "/diag/cmd?cmd=ifconfig", "/diag/cmd?cmd=dmesg", "/diag/cmd?cmd=top",
		"/diag/log?file=messages"} {
		r := hd.Do(t, "GET", p, "")
		for _, c := range hd.Calls() {
			t.Logf("%-32s %d  %s %s", p, r.Status, c.Cmd, strings.Join(c.Args, " "))
		}
	}
}
