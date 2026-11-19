package pf

import (
	"strings"
	"sync"

	"github.com/mohicann/pfortainer/internal/httpx"
)

type PoolStatus struct {
	Name    string     `json:"name"`
	State   string     `json:"state"`
	Status  string     `json:"status"`
	Action  string     `json:"action"`
	Scan    string     `json:"scan"`
	Errors  string     `json:"errors"`
	Vdevs   []VdevNode `json:"vdevs"`
	Healthy bool       `json:"healthy"`
}

type VdevNode struct {
	Name     string `json:"name"`
	State    string `json:"state"`
	Read     string `json:"read"`
	Write    string `json:"write"`
	Cksum    string `json:"cksum"`
	IndentPx int32  `json:"indent_px"`
	Healthy  bool   `json:"healthy"`
}

type DiskSmart struct {
	Device       string `json:"device"`
	Model        string `json:"model"`
	Serial       string `json:"serial"`
	Capacity     string `json:"capacity"`
	Health       string `json:"health"`
	Healthy      bool   `json:"healthy"`
	TempC        string `json:"temp_c"`
	PowerOnHours string `json:"power_on_hours"`
	PowerCycles  string `json:"power_cycles"`
	Reallocated  string `json:"reallocated"`
	Err          string `json:"err"`
}

var smartPrefixes = []string{"ada", "da", "nvd", "nvme", "ad", "vtbd", "mfid", "nda"}

func isValidSmartDev(dev string) bool {
	idx := strings.IndexAny(dev, "0123456789")
	if idx < 0 {
		return false
	}
	prefix, rest := dev[:idx], dev[idx:]
	if rest == "" {
		return false
	}
	for _, c := range rest {
		if c < '0' || c > '9' {
			return false
		}
	}
	for _, p := range smartPrefixes {
		if p == prefix {
			return true
		}
	}
	return false
}

func poolStatus(hc *HostClient) ([]PoolStatus, error) {
	out, err := hc.Get("/zpool-status", []string{"zpool", "status"})
	if err != nil {
		return nil, err
	}
	return parseZpoolStatus(out), nil
}

func smartSummary(hc *HostClient) ([]DiskSmart, error) {
	out, err := hc.Get("/smart-scan", []string{"smartctl", "--scan"})
	if err != nil {
		return nil, err
	}
	devs := parseSmartScan(out)
	disks := []DiskSmart{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 5)
	for _, dev := range devs {
		wg.Add(1)
		sem <- struct{}{}
		go func(dev string) {
			defer wg.Done()
			defer func() { <-sem }()
			var d DiskSmart
			o, err := hc.Get("/smart?dev="+dev, []string{"smartctl", "-a", "/dev/" + dev})
			switch {
			case err != nil:
				d = DiskSmart{Device: dev, Health: "unknown", Err: err.Error()}
			case len(o) == 0:
				d = DiskSmart{Device: dev, Health: "unknown"}
			default:
				d = parseSmart(dev, o)
			}
			mu.Lock()
			disks = append(disks, d)
			mu.Unlock()
		}(dev)
	}
	wg.Wait()
	return disks, nil
}

func startSmartTest(hc *HostClient, device, testType string) (string, error) {
	resp, err := hostPost(hc, "/smart/test", httpx.Ordered{{"device", device}, {"type", testType}})
	if err != nil {
		return "", err
	}
	var o struct {
		Output string `json:"output" serde:"default"`
	}
	if httpx.Unmarshal(resp, &o) != nil {
		return "", nil
	}
	return o.Output, nil
}

func parseZpoolStatus(out []byte) []PoolStatus {
	text := lossy(out)
	pools := []PoolStatus{}
	var cur *PoolStatus
	inConfig, headerSeen := false, false
	flush := func() {
		if cur != nil {
			if cur.Vdevs == nil {
				cur.Vdevs = []VdevNode{}
			}
			pools = append(pools, *cur)
			cur = nil
		}
	}
	for _, line := range rustLines(text) {
		t := strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(t, "pool:"); ok {
			flush()
			cur = &PoolStatus{Name: strings.TrimSpace(rest)}
			inConfig, headerSeen = false, false
			continue
		}
		if cur == nil {
			continue
		}
		if rest, ok := strings.CutPrefix(t, "state:"); ok {
			cur.State = strings.TrimSpace(rest)
			cur.Healthy = cur.State == "ONLINE"
		} else if rest, ok := strings.CutPrefix(t, "status:"); ok {
			cur.Status = strings.TrimSpace(rest)
		} else if rest, ok := strings.CutPrefix(t, "action:"); ok {
			cur.Action = strings.TrimSpace(rest)
		} else if rest, ok := strings.CutPrefix(t, "scan:"); ok {
			cur.Scan = strings.TrimSpace(rest)
		} else if rest, ok := strings.CutPrefix(t, "errors:"); ok {
			cur.Errors = strings.TrimSpace(rest)
			inConfig = false
		} else if strings.HasPrefix(t, "config:") {
			inConfig, headerSeen = true, false
		} else if inConfig {
			if t == "" {
				continue
			}
			if !headerSeen {
				if strings.HasPrefix(t, "NAME") {
					headerSeen = true
				}
				continue
			}
			body := strings.TrimPrefix(line, "\t")
			indent := len(body) - len(strings.TrimLeft(body, " "))
			f := strings.Fields(t)
			if len(f) == 0 {
				continue
			}
			n := VdevNode{Name: f[0], IndentPx: int32(indent/2) * 16}
			if len(f) >= 2 {
				n.State, n.Healthy = f[1], f[1] == "ONLINE"
			}
			if len(f) >= 5 {
				n.Read, n.Write, n.Cksum = f[2], f[3], f[4]
			}
			cur.Vdevs = append(cur.Vdevs, n)
		}
	}
	flush()
	return pools
}

func parseSmartScan(out []byte) []string {
	devs := []string{}
	seen := map[string]bool{}
	for _, line := range rustLines(lossy(out)) {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		dev := f[0]
		for strings.HasPrefix(dev, "/dev/") {
			dev = dev[5:]
		}
		if strings.HasPrefix(dev, "nda") || strings.HasPrefix(dev, "nvd") {
			continue
		}
		if isValidSmartDev(dev) && !seen[dev] {
			seen[dev] = true
			devs = append(devs, dev)
		}
	}
	return devs
}

func parseSmart(dev string, out []byte) DiskSmart {
	d := DiskSmart{Device: dev, Health: "unknown"}
	val := func(s string) string {
		if i := strings.Index(s, ":"); i >= 0 {
			return strings.TrimSpace(s[i+1:])
		}
		return ""
	}
	for _, line := range rustLines(lossy(out)) {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "Device Model:") || strings.HasPrefix(t, "Model Number:"):
			d.Model = val(t)
		case d.Model == "" && strings.HasPrefix(t, "Model Family:"):
			d.Model = val(t)
		case strings.HasPrefix(t, "Serial Number:"):
			d.Serial = strings.TrimSpace(strings.TrimPrefix(t, "Serial Number:"))
		case strings.HasPrefix(t, "User Capacity:"):
			d.Capacity = val(t)
		case d.Capacity == "" && strings.HasPrefix(t, "Total NVM Capacity:"):
			d.Capacity = val(t)
		case strings.Contains(t, "overall-health"):
			d.Health = val(t)
			d.Healthy = strings.Contains(t, "PASSED")
		case strings.HasPrefix(t, "Power On Hours:"):
			d.PowerOnHours = strings.ReplaceAll(val(t), ",", "")
		case strings.HasPrefix(t, "Power Cycles:"):
			d.PowerCycles = strings.ReplaceAll(val(t), ",", "")
		case strings.HasPrefix(t, "Temperature:") && d.TempC == "":
			if f := strings.Fields(val(t)); len(f) > 0 {
				d.TempC = f[0]
			}
		}
		f := strings.Fields(t)
		if len(f) >= 10 {
			switch f[1] {
			case "Temperature_Celsius", "Airflow_Temperature_Cel":
				if d.TempC == "" {
					d.TempC = f[9]
				}
			case "Power_On_Hours":
				n := 0
				for n < len(f[9]) && f[9][n] >= '0' && f[9][n] <= '9' {
					n++
				}
				d.PowerOnHours = f[9][:n]
			case "Power_Cycle_Count":
				d.PowerCycles = f[9]
			case "Reallocated_Sector_Ct":
				d.Reallocated = f[9]
			}
		}
	}
	return d
}
