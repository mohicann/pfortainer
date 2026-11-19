package pf

import (
	"os"
	"path/filepath"
	"testing"
)

// templates/는 pfortainer-rs templates/의 사본이어야 한다(전환 전까지 Rust가 원본).
func TestTemplatesMatchRust(t *testing.T) {
	rs := filepath.Join("..", "..", "..", "pfortainer-rs", "templates")
	if v := os.Getenv("RUST_REPO"); v != "" {
		rs = filepath.Join(v, "templates")
	}
	files, err := filepath.Glob(filepath.Join(rs, "*.html"))
	if err != nil || len(files) == 0 {
		t.Skip("pfortainer-rs 저장소 없음")
	}
	ours, _ := filepath.Glob("templates/*.html")
	if len(ours) != len(files) {
		t.Errorf("템플릿 개수 다름: Go %d, Rust %d", len(ours), len(files))
	}
	for _, f := range files {
		a, _ := os.ReadFile(f)
		b, err := os.ReadFile(filepath.Join("templates", filepath.Base(f)))
		if err != nil || string(a) != string(b) {
			t.Errorf("%s: Rust와 다름 — cp %s internal/pf/templates/", filepath.Base(f), f)
		}
	}
}
