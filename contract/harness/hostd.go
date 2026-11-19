package harness

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
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

// ShimRule: 가짜 명령 응답.
type ShimRule struct {
	Stdout    string `json:"stdout,omitempty"`
	Stderr    string `json:"stderr,omitempty"`
	Exit      int    `json:"exit,omitempty"`
	DelayMs   int    `json:"delay_ms,omitempty"`
	EchoStdin bool   `json:"echo_stdin,omitempty"`
}

// ShimCall: 가짜 명령이 받은 호출.
type ShimCall struct {
	Cmd   string   `json:"cmd"`
	Args  []string `json:"args"`
	Stdin string   `json:"stdin,omitempty"`
}

// hostd가 실행할 수 있는 명령 이름(전부 shim으로 연결).
var shimCommands = []string{"zfs", "zpool", "smartctl", "pw", "smbpasswd", "pdbedit", "service", "ifconfig", "netstat",
	"jls", "ps", "sockstat", "tail", "df", "mount", "top", "dmesg", "podman-compose", "date", "ssh", "sysctl", "chown", "id"}

var (
	shimOnce sync.Once
	shimBin  string
	shimErr  error
)

func buildShim(t testing.TB) string {
	t.Helper()
	shimOnce.Do(func() {
		d, err := os.MkdirTemp("", "pfshim")
		if err != nil {
			shimErr = err
			return
		}
		shimBin = filepath.Join(d, "shim")
		cmd := exec.Command("go", "build", "-o", shimBin, "./contract/harness/shim")
		cmd.Dir = RepoRoot()
		out, err := cmd.CombinedOutput()
		if err != nil {
			shimErr = err
			shimBin = string(out)
		}
	})
	if shimErr != nil {
		t.Fatalf("shim 빌드 실패: %v\n%s", shimErr, shimBin)
	}
	return shimBin
}

// Hostd: 실행 중인 SUT -hostd.
type Hostd struct {
	t       testing.TB
	Dir     string // 짧은 임시 디렉터리
	Root    string // TEST_ROOT
	Sock    string
	ShimDir string
	rules   map[string]ShimRule
	cmd     *exec.Cmd
	logs    *safeBuf
	hc      *http.Client
}

// StartHostd: shim을 PATH 맨 앞(유일)에 두고 hostd를 띄운다.
func StartHostd(t testing.TB, rules map[string]ShimRule) *Hostd {
	t.Helper()
	bin := requireBin(t)
	shim := buildShim(t)
	dir := ShortTempDir(t)
	hd := &Hostd{t: t, Dir: dir, Root: filepath.Join(dir, "root"), Sock: filepath.Join(dir, "h.sock"), ShimDir: filepath.Join(dir, "shim"), rules: map[string]ShimRule{}, logs: &safeBuf{}}
	os.MkdirAll(hd.ShimDir, 0o755)
	os.MkdirAll(filepath.Join(hd.Root, "usr", "local", "bin"), 0o755)
	for _, c := range shimCommands {
		os.Symlink(shim, filepath.Join(hd.ShimDir, c))
	}
	os.Symlink(shim, filepath.Join(hd.Root, "usr", "local", "bin", "appjail"))
	for k, v := range rules {
		hd.rules[k] = v
	}
	hd.writeRules()
	env := baseEnv(dir, hd.Root)
	env = mergeEnv(env, map[string]string{"PATH": hd.ShimDir, "SHIM_DIR": hd.ShimDir, "HOSTD_SOCKET": hd.Sock})
	cmd := exec.Command(bin, "-hostd")
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = hd.logs
	cmd.Stderr = hd.logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	hd.cmd = cmd
	t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
		if t.Failed() {
			t.Logf("── hostd 로그 ──\n%s", hd.logs.String())
		}
	})
	hd.hc = &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", hd.Sock)
		}}}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(hd.Sock); err == nil {
			return hd
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("hostd가 뜨지 않음\n%s", hd.logs.String())
	return nil
}

func (hd *Hostd) writeRules() {
	b, _ := json.MarshalIndent(hd.rules, "", " ")
	os.WriteFile(filepath.Join(hd.ShimDir, "rules.json"), b, 0o644)
}

// Rule: 규칙 추가/교체("zfs list -Hp …" 또는 "zfs *").
func (hd *Hostd) Rule(key string, r ShimRule) {
	hd.rules[key] = r
	hd.writeRules()
}

// Calls: shim 호출 기록을 읽고 비운다.
func (hd *Hostd) Calls() []ShimCall {
	p := filepath.Join(hd.ShimDir, "calls.jsonl")
	f, err := os.Open(p)
	if err != nil {
		return nil
	}
	var out []ShimCall
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		var c ShimCall
		if json.Unmarshal(sc.Bytes(), &c) == nil {
			out = append(out, c)
		}
	}
	f.Close()
	os.Remove(p)
	return out
}

// Do: hostd 소켓에 요청.
func (hd *Hostd) Do(t testing.TB, method, path string, body string) *Resp {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, "http://hostd"+path, rd)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	start := time.Now()
	resp, err := hd.hc.Do(req)
	if err != nil {
		return &Resp{Status: 0, Header: http.Header{}, Body: []byte("<connection closed without response>"), Elapsed: time.Since(start)}
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return &Resp{Status: resp.StatusCode, Header: resp.Header, Body: b, Elapsed: time.Since(start)}
}

func (hd *Hostd) Logs() string { return hd.logs.String() }

// Masker: 임시 경로 치환.
func (hd *Hostd) Masker() *Masker {
	m := &Masker{}
	m.Lit(hd.Root, "<ROOT>").Lit(hd.Dir, "<DIR>")
	return m
}
