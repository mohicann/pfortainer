package harness

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// CaptureDir: fbnas 운영 hostd/Podman에서 읽기 전용 GET으로 수집한 응답(2026-11-19).
// 수집 방법은 contract/fixtures/README.md. 비밀값은 수집 직후 치환함.
func CaptureDir() string { return filepath.Join(ContractDir(), "fixtures", "capture") }

var capSlug = regexp.MustCompile(`[^A-Za-z0-9]`)

type capEntry struct {
	Kind, URI string
	Status    int
	Body      string
}

func loadCaptures(t testing.TB) []capEntry {
	t.Helper()
	f, err := os.Open(filepath.Join(CaptureDir(), "index.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []capEntry
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		parts := strings.SplitN(sc.Text(), " ", 3)
		if len(parts) != 3 {
			continue
		}
		code, _ := strconv.Atoi(parts[1])
		slug := strings.TrimPrefix(capSlug.ReplaceAllString(parts[2], "_"), "_")
		b, err := os.ReadFile(filepath.Join(CaptureDir(), parts[0]+"__"+slug+".body"))
		if err != nil {
			t.Fatalf("capture %s: %v", parts[2], err)
		}
		out = append(out, capEntry{parts[0], parts[2], code, string(b)})
	}
	return out
}

func ctFor(body string) string {
	s := strings.TrimSpace(body)
	if strings.HasPrefix(s, "{") || strings.HasPrefix(s, "[") {
		return "application/json"
	}
	return "text/plain; charset=utf-8"
}

// Capture 픽스처 본문 하나(테스트에서 가공할 때).
func CaptureBody(t testing.TB, kind, uri string) string {
	t.Helper()
	for _, e := range loadCaptures(t) {
		if e.Kind == kind && e.URI == uri {
			return e.Body
		}
	}
	t.Fatalf("capture 없음: %s %s", kind, uri)
	return ""
}

// LoadCaptures: 수집한 응답 전부를 가짜 hostd/Podman에 등록한다.
// 수집 URI와 앱이 보내는 URI가 다를 수 있는 것(경로 인코딩)은 쿼리 없는 키로도 등록한다.
func (a *App) LoadCaptures(t testing.TB) {
	t.Helper()
	for _, e := range loadCaptures(t) {
		var fs *FakeSock
		if e.Kind == "hostd" {
			fs = a.Hostd
		} else {
			fs = a.Podman
		}
		if fs == nil {
			continue
		}
		rep := Reply{Status: e.Status, ContentType: ctFor(e.Body), Body: e.Body}
		fs.Set("GET "+e.URI, rep)
	}
	// 쿼리 없는 대표 응답
	if a.Hostd != nil {
		for _, k := range []string{"/ps?jid=5", "/smart?dev=ada0", "/appjail/detail?name=audio", "/diag/cmd?cmd=df",
			"/diag/log?file=dmesg&lines=50", "/fs/stat?path=%2Fzdata%2Fapps"} {
			for _, e := range loadCaptures(t) {
				if e.Kind == "hostd" && e.URI == k {
					p := k[:strings.IndexByte(k, '?')]
					a.Hostd.Set("GET "+p, Reply{Status: e.Status, ContentType: ctFor(e.Body), Body: e.Body})
				}
			}
		}
	}
}

// StartAppWithCaptures: 수집 픽스처를 실은 앱.
func StartAppWithCaptures(t testing.TB, opts AppOpts) *App {
	t.Helper()
	a := StartApp(t, opts)
	a.LoadCaptures(t)
	a.DumpLogsOnFail()
	return a
}
