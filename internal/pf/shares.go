package pf

import (
	"sort"
	"strings"

	"github.com/mohicann/pfortainer/internal/httpx"
)

type NfsExport struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Line    string `json:"line" serde:"default"`
	Clients string `json:"clients" serde:"default"`
}

type NfsStatus struct {
	Running bool        `json:"running" serde:"default"`
	Exports []NfsExport `json:"exports" serde:"default"`
}

type SmbShare struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	Comment    string `json:"comment" serde:"default"`
	ValidUsers string `json:"valid_users" serde:"default"`
	ReadOnly   bool   `json:"read_only" serde:"default"`
	Browseable bool   `json:"browseable" serde:"default"`
	GuestOk    bool   `json:"guest_ok" serde:"default"`
}

type SmbStatus struct {
	Running bool       `json:"running" serde:"default"`
	SetupOk bool       `json:"setup_ok" serde:"default"`
	Shares  []SmbShare `json:"shares" serde:"default"`
}

type LocalUser struct {
	Username string   `json:"username"`
	UID      int64    `json:"uid"`
	GID      int64    `json:"gid"`
	Fullname string   `json:"fullname" serde:"default"`
	Home     string   `json:"home" serde:"default"`
	Shell    string   `json:"shell" serde:"default"`
	Groups   []string `json:"groups" serde:"default"`
	HasSmb   bool     `json:"has_smb" serde:"default"`
}

type LocalGroup struct {
	Name    string   `json:"name"`
	GID     int64    `json:"gid"`
	Members []string `json:"members" serde:"default"`
}

type LocalUsersStatus struct {
	Users  []LocalUser  `json:"users" serde:"default"`
	Groups []LocalGroup `json:"groups" serde:"default"`
}

type NetworkIface struct {
	Name   string   `json:"name" serde:"default"`
	Flags  []string `json:"flags" serde:"default"`
	Mac    string   `json:"mac" serde:"default"`
	Mtu    int32    `json:"mtu" serde:"default"`
	Status string   `json:"status" serde:"default"`
	Inet   []string `json:"inet" serde:"default"`
	Inet6  []string `json:"inet6" serde:"default"`
	Media  string   `json:"media" serde:"default"`
	Up     bool     `json:"up" serde:"default"`
}

type NetworkStatus struct {
	Interfaces []NetworkIface `json:"interfaces" serde:"default"`
	Routes     string         `json:"routes" serde:"default"`
	DNS        []string       `json:"dns" serde:"default"`
}

type JailInfo struct {
	Name     string `json:"name"`
	Status   string `json:"status" serde:"default"`
	Running  bool   `json:"running" serde:"default"`
	JailType string `json:"jail_type" serde:"default"`
	Version  string `json:"version" serde:"default"`
	Boot     bool   `json:"boot" serde:"default"`
	Priority string `json:"priority" serde:"default"`
	IP       string `json:"ip" serde:"default"`
	Ports    string `json:"ports" serde:"default"`
	Created  string `json:"created" serde:"default"`
}

type AppJailStatus struct {
	Jails []JailInfo `json:"jails" serde:"default"`
}

type ListTable struct {
	Headers []string   `json:"headers" serde:"default"`
	Rows    [][]string `json:"rows" serde:"default"`
	Error   string     `json:"error" serde:"default"`
}

type JailProcess struct {
	Pid     int32  `json:"pid"`
	User    string `json:"user"`
	Pcpu    string `json:"pcpu"`
	Rss     string `json:"rss"`
	Command string `json:"command"`
}

type JailDetail struct {
	Info         JailInfo      `json:"info"`
	Path         string        `json:"path" serde:"default"`
	TemplateConf string        `json:"template_conf" serde:"default"`
	Fstab        ListTable     `json:"fstab" serde:"default"`
	Expose       ListTable     `json:"expose" serde:"default"`
	Healthcheck  ListTable     `json:"healthcheck" serde:"default"`
	Dataset      string        `json:"dataset" serde:"default"`
	ZfsUsed      string        `json:"zfs_used" serde:"default"`
	ZfsRefer     string        `json:"zfs_refer" serde:"default"`
	Processes    []JailProcess `json:"processes" serde:"default"`
}

func agentJSON(hc *HostClient, path string, dst any) error {
	body, err := hc.GetAgent(path)
	if err != nil {
		return err
	}
	if err := httpx.Unmarshal(body, dst); err != nil {
		return hostOther(err.Error())
	}
	return nil
}

type outputResp struct {
	Output string `json:"output" serde:"default"`
}

func postOutput(hc *HostClient, path string, body any) (string, error) {
	var b []byte
	if body != nil {
		b = httpx.MarshalJSON(body)
	}
	resp, err := hc.Post(path, b)
	if err != nil {
		return "", err
	}
	var o outputResp
	if httpx.Unmarshal(resp, &o) != nil {
		return "", nil
	}
	return o.Output, nil
}

// ── services ────────────────────────────────────────────────────────────────

type ServiceEntry struct {
	Port    int32  `json:"port"`
	Proto   string `json:"proto"`
	Addr    string `json:"addr"`
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Command string `json:"command"`
	User    string `json:"user"`
	Pid     int32  `json:"pid"`
}

func parseI32(s string) (int32, bool) {
	n, msg := httpx.ParseI64(s)
	if msg != "" || n < -1<<31 || n > 1<<31-1 {
		return 0, false
	}
	return int32(n), true
}

func jailNames(hc *HostClient) map[int32]string {
	m := map[int32]string{}
	out, err := hc.Get("/jls", []string{"jls", "jid", "name"})
	if err != nil {
		return m
	}
	for _, line := range rustLines(lossy(out)) {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		jid, ok := parseI32(f[0])
		if !ok || jid == 0 {
			continue
		}
		m[jid] = f[1]
	}
	return m
}

func pidJids(hc *HostClient, jails map[int32]string) map[int32]int32 {
	m := map[int32]int32{}
	for jid := range jails {
		js := itoa(int(jid))
		out, err := hc.Get("/ps?jid="+js, []string{"ps", "-J", js, "-o", "pid="})
		if err != nil {
			continue
		}
		for _, line := range rustLines(lossy(out)) {
			pid, ok := parseI32(strings.TrimSpace(line))
			if !ok || pid == 0 {
				continue
			}
			m[pid] = jid
		}
	}
	return m
}

func jidContainerNames(pc *PodmanClient, jails map[int32]string) map[int32]string {
	res := map[int32]string{}
	cs, e := pc.ListContainers()
	if e != nil {
		return res
	}
	idToName := map[string]string{}
	for i := range cs {
		idToName[cs[i].ID] = firstName(cs[i].Names)
	}
	for jid, jn := range jails {
		if n, ok := idToName[jn]; ok {
			res[jid] = n
			continue
		}
		for id, n := range idToName {
			if strings.HasPrefix(id, jn) || strings.HasPrefix(jn, id) {
				res[jid] = n
				break
			}
		}
	}
	return res
}

func listeningSockets(hc *HostClient) ([]ServiceEntry, error) {
	out, err := hc.Get("/sockstat", []string{"sockstat", "-l"})
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	entries := []ServiceEntry{}
	for _, line := range rustLines(lossy(out)) {
		f := strings.Fields(line)
		if len(f) < 7 || f[0] == "USER" {
			continue
		}
		proto := strings.ToLower(f[4])
		if proto == "unix" {
			continue
		}
		pid, ok := parseI32(f[2])
		if !ok {
			continue
		}
		local := f[5]
		sep := strings.LastIndex(local, ":")
		if sep < 0 {
			continue
		}
		addr := local[:sep]
		if addr == "*" {
			addr = "0.0.0.0"
		}
		port, ok := parseI32(local[sep+1:])
		if !ok || port <= 0 {
			continue
		}
		fam := strings.TrimRight(proto, "46")
		key := f[1] + ":" + itoa(int(port)) + ":" + fam
		if seen[key] {
			continue
		}
		seen[key] = true
		entries = append(entries, ServiceEntry{Port: port, Proto: strings.ToUpper(fam), Addr: addr, Command: f[1], User: f[0], Pid: pid})
	}
	return entries, nil
}

func collectServices(hc *HostClient, pc *PodmanClient) ([]ServiceEntry, string) {
	entries, err := listeningSockets(hc)
	fetchErr := ""
	if err != nil {
		entries, fetchErr = []ServiceEntry{}, "sockstat 실행 실패: "+err.Error()
	}
	jails := jailNames(hc)
	byJid := jidContainerNames(pc, jails)
	pj := pidJids(hc, jails)
	for i := range entries {
		e := &entries[i]
		jid := pj[e.Pid]
		if jid > 0 {
			if n, ok := byJid[jid]; ok {
				e.Kind, e.Name = "container", n
			} else {
				e.Kind = "jail"
				if jn, ok := jails[jid]; ok {
					if len(jn) > 12 {
						jn = jn[:12]
					}
					e.Name = jn
				} else {
					e.Name = "JID:" + itoa(int(jid))
				}
			}
		} else {
			e.Kind, e.Name = "native", e.Command
		}
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Port != entries[j].Port {
			return entries[i].Port < entries[j].Port
		}
		return entries[i].Proto < entries[j].Proto
	})
	return entries, fetchErr
}
