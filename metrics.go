package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const (
	collectInterval = 5 * time.Second
	maxSamples      = 360 // 30 min at 5s intervals
)

// kern.cp_time indices
const (
	cpUser = 0
	cpNice = 1
	cpSys  = 2
	cpIntr = 3
	cpIdle = 4
)

type MetricSample struct {
	Time int64
	CPU  [4]float64 // user, nice, sys, intr (%)
	Mem  [4]float64 // free, active, inactive, wired (MiB)
	Load [3]float64 // 1, 5, 15 min

	// Network KB/s [rx, tx] per interface
	NetIgc0 [2]float64
	NetTs   [2]float64

	// Disk I/O KB/s [read, write]
	Disk [2]float64

	// ZFS ARC
	ARCSize float64 // MiB
	ARCHit  float64 // %
	ARCMiss float64 // %

	// TCP established connections
	TCPEstab float64

	// ZFS pool [used%, usedMiB, totalMiB]
	PoolZdata [3]float64
	PoolZboot [3]float64
}

type MetricsCollector struct {
	mu    sync.RWMutex
	ring  [maxSamples]MetricSample
	head  int
	count int

	prevCPU      [5]uint64
	prevNetBytes map[string][2]uint64
	prevDiskBytes [2]uint64
	prevARCHits  [2]uint64 // [hits, misses]
	prevTime     time.Time
}

func newMetricsCollector() *MetricsCollector {
	mc := &MetricsCollector{
		prevNetBytes: make(map[string][2]uint64),
	}
	mc.prevCPU, _ = mc.readCPUTicks()
	mc.prevNetBytes = mc.readNetBytes()
	mc.prevDiskBytes = mc.readDiskBytes()
	mc.prevARCHits = mc.readARCHits()
	mc.prevTime = time.Now()
	go mc.run()
	return mc
}

func (mc *MetricsCollector) run() {
	ticker := time.NewTicker(collectInterval)
	for range ticker.C {
		s := mc.collect()
		mc.mu.Lock()
		mc.head = (mc.head + 1) % maxSamples
		mc.ring[mc.head] = s
		if mc.count < maxSamples {
			mc.count++
		}
		mc.mu.Unlock()
	}
}

func (mc *MetricsCollector) collect() MetricSample {
	now := time.Now()
	elapsed := now.Sub(mc.prevTime).Seconds()
	if elapsed <= 0 {
		elapsed = float64(collectInterval.Seconds())
	}
	mc.prevTime = now

	s := MetricSample{Time: now.Unix()}
	s.CPU = mc.collectCPU()
	s.Mem = mc.collectMem()
	s.Load = mc.collectLoad()
	s.NetIgc0, s.NetTs = mc.collectNet(elapsed)
	s.Disk = mc.collectDisk(elapsed)
	s.ARCSize, s.ARCHit, s.ARCMiss = mc.collectARC()
	s.TCPEstab = mc.collectTCP()
	s.PoolZdata, s.PoolZboot = mc.collectPools()
	return s
}

// ── CPU ──────────────────────────────────────────────────────────────────────

func (mc *MetricsCollector) readCPUTicks() ([5]uint64, error) {
	b, err := unix.SysctlRaw("kern.cp_time")
	if err != nil {
		return [5]uint64{}, err
	}
	var t [5]uint64
	for i := 0; i < 5 && (i+1)*8 <= len(b); i++ {
		t[i] = binary.LittleEndian.Uint64(b[i*8:])
	}
	return t, nil
}

func (mc *MetricsCollector) collectCPU() [4]float64 {
	cur, err := mc.readCPUTicks()
	if err != nil {
		return [4]float64{}
	}
	var delta [5]uint64
	var total uint64
	for i := 0; i < 5; i++ {
		if cur[i] >= mc.prevCPU[i] {
			delta[i] = cur[i] - mc.prevCPU[i]
		}
		total += delta[i]
	}
	mc.prevCPU = cur
	if total == 0 {
		return [4]float64{}
	}
	return [4]float64{
		float64(delta[cpUser]) / float64(total) * 100,
		float64(delta[cpNice]) / float64(total) * 100,
		float64(delta[cpSys]) / float64(total) * 100,
		float64(delta[cpIntr]) / float64(total) * 100,
	}
}

// ── Memory ────────────────────────────────────────────────────────────────────

func sysctlUintVal(name string) uint64 {
	b, err := unix.SysctlRaw(name)
	if err != nil {
		return 0
	}
	switch len(b) {
	case 4:
		return uint64(binary.LittleEndian.Uint32(b))
	case 8:
		return binary.LittleEndian.Uint64(b)
	}
	return 0
}

func (mc *MetricsCollector) collectMem() [4]float64 {
	ps := sysctlUintVal("hw.pagesize")
	if ps == 0 {
		ps = 4096
	}
	mib := float64(ps) / 1024 / 1024
	return [4]float64{
		float64(sysctlUintVal("vm.stats.vm.v_free_count")) * mib,
		float64(sysctlUintVal("vm.stats.vm.v_active_count")) * mib,
		float64(sysctlUintVal("vm.stats.vm.v_inactive_count")) * mib,
		float64(sysctlUintVal("vm.stats.vm.v_wire_count")) * mib,
	}
}

// ── Load ──────────────────────────────────────────────────────────────────────

func (mc *MetricsCollector) collectLoad() [3]float64 {
	s, err := unix.Sysctl("vm.loadavg")
	if err != nil {
		return [3]float64{}
	}
	// format: "{ 0.12 0.15 0.10 }"
	s = strings.Trim(s, "{ }")
	parts := strings.Fields(s)
	var l [3]float64
	for i := 0; i < 3 && i < len(parts); i++ {
		l[i], _ = strconv.ParseFloat(parts[i], 64)
	}
	return l
}

// ── Network ───────────────────────────────────────────────────────────────────

// readNetBytes returns cumulative rx/tx bytes per interface (link# lines only)
func (mc *MetricsCollector) readNetBytes() map[string][2]uint64 {
	out, err := exec.Command("netstat", "-ibn").Output()
	if err != nil {
		return nil
	}
	result := make(map[string][2]uint64)
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.Contains(line, "<link#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 10 {
			continue
		}
		name := f[0]
		rx, _ := strconv.ParseUint(f[6], 10, 64)
		tx, _ := strconv.ParseUint(f[9], 10, 64)
		result[name] = [2]uint64{rx, tx}
	}
	return result
}

func (mc *MetricsCollector) collectNet(elapsed float64) (igc0, ts [2]float64) {
	cur := mc.readNetBytes()
	for iface, cb := range cur {
		pb := mc.prevNetBytes[iface]
		rx := float64(cb[0]-pb[0]) / elapsed / 1024
		tx := float64(cb[1]-pb[1]) / elapsed / 1024
		if rx < 0 {
			rx = 0
		}
		if tx < 0 {
			tx = 0
		}
		switch iface {
		case "igc0":
			igc0 = [2]float64{rx, tx}
		case "tailscale0":
			ts = [2]float64{rx, tx}
		}
	}
	mc.prevNetBytes = cur
	return
}

// ── Disk ──────────────────────────────────────────────────────────────────────

// readDiskBytes reads cumulative read/write bytes from kern.devstat.all.
// struct devstat layout (FreeBSD amd64):
//
//	offset  0: sequence0 (4), allocated (4), start_count (4), end_count (4)
//	offset 16: busy_time (16), start_time (16), last_comp_time (16)
//	offset 64: bytes[4] → [0]=read, [1]=write  (4×8=32 bytes)
//	offset 96: operations[4] (16), duration[4] (96), sequence1 (8), ident (8)
//	offset 224: device_name[16]
func (mc *MetricsCollector) readDiskBytes() [2]uint64 {
	numdevsRaw, err := unix.SysctlRaw("kern.devstat.numdevs")
	if err != nil || len(numdevsRaw) < 4 {
		return [2]uint64{}
	}
	numdevs := int(binary.LittleEndian.Uint32(numdevsRaw))
	if numdevs <= 0 {
		return [2]uint64{}
	}
	all, err := unix.SysctlRaw("kern.devstat.all")
	if err != nil || len(all) < 8 {
		return [2]uint64{}
	}
	data := all[8:] // skip 8-byte generation number
	structSize := len(data) / numdevs
	if structSize < 80 {
		return [2]uint64{}
	}
	var totalRead, totalWrite uint64
	for i := 0; i < numdevs; i++ {
		base := data[i*structSize:]
		if len(base) < 80 {
			break
		}
		// Skip pass-through devices (device_name starts with "pass")
		if structSize >= 240 {
			nameBytes := base[224:240]
			name := strings.TrimRight(string(nameBytes), "\x00")
			if strings.HasPrefix(name, "pass") {
				continue
			}
		}
		totalRead += binary.LittleEndian.Uint64(base[64:])
		totalWrite += binary.LittleEndian.Uint64(base[72:])
	}
	return [2]uint64{totalRead, totalWrite}
}

func (mc *MetricsCollector) collectDisk(elapsed float64) [2]float64 {
	cur := mc.readDiskBytes()
	rd := float64(cur[0]-mc.prevDiskBytes[0]) / elapsed / 1024
	wr := float64(cur[1]-mc.prevDiskBytes[1]) / elapsed / 1024
	mc.prevDiskBytes = cur
	if rd < 0 {
		rd = 0
	}
	if wr < 0 {
		wr = 0
	}
	return [2]float64{rd, wr}
}

// ── ZFS ARC ───────────────────────────────────────────────────────────────────

func (mc *MetricsCollector) readARCHits() [2]uint64 {
	hits := sysctlUintVal("kstat.zfs.misc.arcstats.hits")
	misses := sysctlUintVal("kstat.zfs.misc.arcstats.misses")
	return [2]uint64{hits, misses}
}

func (mc *MetricsCollector) collectARC() (sizeMiB, hitPct, missPct float64) {
	sizeBytes := sysctlUintVal("kstat.zfs.misc.arcstats.size")
	sizeMiB = float64(sizeBytes) / 1024 / 1024

	cur := mc.readARCHits()
	dHits := float64(cur[0] - mc.prevARCHits[0])
	dMiss := float64(cur[1] - mc.prevARCHits[1])
	mc.prevARCHits = cur

	total := dHits + dMiss
	if total > 0 {
		hitPct = dHits / total * 100
		missPct = dMiss / total * 100
	}
	return
}

// ── TCP ───────────────────────────────────────────────────────────────────────

func (mc *MetricsCollector) collectTCP() float64 {
	// net.inet.tcp.states returns TCP_NSTATES (11) uint64 values
	// TCPS_ESTABLISHED = 4
	b, err := unix.SysctlRaw("net.inet.tcp.states")
	if err != nil || len(b) < 5*8 {
		return 0
	}
	return float64(binary.LittleEndian.Uint64(b[4*8:]))
}

// ── ZFS Pools ────────────────────────────────────────────────────────────────

func (mc *MetricsCollector) collectPools() (zdata, zboot [3]float64) {
	// zpool list -Hp -o name,size,alloc
	out, err := exec.Command("zpool", "list", "-Hp", "-o", "name,size,alloc").Output()
	if err != nil {
		return
	}
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		f := strings.Fields(scanner.Text())
		if len(f) < 3 {
			continue
		}
		name := f[0]
		total, _ := strconv.ParseFloat(f[1], 64)
		used, _ := strconv.ParseFloat(f[2], 64)
		pct := 0.0
		if total > 0 {
			pct = used / total * 100
		}
		v := [3]float64{pct, used / 1024 / 1024, total / 1024 / 1024}
		switch name {
		case "zdata":
			zdata = v
		case "zboot":
			zboot = v
		}
	}
	return
}

// ── Query API ─────────────────────────────────────────────────────────────────

type ndResponse struct {
	Labels []string    `json:"labels"`
	Data   [][]float64 `json:"data"`
}

var chartMeta = map[string][]string{
	"system.cpu":      {"time", "user", "nice", "sys", "intr"},
	"system.ram":      {"time", "free", "active", "inactive", "wired"},
	"system.load":     {"time", "load1", "load5", "load15"},
	"net.igc0":        {"time", "received", "sent"},
	"net.tailscale0":  {"time", "received", "sent"},
	"system.io":       {"time", "read", "write"},
	"zfs.arc_size":    {"time", "size"},
	"zfs.hits":        {"time", "hits", "misses"},
	"ipv4.tcpsock":    {"time", "CurrEstab"},
	"zfspool.zdata":   {"time", "utilization", "usedMiB", "totalMiB"},
	"zfspool.zboot":   {"time", "utilization", "usedMiB", "totalMiB"},
}

func (mc *MetricsCollector) sampleRow(chart string, s MetricSample) []float64 {
	ts := float64(s.Time)
	switch chart {
	case "system.cpu":
		return []float64{ts, s.CPU[0], s.CPU[1], s.CPU[2], s.CPU[3]}
	case "system.ram":
		return []float64{ts, s.Mem[0], s.Mem[1], s.Mem[2], s.Mem[3]}
	case "system.load":
		return []float64{ts, s.Load[0], s.Load[1], s.Load[2]}
	case "net.igc0":
		return []float64{ts, s.NetIgc0[0], s.NetIgc0[1]}
	case "net.tailscale0":
		return []float64{ts, s.NetTs[0], s.NetTs[1]}
	case "system.io":
		return []float64{ts, s.Disk[0], s.Disk[1]}
	case "zfs.arc_size":
		return []float64{ts, s.ARCSize}
	case "zfs.hits":
		return []float64{ts, s.ARCHit, s.ARCMiss}
	case "ipv4.tcpsock":
		return []float64{ts, s.TCPEstab}
	case "zfspool.zdata":
		return []float64{ts, s.PoolZdata[0], s.PoolZdata[1], s.PoolZdata[2]}
	case "zfspool.zboot":
		return []float64{ts, s.PoolZboot[0], s.PoolZboot[1], s.PoolZboot[2]}
	}
	return []float64{ts}
}

func (mc *MetricsCollector) query(chart string, afterSecs, maxPoints int) (ndResponse, error) {
	labels, ok := chartMeta[chart]
	if !ok {
		return ndResponse{}, fmt.Errorf("unknown chart: %s", chart)
	}

	mc.mu.RLock()
	defer mc.mu.RUnlock()

	if mc.count == 0 {
		return ndResponse{Labels: labels, Data: [][]float64{}}, nil
	}

	cutoff := time.Now().Unix() + int64(afterSecs) // afterSecs is negative
	var rows [][]float64
	for i := 0; i < mc.count; i++ {
		idx := (mc.head - i + maxSamples) % maxSamples
		s := mc.ring[idx]
		if s.Time < cutoff {
			break
		}
		rows = append(rows, mc.sampleRow(chart, s))
	}

	// Downsample if we have more points than requested
	if maxPoints > 0 && len(rows) > maxPoints {
		step := float64(len(rows)) / float64(maxPoints)
		sampled := make([][]float64, maxPoints)
		for i := 0; i < maxPoints; i++ {
			sampled[i] = rows[int(float64(i)*step)]
		}
		rows = sampled
	}

	return ndResponse{Labels: labels, Data: rows}, nil
}

func (mc *MetricsCollector) serveHTTP(w http.ResponseWriter, r *http.Request) {
	chart := r.URL.Query().Get("chart")
	afterStr := r.URL.Query().Get("after")
	pointsStr := r.URL.Query().Get("points")

	after := -1800
	if afterStr != "" {
		after, _ = strconv.Atoi(afterStr)
	}
	points := 60
	if pointsStr != "" {
		points, _ = strconv.Atoi(pointsStr)
	}

	resp, err := mc.query(chart, after, points)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
