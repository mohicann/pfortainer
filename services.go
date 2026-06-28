package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

// hostGetAgent fetches a path from the host-agent socket only — no local exec fallback.
// Use for endpoints that have no meaningful local equivalent (SMB, NFS, etc.).
func hostGetAgent(path string) ([]byte, error) {
	hc := hostdClient()
	if hc == nil {
		return nil, fmt.Errorf("host agent unavailable (socket not found)")
	}
	resp, err := hc.Get("http://hostd" + path)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s", strings.TrimSpace(string(b)))
	}
	return b, nil
}

// hostPost sends a JSON POST to the host-agent socket and returns the response body.
// It does NOT fall back to local exec — write operations must go through the agent.
func hostPost(path string, body []byte) ([]byte, error) {
	hc := hostdClient()
	if hc == nil {
		return nil, fmt.Errorf("host agent unavailable (socket not found)")
	}
	resp, err := hc.Post("http://hostd"+path, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s", strings.TrimSpace(string(b)))
	}
	return b, nil
}

// ── View Models ───────────────────────────────────────────────────────────────

type ServiceEntry struct {
	Port    int
	Proto   string // "TCP" | "UDP"
	Addr    string
	Type    string // "container" | "jail" | "native"
	Name    string // container name, jail name, or command
	Command string
	User    string
	PID     int
}

type ServicesVM struct {
	ActivePage  string
	CurrentUser SessionUser
	Services    []ServiceEntry
	FetchError  string
}

// ── Data collection ───────────────────────────────────────────────────────────

// hostGet fetches a path from the host agent socket, or falls back to running
// cmd directly when the socket is not available (local dev / host execution).
func hostGet(path string, cmd []string) ([]byte, error) {
	if hc := hostdClient(); hc != nil {
		resp, err := hc.Get("http://hostd" + path)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(resp.Body)
			return nil, fmt.Errorf("hostd %s: %s", path, string(b))
		}
		return io.ReadAll(resp.Body)
	}
	return exec.Command(cmd[0], cmd[1:]...).Output()
}

// jailNames returns jid → jail name via `jls jid name`.
// On FreeBSD+Podman the jail name is the full container ID.
func jailNames() map[int]string {
	m := make(map[int]string)
	out, err := hostGet("/jls", []string{"jls", "jid", "name"})
	if err != nil {
		return m
	}
	s := bufio.NewScanner(bytes.NewReader(out))
	for s.Scan() {
		f := strings.Fields(s.Text())
		if len(f) < 2 {
			continue
		}
		jid, err := strconv.Atoi(f[0])
		if err != nil || jid == 0 {
			continue // skip header "JID" line
		}
		m[jid] = f[1]
	}
	return m
}

// pidJIDsFromJails builds PID → JID by running `ps -J <JID>` for each jail.
func pidJIDsFromJails(jailMap map[int]string) map[int]int {
	m := make(map[int]int)
	hc := hostdClient()
	for jid := range jailMap {
		var out []byte
		var err error
		if hc != nil {
			resp, e := hc.Get(fmt.Sprintf("http://hostd/ps?jid=%d", jid))
			if e != nil {
				continue
			}
			out, _ = io.ReadAll(resp.Body)
			resp.Body.Close()
		} else {
			out, err = exec.Command("ps", "-J", strconv.Itoa(jid), "-o", "pid=").Output()
			if err != nil {
				continue
			}
		}
		s := bufio.NewScanner(bytes.NewReader(out))
		for s.Scan() {
			pid, err := strconv.Atoi(strings.TrimSpace(s.Text()))
			if err != nil || pid == 0 {
				continue
			}
			m[pid] = jid
		}
	}
	return m
}

// jidContainerNames matches jail names (= Podman container IDs) to container
// display names, returning JID → containerName for Podman-managed jails.
func (h *handlers) jidContainerNames(jailMap map[int]string) map[int]string {
	result := make(map[int]string)
	cs, err := h.pc.ListContainers()
	if err != nil {
		return result
	}

	// Build containerID → displayName
	idToName := make(map[string]string)
	for _, c := range cs {
		name := "unnamed"
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		idToName[c.ID] = name
	}

	// Match: jail name == container ID (Podman names jails after container IDs)
	for jid, jailName := range jailMap {
		if name, ok := idToName[jailName]; ok {
			result[jid] = name
			continue
		}
		// Prefix match for truncated IDs
		for id, name := range idToName {
			if strings.HasPrefix(id, jailName) || strings.HasPrefix(jailName, id) {
				result[jid] = name
				break
			}
		}
	}
	return result
}

// listeningSockets parses `sockstat -l` and returns deduplicated entries.
// Dedup key: command + port + protoFamily — collapses tcp4/tcp6 duplicates.
func listeningSockets() ([]ServiceEntry, error) {
	out, err := hostGet("/sockstat", []string{"sockstat", "-l"})
	if err != nil {
		return nil, err
	}

	seen := make(map[string]bool)
	var entries []ServiceEntry

	s := bufio.NewScanner(bytes.NewReader(out))
	for s.Scan() {
		f := strings.Fields(s.Text())
		// sockstat -l columns: USER COMMAND PID FD PROTO LOCAL FOREIGN
		if len(f) < 7 || f[0] == "USER" {
			continue
		}

		proto := strings.ToLower(f[4])
		if proto == "unix" {
			continue
		}

		pid, err := strconv.Atoi(f[2])
		if err != nil {
			continue
		}

		// Parse "addr:port" — use LastIndex to handle IPv6 ":::80"
		local := f[5]
		sep := strings.LastIndex(local, ":")
		if sep < 0 {
			continue
		}
		addr := local[:sep]
		if addr == "*" {
			addr = "0.0.0.0"
		}
		port, err := strconv.Atoi(local[sep+1:])
		if err != nil || port <= 0 {
			continue
		}

		// Strip trailing 4/6 to normalize: tcp4/tcp6 → tcp, udp4/udp6 → udp
		protoFam := strings.TrimRight(proto, "46")
		protoDisplay := strings.ToUpper(protoFam)

		// Dedup: same service (command+port+protoFam) → one row
		key := f[1] + ":" + strconv.Itoa(port) + ":" + protoFam
		if seen[key] {
			continue
		}
		seen[key] = true

		entries = append(entries, ServiceEntry{
			Port:    port,
			Proto:   protoDisplay,
			Addr:    addr,
			Command: f[1],
			User:    f[0],
			PID:     pid,
		})
	}

	return entries, nil
}

// ── Handler ───────────────────────────────────────────────────────────────────

func (h *handlers) servicesInfo(w http.ResponseWriter, r *http.Request) {
	vm := ServicesVM{ActivePage: "services", CurrentUser: userFrom(r)}

	sockEntries, sockErr := listeningSockets()
	if sockErr != nil {
		vm.FetchError = "sockstat 실행 실패: " + sockErr.Error()
	}

	jailMap := jailNames()
	containerByJID := h.jidContainerNames(jailMap)
	pidJID := pidJIDsFromJails(jailMap)

	for i := range sockEntries {
		e := &sockEntries[i]

		jid := pidJID[e.PID]
		if jid > 0 {
			if name, ok := containerByJID[jid]; ok {
				// Process is in a Podman container jail
				e.Type = "container"
				e.Name = name
			} else {
				// Process is in a non-container FreeBSD jail
				e.Type = "jail"
				if jname, ok := jailMap[jid]; ok {
					if len(jname) > 12 {
						e.Name = jname[:12] // truncate long container IDs
					} else {
						e.Name = jname
					}
				} else {
					e.Name = "JID:" + strconv.Itoa(jid)
				}
			}
		} else {
			e.Type = "native"
			e.Name = e.Command
		}
	}

	sort.Slice(sockEntries, func(i, j int) bool {
		if sockEntries[i].Port != sockEntries[j].Port {
			return sockEntries[i].Port < sockEntries[j].Port
		}
		return sockEntries[i].Proto < sockEntries[j].Proto
	})

	vm.Services = sockEntries
	render(w, "services", vm)
}
