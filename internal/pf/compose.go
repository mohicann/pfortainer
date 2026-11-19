package pf

import (
	"bufio"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/mohicann/pfortainer/internal/httpx"
)

// lineSink: NDJSON 줄을 순서대로 내보내는 곳(스트리밍 응답).
type lineSink struct {
	mu sync.Mutex
	w  http.ResponseWriter
	f  http.Flusher
}

func newNDJSON(w http.ResponseWriter) *lineSink {
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	f, _ := w.(http.Flusher)
	if f != nil {
		f.Flush()
	}
	return &lineSink{w: w, f: f}
}

func (s *lineSink) raw(line string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	io.WriteString(s.w, line)
	if s.f != nil {
		s.f.Flush()
	}
}

// emit: hostd::emit — json!({"type","line"}) → 키 정렬 {"line":…,"type":…}.
func (s *lineSink) emit(ltype, line string) {
	s.raw(httpx.MarshalString(map[string]string{"type": ltype, "line": line}) + "\n")
}

func emitLine(s *lineSink, line string) {
	lower := strings.ToLower(line)
	t := "log"
	if strings.Contains(lower, "error") || strings.Contains(lower, "failed") {
		t = "error"
	}
	s.emit(t, line)
}

// streamComposeUp: podman-compose -f <path> up -d 출력을 줄 단위로.
func streamComposeUp(path string, s *lineSink) {
	cmd := exec.Command("podman-compose", "-f", path, "up", "-d")
	so, _ := cmd.StdoutPipe()
	se, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		s.emit("error", "podman-compose 실행 실패: "+rustIOErr(err))
		s.emit("done", "1")
		return
	}
	var wg sync.WaitGroup
	read := func(r io.Reader) {
		defer wg.Done()
		br := bufio.NewReader(r)
		for {
			line, err := br.ReadString('\n')
			if line != "" {
				line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
				emitLine(s, line)
			}
			if err != nil {
				return
			}
		}
	}
	wg.Add(2)
	go read(so)
	go read(se)
	wg.Wait()
	code := "0"
	if cmd.Wait() != nil {
		code = "1"
	}
	s.emit("done", code)
}

func composeTempFile(content []byte) (string, func(), error) {
	dir, err := os.MkdirTemp("", "pfortainer-compose-")
	if err != nil {
		return "", nil, err
	}
	p := filepath.Join(dir, "docker-compose.yml")
	if err := os.WriteFile(p, content, 0o644); err != nil {
		os.RemoveAll(dir)
		return "", nil, err
	}
	return p, func() { os.RemoveAll(dir) }, nil
}
