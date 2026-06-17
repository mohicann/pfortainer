package main

import (
	"bufio"
	"bytes"
	"net/http"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

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
	ActivePage string
	Services   []ServiceEntry
	FetchError string
}

// ── Data collection ───────────────────────────────────────────────────────────

// containerHostPorts returns hostPort → containerName for running containers.
func (h *handlers) containerHostPorts() map[int]string {
	m := make(map[int]string)
	cs, err := h.pc.ListContainers()
	if err != nil {
		return m
	}
	for _, c := range cs {
		if strings.ToLower(c.State) != "running" {
			continue
		}
		name := "unnamed"
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		for _, p := range c.Ports {
			if p.HostPort > 0 {
				m[int(p.HostPort)] = name
			}
		}
	}
	return m
}

// jailNames returns jid → jail name via `jls jid name`.
func jailNames() map[int]string {
	m := make(map[int]string)
	out, err := exec.Command("jls", "jid", "name").Output()
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
			continue // skip header ("JID") or bad lines
		}
		m[jid] = f[1]
	}
	return m
}

// pidJIDs returns pid → jid (0 = not in a jail) via `ps`.
func pidJIDs() map[int]int {
	m := make(map[int]int)
	out, err := exec.Command("ps", "-ax", "-o", "pid=,jid=").Output()
	if err != nil {
		return m
	}
	s := bufio.NewScanner(bytes.NewReader(out))
	for s.Scan() {
		f := strings.Fields(s.Text())
		if len(f) < 2 {
			continue
		}
		pid, e1 := strconv.Atoi(f[0])
		jid, e2 := strconv.Atoi(f[1])
		if e1 != nil || e2 != nil {
			continue
		}
		m[pid] = jid
	}
	return m
}

// listeningSockets parses `sockstat -l` and returns deduplicated entries.
// Dedup key: command + port + protoFamily — collapses tcp4/tcp6 duplicates.
func listeningSockets() ([]ServiceEntry, error) {
	out, err := exec.Command("sockstat", "-l").Output()
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

		// Strip 4/6 suffix to get proto family
		protoFam := proto
		if strings.HasSuffix(protoFam, "4") || strings.HasSuffix(protoFam, "6") {
			protoFam = protoFam[:len(protoFam)-1]
		}

		key := f[1] + ":" + strconv.Itoa(port) + ":" + protoFam
		if seen[key] {
			continue
		}
		seen[key] = true

		entries = append(entries, ServiceEntry{
			Port:    port,
			Proto:   strings.ToUpper(protoFam),
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
	vm := ServicesVM{ActivePage: "services"}

	entries, err := listeningSockets()
	if err != nil {
		vm.FetchError = "sockstat 실행 실패: " + err.Error()
		render(w, "services", vm)
		return
	}

	// Lookup tables
	cPorts := h.containerHostPorts()
	pidJID := pidJIDs()
	jails := jailNames()

	for i := range entries {
		e := &entries[i]

		// 1. Container: port matches a published container host port
		if name, ok := cPorts[e.Port]; ok {
			e.Type = "container"
			e.Name = name
			continue
		}

		// 2. Jail: process has a non-zero JID
		if jid := pidJID[e.PID]; jid > 0 {
			e.Type = "jail"
			if name, ok := jails[jid]; ok {
				e.Name = name
			} else {
				e.Name = "JID:" + strconv.Itoa(jid)
			}
			continue
		}

		// 3. Native
		e.Type = "native"
		e.Name = e.Command
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Port != entries[j].Port {
			return entries[i].Port < entries[j].Port
		}
		return entries[i].Proto < entries[j].Proto
	})

	vm.Services = entries
	render(w, "services", vm)
}
