package harness

import (
	"bytes"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	AdminPassword = "contract-admin-pw"
	SessionSecret = "contract-session-secret-0123456789"
)

// AppOpts: 앱 기동 옵션.
type AppOpts struct {
	Env         map[string]string // 추가/덮어쓸 env
	NoHostd     bool              // hostd 소켓을 만들지 않음(Unavailable/로컬 실행 경로)
	NoPodman    bool              // Podman 소켓을 만들지 않음
	BeforeStart func(a *App)      // DB 사전 작성 등(기동 전)
	Scheduler   bool              // 스케줄러 켜기(기본 끔)
}

// App: 실행 중인 앱 인스턴스.
type App struct {
	t         testing.TB
	Dir       string // 작업 디렉터리(짧은 /tmp 경로 — Unix 소켓 길이 제한)
	Root      string // TEST_ROOT
	URL       string
	Port      string
	ConfigDB  string
	MetricsDB string
	Hostd     *FakeSock
	Podman    *FakeSock
	Env       []string

	cmd  *exec.Cmd
	logs *safeBuf
}

type safeBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *safeBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// ShortTempDir: Unix 소켓 경로 제한(104바이트) 때문에 /tmp 아래 짧은 임시 디렉터리를 쓴다.
func ShortTempDir(t testing.TB) string {
	t.Helper()
	d, err := os.MkdirTemp("/tmp", "pfc")
	if err != nil {
		t.Fatal(err)
	}
	d, _ = filepath.EvalSymlinks(d)
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

func FreePort(t testing.TB) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return fmt.Sprint(l.Addr().(*net.TCPAddr).Port)
}

// BaseEnv: 앱/hostd 공통 env (운영 .env 영향 차단을 위해 작업 디렉터리는 임시 디렉터리).
func baseEnv(dir, root string) []string {
	return []string{
		"APP_ENV=test",
		"TZ=Asia/Seoul",
		"HOME=" + dir,
		"PATH=/usr/bin:/bin",
		"TEST_ROOT=" + root,
		"TEST_DISABLE_METRICS=1",
		"RUST_LOG=info",
		"ADMIN_PASSWORD=" + AdminPassword,
		"SESSION_SECRET=" + SessionSecret,
		"METRICS_POOL1=zdata",
		"METRICS_POOL2=zbak",
		"METRICS_NET_IFACE=igc3",
	}
}

func mergeEnv(base []string, extra map[string]string) []string {
	out := []string{}
	seen := map[string]bool{}
	for k := range extra {
		seen[k] = true
	}
	for _, kv := range base {
		k := kv[:strings.IndexByte(kv, '=')]
		if !seen[k] {
			out = append(out, kv)
		}
	}
	for k, v := range extra {
		if v == "\x00unset" {
			continue
		}
		out = append(out, k+"="+v)
	}
	return out
}

// StartApp: 가짜 hostd/Podman을 붙여 앱을 띄운다.
func StartApp(t testing.TB, opts AppOpts) *App {
	t.Helper()
	bin := requireBin(t)
	dir := ShortTempDir(t)
	a := &App{t: t, Dir: dir, Root: filepath.Join(dir, "root"), logs: &safeBuf{}}
	os.MkdirAll(a.Root, 0o755)
	a.Port = FreePort(t)
	a.URL = "http://127.0.0.1:" + a.Port
	a.ConfigDB = filepath.Join(dir, "config.db")
	a.MetricsDB = filepath.Join(dir, "metrics.db")
	hsock := filepath.Join(dir, "h.sock")
	psock := filepath.Join(dir, "p.sock")
	if !opts.NoHostd {
		a.Hostd = NewFakeSock(t, hsock, HostdDefault)
	}
	if !opts.NoPodman {
		a.Podman = NewFakeSock(t, psock, PodmanDefault)
	}
	env := baseEnv(dir, a.Root)
	env = append(env,
		"HOST=127.0.0.1", "PORT="+a.Port,
		"CONFIG_DB="+a.ConfigDB, "METRICS_DB="+a.MetricsDB,
		"HOSTD_SOCKET="+hsock, "PODMAN_SOCKET="+psock,
	)
	if !opts.Scheduler {
		env = append(env, "TEST_DISABLE_SCHEDULER=1")
	}
	a.Env = mergeEnv(env, opts.Env)
	if opts.BeforeStart != nil {
		opts.BeforeStart(a)
	}
	a.start(bin)
	return a
}

func (a *App) start(bin string) {
	t := a.t
	cmd := exec.Command(bin)
	cmd.Dir = a.Dir
	cmd.Env = a.Env
	cmd.Stdout = a.logs
	cmd.Stderr = a.logs
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", bin, err)
	}
	a.cmd = cmd
	t.Cleanup(a.Stop)
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(a.URL + "/login")
		if err == nil {
			resp.Body.Close()
			return
		}
		if cmd.ProcessState != nil {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatalf("앱이 뜨지 않음\n%s", a.logs.String())
}

// Restart: 같은 DB/환경으로 다시 띄운다(바이너리 교체 가능 — 호환 테스트용).
func (a *App) Restart(bin string) {
	a.Stop()
	if bin == "" {
		bin = SUTBin()
	}
	a.start(bin)
}

func (a *App) Stop() {
	if a.cmd != nil && a.cmd.Process != nil && a.cmd.ProcessState == nil {
		a.cmd.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() { a.cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			a.cmd.Process.Kill()
			<-done
		}
	}
}

func (a *App) Logs() string { return a.logs.String() }

// DumpLogsOnFail: 테스트 실패 시 앱 로그를 출력한다.
func (a *App) DumpLogsOnFail() {
	a.t.Cleanup(func() {
		if a.t.Failed() {
			a.t.Logf("── 앱 로그 ──\n%s", a.Logs())
		}
	})
}
