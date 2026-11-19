package pf

import (
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	localOnce sync.Once
	localZone = time.UTC
)

// localOffset: `date +%z` 결과를 한 번만 계산(Rust localtime.rs와 같음).
func toLocal(t time.Time) time.Time {
	localOnce.Do(func() {
		out, err := exec.Command("date", "+%z").Output()
		if err != nil {
			return
		}
		s := strings.TrimSpace(string(out))
		if len(s) != 5 {
			return
		}
		sign := 1
		if s[0] == '-' {
			sign = -1
		}
		h, e1 := strconv.Atoi(s[1:3])
		m, e2 := strconv.Atoi(s[3:5])
		if e1 != nil || e2 != nil {
			return
		}
		localZone = time.FixedZone("", sign*(h*3600+m*60))
	})
	return t.In(localZone)
}
