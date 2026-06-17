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

type containerPort struct {
	Name  string
	Proto string // "TCP" | "UDP"
}

// containerPubPorts returns "port:PROTO" → containerPort for running containers.
// Using proto in the key correctly handles containers that publish the same port
// number on both tcp and udp (e.g. DNS: -p 53:53/tcp -p 53:53/udp).
func (h *handlers) containerPubPorts() (map[string]containerPort, []ServiceEntry) {
	byKey := make(map[string]containerPort)
	var direct []ServiceEntry // entries to inject directly (not via sockstat)

	cs, err := h.pc.ListContainers()
	if err != nil {
		return byKey, direct
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
			if p.HostPort == 0 {
				continue
			}
			proto := strings.ToUpper(p.Protocol)
			if proto == "" {
				proto = "TCP"
			}
			key := strconv.Itoa(int(p.HostPort)) + ":" + proto
			byKey[key] = containerPort{Name: name, Proto: proto}

			// Pre-build a direct entry; we'll deduplicate against sockstat later.
			direct = append(direct, ServiceEntry{
				Port:    int(p.HostPort),
				Proto:   proto,
				Addr:    "0.0.0.0",
				Type:    "container",
				Name:    name,
				Command: "—",
				User:    "—",
			})
		}
	}
	return byKey, direct
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

// ── Debug ─────────────────────────────────────────────────────────────────────

func (h *handlers) servicesDebug(w http.ResponseWriter, r *http.Request) {
	type debugOut struct {
		Containers []map[string]any `json:"containers"`
		Sockstat   []ServiceEntry   `json:"sockstat"`
		PidJIDs    map[int]int      `json:"pidJIDs"`
		Jails      map[int]string   `json:"jails"`
		Error      string           `json:"error,omitempty"`
	}
	out := debugOut{}

	cs, err := h.pc.ListContainers()
	if err != nil {
		out.Error = "ListContainers: " + err.Error()
	}
	for _, c := range cs {
		name := "unnamed"
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		ports := make([]map[string]any, 0, len(c.Ports))
		for _, p := range c.Ports {
			ports = append(ports, map[string]any{
				"host_port": p.HostPort, "container_port": p.ContainerPort,
				"protocol": p.Protocol, "host_ip": p.HostIP,
			})
		}
		out.Containers = append(out.Containers, map[string]any{
			"name": name, "state": c.State, "ports": ports,
		})
	}

	sock, sockErr := listeningSockets()
	if sockErr != nil {
		out.Error += " | sockstat: " + sockErr.Error()
	}
	out.Sockstat = sock
	out.PidJIDs = pidJIDs()
	out.Jails = jailNames()

	writeJSON(w, http.StatusOK, out)
}

// ── Handler ───────────────────────────────────────────────────────────────────

func (h *handlers) servicesInfo(w http.ResponseWriter, r *http.Request) {
	vm := ServicesVM{ActivePage: "services"}

	// Gather all data sources.
	sockEntries, sockErr := listeningSockets()
	cByKey, cDirect := h.containerPubPorts() // from Podman API
	pidJID := pidJIDs()
	jails := jailNames()

	if sockErr != nil {
		vm.FetchError = "sockstat 실행 실패: " + sockErr.Error()
	}

	// Step 1: classify sockstat entries.
	// "port:PROTO" keys that were already matched to a container.
	coveredByContainer := make(map[string]bool)

	var entries []ServiceEntry
	for i := range sockEntries {
		e := sockEntries[i]
		key := strconv.Itoa(e.Port) + ":" + e.Proto

		// 1. Container: port+proto matches a Podman published port
		if cp, ok := cByKey[key]; ok {
			e.Type = "container"
			e.Name = cp.Name
			coveredByContainer[key] = true
		} else if jid := pidJID[e.PID]; jid > 0 {
			// 2. Jail: process runs inside a FreeBSD jail
			e.Type = "jail"
			if jname, ok := jails[jid]; ok {
				e.Name = jname
			} else {
				e.Name = "JID:" + strconv.Itoa(jid)
			}
		} else {
			// 3. Native
			e.Type = "native"
			e.Name = e.Command
		}
		entries = append(entries, e)
	}

	// Step 2: add container ports that sockstat didn't expose.
	// On FreeBSD, Podman may forward ports via pf(4) rules rather than binding
	// a user-space socket, so those ports never appear in sockstat output.
	for _, ce := range cDirect {
		key := strconv.Itoa(ce.Port) + ":" + ce.Proto
		if !coveredByContainer[key] {
			entries = append(entries, ce)
		}
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
