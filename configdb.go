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
	`)
	return err
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
