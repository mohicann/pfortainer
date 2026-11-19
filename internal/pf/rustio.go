package pf

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// rustIOErr: Rust std::io::Error Display("{strerror} (os error N)").
func rustIOErr(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, exec.ErrNotFound) {
		return "No such file or directory (os error 2)"
	}
	var en syscall.Errno
	if errors.As(err, &en) {
		s := en.Error()
		if s != "" {
			s = strings.ToUpper(s[:1]) + s[1:]
		}
		return fmt.Sprintf("%s (os error %d)", s, int(en))
	}
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return rustIOErr(pe.Err)
	}
	return err.Error()
}

// rustExitStatus: Rust ExitStatus Display.
func rustExitStatus(ps *os.ProcessState) string {
	if ps == nil {
		return "exit status: unknown"
	}
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		sig := ws.Signal()
		name := sigName(sig)
		if ws.CoreDump() {
			return fmt.Sprintf("signal: %d (%s) (core dumped)", int(sig), name)
		}
		return fmt.Sprintf("signal: %d (%s)", int(sig), name)
	}
	return fmt.Sprintf("exit status: %d", ps.ExitCode())
}

func sigName(s syscall.Signal) string {
	names := map[syscall.Signal]string{syscall.SIGHUP: "SIGHUP", syscall.SIGINT: "SIGINT", syscall.SIGQUIT: "SIGQUIT", syscall.SIGKILL: "SIGKILL",
		syscall.SIGPIPE: "SIGPIPE", syscall.SIGTERM: "SIGTERM", syscall.SIGSEGV: "SIGSEGV", syscall.SIGABRT: "SIGABRT", syscall.SIGALRM: "SIGALRM", syscall.SIGUSR1: "SIGUSR1", syscall.SIGUSR2: "SIGUSR2"}
	if n, ok := names[s]; ok {
		return n
	}
	return fmt.Sprintf("SIG%d", int(s))
}

// cmdResult: Command::output() 결과.
type cmdResult struct {
	stdout, stderr []byte
	ok             bool
	status         string // ExitStatus Display
	spawnErr       string // 실행 자체 실패(io::Error Display)
}

func runOutput(name string, args []string, stdin []byte) cmdResult {
	cmd := exec.Command(name, args...)
	var so, se strings.Builder
	cmd.Stdout, cmd.Stderr = &so, &se
	if stdin != nil {
		cmd.Stdin = strings.NewReader(string(stdin))
	}
	err := cmd.Run()
	r := cmdResult{stdout: []byte(so.String()), stderr: []byte(se.String())}
	var ee *exec.ExitError
	switch {
	case err == nil:
		r.ok, r.status = true, "exit status: 0"
	case errors.As(err, &ee):
		r.status = rustExitStatus(ee.ProcessState)
	default:
		r.spawnErr = rustIOErr(err)
	}
	return r
}
