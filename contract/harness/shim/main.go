// shim: hostd 계약 테스트용 가짜 명령. 심볼릭 링크 이름(argv[0])이 명령 이름이다.
//
// $SHIM_DIR/rules.json 에서 "명령 인자…" 정확히 일치 → "명령 *" 순으로 규칙을 찾고,
// 호출 내용(명령·인자·stdin)을 $SHIM_DIR/calls.jsonl 에 한 줄씩 남긴다.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type rule struct {
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`
	Exit      int    `json:"exit"`
	DelayMs   int    `json:"delay_ms"`
	EchoStdin bool   `json:"echo_stdin"` // stdin을 그대로 stdout으로(파이프 검증용)
}

func main() {
	dir := os.Getenv("SHIM_DIR")
	name := filepath.Base(os.Args[0])
	args := os.Args[1:]
	stdin, _ := io.ReadAll(os.Stdin)

	rec, _ := json.Marshal(map[string]any{"cmd": name, "args": args, "stdin": string(stdin)})
	if f, err := os.OpenFile(filepath.Join(dir, "calls.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
		f.Write(append(rec, '\n'))
		f.Close()
	}

	var rules map[string]rule
	if b, err := os.ReadFile(filepath.Join(dir, "rules.json")); err == nil {
		json.Unmarshal(b, &rules)
	}
	key := strings.Join(append([]string{name}, args...), " ")
	r, ok := rules[key]
	// 정확히 맞는 규칙이 없으면 뒤에서부터 인자를 줄여 "명령 앞인자… *"를 찾는다
	for n := len(args); !ok && n >= 0; n-- {
		r, ok = rules[strings.Join(append([]string{name}, args[:n]...), " ")+" *"]
	}
	if !ok {
		fmt.Fprintf(os.Stderr, "shim: no rule for %q\n", key)
		os.Exit(127)
	}
	if r.DelayMs > 0 {
		time.Sleep(time.Duration(r.DelayMs) * time.Millisecond)
	}
	if r.EchoStdin {
		os.Stdout.Write(stdin)
	}
	io.WriteString(os.Stdout, r.Stdout)
	io.WriteString(os.Stderr, r.Stderr)
	os.Exit(r.Exit)
}
