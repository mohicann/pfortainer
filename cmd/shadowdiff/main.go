// shadowdiff: 섀도 Rust/Go 인스턴스에 같은 GET 요청을 동시에 보내 응답을 비교한다(읽기 전용).
//
//	app 모드:   shadowdiff -mode app -a http://127.0.0.1:11101 -b http://127.0.0.1:11102 -secret S [-user shadow]
//	hostd 모드: shadowdiff -mode hostd -a /var/run/pfortainer-rs/host.sock -b /tmp/pfshadow/go.sock
//
// 페이지 목록은 고정 경로 + 화면에서 찾은 컨테이너·jail·파일 경로. POST는 보내지 않는다.
package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	h "github.com/mohicann/pfortainer/contract/harness"
)

type result struct {
	status int
	ct     string
	loc    string
	body   string
	dur    time.Duration
	err    string
}

func fetch(c *http.Client, base, path, cookie string) result {
	req, _ := http.NewRequest("GET", base+path, nil)
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	req.Header.Set("User-Agent", "shadowdiff")
	t := time.Now()
	resp, err := c.Do(req)
	if err != nil {
		return result{err: err.Error(), dur: time.Since(t)}
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return result{status: resp.StatusCode, ct: resp.Header.Get("Content-Type"), loc: resp.Header.Get("Location"), body: string(b), dur: time.Since(t)}
}

var (
	labelSort = h.SortAdjacent(`<div class="mono" style="font-size:.8rem">[^<]*</div>`)
	diskRows  = h.SortAdjacent(`(?s)<tr><td class="mono">(?:ada|da|nvd|nvme|ad|vtbd|mfid|nda)\d+</td>.*?</tr>`)
	diskOpts  = h.SortAdjacent(`<option value="(?:ada|da|nvd|nvme|ad|vtbd|mfid|nda)\d+">[^<]*</option>`)
)

func normalize(r result) string {
	b := r.body
	switch {
	case strings.Contains(r.ct, "html"):
		b = diskOpts(diskRows(labelSort(h.NormHTML(b))))
	case strings.Contains(r.ct, "json"):
		var v any
		if json.Unmarshal([]byte(b), &v) == nil {
			nb, _ := json.Marshal(v) // 키 정렬
			b = string(nb)
		}
	}
	return fmt.Sprintf("%d|%s|%s|%s", r.status, r.ct, r.loc, b)
}

func firstDiff(a, b string) string {
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	lo := i - 120
	if lo < 0 {
		lo = 0
	}
	cut := func(s string) string {
		hi := i + 160
		if hi > len(s) {
			hi = len(s)
		}
		if lo > len(s) {
			return ""
		}
		return s[lo:hi]
	}
	return fmt.Sprintf("@%d\n  A: %q\n  B: %q", i, cut(a), cut(b))
}

func sign(secret, v string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(v))
	return v + "." + hex.EncodeToString(m.Sum(nil))
}

type stat struct {
	path   string
	da, db time.Duration
}

func main() {
	mode := flag.String("mode", "app", "app | hostd")
	a := flag.String("a", "", "A(Rust) 주소 또는 소켓")
	b := flag.String("b", "", "B(Go) 주소 또는 소켓")
	secret := flag.String("secret", "", "SESSION_SECRET(app)")
	user := flag.String("user", "shadow", "쿠키 사용자")
	rounds := flag.Int("rounds", 1, "같은 목록 반복 횟수")
	flag.Parse()

	var ca, cb *http.Client
	var baseA, baseB, cookie string
	if *mode == "hostd" {
		mk := func(sock string) *http.Client {
			return &http.Client{Timeout: 120 * time.Second, Transport: &http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", sock)
			}}}
		}
		ca, cb, baseA, baseB = mk(*a), mk(*b), "http://hostd", "http://hostd"
	} else {
		mk := func() *http.Client {
			return &http.Client{Timeout: 120 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
				Transport: &http.Transport{DisableKeepAlives: true}}
		}
		ca, cb, baseA, baseB = mk(), mk(), *a, *b
		cookie = "pfsession=" + sign(*secret, *user+":admin")
	}

	paths := appPaths(ca, baseA, cookie)
	if *mode == "hostd" {
		paths = hostdPaths(ca, baseA)
	}
	diffs, total := 0, 0
	var stats []stat
	for round := 0; round < *rounds; round++ {
		for _, p := range paths {
			var ra, rb result
			var wg sync.WaitGroup
			wg.Add(2)
			go func() { defer wg.Done(); ra = fetch(ca, baseA, p, cookie) }()
			go func() { defer wg.Done(); rb = fetch(cb, baseB, p, cookie) }()
			wg.Wait()
			total++
			stats = append(stats, stat{p, ra.dur, rb.dur})
			na, nb := normalize(ra), normalize(rb)
			if ra.err != "" || rb.err != "" {
				diffs++
				fmt.Printf("DIFF %s 전송 오류 A=%q B=%q\n", p, ra.err, rb.err)
				continue
			}
			if na != nb {
				diffs++
				fmt.Printf("DIFF %s (A %d %d바이트, B %d %d바이트) %s\n", p, ra.status, len(ra.body), rb.status, len(rb.body), firstDiff(na, nb))
			}
		}
	}
	sort.Slice(stats, func(i, j int) bool { return stats[i].da > stats[j].da })
	fmt.Printf("\n요청 %d건(경로 %d × %d회), 차이 %d건\n", total, len(paths), *rounds, diffs)
	pct := func(xs []time.Duration, q float64) time.Duration {
		sort.Slice(xs, func(i, j int) bool { return xs[i] < xs[j] })
		return xs[int(float64(len(xs)-1)*q)]
	}
	var as, bs []time.Duration
	for _, s := range stats {
		as, bs = append(as, s.da), append(bs, s.db)
	}
	fmt.Printf("지연 A(Rust) p50 %v p95 %v max %v / B(Go) p50 %v p95 %v max %v\n", pct(as, .5), pct(as, .95), pct(as, 1), pct(bs, .5), pct(bs, .95), pct(bs, 1))
	fmt.Println("느린 경로(A 기준 상위 8):")
	for i, s := range stats {
		if i >= 8 {
			break
		}
		fmt.Printf("  %-60s A %-10v B %v\n", s.path, s.da.Round(time.Millisecond), s.db.Round(time.Millisecond))
	}
	if diffs > 0 {
		os.Exit(1)
	}
}

func appPaths(c *http.Client, base, cookie string) []string {
	ps := []string{"/", "/bastille", "/login", "/containers", "/containers/new", "/system", "/filesystem", "/storage", "/snapshots",
		"/files", "/files?path=/zdata", "/files?path=/zdata&hidden=1&sort=mtime&order=desc", "/files?path=/zdata/apps",
		"/api/files/list?path=/zdata", "/api/files/list?path=/zdata/apps", "/localusers", "/shares", "/shares?tab=smb", "/shares?tab=nfs",
		"/shares?tab=users", "/services", "/network", "/appjail", "/diagnostics", "/api/diag/cmd?cmd=df", "/api/diag/cmd?cmd=mount",
		"/api/diag/cmd?cmd=netstat", "/api/diag/cmd?cmd=bogus", "/api/diag/host-log?file=dmesg&lines=50", "/api/diag/host-log?file=bogus",
		"/desktop", "/api/desktop/prefs", "/metrics", "/alerts", "/profile", "/replications", "/schedules", "/admin/users", "/nope",
		"/files/edit?path=/opt/pfortainer-rs/Cargo.toml", "/api/files/download?path=/opt/pfortainer-rs/Cargo.toml", "/files/edit?path=/zdata"}
	for _, ch := range []string{"system.cpu", "system.ram", "system.load", "net.igc0", "zfspool.zdata", "zfspool.zboot", "ipv4.tcpsock", "zfs.arc_size"} {
		ps = append(ps, "/api/metrics/data?chart="+ch, "/api/metrics/data?chart="+ch+"&after=-86400&points=60")
	}
	seen := map[string]bool{}
	for _, p := range ps {
		seen[p] = true
	}
	add := func(page string, re *regexp.Regexp) {
		r := fetch(c, base, page, cookie)
		for _, m := range re.FindAllStringSubmatch(r.body, -1) {
			p := m[1]
			if !seen[p] {
				seen[p] = true
				ps = append(ps, p)
			}
		}
	}
	add("/containers", regexp.MustCompile(`href="(/containers/[0-9a-f]{12,})"`))
	add("/appjail", regexp.MustCompile(`href="(/appjail/[A-Za-z0-9_-]+)"`))
	r := fetch(c, base, "/api/files/list?path=/zdata/apps", cookie)
	var fl struct {
		Entries []struct {
			Path  string `json:"path"`
			IsDir bool   `json:"isDir"`
		} `json:"entries"`
	}
	json.Unmarshal([]byte(r.body), &fl)
	for i, e := range fl.Entries {
		if i >= 6 {
			break
		}
		ps = append(ps, "/files?path="+url.QueryEscape(e.Path))
	}
	return ps
}

func hostdPaths(c *http.Client, base string) []string {
	ps := []string{"/sockstat", "/jls", "/zpool-status", "/zpool-list", "/zfs-list", "/zfs-list-snapshot", "/zfs-list-snapshot?dataset=zdata%2Fapps",
		"/smart-scan", "/localusers/status", "/nfs/status", "/smb/status", "/network/status", "/appjail/list", "/diag/cmd?cmd=df",
		"/diag/cmd?cmd=mount", "/diag/cmd?cmd=bogus", "/diag/log?file=dmesg&lines=100", "/diag/log?file=bogus",
		"/fs/list?path=%2F&hidden=0&sort=name&order=asc", "/fs/list?path=%2Fzdata&hidden=1&sort=mtime&order=desc",
		"/fs/list?path=%2Fzdata%2Fapps&hidden=0&sort=name&order=asc", "/fs/stat?path=%2Fzdata%2Fapps", "/fs/stat?path=%2Fnope",
		"/fs/read?path=%2Fetc%2Fresolv.conf", "/ps", "/smart?dev=bad"}
	r := fetch(c, base, "/jls", "")
	for _, l := range strings.Split(r.body, "\n") {
		f := strings.Fields(l)
		if len(f) == 2 {
			ps = append(ps, "/ps?jid="+f[0])
		}
	}
	r = fetch(c, base, "/smart-scan", "")
	for _, m := range regexp.MustCompile(`/dev/((?:ada|nvme|nda)\d+)`).FindAllStringSubmatch(r.body, -1) {
		ps = append(ps, "/smart?dev="+m[1])
	}
	r = fetch(c, base, "/appjail/list", "")
	var al struct {
		Jails []struct {
			Name string `json:"name"`
		} `json:"jails"`
	}
	json.Unmarshal([]byte(r.body), &al)
	for _, j := range al.Jails {
		ps = append(ps, "/appjail/detail?name="+j.Name)
	}
	return ps
}
