package main

import (
	"database/sql"
	"fmt"
	"time"

	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

// Role constants. roleRank determines precedence: higher = more privileged.
const (
	RoleViewer   = "viewer"
	RoleOperator = "operator"
	RoleAdmin    = "admin"
)

var roleRank = map[string]int{
	RoleViewer:   1,
	RoleOperator: 2,
	RoleAdmin:    3,
}

// roleAtLeast reports whether actual satisfies the minimum required role.
func roleAtLeast(actual, required string) bool {
	return roleRank[actual] >= roleRank[required]
}

// ── DB ────────────────────────────────────────────────────────────────────────

type ConfigDB struct {
	db *sql.DB
}

type DBUser struct {
	ID        int64
	Username  string
	Role      string
	CreatedAt time.Time
}

func openConfigDB(path string) (*ConfigDB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		return nil, err
	}
	return &ConfigDB{db: db}, nil
}

func migrate(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS users (
			id            INTEGER PRIMARY KEY,
			username      TEXT    UNIQUE NOT NULL,
			password_hash TEXT    NOT NULL,
			role          TEXT    NOT NULL DEFAULT 'viewer',
			created_at    INTEGER NOT NULL
		);
		CREATE TABLE IF NOT EXISTS meta (
			key        TEXT PRIMARY KEY,
			value      TEXT NOT NULL,
			updated_at INTEGER NOT NULL
		);
		CREATE TABLE IF NOT EXISTS schedules (
			id         INTEGER PRIMARY KEY,
			type       TEXT    NOT NULL,
			target     TEXT    NOT NULL,
			frequency  TEXT    NOT NULL,
			retention  INTEGER NOT NULL DEFAULT 7,
			prefix     TEXT    NOT NULL DEFAULT 'auto',
			enabled    INTEGER NOT NULL DEFAULT 1,
			last_run   INTEGER,
			next_run   INTEGER,
			created_at INTEGER NOT NULL
		);
		CREATE TABLE IF NOT EXISTS alert_history (
			id         INTEGER PRIMARY KEY,
			event_type TEXT    NOT NULL,
			target     TEXT    NOT NULL,
			fired_at   INTEGER NOT NULL
		);
		CREATE TABLE IF NOT EXISTS replication_tasks (
			id             INTEGER PRIMARY KEY AUTOINCREMENT,
			name           TEXT    NOT NULL UNIQUE,
			source_dataset TEXT    NOT NULL,
			target_path    TEXT    NOT NULL,
			recursive      INTEGER NOT NULL DEFAULT 0,
			schedule       TEXT    NOT NULL DEFAULT 'manual',
			last_snapshot  TEXT    NOT NULL DEFAULT '',
			last_run_at    INTEGER,
			last_status    TEXT    NOT NULL DEFAULT '',
			last_error     TEXT    NOT NULL DEFAULT '',
			enabled        INTEGER NOT NULL DEFAULT 1,
			created_at     INTEGER NOT NULL
		);
	`)
	if err != nil {
		return err
	}
	// ADD COLUMN은 IF NOT EXISTS 미지원 — 에러 무시(이미 존재하는 경우)
	db.Exec(`ALTER TABLE users ADD COLUMN totp_secret  TEXT NOT NULL DEFAULT ''`)
	db.Exec(`ALTER TABLE users ADD COLUMN totp_enabled INTEGER NOT NULL DEFAULT 0`)
	return nil
}

// ── Replication Task CRUD ─────────────────────────────────────────────────────

type DBReplTask struct {
	ID            int64
	Name          string
	SourceDataset string
	TargetPath    string
	Recursive     bool
	Schedule      string // "manual"|"hourly"|"daily"|"weekly"
	LastSnapshot  string
	LastRunAt     *time.Time
	LastStatus    string // ""|"ok"|"error"
	LastError     string
	Enabled       bool
	CreatedAt     time.Time
}

func (c *ConfigDB) CreateReplTask(t DBReplTask) (int64, error) {
	res, err := c.db.Exec(
		`INSERT INTO replication_tasks
		 (name, source_dataset, target_path, recursive, schedule, enabled, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		t.Name, t.SourceDataset, t.TargetPath, boolInt(t.Recursive),
		t.Schedule, boolInt(t.Enabled), time.Now().Unix(),
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (c *ConfigDB) ListReplTasks() ([]DBReplTask, error) {
	rows, err := c.db.Query(
		`SELECT id, name, source_dataset, target_path, recursive, schedule,
		        last_snapshot, last_run_at, last_status, last_error, enabled, created_at
		 FROM replication_tasks ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DBReplTask
	for rows.Next() {
		var t DBReplTask
		var recursive, enabled int
		var lastRunAt *int64
		var createdAt int64
		if err := rows.Scan(
			&t.ID, &t.Name, &t.SourceDataset, &t.TargetPath,
			&recursive, &t.Schedule, &t.LastSnapshot,
			&lastRunAt, &t.LastStatus, &t.LastError, &enabled, &createdAt,
		); err != nil {
			return nil, err
		}
		t.Recursive = recursive == 1
		t.Enabled = enabled == 1
		t.CreatedAt = time.Unix(createdAt, 0)
		if lastRunAt != nil {
			ts := time.Unix(*lastRunAt, 0)
			t.LastRunAt = &ts
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (c *ConfigDB) UpdateReplTaskResult(id int64, snap, status, errMsg string) error {
	now := time.Now().Unix()
	_, err := c.db.Exec(
		`UPDATE replication_tasks
		 SET last_snapshot=?, last_run_at=?, last_status=?, last_error=?
		 WHERE id=?`,
		snap, now, status, errMsg, id,
	)
	return err
}

func (c *ConfigDB) ToggleReplTask(id int64, enabled bool) error {
	_, err := c.db.Exec("UPDATE replication_tasks SET enabled=? WHERE id=?", boolInt(enabled), id)
	return err
}

func (c *ConfigDB) DeleteReplTask(id int64) error {
	_, err := c.db.Exec("DELETE FROM replication_tasks WHERE id=?", id)
	return err
}

func (c *ConfigDB) DueReplTasks() ([]DBReplTask, error) {
	all, err := c.ListReplTasks()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	var due []DBReplTask
	for _, t := range all {
		if !t.Enabled || t.Schedule == "manual" {
			continue
		}
		if t.LastRunAt == nil {
			due = append(due, t)
			continue
		}
		next := nextRunTime(t.Schedule, *t.LastRunAt)
		if now.After(next) {
			due = append(due, t)
		}
	}
	return due, nil
}

// ── Schedule CRUD ─────────────────────────────────────────────────────────────

type DBSchedule struct {
	ID        int64
	Type      string // "snapshot" | "scrub"
	Target    string // dataset (snapshot) or pool name (scrub)
	Frequency string // "hourly" | "daily" | "weekly" | "monthly"
	Retention int    // keep N snapshots (0 = unlimited)
	Prefix    string // auto-snapshot name prefix
	Enabled   bool
	LastRun   *time.Time
	NextRun   *time.Time
	CreatedAt time.Time
}

func (c *ConfigDB) CreateSchedule(s DBSchedule) (int64, error) {
	res, err := c.db.Exec(
		`INSERT INTO schedules (type, target, frequency, retention, prefix, enabled, next_run, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		s.Type, s.Target, s.Frequency, s.Retention, s.Prefix,
		boolInt(s.Enabled), unixOrNil(s.NextRun), time.Now().Unix(),
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (c *ConfigDB) ListSchedules() ([]DBSchedule, error) {
	rows, err := c.db.Query(
		`SELECT id, type, target, frequency, retention, prefix, enabled, last_run, next_run, created_at
		 FROM schedules ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DBSchedule
	for rows.Next() {
		var s DBSchedule
		var enabled int
		var lastRun, nextRun *int64
		var createdAt int64
		if err := rows.Scan(&s.ID, &s.Type, &s.Target, &s.Frequency, &s.Retention,
			&s.Prefix, &enabled, &lastRun, &nextRun, &createdAt); err != nil {
			return nil, err
		}
		s.Enabled = enabled != 0
		s.CreatedAt = time.Unix(createdAt, 0)
		if lastRun != nil {
			t := time.Unix(*lastRun, 0)
			s.LastRun = &t
		}
		if nextRun != nil {
			t := time.Unix(*nextRun, 0)
			s.NextRun = &t
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (c *ConfigDB) GetSchedule(id int64) (*DBSchedule, error) {
	var s DBSchedule
	var lastRun, nextRun *int64
	var createdAt, enabled int64
	err := c.db.QueryRow(
		`SELECT id, type, target, frequency, retention, prefix, enabled, last_run, next_run, created_at
		 FROM schedules WHERE id=?`, id,
	).Scan(&s.ID, &s.Type, &s.Target, &s.Frequency, &s.Retention,
		&s.Prefix, &enabled, &lastRun, &nextRun, &createdAt)
	if err != nil {
		return nil, err
	}
	s.Enabled = enabled != 0
	s.CreatedAt = time.Unix(createdAt, 0)
	if lastRun != nil {
		t := time.Unix(*lastRun, 0)
		s.LastRun = &t
	}
	if nextRun != nil {
		t := time.Unix(*nextRun, 0)
		s.NextRun = &t
	}
	return &s, nil
}

func (c *ConfigDB) UpdateScheduleRun(id int64, lastRun, nextRun time.Time) error {
	_, err := c.db.Exec(
		"UPDATE schedules SET last_run=?, next_run=? WHERE id=?",
		lastRun.Unix(), nextRun.Unix(), id,
	)
	return err
}

func (c *ConfigDB) ToggleSchedule(id int64, enabled bool) error {
	_, err := c.db.Exec("UPDATE schedules SET enabled=? WHERE id=?", boolInt(enabled), id)
	return err
}

func (c *ConfigDB) DeleteSchedule(id int64) error {
	_, err := c.db.Exec("DELETE FROM schedules WHERE id=?", id)
	return err
}

func (c *ConfigDB) DueSchedules() ([]DBSchedule, error) {
	now := time.Now().Unix()
	rows, err := c.db.Query(
		`SELECT id FROM schedules WHERE enabled=1 AND (next_run IS NULL OR next_run <= ?)`, now)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	var out []DBSchedule
	for _, id := range ids {
		s, err := c.GetSchedule(id)
		if err != nil {
			continue
		}
		out = append(out, *s)
	}
	return out, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func unixOrNil(t *time.Time) *int64 {
	if t == nil {
		return nil
	}
	v := t.Unix()
	return &v
}

// BootstrapAdmin creates an "admin" user with the given password only when the
// users table is empty (first launch). This lets existing deployments keep
// using ADMIN_PASSWORD without any manual migration step.
func (c *ConfigDB) BootstrapAdmin(password string) error {
	var count int
	if err := c.db.QueryRow("SELECT COUNT(*) FROM users").Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	return c.CreateUser("admin", password, RoleAdmin)
}

// ── User CRUD ─────────────────────────────────────────────────────────────────

func (c *ConfigDB) CreateUser(username, password, role string) error {
	if _, ok := roleRank[role]; !ok {
		return fmt.Errorf("invalid role: %q", role)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return err
	}
	_, err = c.db.Exec(
		"INSERT INTO users (username, password_hash, role, created_at) VALUES (?, ?, ?, ?)",
		username, string(hash), role, time.Now().Unix(),
	)
	return err
}

func (c *ConfigDB) ListUsers() ([]DBUser, error) {
	rows, err := c.db.Query("SELECT id, username, role, created_at FROM users ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []DBUser
	for rows.Next() {
		var u DBUser
		var ts int64
		if err := rows.Scan(&u.ID, &u.Username, &u.Role, &ts); err != nil {
			return nil, err
		}
		u.CreatedAt = time.Unix(ts, 0)
		users = append(users, u)
	}
	return users, rows.Err()
}

func (c *ConfigDB) GetUser(username string) (*DBUser, error) {
	var u DBUser
	var ts int64
	err := c.db.QueryRow(
		"SELECT id, username, role, created_at FROM users WHERE username = ?", username,
	).Scan(&u.ID, &u.Username, &u.Role, &ts)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	u.CreatedAt = time.Unix(ts, 0)
	return &u, nil
}

// VerifyPassword checks credentials and returns the user on success.
func (c *ConfigDB) VerifyPassword(username, password string) (*DBUser, error) {
	var u DBUser
	var hash string
	var ts int64
	err := c.db.QueryRow(
		"SELECT id, username, password_hash, role, created_at FROM users WHERE username = ?",
		username,
	).Scan(&u.ID, &u.Username, &hash, &u.Role, &ts)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		return nil, nil
	}
	u.CreatedAt = time.Unix(ts, 0)
	return &u, nil
}

func (c *ConfigDB) UpdatePassword(username, newPassword string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), 12)
	if err != nil {
		return err
	}
	res, err := c.db.Exec(
		"UPDATE users SET password_hash = ? WHERE username = ?", string(hash), username,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("user not found: %s", username)
	}
	return nil
}

func (c *ConfigDB) UpdateRole(username, role string) error {
	if _, ok := roleRank[role]; !ok {
		return fmt.Errorf("invalid role: %q", role)
	}
	res, err := c.db.Exec(
		"UPDATE users SET role = ? WHERE username = ?", role, username,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("user not found: %s", username)
	}
	return nil
}

func (c *ConfigDB) DeleteUser(username string) error {
	res, err := c.db.Exec("DELETE FROM users WHERE username = ?", username)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("user not found: %s", username)
	}
	return nil
}

// ── Meta (key-value 설정 스토어) ─────────────────────────────────────────────

func (c *ConfigDB) MetaGet(key string) (string, error) {
	var val string
	err := c.db.QueryRow("SELECT value FROM meta WHERE key = ?", key).Scan(&val)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return val, err
}

func (c *ConfigDB) MetaSet(key, value string) error {
	_, err := c.db.Exec(
		`INSERT INTO meta (key, value, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value, time.Now().Unix(),
	)
	return err
}

// ── Alert History ─────────────────────────────────────────────────────────────

// AlertFiredRecently reports whether an alert for (eventType, target) was fired
// within the given cooldown window.
func (c *ConfigDB) AlertFiredRecently(eventType, target string, cooldown time.Duration) bool {
	cutoff := time.Now().Add(-cooldown).Unix()
	var count int
	c.db.QueryRow(
		"SELECT COUNT(*) FROM alert_history WHERE event_type=? AND target=? AND fired_at > ?",
		eventType, target, cutoff,
	).Scan(&count)
	return count > 0
}

func (c *ConfigDB) RecordAlert(eventType, target string) error {
	_, err := c.db.Exec(
		"INSERT INTO alert_history (event_type, target, fired_at) VALUES (?, ?, ?)",
		eventType, target, time.Now().Unix(),
	)
	return err
}

// PruneAlertHistory removes history older than the given duration.
func (c *ConfigDB) PruneAlertHistory(olderThan time.Duration) {
	cutoff := time.Now().Add(-olderThan).Unix()
	c.db.Exec("DELETE FROM alert_history WHERE fired_at < ?", cutoff)
}

// ── TOTP 2FA ─────────────────────────────────────────────────────────────────

func (c *ConfigDB) GetUserTOTP(username string) (secret string, enabled bool, err error) {
	var e int
	err = c.db.QueryRow(
		"SELECT totp_secret, totp_enabled FROM users WHERE username = ?", username,
	).Scan(&secret, &e)
	enabled = e == 1
	return
}

func (c *ConfigDB) SetTOTPSecret(username, secret string) error {
	_, err := c.db.Exec(
		"UPDATE users SET totp_secret = ?, totp_enabled = 0 WHERE username = ?", secret, username,
	)
	return err
}

func (c *ConfigDB) EnableTOTP(username string) error {
	_, err := c.db.Exec(
		"UPDATE users SET totp_enabled = 1 WHERE username = ?", username,
	)
	return err
}

func (c *ConfigDB) DisableTOTP(username string) error {
	_, err := c.db.Exec(
		"UPDATE users SET totp_secret = '', totp_enabled = 0 WHERE username = ?", username,
	)
	return err
}
