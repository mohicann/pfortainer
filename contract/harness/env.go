// Package harness는 pfortainer 계약 테스트(Rust ↔ Go 블랙박스 비교)의 공통 도구다.
//
// 테스트는 바이너리 경로만 받는다(SUT_BIN). 골든은 Rust 빌드로만 기록한다.
package harness

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// RepoRoot: 이 Go 저장소의 루트.
func RepoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

// ContractDir: contract/ 디렉터리.
func ContractDir() string { return filepath.Join(RepoRoot(), "contract") }

func RustRepo() string {
	if v := os.Getenv("RUST_REPO"); v != "" {
		return v
	}
	return filepath.Join(filepath.Dir(RepoRoot()), "pfortainer-rs")
}

func RustBin() string {
	if v := os.Getenv("RUST_BIN"); v != "" {
		return v
	}
	return filepath.Join(RustRepo(), "target", "release", "pfortainer")
}

// SUTBin: 테스트 대상 바이너리. 기본은 Rust.
func SUTBin() string {
	if v := os.Getenv("SUT_BIN"); v != "" {
		return v
	}
	return RustBin()
}

// SUTIsRust: 골든 기록(-update)은 Rust일 때만 허용한다.
func SUTIsRust() bool {
	a, _ := filepath.Abs(SUTBin())
	b, _ := filepath.Abs(RustBin())
	return a == b
}

func requireBin(t testing.TB) string {
	t.Helper()
	bin := SUTBin()
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("SUT 바이너리 없음: %s (Rust는 pfortainer-rs에서 cargo build --release)", bin)
	}
	return bin
}
