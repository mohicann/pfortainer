package pf

import (
	"bufio"
	"database/sql"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mohicann/pfortainer/internal/httpx"
)

const (
	collectInterval = 5
	maxSamples      = 360
)

type MetricSample struct {
	Time                     int64
	CPU                      [4]float64
	CPUTemp                  float64
	Mem                      [4]float64
	Load                     [3]float64
	NetMain, NetTS           [2]float64
	Disk                     [2]float64
	ArcSize, ArcHit, ArcMiss float64
	TCPEstab                 float64
	Pool1, Pool2             [3]float64
}

type MetricsOptions struct{ Pool1, Pool2, NetIface, HostdSocket string }

// HostSample: hostd /metrics/host. pools는 (이름, size, alloc) 튜플.
type HostSample struct {
	Net      map[string][2]uint64 `json:"net"`
	TCPEstab float64              `json:"tcp_estab"`
	Pools    []PoolTuple          `json:"pools"`
}

// PoolTuple: Rust (String, f64, f64).
type PoolTuple struct {
	Name        string
	Size, Alloc float64
}

func (p PoolTuple) SerdeJSON() string {
	return "[" + httpx.MarshalString(p.Name) + "," + httpx.RyuF64(p.Size) + "," + httpx.RyuF64(p.Alloc) + "]"
}

func (p *PoolTuple) SerdeDecode(v any) (string, bool) {
	arr, ok := v.([]any)
	if !ok || len(arr) != 3 {
		return "a tuple of size 3", false
	}
	n, ok1 := arr[0].(string)
	s, ok2 := arr[1].(httpx.JNumber)
	a, ok3 := arr[2].(httpx.JNumber)
	if !ok1 || !ok2 || !ok3 {
		return "a tuple of size 3", false
	}
	p.Name, p.Size, p.Alloc = n, s.Float(), a.Float()
	return "", true
}

type MetricsCollector struct {
	mu        sync.Mutex
	ring      []MetricSample
	prevCPU   [5]uint64
	prevNet   map[string][2]uint64
	prevDisk  [2]uint64
	prevArc   [2]uint64
	prevTime  time.Time
	dbmu      sync.Mutex
	db        *sql.DB
	retention int64
	opts      MetricsOptions
}

const metricsSchema = `
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

func NewMetricsCollector(path string, days int64, o MetricsOptions) *MetricsCollector {
	mc := &MetricsCollector{retention: days, opts: o, prevNet: map[string][2]uint64{}, prevTime: time.Now()}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err == nil {
		db.SetMaxOpenConns(1)
		db.Exec("PRAGMA journal_mode=WAL")
		if _, err := db.Exec(metricsSchema); err != nil {
			log.Printf("metrics: SQLite 스키마 초기화 실패 (%v), 메모리 전용으로 동작", err)
			db = nil
		}
	} else {
		db = nil
	}
	mc.db = db
	if db != nil {
		mc.populateRing()
	}
	if c, ok := readCPUTicks(); ok {
		mc.prevCPU = c
	}
	mc.prevDisk = readDiskBytes()
	mc.prevArc = readArcHits()
	mc.cleanup()
	if !metricsDisabled() {
		go mc.run()
	}
	return mc
}

func (mc *MetricsCollector) run() {
	ticks := 0
	for {
		time.Sleep(collectInterval * time.Second)
		s := mc.collect()
		mc.mu.Lock()
		mc.ring = append(mc.ring, s)
		if len(mc.ring) > maxSamples {
			mc.ring = mc.ring[1:]
		}
		mc.mu.Unlock()
		mc.write(s)
		ticks++
		if ticks%(3600/collectInterval) == 0 {
			mc.cleanup()
		}
	}
}

func (mc *MetricsCollector) collect() MetricSample {
	now := time.Now()
	mc.mu.Lock()
	defer mc.mu.Unlock()
	elapsed := now.Sub(mc.prevTime).Seconds()
	if elapsed <= 0 {
		elapsed = collectInterval
	}
	mc.prevTime = now
	s := MetricSample{Time: now.Unix()}
	s.CPU = collectCPU(&mc.prevCPU)
	s.CPUTemp = collectCPUTemp()
	s.Mem = collectMem()
	s.Load = collectLoad()
	hs, ok := fetchHostSample(mc.opts.HostdSocket)
	if !ok {
		hs = hostSampleLocal()
	}
	if len(mc.prevNet) > 0 {
		s.NetMain, s.NetTS = diffNet(mc.prevNet, hs.Net, elapsed, mc.opts.NetIface)
	}
	mc.prevNet = hs.Net
	cur := readDiskBytes()
	s.Disk = diffDisk(mc.prevDisk, cur, elapsed)
	mc.prevDisk = cur
	s.ArcSize, s.ArcHit, s.ArcMiss = collectArc(&mc.prevArc)
	s.TCPEstab = hs.TCPEstab
	s.Pool1 = poolValues(hs.Pools, mc.opts.Pool1)
	s.Pool2 = poolValues(hs.Pools, mc.opts.Pool2)
	return s
}

func (s MetricSample) values() []any {
	return []any{s.Time, s.CPU[0], s.CPU[1], s.CPU[2], s.CPU[3], s.CPUTemp, s.Mem[0], s.Mem[1], s.Mem[2], s.Mem[3],
		s.Load[0], s.Load[1], s.Load[2], s.NetMain[0], s.NetMain[1], s.NetTS[0], s.NetTS[1], s.Disk[0], s.Disk[1],
		s.ArcSize, s.ArcHit, s.ArcMiss, s.TCPEstab, s.Pool1[0], s.Pool1[1], s.Pool1[2], s.Pool2[0], s.Pool2[1], s.Pool2[2]}
}

func (mc *MetricsCollector) write(s MetricSample) {
	mc.dbmu.Lock()
	defer mc.dbmu.Unlock()
	if mc.db == nil {
		return
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", 29), ",")
	mc.db.Exec("INSERT OR REPLACE INTO metrics VALUES ("+ph+")", s.values()...)
}

func (mc *MetricsCollector) cleanup() {
	mc.dbmu.Lock()
	defer mc.dbmu.Unlock()
	if mc.db == nil {
		return
	}
	cutoff := time.Now().Unix() - mc.retention*86400
	if res, err := mc.db.Exec("DELETE FROM metrics WHERE time < ?", cutoff); err == nil {
		if n, _ := res.RowsAffected(); n > 0 {
			log.Printf("metrics: 오래된 데이터 %d rows 삭제", n)
		}
	}
}

func (mc *MetricsCollector) populateRing() {
	rows, err := mc.db.Query(`SELECT time,cpu_user,cpu_nice,cpu_sys,cpu_intr,cpu_temp,
                mem_free,mem_active,mem_inactive,mem_wired, load1,load5,load15,
                net_igc0_rx,net_igc0_tx,net_ts_rx,net_ts_tx, disk_read,disk_write,
                arc_size,arc_hit,arc_miss,tcp_estab,
                pool_zdata_pct,pool_zdata_used,pool_zdata_total, pool_zboot_pct,pool_zboot_used,pool_zboot_total
         FROM metrics ORDER BY time DESC LIMIT ?`, maxSamples)
	if err != nil {
		return
	}
	defer rows.Close()
	var loaded []MetricSample
	for rows.Next() {
		var s MetricSample
		if rows.Scan(&s.Time, &s.CPU[0], &s.CPU[1], &s.CPU[2], &s.CPU[3], &s.CPUTemp, &s.Mem[0], &s.Mem[1], &s.Mem[2], &s.Mem[3],
			&s.Load[0], &s.Load[1], &s.Load[2], &s.NetMain[0], &s.NetMain[1], &s.NetTS[0], &s.NetTS[1], &s.Disk[0], &s.Disk[1],
			&s.ArcSize, &s.ArcHit, &s.ArcMiss, &s.TCPEstab, &s.Pool1[0], &s.Pool1[1], &s.Pool1[2], &s.Pool2[0], &s.Pool2[1], &s.Pool2[2]) == nil {
			loaded = append(loaded, s)
		}
	}
	for i := len(loaded) - 1; i >= 0; i-- {
		mc.ring = append(mc.ring, loaded[i])
	}
	log.Printf("metrics: DB에서 %d개 샘플 복원", len(loaded))
}

var chartLabels = map[string][]string{
	"system.cpu": {"time", "user", "nice", "sys", "intr"}, "system.cputemp": {"time", "tempC"},
	"system.ram": {"time", "free", "active", "inactive", "wired"}, "system.load": {"time", "load1", "load5", "load15"},
	"net.igc0": {"time", "received", "sent"}, "net.tailscale0": {"time", "received", "sent"}, "system.io": {"time", "read", "write"},
	"zfs.arc_size": {"time", "size"}, "zfs.hits": {"time", "hits", "misses"}, "ipv4.tcpsock": {"time", "CurrEstab"},
	"zfspool.zdata": {"time", "utilization", "usedMiB", "totalMiB"}, "zfspool.zboot": {"time", "utilization", "usedMiB", "totalMiB"},
}

var chartCols = map[string][]string{
	"system.cpu": {"cpu_user", "cpu_nice", "cpu_sys", "cpu_intr"}, "system.cputemp": {"cpu_temp"},
	"system.ram": {"mem_free", "mem_active", "mem_inactive", "mem_wired"}, "system.load": {"load1", "load5", "load15"},
	"net.igc0": {"net_igc0_rx", "net_igc0_tx"}, "net.tailscale0": {"net_ts_rx", "net_ts_tx"}, "system.io": {"disk_read", "disk_write"},
	"zfs.arc_size": {"arc_size"}, "zfs.hits": {"arc_hit", "arc_miss"}, "ipv4.tcpsock": {"tcp_estab"},
	"zfspool.zdata": {"pool_zdata_pct", "pool_zdata_used", "pool_zdata_total"}, "zfspool.zboot": {"pool_zboot_pct", "pool_zboot_used", "pool_zboot_total"},
}

func sampleRow(chart string, s MetricSample) []float64 {
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
		return []float64{ts, s.NetMain[0], s.NetMain[1]}
	case "net.tailscale0":
		return []float64{ts, s.NetTS[0], s.NetTS[1]}
	case "system.io":
		return []float64{ts, s.Disk[0], s.Disk[1]}
	case "zfs.arc_size":
		return []float64{ts, s.ArcSize}
	case "zfs.hits":
		return []float64{ts, s.ArcHit, s.ArcMiss}
	case "ipv4.tcpsock":
		return []float64{ts, s.TCPEstab}
	case "zfspool.zdata":
		return []float64{ts, s.Pool1[0], s.Pool1[1], s.Pool1[2]}
	case "zfspool.zboot":
		return []float64{ts, s.Pool2[0], s.Pool2[1], s.Pool2[2]}
	}
	return []float64{ts}
}

type ndResponse struct {
	Labels []string    `json:"labels"`
	Data   [][]float64 `json:"data"`
}

func (mc *MetricsCollector) Query(chart string, after int64, maxPoints int) (ndResponse, string) {
	labels, ok := chartLabels[chart]
	if !ok {
		return ndResponse{}, "unknown chart: " + chart
	}
	if after >= -maxSamples*collectInterval {
		return mc.queryRing(chart, labels, after, maxPoints), ""
	}
	mc.dbmu.Lock()
	defer mc.dbmu.Unlock()
	if mc.db == nil {
		return ndResponse{labels, [][]float64{}}, ""
	}
	return queryDB(mc.db, chart, labels, after, maxPoints), ""
}

func (mc *MetricsCollector) queryRing(chart string, labels []string, after int64, maxPoints int) ndResponse {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	cutoff := time.Now().Unix() + after
	rows := [][]float64{}
	for i := len(mc.ring) - 1; i >= 0; i-- {
		if mc.ring[i].Time < cutoff {
			break
		}
		rows = append(rows, sampleRow(chart, mc.ring[i]))
	}
	if maxPoints > 0 && len(rows) > maxPoints {
		step := float64(len(rows)) / float64(maxPoints)
		out := make([][]float64, 0, maxPoints)
		for i := 0; i < maxPoints; i++ {
			out = append(out, rows[int(float64(i)*step)])
		}
		rows = out
	}
	return ndResponse{labels, rows}
}

func queryDB(db *sql.DB, chart string, labels []string, after int64, maxPoints int) ndResponse {
	cols := chartCols[chart]
	cutoff := time.Now().Unix() + after
	mp := int64(maxPoints)
	if mp < 1 {
		mp = 1
	}
	bucket := (-after) / mp
	if bucket < 1 {
		bucket = 1
	}
	var avg []string
	for _, c := range cols {
		avg = append(avg, "AVG("+c+")")
	}
	q := fmt.Sprintf("SELECT (time / %d) * %d AS t, %s FROM metrics WHERE time >= %d GROUP BY t ORDER BY t DESC LIMIT %d", bucket, bucket, strings.Join(avg, ","), cutoff, mp)
	rows, err := db.Query(q)
	if err != nil {
		return ndResponse{labels, [][]float64{}}
	}
	defer rows.Close()
	data := [][]float64{}
	for rows.Next() {
		vals := make([]sql.NullFloat64, len(cols)+1)
		ptrs := make([]any, len(vals))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if rows.Scan(ptrs...) != nil {
			continue
		}
		row := make([]float64, len(vals))
		for i, v := range vals {
			row[i] = v.Float64
		}
		data = append(data, row)
	}
	return ndResponse{labels, data}
}

// ── 수집 함수(FreeBSD sysctl) ────────────────────────────────────────────────

func le64(b []byte, off int) uint64 {
	if off+8 > len(b) {
		return 0
	}
	return binary.LittleEndian.Uint64(b[off:])
}

func sysctlUint(name string) uint64 {
	b, ok := sysctlRaw(name)
	if !ok {
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

func readCPUTicks() ([5]uint64, bool) {
	var t [5]uint64
	b, ok := sysctlRaw("kern.cp_time")
	if !ok {
		return t, false
	}
	for i := range t {
		t[i] = le64(b, i*8)
	}
	return t, true
}

func collectCPU(prev *[5]uint64) [4]float64 {
	cur, ok := readCPUTicks()
	if !ok {
		return [4]float64{}
	}
	var d [5]uint64
	var total uint64
	for i := 0; i < 5; i++ {
		if cur[i] >= prev[i] {
			d[i] = cur[i] - prev[i]
		}
		total += d[i]
	}
	*prev = cur
	if total == 0 {
		return [4]float64{}
	}
	t := float64(total)
	return [4]float64{float64(d[0]) / t * 100, float64(d[1]) / t * 100, float64(d[2]) / t * 100, float64(d[3]) / t * 100}
}

func collectCPUTemp() float64 {
	sum, n := 0.0, 0
	for i := 0; i < 64; i++ {
		b, ok := sysctlRaw(fmt.Sprintf("dev.cpu.%d.temperature", i))
		if !ok {
			if i == 0 {
				continue
			}
			break
		}
		if len(b) >= 4 {
			sum += float64(int32(binary.LittleEndian.Uint32(b)))/10 - 273.15
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}

func collectMem() [4]float64 {
	ps := sysctlUint("hw.pagesize")
	if ps == 0 {
		ps = 4096
	}
	mib := float64(ps) / 1024 / 1024
	return [4]float64{float64(sysctlUint("vm.stats.vm.v_free_count")) * mib, float64(sysctlUint("vm.stats.vm.v_active_count")) * mib,
		float64(sysctlUint("vm.stats.vm.v_inactive_count")) * mib, float64(sysctlUint("vm.stats.vm.v_wire_count")) * mib}
}

// parseLoadavg: struct loadavg { u32 ldavg[3]; (pad) i64 fscale } — 24바이트.
func parseLoadavg(b []byte) [3]float64 {
	if len(b) < 24 {
		return [3]float64{}
	}
	fs := float64(le64(b, 16))
	if fs <= 0 {
		return [3]float64{}
	}
	u := func(o int) float64 { return float64(binary.LittleEndian.Uint32(b[o:])) }
	return [3]float64{u(0) / fs, u(4) / fs, u(8) / fs}
}

func collectLoad() [3]float64 {
	b, ok := sysctlRaw("vm.loadavg")
	if !ok {
		return [3]float64{}
	}
	return parseLoadavg(b)
}

func parseNetstatIbn(text string) map[string][2]uint64 {
	res := map[string][2]uint64{}
	for _, line := range rustLines(text) {
		if !strings.Contains(strings.ToLower(line), "<link#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 9 {
			continue
		}
		n := len(f)
		rx, _ := strconv.ParseUint(f[n-5], 10, 64)
		tx, _ := strconv.ParseUint(f[n-2], 10, 64)
		res[strings.TrimRight(f[0], "*")] = [2]uint64{rx, tx}
	}
	return res
}

func readNetBytes() map[string][2]uint64 {
	r := runOutput("netstat", []string{"-ibn"}, nil)
	if !r.ok {
		return map[string][2]uint64{}
	}
	return parseNetstatIbn(lossy(r.stdout))
}

func diffNet(prev, cur map[string][2]uint64, elapsed float64, main string) ([2]float64, [2]float64) {
	var m, ts [2]float64
	for iface, cb := range cur {
		pb := prev[iface]
		rx := (float64(cb[0]) - float64(pb[0])) / elapsed / 1024
		tx := (float64(cb[1]) - float64(pb[1])) / elapsed / 1024
		if rx < 0 {
			rx = 0
		}
		if tx < 0 {
			tx = 0
		}
		if iface == main {
			m = [2]float64{rx, tx}
		} else if iface == "tailscale0" {
			ts = [2]float64{rx, tx}
		}
	}
	return m, ts
}

// parseDevstat: kern.devstat.all(앞 8바이트 generation) → 읽기·쓰기 누적 바이트(오프셋 72/80, pass* 제외).
func parseDevstat(numdevs int, all []byte) [2]uint64 {
	if numdevs == 0 || len(all) < 8 {
		return [2]uint64{}
	}
	data := all[8:]
	size := len(data) / numdevs
	if size < 96 {
		return [2]uint64{}
	}
	var rd, wr uint64
	for i := 0; i < numdevs; i++ {
		base := data[i*size:]
		if len(base) < 96 {
			break
		}
		name := base[44:60]
		end := len(name)
		for j, c := range name {
			if c == 0 {
				end = j
				break
			}
		}
		if strings.HasPrefix(string(name[:end]), "pass") {
			continue
		}
		rd += le64(base, 72)
		wr += le64(base, 80)
	}
	return [2]uint64{rd, wr}
}

func readDiskBytes() [2]uint64 {
	nb, ok := sysctlRaw("kern.devstat.numdevs")
	if !ok || len(nb) < 4 {
		return [2]uint64{}
	}
	all, ok := sysctlRaw("kern.devstat.all")
	if !ok {
		return [2]uint64{}
	}
	return parseDevstat(int(binary.LittleEndian.Uint32(nb)), all)
}

func diffDisk(prev, cur [2]uint64, elapsed float64) [2]float64 {
	rd := (float64(cur[0]) - float64(prev[0])) / elapsed / 1024
	wr := (float64(cur[1]) - float64(prev[1])) / elapsed / 1024
	if rd < 0 {
		rd = 0
	}
	if wr < 0 {
		wr = 0
	}
	return [2]float64{rd, wr}
}

func readArcHits() [2]uint64 {
	return [2]uint64{sysctlUint("kstat.zfs.misc.arcstats.hits"), sysctlUint("kstat.zfs.misc.arcstats.misses")}
}

func collectArc(prev *[2]uint64) (float64, float64, float64) {
	size := float64(sysctlUint("kstat.zfs.misc.arcstats.size")) / 1024 / 1024
	cur := readArcHits()
	dh, dm := float64(cur[0])-float64(prev[0]), float64(cur[1])-float64(prev[1])
	*prev = cur
	if t := dh + dm; t > 0 {
		return size, dh / t * 100, dm / t * 100
	}
	return size, 0, 0
}

func collectTCP() float64 {
	b, ok := sysctlRaw("net.inet.tcp.states")
	if !ok || len(b) < 40 {
		return 0
	}
	return float64(le64(b, 32))
}

func readPools() []PoolTuple {
	pools := []PoolTuple{}
	r := runOutput("zpool", []string{"list", "-Hp", "-o", "name,size,alloc"}, nil)
	if !r.ok {
		return pools
	}
	for _, line := range rustLines(lossy(r.stdout)) {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		s, _ := strconv.ParseFloat(f[1], 64)
		a, _ := strconv.ParseFloat(f[2], 64)
		pools = append(pools, PoolTuple{f[0], s, a})
	}
	return pools
}

func hostSampleLocal() HostSample {
	return HostSample{Net: readNetBytes(), TCPEstab: collectTCP(), Pools: readPools()}
}

func poolValues(pools []PoolTuple, name string) [3]float64 {
	for _, p := range pools {
		if p.Name == name {
			total, used := p.Size, p.Alloc
			pct := 0.0
			if total > 0 {
				pct = used / total * 100
			}
			return [3]float64{pct, used / 1024 / 1024, total / 1024 / 1024}
		}
	}
	return [3]float64{}
}

// fetchHostSample: hostd /metrics/host(동기 HTTP/1.1, 4초 제한).
func fetchHostSample(sock string) (HostSample, bool) {
	c, err := net.DialTimeout("unix", sock, 4*time.Second)
	if err != nil {
		return HostSample{}, false
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(4 * time.Second))
	io.WriteString(c, "GET /metrics/host HTTP/1.1\r\nHost: hostd\r\nConnection: close\r\n\r\n")
	b, _ := io.ReadAll(bufio.NewReader(c))
	text := string(b)
	head, body, ok := strings.Cut(text, "\r\n\r\n")
	if !ok || !(strings.HasPrefix(head, "HTTP/1.1 200") || strings.HasPrefix(head, "HTTP/1.0 200")) {
		return HostSample{}, false
	}
	var hs HostSample
	if httpx.Unmarshal([]byte(body), &hs) != nil {
		return HostSample{}, false
	}
	return hs, true
}

var _ = exec.Command
