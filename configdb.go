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
	`)
	return err
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
