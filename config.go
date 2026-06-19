package main

import (
	"os"
	"strconv"
	"strings"
)

type Config struct {
	PodmanSocket      string
	AdminPassword     string
	SessionSecret     string
	Host              string
	Port              string
	MetricsDB         string
	MetricsRetainDays int
}

func loadConfig() *Config {
	loadDotEnv()
	retainDays := 10
	if v := getEnv("METRICS_RETENTION_DAYS", ""); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			retainDays = n
		}
	}
	return &Config{
		PodmanSocket:      getEnv("PODMAN_SOCKET", "/run/podman/podman.sock"),
		AdminPassword:     getEnv("ADMIN_PASSWORD", "changeme"),
		SessionSecret:     getEnv("SESSION_SECRET", "change-this-secret-in-production-xx"),
		Host:              getEnv("HOST", "0.0.0.0"),
		Port:              getEnv("PORT", "11000"),
		MetricsDB:         getEnv("METRICS_DB", "./metrics.db"),
		MetricsRetainDays: retainDays,
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func loadDotEnv() {
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
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if os.Getenv(k) == "" {
			os.Setenv(k, v)
		}
	}
}
