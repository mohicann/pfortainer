package harness

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// CmdDir: fbnas에서 읽기 전용 명령으로 수집한 출력(contract/fixtures/cmd, 2026-11-19).
func CmdDir() string { return filepath.Join(ContractDir(), "fixtures", "cmd") }

// CmdRules: 수집한 명령 출력을 shim 규칙으로. 키는 "명령 인자…"(appjail은 경로 없이).
func CmdRules(t testing.TB) map[string]ShimRule {
	t.Helper()
	f, err := os.Open(filepath.Join(CmdDir(), "index.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	out := map[string]ShimRule{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		parts := strings.SplitN(sc.Text(), "\t", 3)
		if len(parts) != 3 {
			continue
		}
		code, _ := strconv.Atoi(parts[1])
		stdout, _ := os.ReadFile(filepath.Join(CmdDir(), parts[0]+".out"))
		stderr, _ := os.ReadFile(filepath.Join(CmdDir(), parts[0]+".err"))
		key := strings.TrimSpace(strings.TrimPrefix(parts[2], "/usr/local/bin/"))
		out[key] = ShimRule{Stdout: string(stdout), Stderr: string(stderr), Exit: code}
	}
	return out
}

// InstallHostFiles: 수집한 호스트 파일(/etc/passwd 등)을 TEST_ROOT 아래에 둔다.
func (hd *Hostd) InstallHostFiles(t testing.TB) {
	t.Helper()
	ents, _ := os.ReadDir(filepath.Join(CmdDir(), "files"))
	for _, e := range ents {
		b, _ := os.ReadFile(filepath.Join(CmdDir(), "files", e.Name()))
		p := filepath.Join(hd.Root, strings.ReplaceAll(e.Name(), "_", "/"))
		// 파일 이름의 원래 '_'(smb4.conf 등에는 없음, appjail_jails 경로만 해당) 복원
		p = strings.Replace(p, "/usr/local/appjail/jails/", "/usr/local/appjail/jails/", 1)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, b, 0o644)
	}
	// 수집 jail(audio) 디렉터리
	os.MkdirAll(filepath.Join(hd.Root, "usr/local/appjail/jails/audio/jail"), 0o755)
}
