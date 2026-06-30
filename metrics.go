package main

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
	_ "modernc.org/sqlite"
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
	Time    int64
	CPU     [4]float64 // user, nice, sys, intr (%)
	CPUTemp float64    // °C (average across cores, 0 if unavailable)
	Mem     [4]float64 // free, active, inactive, wired (MiB)
	Load    [3]float64 // 1, 5, 15 min

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
	db            *sql.DB
	insertStmt    *sql.Stmt
	retentionDays int

	mu    sync.RWMutex
	ring  [maxSamples]MetricSample
	head  int
	count int

	prevCPU       [5]uint64
	prevNetBytes  map[string][2]uint64
	prevDiskBytes [2]uint64
	prevARCHits   [2]uint64
	prevTime      time.Time
}

func newMetricsCollector(dbPath string, retentionDays int) *MetricsCollector {
	mc := &MetricsCollector{
		prevNetBytes:  make(map[string][2]uint64),
		retentionDays: retentionDays,
	}

	if err := mc.setupDB(dbPath); err != nil {
		log.Printf("metrics: SQLite 초기화 실패 (%v), 메모리 전용으로 동작", err)
	} else {
		mc.populateRing()
		mc.cleanup()
	}

	mc.prevCPU, _ = mc.readCPUTicks()
	mc.prevNetBytes = mc.readNetBytes()
	mc.prevDiskBytes = mc.readDiskBytes()
	mc.prevARCHits = mc.readARCHits()
	mc.prevTime = time.Now()

	go mc.run()
	return mc
}

const createSchema = `
CREATE TABLE IF NOT EXISTS metrics (
	time             INTEGER PRIMARY KEY NOT NULL,
	cpu_user         REAL NOT NULL DEFAULT 0,
	cpu_nice         REAL NOT NULL DEFAULT 0,
	cpu_sys          REAL NOT NULL DEFAULT 0,
	cpu_intr         REAL NOT NULL DEFAULT 0,
	cpu_temp         REAL NOT NULL DEFAULT 0,
	mem_free         REAL NOT NULL DEFAULT 0,
	mem_active       REAL NOT NULL DEFAULT 0,
	mem_inactive     REAL NOT NULL DEFAULT 0,
	mem_wired        REAL NOT NULL DEFAULT 0,
	load1            REAL NOT NULL DEFAULT 0,
	load5            REAL NOT NULL DEFAULT 0,
	load15           REAL NOT NULL DEFAULT 0,
	net_igc0_rx      REAL NOT NULL DEFAULT 0,
	net_igc0_tx      REAL NOT NULL DEFAULT 0,
	net_ts_rx        REAL NOT NULL DEFAULT 0,
	net_ts_tx        REAL NOT NULL DEFAULT 0,
	disk_read        REAL NOT NULL DEFAULT 0,
	disk_write       REAL NOT NULL DEFAULT 0,
	arc_size         REAL NOT NULL DEFAULT 0,
	arc_hit          REAL NOT NULL DEFAULT 0,
	arc_miss         REAL NOT NULL DEFAULT 0,
	tcp_estab        REAL NOT NULL DEFAULT 0,
	pool_zdata_pct   REAL NOT NULL DEFAULT 0,
	pool_zdata_used  REAL NOT NULL DEFAULT 0,
	pool_zdata_total REAL NOT NULL DEFAULT 0,
	pool_zboot_pct   REAL NOT NULL DEFAULT 0,
	pool_zboot_used  REAL NOT NULL DEFAULT 0,
	pool_zboot_total REAL NOT NULL DEFAULT 0
);`

const insertSQL = `INSERT OR REPLACE INTO metrics VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`

func (mc *MetricsCollector) setupDB(path string) error {
	db, err := sql.Open("sqlite", path+"?_journal=WAL&_timeout=5000&_synchronous=NORMAL")
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(createSchema); err != nil {
		db.Close()
		return err
	}
	stmt, err := db.Prepare(insertSQL)
	if err != nil {
		db.Close()
		return err
	}
	mc.db = db
	mc.insertStmt = stmt
	return nil
}

// populateRing loads the most recent maxSamples rows from SQLite into the ring buffer.
func (mc *MetricsCollector) populateRing() {
	if mc.db == nil {
		return
	}
	rows, err := mc.db.Query(`
		SELECT time,cpu_user,cpu_nice,cpu_sys,cpu_intr,cpu_temp,
		       mem_free,mem_active,mem_inactive,mem_wired,
		       load1,load5,load15,
		       net_igc0_rx,net_igc0_tx,net_ts_rx,net_ts_tx,
		       disk_read,disk_write,
		       arc_size,arc_hit,arc_miss,tcp_estab,
		       pool_zdata_pct,pool_zdata_used,pool_zdata_total,
		       pool_zboot_pct,pool_zboot_used,pool_zboot_total
		FROM metrics ORDER BY time DESC LIMIT ?`, maxSamples)
	if err != nil {
		return
	}
	defer rows.Close()

	var loaded []MetricSample
	for rows.Next() {
		var s MetricSample
		rows.Scan(&s.Time,
			&s.CPU[0], &s.CPU[1], &s.CPU[2], &s.CPU[3], &s.CPUTemp,
			&s.Mem[0], &s.Mem[1], &s.Mem[2], &s.Mem[3],
			&s.Load[0], &s.Load[1], &s.Load[2],
			&s.NetIgc0[0], &s.NetIgc0[1], &s.NetTs[0], &s.NetTs[1],
			&s.Disk[0], &s.Disk[1],
			&s.ARCSize, &s.ARCHit, &s.ARCMiss, &s.TCPEstab,
			&s.PoolZdata[0], &s.PoolZdata[1], &s.PoolZdata[2],
			&s.PoolZboot[0], &s.PoolZboot[1], &s.PoolZboot[2])
		loaded = append(loaded, s)
	}
	// loaded is newest-first; insert oldest-first into ring
	mc.mu.Lock()
	for i := len(loaded) - 1; i >= 0; i-- {
		mc.head = (mc.head + 1) % maxSamples
		mc.ring[mc.head] = loaded[i]
		if mc.count < maxSamples {
			mc.count++
		}
	}
	mc.mu.Unlock()
	log.Printf("metrics: DB에서 %d개 샘플 복원", len(loaded))
}

func (mc *MetricsCollector) cleanup() {
	if mc.db == nil {
		return
	}
	cutoff := time.Now().Unix() - int64(mc.retentionDays*24*60*60)
	res, err := mc.db.Exec("DELETE FROM metrics WHERE time < ?", cutoff)
	if err == nil {
		if n, _ := res.RowsAffected(); n > 0 {
			log.Printf("metrics: 오래된 데이터 %d rows 삭제", n)
		}
	}
}

func (mc *MetricsCollector) run() {
	cleanTicker := time.NewTicker(time.Hour)
	collectTicker := time.NewTicker(collectInterval)
	for {
		select {
		case <-collectTicker.C:
			s := mc.collect()
			mc.mu.Lock()
			mc.head = (mc.head + 1) % maxSamples
			mc.ring[mc.head] = s
			if mc.count < maxSamples {
				mc.count++
			}
			mc.mu.Unlock()
			mc.writeToDB(s)
		case <-cleanTicker.C:
			mc.cleanup()
		}
	}
}

func (mc *MetricsCollector) writeToDB(s MetricSample) {
	if mc.insertStmt == nil {
		return
	}
	mc.insertStmt.Exec(
		s.Time,
		s.CPU[0], s.CPU[1], s.CPU[2], s.CPU[3], s.CPUTemp,
		s.Mem[0], s.Mem[1], s.Mem[2], s.Mem[3],
		s.Load[0], s.Load[1], s.Load[2],
		s.NetIgc0[0], s.NetIgc0[1], s.NetTs[0], s.NetTs[1],
		s.Disk[0], s.Disk[1],
		s.ARCSize, s.ARCHit, s.ARCMiss, s.TCPEstab,
		s.PoolZdata[0], s.PoolZdata[1], s.PoolZdata[2],
		s.PoolZboot[0], s.PoolZboot[1], s.PoolZboot[2],
	)
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
	s.CPUTemp = mc.collectCPUTemp()
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

// collectCPUTemp reads per-core temperature from dev.cpu.N.temperature
// (coretemp/amdtemp drivers), which report deciKelvin as a 32-bit int.
// Returns the average across available cores, or 0 if the driver isn't loaded.
func (mc *MetricsCollector) collectCPUTemp() float64 {
	var sum float64
	var n int
	for i := 0; i < 64; i++ {
		b, err := unix.SysctlRaw(fmt.Sprintf("dev.cpu.%d.temperature", i))
		if err != nil {
			if i == 0 {
				continue // core 0 missing doesn't necessarily mean no more cores
			}
			break
		}
		if len(b) < 4 {
			continue
		}
		deciKelvin := int32(binary.LittleEndian.Uint32(b))
		sum += float64(deciKelvin)/10 - 273.15
		n++
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
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
	"system.cputemp":  {"time", "tempC"},
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
	case "system.cputemp":
		return []float64{ts, s.CPUTemp}
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

	ringWindow := -maxSamples * int(collectInterval.Seconds()) // -1800s

	// 30분 이내: ring buffer에서 반환
	if afterSecs >= ringWindow {
		return mc.queryRing(chart, labels, afterSecs, maxPoints), nil
	}

	// 30분 초과: SQLite에서 조회 (bucket 평균으로 다운샘플)
	if mc.db != nil {
		return mc.queryDB(chart, labels, afterSecs, maxPoints)
	}
	return ndResponse{Labels: labels, Data: [][]float64{}}, nil
}

func (mc *MetricsCollector) queryRing(chart string, labels []string, afterSecs, maxPoints int) ndResponse {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	cutoff := time.Now().Unix() + int64(afterSecs)
	var rows [][]float64
	for i := 0; i < mc.count; i++ {
		idx := (mc.head - i + maxSamples) % maxSamples
		s := mc.ring[idx]
		if s.Time < cutoff {
			break
		}
		rows = append(rows, mc.sampleRow(chart, s))
	}
	if maxPoints > 0 && len(rows) > maxPoints {
		step := float64(len(rows)) / float64(maxPoints)
		sampled := make([][]float64, maxPoints)
		for i := 0; i < maxPoints; i++ {
			sampled[i] = rows[int(float64(i)*step)]
		}
		rows = sampled
	}
	return ndResponse{Labels: labels, Data: rows}
}

// chartCols maps chart names to their SQL column expressions
var chartCols = map[string]string{
	"system.cpu":     "cpu_user,cpu_nice,cpu_sys,cpu_intr",
	"system.cputemp": "cpu_temp",
	"system.ram":     "mem_free,mem_active,mem_inactive,mem_wired",
	"system.load":    "load1,load5,load15",
	"net.igc0":       "net_igc0_rx,net_igc0_tx",
	"net.tailscale0": "net_ts_rx,net_ts_tx",
	"system.io":      "disk_read,disk_write",
	"zfs.arc_size":   "arc_size",
	"zfs.hits":       "arc_hit,arc_miss",
	"ipv4.tcpsock":   "tcp_estab",
	"zfspool.zdata":  "pool_zdata_pct,pool_zdata_used,pool_zdata_total",
	"zfspool.zboot":  "pool_zboot_pct,pool_zboot_used,pool_zboot_total",
}

func (mc *MetricsCollector) queryDB(chart string, labels []string, afterSecs, maxPoints int) (ndResponse, error) {
	cols, ok := chartCols[chart]
	if !ok {
		return ndResponse{Labels: labels, Data: [][]float64{}}, nil
	}
	cutoff := time.Now().Unix() + int64(afterSecs)
	bucketSec := int64(-afterSecs / maxPoints)
	if bucketSec < 1 {
		bucketSec = 1
	}

	// Build AVG expressions for each column
	colList := strings.Split(cols, ",")
	avgExprs := make([]string, len(colList))
	for i, c := range colList {
		avgExprs[i] = "AVG(" + c + ")"
	}
	q := fmt.Sprintf(`
		SELECT (time / %d) * %d AS t, %s
		FROM metrics WHERE time >= %d
		GROUP BY t ORDER BY t DESC LIMIT %d`,
		bucketSec, bucketSec, strings.Join(avgExprs, ","), cutoff, maxPoints)

	sqlRows, err := mc.db.Query(q)
	if err != nil {
		return ndResponse{Labels: labels, Data: [][]float64{}}, err
	}
	defer sqlRows.Close()

	nCols := len(colList) + 1 // +1 for time
	var result [][]float64
	for sqlRows.Next() {
		vals := make([]float64, nCols)
		ptrs := make([]any, nCols)
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := sqlRows.Scan(ptrs...); err != nil {
			continue
		}
		result = append(result, vals)
	}
	return ndResponse{Labels: labels, Data: result}, nil
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
