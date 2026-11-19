package pf

import (
	"os"
	"strconv"
	"strings"
)

// Config: Rust config.rs와 같은 env·기본값.
type Config struct {
	PodmanSocket      string
	AdminPassword     string
	SessionSecret     string
	Host              string
	Port              string
	MetricsDB         string
	ConfigDB          string
	MetricsRetainDays int64
	HostdSocket       string
	MetricsPool1      string
	MetricsPool2      string
	MetricsNetIface   string
}

func LoadConfig() Config {
	loadDotenv()
	retain := int64(10)
	if v, err := strconv.ParseInt(os.Getenv("METRICS_RETENTION_DAYS"), 10, 64); err == nil && v > 0 {
		retain = v
	}
	return Config{
		PodmanSocket:      getEnv("PODMAN_SOCKET", "/run/podman/podman.sock"),
		AdminPassword:     getEnv("ADMIN_PASSWORD", "changeme"),
		SessionSecret:     getEnv("SESSION_SECRET", "change-this-secret-in-production-xx"),
		Host:              getEnv("HOST", "0.0.0.0"),
		Port:              getEnv("PORT", "11100"),
		MetricsDB:         getEnv("METRICS_DB", "./metrics.db"),
		ConfigDB:          getEnv("CONFIG_DB", "./config.db"),
		MetricsRetainDays: retain,
		HostdSocket:       getEnv("HOSTD_SOCKET", "/run/pfortainer-rs/host.sock"),
		MetricsPool1:      getEnv("METRICS_POOL1", "zdata"),
		MetricsPool2:      getEnv("METRICS_POOL2", "zbak"),
		MetricsNetIface:   getEnv("METRICS_NET_IFACE", "igc0"),
	}
}

// getEnv: Rust env::var(key).unwrap_or(fallback) — 빈 문자열도 값으로 본다.
func getEnv(k, def string) string {
	if v, ok := os.LookupEnv(k); ok {
		return v
	}
	return def
}

func loadDotenv() {
	data, err := os.ReadFile(".env")
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if _, set := os.LookupEnv(k); !set {
			os.Setenv(k, v)
		}
	}
}

// ── testhooks (APP_ENV=test 전용, Rust testhooks.rs와 같음) ─────────────────

func testEnv(k string) string {
	if os.Getenv("APP_ENV") != "test" {
		return ""
	}
	return os.Getenv(k)
}

// Root: 고정 절대경로를 TEST_ROOT 아래로.
func Root(p string) string {
	if r := testEnv("TEST_ROOT"); r != "" {
		return strings.TrimRight(r, "/") + p
	}
	return p
}

func metricsDisabled() bool   { return testEnv("TEST_DISABLE_METRICS") == "1" }
func schedulerDisabled() bool { return testEnv("TEST_DISABLE_SCHEDULER") == "1" }

func schedIntervals() (int64, int64, int64) {
	v := testEnv("TEST_SCHED_INTERVALS")
	if v == "" {
		return 60, 300, 600
	}
	var ps []int64
	for _, s := range strings.Split(v, ",") {
		if n, err := strconv.ParseUint(strings.TrimSpace(s), 10, 64); err == nil {
			ps = append(ps, int64(n))
		}
	}
	if len(ps) == 3 && ps[0] > 0 && ps[1] > 0 && ps[2] > 0 {
		return ps[0], ps[1], ps[2]
	}
	return 60, 300, 600
}
