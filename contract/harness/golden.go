package harness

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "골든 파일 다시 기록 (SUT가 Rust일 때만)")

// GoldenPath: contract/testdata/golden/<name>.json
func GoldenPath(name string) string {
	return filepath.Join(ContractDir(), "testdata", "golden", name+".json")
}

var slugRe = regexp.MustCompile(`[^A-Za-z0-9_.-]+`)

// Slug: 골든 파일 이름으로 쓸 수 있게 바꾼다.
func Slug(s string) string { return strings.Trim(slugRe.ReplaceAllString(s, "_"), "_") }

// Golden: v를 JSON으로 직렬화해 골든과 비교한다.
func Golden(t testing.TB, name string, v any) {
	t.Helper()
	got, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	p := GoldenPath(name)
	if *update {
		// 2026-11-19 전환 이후 Go가 원본: 골든은 Go 빌드로 기록한다(Rust 저장소는 보관됨).
		if SUTIsRust() {
			t.Fatalf("-update는 Go(SUT_BIN=bin/pfortainer)로만 기록 — Rust는 보관된 구현")
		}
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("골든 없음 %s (Rust로 -update 먼저): %v\n--- got ---\n%s", p, err, got)
	}
	if string(want) != string(got) {
		gp := filepath.Join(os.TempDir(), "pf-golden-got", name+".json")
		os.MkdirAll(filepath.Dir(gp), 0o755)
		os.WriteFile(gp, got, 0o644)
		t.Errorf("골든 불일치 %s (받은 값: %s)\n%s", name, gp, LineDiff(string(want), string(got)))
	}
}

// LineDiff: 간단한 줄 단위 diff(앞뒤 공통부분 제외, 최대 80줄).
func LineDiff(want, got string) string {
	w := strings.Split(want, "\n")
	g := strings.Split(got, "\n")
	i := 0
	for i < len(w) && i < len(g) && w[i] == g[i] {
		i++
	}
	j := 0
	for j < len(w)-i && j < len(g)-i && w[len(w)-1-j] == g[len(g)-1-j] {
		j++
	}
	var b strings.Builder
	fmt.Fprintf(&b, "@@ 줄 %d @@\n", i+1)
	n := 0
	for _, l := range w[i : len(w)-j] {
		if n++; n > 40 {
			b.WriteString("- ...\n")
			break
		}
		b.WriteString("- " + l + "\n")
	}
	n = 0
	for _, l := range g[i : len(g)-j] {
		if n++; n > 40 {
			b.WriteString("+ ...\n")
			break
		}
		b.WriteString("+ " + l + "\n")
	}
	return b.String()
}
