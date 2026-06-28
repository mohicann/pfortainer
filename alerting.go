package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/smtp"
	"strings"
	"time"
)

// AlertSettings holds all alert configuration. Stored as JSON in the meta table.
type AlertSettings struct {
	EmailEnabled    bool   `json:"email_enabled"`
	SMTPHost        string `json:"smtp_host"`
	SMTPPort        int    `json:"smtp_port"`
	SMTPUser        string `json:"smtp_user"`
	SMTPPass        string `json:"smtp_pass"`
	SMTPFrom        string `json:"smtp_from"`
	SMTPTo          string `json:"smtp_to"`
	WebhookEnabled  bool   `json:"webhook_enabled"`
	WebhookURL      string `json:"webhook_url"`
	CheckPoolHealth bool   `json:"check_pool_health"`
	CheckSMART      bool   `json:"check_smart"`
	CheckCapacity   bool   `json:"check_capacity"`
	CapacityPct     int    `json:"capacity_pct"`
	CheckScrub      bool   `json:"check_scrub"`
	CooldownHours   int    `json:"cooldown_hours"`
}

func defaultAlertSettings() AlertSettings {
	return AlertSettings{
		CheckPoolHealth: true,
		CheckSMART:      true,
		CheckCapacity:   true,
		CapacityPct:     85,
		CheckScrub:      true,
		CooldownHours:   4,
		SMTPPort:        587,
	}
}

func loadAlertSettings(cdb *ConfigDB) AlertSettings {
	s := defaultAlertSettings()
	v, err := cdb.MetaGet("alert_settings")
	if err != nil || v == "" {
		return s
	}
	json.Unmarshal([]byte(v), &s)
	return s
}

func saveAlertSettings(cdb *ConfigDB, s AlertSettings) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return cdb.MetaSet("alert_settings", string(b))
}

// sendAlertEmail sends an alert via SMTP. Supports STARTTLS (587) and SMTPS (465).
func sendAlertEmail(s AlertSettings, subject, body string) error {
	if s.SMTPHost == "" || s.SMTPTo == "" {
		return fmt.Errorf("SMTP not configured")
	}
	from := s.SMTPFrom
	if from == "" {
		from = s.SMTPUser
	}
	addr := fmt.Sprintf("%s:%d", s.SMTPHost, s.SMTPPort)
	msg := fmt.Sprintf(
		"From: %s\r\nTo: %s\r\nSubject: %s\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s",
		from, s.SMTPTo, subject, body,
	)

	var auth smtp.Auth
	if s.SMTPUser != "" {
		auth = smtp.PlainAuth("", s.SMTPUser, s.SMTPPass, s.SMTPHost)
	}

	if s.SMTPPort == 465 {
		// SMTPS: TLS from the start (no STARTTLS)
		conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: s.SMTPHost})
		if err != nil {
			return fmt.Errorf("TLS dial: %w", err)
		}
		c, err := smtp.NewClient(conn, s.SMTPHost)
		if err != nil {
			return fmt.Errorf("SMTP client: %w", err)
		}
		defer c.Quit()
		if auth != nil {
			if err := c.Auth(auth); err != nil {
				return fmt.Errorf("SMTP auth: %w", err)
			}
		}
		if err := c.Mail(from); err != nil {
			return err
		}
		if err := c.Rcpt(s.SMTPTo); err != nil {
			return err
		}
		wc, err := c.Data()
		if err != nil {
			return err
		}
		fmt.Fprint(wc, msg)
		return wc.Close()
	}

	// Port 587 or others: STARTTLS via smtp.SendMail
	return smtp.SendMail(addr, auth, from, []string{s.SMTPTo}, []byte(msg))
}

// sendWebhook POSTs a JSON event payload to the configured URL.
func sendWebhook(url, eventType, target, message string) error {
	payload, _ := json.Marshal(map[string]string{
		"event":     eventType,
		"target":    target,
		"message":   message,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"source":    "pfortainer",
	})
	resp, err := http.Post(url, "application/json", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("webhook HTTP %d", resp.StatusCode)
	}
	return nil
}

// fireAlert dispatches an alert via all enabled channels, respecting the cooldown.
func fireAlert(cdb *ConfigDB, s AlertSettings, eventType, target, subject, body string) {
	cooldown := time.Duration(s.CooldownHours) * time.Hour
	if cdb.AlertFiredRecently(eventType, target, cooldown) {
		return
	}
	if err := cdb.RecordAlert(eventType, target); err != nil {
		log.Printf("[alert] RecordAlert: %v", err)
	}
	log.Printf("[alert] %s / %s: %s", eventType, target, subject)
	if s.EmailEnabled {
		if err := sendAlertEmail(s, subject, body); err != nil {
			log.Printf("[alert] email: %v", err)
		}
	}
	if s.WebhookEnabled && s.WebhookURL != "" {
		if err := sendWebhook(s.WebhookURL, eventType, target, subject); err != nil {
			log.Printf("[alert] webhook: %v", err)
		}
	}
}

// checkAndFireAlerts inspects pool health, SMART status, pool capacity, and scrub errors.
// Safe to call even when no channels are configured — exits early.
func checkAndFireAlerts(cdb *ConfigDB) {
	s := loadAlertSettings(cdb)
	if !s.EmailEnabled && !s.WebhookEnabled {
		return
	}

	if s.CheckPoolHealth || s.CheckScrub {
		pools, err := poolStatus()
		if err != nil {
			log.Printf("[alert] poolStatus: %v", err)
		} else {
			for _, p := range pools {
				if s.CheckPoolHealth && !p.Healthy {
					subj := fmt.Sprintf("[pfortainer] ZFS 풀 경보: %s (%s)", p.Name, p.State)
					body := strings.Join([]string{
						"풀: " + p.Name,
						"상태: " + p.State,
						"",
						p.Status,
						p.Action,
					}, "\n")
					fireAlert(cdb, s, "pool_health", p.Name, subj, body)
				}
				if s.CheckScrub && strings.Contains(p.Scan, "repaired") &&
					!strings.Contains(p.Scan, "with 0 errors") {
					subj := fmt.Sprintf("[pfortainer] ZFS 스크럽 에러: %s", p.Name)
					body := "풀: " + p.Name + "\n스크럽 결과: " + p.Scan
					fireAlert(cdb, s, "scrub_error", p.Name, subj, body)
				}
			}
		}
	}

	if s.CheckSMART {
		disks, err := smartSummary()
		if err != nil {
			log.Printf("[alert] smartSummary: %v", err)
		} else {
			for _, d := range disks {
				if !d.Healthy {
					subj := fmt.Sprintf("[pfortainer] SMART 경보: %s (%s)", d.Device, d.Health)
					body := strings.Join([]string{
						"디스크: " + d.Device,
						"모델: " + d.Model,
						"S/N: " + d.Serial,
						"SMART 상태: " + d.Health,
						"용량: " + d.Capacity,
					}, "\n")
					fireAlert(cdb, s, "smart_health", d.Device, subj, body)
				}
			}
		}
	}

	if s.CheckCapacity && s.CapacityPct > 0 {
		zpools, err := listZFSPools()
		if err != nil {
			log.Printf("[alert] listZFSPools: %v", err)
		} else {
			for _, p := range zpools {
				if p.UsePct >= s.CapacityPct {
					subj := fmt.Sprintf("[pfortainer] 풀 용량 경보: %s (%d%%)", p.Name, p.UsePct)
					body := fmt.Sprintf("풀: %s\n사용률: %d%% (임계: %d%%)\n\n전체: %s / 사용: %s / 여유: %s",
						p.Name, p.UsePct, s.CapacityPct, p.Size, p.Alloc, p.Free)
					fireAlert(cdb, s, "pool_capacity", p.Name, subj, body)
				}
			}
		}
	}

	// Prune old history weekly
	cdb.PruneAlertHistory(7 * 24 * time.Hour)
}
