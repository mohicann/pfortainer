package pf

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/smtp"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/mohicann/pfortainer/internal/httpx"
)

// AlertSettings: meta alert_settings(JSON, 필드 순서 = Rust 구조체).
type AlertSettings struct {
	EmailEnabled    bool   `json:"email_enabled" serde:"default"`
	SMTPHost        string `json:"smtp_host" serde:"default"`
	SMTPPort        int32  `json:"smtp_port" serde:"default"`
	SMTPUser        string `json:"smtp_user" serde:"default"`
	SMTPPass        string `json:"smtp_pass" serde:"default"`
	SMTPFrom        string `json:"smtp_from" serde:"default"`
	SMTPTo          string `json:"smtp_to" serde:"default"`
	WebhookEnabled  bool   `json:"webhook_enabled" serde:"default"`
	WebhookURL      string `json:"webhook_url" serde:"default"`
	CheckPoolHealth bool   `json:"check_pool_health" serde:"default"`
	CheckSmart      bool   `json:"check_smart" serde:"default"`
	CheckCapacity   bool   `json:"check_capacity" serde:"default"`
	CapacityPct     int32  `json:"capacity_pct" serde:"default"`
	CheckScrub      bool   `json:"check_scrub" serde:"default"`
	CooldownHours   int64  `json:"cooldown_hours" serde:"default"`
}

func defaultAlertSettings() AlertSettings {
	return AlertSettings{SMTPPort: 587, CheckPoolHealth: true, CheckSmart: true, CheckCapacity: true, CapacityPct: 85, CheckScrub: true, CooldownHours: 4}
}

func loadAlertSettings(db *ConfigDB) AlertSettings {
	v, _ := db.MetaGet("alert_settings")
	if v == "" {
		return defaultAlertSettings()
	}
	var s AlertSettings
	if httpx.Unmarshal([]byte(v), &s) != nil {
		return defaultAlertSettings()
	}
	return s
}

func saveAlertSettings(db *ConfigDB, s AlertSettings) error {
	return db.MetaSet("alert_settings", httpx.MarshalString(s))
}

// lettre Mailbox 파싱 흉내: "addr" 또는 "Name <addr>".
var (
	addrRe = regexp.MustCompile(`^[A-Za-z0-9!#$%&'*+/=?^_` + "`" + `{|}~-]+(\.[A-Za-z0-9!#$%&'*+/=?^_` + "`" + `{|}~-]+)*@([A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)*|\[[0-9.:A-Fa-f]+\])$`)
	nameRe = regexp.MustCompile(`^\s*(.*?)\s*<([^<>]+)>\s*$`)
)

func parseMailbox(s string) (string, bool) {
	if m := nameRe.FindStringSubmatch(s); m != nil {
		if addrRe.MatchString(m[2]) {
			return m[2], true
		}
		return "", false
	}
	t := strings.TrimSpace(s)
	return t, addrRe.MatchString(t)
}

func sendAlertEmail(s AlertSettings, subject, body string) error {
	if s.SMTPHost == "" || s.SMTPTo == "" {
		return fmt.Errorf("SMTP not configured")
	}
	from := s.SMTPFrom
	if from == "" {
		from = s.SMTPUser
	}
	if from == "" {
		from = "pfortainer@localhost"
	}
	fromAddr, ok := parseMailbox(from)
	if !ok {
		return fmt.Errorf("Invalid input")
	}
	toAddr, ok := parseMailbox(s.SMTPTo)
	if !ok {
		return fmt.Errorf("Invalid input")
	}
	addr := net.JoinHostPort(s.SMTPHost, itoa(int(uint16(s.SMTPPort))))
	var conn net.Conn
	var err error
	tlsCfg := &tls.Config{ServerName: s.SMTPHost}
	if s.SMTPPort == 465 {
		conn, err = tls.DialWithDialer(&net.Dialer{Timeout: 60 * time.Second}, "tcp", addr, tlsCfg)
	} else {
		conn, err = net.DialTimeout("tcp", addr, 60*time.Second)
	}
	if err != nil {
		return fmt.Errorf("Connection error: %s", rustIOErr(err))
	}
	c, err := smtp.NewClient(conn, s.SMTPHost)
	if err != nil {
		return fmt.Errorf("Connection error: %v", err)
	}
	defer c.Close()
	if s.SMTPPort != 465 {
		if err := c.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("Connection error: %v", err)
		}
	}
	if s.SMTPUser != "" {
		if err := c.Auth(smtp.PlainAuth("", s.SMTPUser, s.SMTPPass, s.SMTPHost)); err != nil {
			return fmt.Errorf("permanent error (535): %v", err)
		}
	}
	if err := c.Mail(fromAddr); err != nil {
		return err
	}
	if err := c.Rcpt(toAddr); err != nil {
		return err
	}
	wc, err := c.Data()
	if err != nil {
		return err
	}
	var msg bytes.Buffer
	fmt.Fprintf(&msg, "From: %s\r\nTo: %s\r\nSubject: %s\r\nContent-Type: text/plain; charset=utf-8\r\nDate: %s\r\n\r\n%s", from, s.SMTPTo, subject, time.Now().Format(time.RFC1123Z), body)
	wc.Write(msg.Bytes())
	if err := wc.Close(); err != nil {
		return err
	}
	return c.Quit()
}

func rfc3339Now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func isDiscordWebhook(u string) bool {
	rest := u
	if r, ok := strings.CutPrefix(u, "https://"); ok {
		rest = r
	} else if r, ok := strings.CutPrefix(u, "http://"); ok {
		rest = r
	}
	for _, p := range []string{"discord.com/api/webhooks/", "discordapp.com/api/webhooks/", "canary.discord.com/api/webhooks/", "ptb.discord.com/api/webhooks/"} {
		if strings.HasPrefix(rest, p) {
			return true
		}
	}
	return false
}

func truncateChars(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max-1]) + "…"
}

func discordPayload(eventType, target, subject, body, ts string) map[string]any {
	color := 0x3498DB
	switch eventType {
	case "pool_health", "smart_health":
		color = 0xE74C3C
	case "scrub_error", "pool_capacity", "smart_unknown":
		color = 0xF39C12
	}
	embed := map[string]any{"title": truncateChars(subject, 256), "color": color, "timestamp": ts,
		"footer": map[string]any{"text": truncateChars(eventType+" · "+target, 2048)}}
	if body != "" {
		embed["description"] = truncateChars(body, 4096)
	}
	return map[string]any{"username": "pfortainer", "embeds": []any{embed}, "allowed_mentions": map[string]any{"parse": []any{}}}
}

// reqwest 기본 프록시: HTTP_PROXY/HTTPS_PROXY(대소문자)·NO_PROXY. Go와 달리 루프백도 프록시한다.
var webhookClient = &http.Client{Transport: &http.Transport{Proxy: reqwestProxy}}

func envAny(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

func reqwestProxy(r *http.Request) (*url.URL, error) {
	var p string
	if r.URL.Scheme == "https" {
		p = envAny("HTTPS_PROXY", "https_proxy", "ALL_PROXY", "all_proxy")
	} else {
		p = envAny("HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy")
	}
	if p == "" {
		return nil, nil
	}
	host := r.URL.Hostname()
	for _, np := range strings.Split(envAny("NO_PROXY", "no_proxy"), ",") {
		np = strings.TrimSpace(np)
		if np == "" {
			continue
		}
		if np == "*" || host == np || strings.HasSuffix(host, "."+strings.TrimPrefix(np, ".")) {
			return nil, nil
		}
	}
	if !strings.Contains(p, "://") {
		p = "http://" + p
	}
	return url.Parse(p)
}

func sendWebhook(url, eventType, target, subject, body string) error {
	ts := rfc3339Now()
	var payload any
	if isDiscordWebhook(url) {
		payload = discordPayload(eventType, target, subject, body, ts)
	} else {
		payload = map[string]any{"event": eventType, "target": target, "message": subject, "timestamp": ts, "source": "pfortainer"}
	}
	req, err := http.NewRequest("POST", url, bytes.NewReader(httpx.MarshalJSON(payload)))
	if err != nil {
		return fmt.Errorf("builder error")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := webhookClient.Do(req)
	if err != nil {
		return fmt.Errorf("error sending request for url (%s)", url)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		b, _ := io.ReadAll(resp.Body)
		d := []rune(lossy(b))
		if len(d) > 200 {
			d = d[:200]
		}
		return fmt.Errorf("%s", strings.TrimRight(fmt.Sprintf("webhook HTTP %d %s", resp.StatusCode, string(d)), " \t\r\n"))
	}
	return nil
}

func fireAlert(db *ConfigDB, s AlertSettings, eventType, target, subject, body string) {
	cd := s.CooldownHours
	if cd < 0 {
		cd = 0
	}
	if db.AlertFiredRecently(eventType, target, time.Duration(cd)*time.Hour) {
		return
	}
	if err := db.RecordAlert(eventType, target); err != nil {
		log.Printf("[alert] record_alert: %v", err)
	}
	log.Printf("[alert] %s / %s: %s", eventType, target, subject)
	if s.EmailEnabled {
		if err := sendAlertEmail(s, subject, body); err != nil {
			log.Printf("[alert] email: %v", err)
		}
	}
	if s.WebhookEnabled && s.WebhookURL != "" {
		if err := sendWebhook(s.WebhookURL, eventType, target, subject, body); err != nil {
			log.Printf("[alert] webhook: %v", err)
		}
	}
}

func checkAndFireAlerts(db *ConfigDB, hc *HostClient) {
	s := loadAlertSettings(db)
	if !s.EmailEnabled && !s.WebhookEnabled {
		return
	}
	var wg sync.WaitGroup
	var pools []PoolStatus
	var perr, derr, cerr error
	var disks []DiskSmart
	var zpools []ZfsPool
	doPool, doSmart, doCap := s.CheckPoolHealth || s.CheckScrub, s.CheckSmart, s.CheckCapacity && s.CapacityPct > 0
	if doPool {
		wg.Add(1)
		go func() { defer wg.Done(); pools, perr = poolStatus(hc) }()
	}
	if doSmart {
		wg.Add(1)
		go func() { defer wg.Done(); disks, derr = smartSummary(hc) }()
	}
	if doCap {
		wg.Add(1)
		go func() { defer wg.Done(); zpools, cerr = listZfsPools(hc) }()
	}
	wg.Wait()
	if doPool {
		if perr != nil {
			log.Printf("[alert] pool_status: %v", perr)
		}
		for _, p := range pools {
			if s.CheckPoolHealth && !p.Healthy {
				fireAlert(db, s, "pool_health", p.Name, fmt.Sprintf("[pfortainer] ZFS 풀 경보: %s (%s)", p.Name, p.State),
					fmt.Sprintf("풀: %s\n상태: %s\n\n%s\n%s", p.Name, p.State, p.Status, p.Action))
			}
			if s.CheckScrub && strings.Contains(p.Scan, "repaired") && !strings.Contains(p.Scan, "with 0 errors") {
				fireAlert(db, s, "scrub_error", p.Name, "[pfortainer] ZFS 스크럽 에러: "+p.Name, fmt.Sprintf("풀: %s\n스크럽 결과: %s", p.Name, p.Scan))
			}
		}
	}
	if doSmart {
		if derr != nil {
			log.Printf("[alert] smart_summary: %v", derr)
		}
		for _, d := range disks {
			if d.Healthy {
				continue
			}
			if d.Health == "unknown" {
				e := d.Err
				if e == "" {
					e = "(출력에 SMART 상태 없음)"
				}
				fireAlert(db, s, "smart_unknown", d.Device, "[pfortainer] SMART 조회 실패: "+d.Device,
					fmt.Sprintf("디스크: %s\nsmartctl로 SMART 상태를 읽지 못했습니다(디스크 고장 판정 아님).\n장치 연결/인식 상태를 확인하세요.\n\n오류: %s", d.Device, e))
			} else {
				fireAlert(db, s, "smart_health", d.Device, fmt.Sprintf("[pfortainer] SMART 경보: %s (%s)", d.Device, d.Health),
					fmt.Sprintf("디스크: %s\n모델: %s\nS/N: %s\nSMART 상태: %s\n용량: %s", d.Device, d.Model, d.Serial, d.Health, d.Capacity))
			}
		}
	}
	if doCap {
		if cerr != nil {
			log.Printf("[alert] list_zfs_pools: %v", cerr)
		}
		for _, p := range zpools {
			if p.UsePct >= s.CapacityPct {
				fireAlert(db, s, "pool_capacity", p.Name, fmt.Sprintf("[pfortainer] 풀 용량 경보: %s (%d%%)", p.Name, p.UsePct),
					fmt.Sprintf("풀: %s\n사용률: %d%% (임계: %d%%)\n\n전체: %s / 사용: %s / 여유: %s", p.Name, p.UsePct, s.CapacityPct, p.Size, p.Alloc, p.Free))
			}
		}
	}
	db.PruneAlertHistory(7 * 24 * time.Hour)
}
