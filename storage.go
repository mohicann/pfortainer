package main

import (
	"bufio"
	"bytes"
	"strings"
)

// ── View Models ───────────────────────────────────────────────────────────────

// StorageVM backs the "스토리지 상태" page: ZFS pool health plus per-disk SMART.
type StorageVM struct {
	ActivePage  string
	CurrentUser SessionUser
	AgentMode   string // "host-socket" | "local-exec"
	Pools       []PoolStatus
	Disks       []DiskSMART
	PoolError   string
	DiskError   string
}

// PoolStatus is the parsed result of `zpool status` for a single pool.
type PoolStatus struct {
	Name    string
	State   string
	Status  string // optional "status:" advisory line
	Action  string // optional "action:" remediation line
	Scan    string // last scrub/resilver summary
	Errors  string // "errors:" line
	Vdevs   []VdevNode
	Healthy bool
}

// VdevNode is one row of the zpool status config tree (pool, vdev, or leaf disk).
type VdevNode struct {
	Name     string
	State    string
	Read     string
	Write    string
	Cksum    string
	IndentPx int // Depth * 16, for template padding-left
	Healthy  bool
}

// DiskSMART summarizes `smartctl -a` for one device.
type DiskSMART struct {
	Device       string
	Model        string
	Serial       string
	Capacity     string
	Health       string // PASSED | FAILED | unknown
	Healthy      bool
	TempC        string
	PowerOnHours string
	PowerCycles  string
	Reallocated  string
	Err          string
}

// ── Fetchers (host-agent socket, with local exec fallback) ─────────────────────

// agentMode reports whether privileged reads go through the host-agent socket or
// fall back to direct execution (local dev / running on the host directly).
func agentMode() string {
	if hostdClient() != nil {
		return "host-socket"
	}
	return "local-exec"
}

func poolStatus() ([]PoolStatus, error) {
	out, err := hostGet("/zpool-status", []string{"zpool", "status"})
	if err != nil {
		return nil, err
	}
	return parseZpoolStatus(out), nil
}

// smartSummary scans for SMART-capable devices and collects a one-line health
// summary for each. A failure to read one disk degrades that row, not the page.
func smartSummary() ([]DiskSMART, error) {
	out, err := hostGet("/smart-scan", []string{"smartctl", "--scan"})
	if err != nil {
		return nil, err
	}
	var disks []DiskSMART
	for _, dev := range parseSmartScan(out) {
		o, err := hostGet("/smart?dev="+dev, []string{"smartctl", "-a", "/dev/" + dev})
		if err != nil && len(o) == 0 {
			disks = append(disks, DiskSMART{Device: dev, Health: "unknown", Err: err.Error()})
			continue
		}
		disks = append(disks, parseSmart(dev, o))
	}
	return disks, nil
}

// ── Parsers ────────────────────────────────────────────────────────────────────

// parseZpoolStatus parses `zpool status` (multi-pool) into structured form.
func parseZpoolStatus(out []byte) []PoolStatus {
	var pools []PoolStatus
	var cur *PoolStatus
	inConfig := false
	headerSeen := false

	flush := func() {
		if cur != nil {
			pools = append(pools, *cur)
			cur = nil
		}
	}

	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "pool:"):
			flush()
			cur = &PoolStatus{Name: strings.TrimSpace(strings.TrimPrefix(t, "pool:"))}
			inConfig, headerSeen = false, false
		case cur == nil:
			continue
		case strings.HasPrefix(t, "state:"):
			cur.State = strings.TrimSpace(strings.TrimPrefix(t, "state:"))
			cur.Healthy = cur.State == "ONLINE"
		case strings.HasPrefix(t, "status:"):
			cur.Status = strings.TrimSpace(strings.TrimPrefix(t, "status:"))
		case strings.HasPrefix(t, "action:"):
			cur.Action = strings.TrimSpace(strings.TrimPrefix(t, "action:"))
		case strings.HasPrefix(t, "scan:"):
			cur.Scan = strings.TrimSpace(strings.TrimPrefix(t, "scan:"))
		case strings.HasPrefix(t, "errors:"):
			cur.Errors = strings.TrimSpace(strings.TrimPrefix(t, "errors:"))
			inConfig = false
		case strings.HasPrefix(t, "config:"):
			inConfig, headerSeen = true, false
		case inConfig:
			if t == "" {
				continue
			}
			if !headerSeen {
				if strings.HasPrefix(t, "NAME") {
					headerSeen = true
				}
				continue // skip the column header row itself
			}
			// Indentation under NAME encodes tree depth: a leading tab then 2
			// spaces per level (pool=0, top-level vdev=1, leaf disk=2, ...).
			body := strings.TrimPrefix(line, "\t")
			indent := len(body) - len(strings.TrimLeft(body, " "))
			f := strings.Fields(t)
			if len(f) == 0 {
				continue
			}
			node := VdevNode{Name: f[0], IndentPx: (indent / 2) * 16}
			if len(f) >= 2 {
				node.State = f[1]
				node.Healthy = f[1] == "ONLINE"
			}
			if len(f) >= 5 {
				node.Read, node.Write, node.Cksum = f[2], f[3], f[4]
			}
			cur.Vdevs = append(cur.Vdevs, node)
		}
	}
	flush()
	return pools
}

// parseSmartScan extracts whitelisted device names from `smartctl --scan` output
// such as "/dev/ada0 -d atacam # /dev/ada0, ATA device".
func parseSmartScan(out []byte) []string {
	var devs []string
	seen := make(map[string]bool)
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 0 {
			continue
		}
		dev := strings.TrimPrefix(f[0], "/dev/")
		if smartDevRe.MatchString(dev) && !seen[dev] {
			seen[dev] = true
			devs = append(devs, dev)
		}
	}
	return devs
}

// parseSmart pulls the health summary and a few key attributes out of
// `smartctl -a` output, handling both ATA attribute tables and NVMe key/value form.
func parseSmart(dev string, out []byte) DiskSMART {
	d := DiskSMART{Device: dev, Health: "unknown"}
	val := func(s string) string {
		if i := strings.Index(s, ":"); i >= 0 {
			return strings.TrimSpace(s[i+1:])
		}
		return ""
	}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		t := strings.TrimSpace(sc.Text())
		switch {
		case strings.HasPrefix(t, "Device Model:"), strings.HasPrefix(t, "Model Number:"):
			d.Model = val(t)
		case d.Model == "" && strings.HasPrefix(t, "Model Family:"):
			d.Model = val(t)
		case strings.HasPrefix(t, "Serial Number:"):
			d.Serial = val(t)
		case strings.HasPrefix(t, "User Capacity:"):
			d.Capacity = val(t)
		case d.Capacity == "" && strings.HasPrefix(t, "Total NVM Capacity:"):
			d.Capacity = val(t)
		case strings.Contains(t, "overall-health"):
			d.Health = val(t)
			d.Healthy = strings.Contains(t, "PASSED")
		// NVMe key/value form
		case strings.HasPrefix(t, "Power On Hours:"):
			d.PowerOnHours = strings.ReplaceAll(val(t), ",", "")
		case strings.HasPrefix(t, "Power Cycles:"):
			d.PowerCycles = strings.ReplaceAll(val(t), ",", "")
		case strings.HasPrefix(t, "Temperature:") && d.TempC == "":
			if fields := strings.Fields(val(t)); len(fields) > 0 {
				d.TempC = fields[0]
			}
		}

		// ATA attribute table rows: ID# NAME FLAG ... RAW_VALUE (10th field).
		if f := strings.Fields(t); len(f) >= 10 {
			switch f[1] {
			case "Temperature_Celsius", "Airflow_Temperature_Cel":
				if d.TempC == "" {
					d.TempC = f[9]
				}
			case "Power_On_Hours":
				d.PowerOnHours = f[9]
			case "Power_Cycle_Count":
				d.PowerCycles = f[9]
			case "Reallocated_Sector_Ct":
				d.Reallocated = f[9]
			}
		}
	}
	return d
}
