package pf

import (
	"encoding/binary"
	"os"
	"testing"
)

// struct loadavg (amd64): u32 ldavg[3] + 4바이트 패딩 + i64 fscale.
func TestParseLoadavg(t *testing.T) {
	b := make([]byte, 24)
	binary.LittleEndian.PutUint32(b[0:], 2048)
	binary.LittleEndian.PutUint32(b[4:], 1024)
	binary.LittleEndian.PutUint32(b[8:], 512)
	binary.LittleEndian.PutUint64(b[16:], 2048)
	got := parseLoadavg(b)
	if got != [3]float64{1, 0.5, 0.25} {
		t.Fatalf("loadavg %v", got)
	}
	if parseLoadavg(b[:20]) != [3]float64{} {
		t.Fatal("짧은 버퍼는 0")
	}
}

// struct devstat(288바이트): 이름 44..60, 읽기 72, 쓰기 80. pass*는 제외.
func TestParseDevstat(t *testing.T) {
	const size = 288
	all := make([]byte, 8+3*size)
	put := func(i int, name string, rd, wr uint64) {
		base := all[8+i*size:]
		copy(base[44:60], name)
		binary.LittleEndian.PutUint64(base[72:], rd)
		binary.LittleEndian.PutUint64(base[80:], wr)
		binary.LittleEndian.PutUint64(base[64:], 999) // NO_DATA(무시)
	}
	put(0, "ada", 100, 10)
	put(1, "pass", 5000, 5000)
	put(2, "nda", 1, 2)
	if got := parseDevstat(3, all); got != [2]uint64{101, 12} {
		t.Fatalf("devstat %v", got)
	}
}

// fbnas 실제 netstat -ibn 출력(<Link#N> 대문자, 바이트 칸은 오른쪽에서 셈).
func TestParseNetstatIbn(t *testing.T) {
	b, err := os.ReadFile("../../contract/fixtures/cmd/6.out")
	if err != nil {
		t.Skip(err)
	}
	m := parseNetstatIbn(string(b))
	if _, ok := m["igc3"]; !ok || len(m) < 3 {
		t.Fatalf("netstat 파싱: %v", m)
	}
	if v := m["igc3"]; v[0] == 0 || v[1] == 0 {
		t.Fatalf("igc3 바이트 0: %v", v)
	}
}
