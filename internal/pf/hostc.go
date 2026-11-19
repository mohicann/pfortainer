package pf

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// HostError: hostc::HostError Display.
type HostError struct {
	Unavailable bool
	Msg         string
}

func (e *HostError) Error() string {
	if e.Unavailable {
		return "host agent unavailable (socket not found)"
	}
	return e.Msg
}

func hostOther(msg string) *HostError { return &HostError{Msg: msg} }

type HostClient struct {
	sock string
	hc   *http.Client
}

func NewHostClient(sock string) *HostClient {
	return &HostClient{sock: sock, hc: &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		},
		DisableCompression: true,
	}}}
}

func (h *HostClient) socketExists() bool { _, err := os.Stat(h.sock); return err == nil }

func (h *HostClient) AgentMode() string {
	if h.socketExists() {
		return "host-socket"
	}
	return "local-exec"
}

func (h *HostClient) do(method, path, ct string, body []byte) (*http.Response, error) {
	req, _ := http.NewRequest(method, "http://localhost"+path, bytes.NewReader(body))
	if method == http.MethodGet {
		req.Body = http.NoBody
		req.ContentLength = 0
	}
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	resp, err := h.hc.Do(req)
	if err != nil {
		return nil, hostOther("client error (Connect)")
	}
	return resp, nil
}

func (h *HostClient) agentGet(path string) ([]byte, error) {
	resp, err := h.do("GET", path, "", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, hostOther(strings.TrimSpace(string(b)))
	}
	return b, nil
}

func (h *HostClient) agentPost(path string, body []byte) ([]byte, error) {
	resp, err := h.do("POST", path, "application/json", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, hostOther(strings.TrimSpace(string(b)))
	}
	return b, nil
}

// GetAgent: hostd 전용(로컬 폴백 없음).
func (h *HostClient) GetAgent(path string) ([]byte, error) {
	if !h.socketExists() {
		return nil, &HostError{Unavailable: true}
	}
	return h.agentGet(path)
}

// Post: hostd 전용 쓰기.
func (h *HostClient) Post(path string, body []byte) ([]byte, error) {
	if !h.socketExists() {
		return nil, &HostError{Unavailable: true}
	}
	return h.agentPost(path, body)
}

// PostStream: compose-up(NDJSON 릴레이).
func (h *HostClient) PostStream(path string, body []byte) (io.ReadCloser, error) {
	if !h.socketExists() {
		return nil, &HostError{Unavailable: true}
	}
	resp, err := h.do("POST", path, "text/plain", body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, hostOther(statusLine(resp.StatusCode) + ": " + strings.TrimSpace(string(b)))
	}
	return resp.Body, nil
}

// statusLine: Rust StatusCode Display("500 Internal Server Error").
func statusLine(code int) string {
	return strings.TrimSpace(strings.Join([]string{itoa(code), http.StatusText(code)}, " "))
}

// Get: hostd가 있으면 경유, 없으면 로컬 실행.
func (h *HostClient) Get(path string, cmd []string) ([]byte, error) {
	if h.socketExists() {
		return h.agentGet(path)
	}
	if len(cmd) == 0 {
		return nil, hostOther("empty command")
	}
	r := runOutput(cmd[0], cmd[1:], []byte{})
	if r.spawnErr != "" {
		return nil, hostOther(r.spawnErr)
	}
	if !r.ok {
		msg := strings.TrimSpace(string(r.stderr))
		if msg == "" {
			msg = "command failed: " + r.status
		}
		return nil, hostOther(msg)
	}
	return r.stdout, nil
}

var _ = time.Second
