package pf

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
	"modernc.org/sqlite"
	_ "modernc.org/sqlite"
)

// Role: viewer(1) < operator(2) < admin(3).
type Role int

const (
	RoleViewer   Role = 1
	RoleOperator Role = 2
	RoleAdmin    Role = 3
)

func (r Role) Str() string {
	switch r {
	case RoleOperator:
		return "operator"
	case RoleAdmin:
		return "admin"
	}
	return "viewer"
}

func (r Role) Display() string { return r.Str() }

func RoleFromStr(s string) (Role, bool) {
	switch s {
	case "viewer":
		return RoleViewer, true
	case "operator":
		return RoleOperator, true
	case "admin":
		return RoleAdmin, true
	}
	return 0, false
}

func roleFromInt(n int64) Role {
	switch n {
	case 2:
		return RoleOperator
	case 3:
		return RoleAdmin
	}
	return RoleViewer
}

type DbUser struct {
	ID        int64
	Username  string
	Role      Role
	CreatedAt time.Time
}

type DbSchedule struct {
	ID        int64
	Kind      string
	Target    string
	Frequency string
	Retention int64
	Prefix    string
	Enabled   bool
	LastRun   *time.Time
	NextRun   *time.Time
	CreatedAt time.Time
}

type DbReplTask struct {
	ID            int64
	Name          string
	SourceDataset string
	TargetPath    string
	Recursive     bool
	Schedule      string
	Retention     int64
	LastSnapshot  string
	LastRunAt     *time.Time
	LastStatus    string
	LastError     string
	Enabled       bool
	CreatedAt     time.Time
}

// errNoRows: rusqlite QueryReturnedNoRows 표시.
var errNoRows = errors.New("Query returned no rows")

// dbErr: modernc 오류를 rusqlite Display와 같은 문구로.
func dbErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return errNoRows
	}
	var se *sqlite.Error
	if errors.As(err, &se) {
		msg := se.Error()
		if i := strings.LastIndex(msg, " ("); i > 0 && strings.HasSuffix(msg, ")") {
			msg = msg[:i]
		}
		if i := strings.Index(msg, ": "); i > 0 && strings.HasPrefix(msg, "constraint failed") {
			msg = msg[i+2:]
		}
		return errors.New(msg)
	}
	return err
}

func unixNow() int64 { return time.Now().Unix() }

func fromUnix(n int64) time.Time { return time.Unix(n, 0).UTC() }

// NextRunTime: configdb::next_run_time (UTC).
func NextRunTime(freq string, from time.Time) time.Time {
	t := from.UTC()
	mid := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	switch freq {
	case "hourly":
		return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, time.UTC).Add(time.Hour)
	case "daily":
		return mid.AddDate(0, 0, 1)
	case "weekly":
		d := mid.AddDate(0, 0, 1)
		for d.Weekday() != time.Monday {
			d = d.AddDate(0, 0, 1)
		}
		return d
	case "monthly":
		return time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, time.UTC)
	}
	return t.Add(24 * time.Hour)
}

// ConfigDB: Rust ConfigDb(Mutex<Connection>)와 같이 연결 하나.
type ConfigDB struct {
	mu sync.Mutex
	db *sql.DB
}

func OpenConfigDB(path string) (*ConfigDB, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		return nil, err
	}
	c := &ConfigDB{db: db}
	if err := c.migrate(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *ConfigDB) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS users (
            id            INTEGER PRIMARY KEY,
            username      TEXT    UNIQUE NOT NULL,
            password_hash TEXT    NOT NULL,
            role          TEXT    NOT NULL DEFAULT 'viewer',
            created_at    INTEGER NOT NULL
        )`,
		`CREATE TABLE IF NOT EXISTS meta (
            key        TEXT PRIMARY KEY,
            value      TEXT NOT NULL,
            updated_at INTEGER NOT NULL
        )`,
		`CREATE TABLE IF NOT EXISTS schedules (
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
        )`,
		`CREATE TABLE IF NOT EXISTS alert_history (
            id         INTEGER PRIMARY KEY,
            event_type TEXT    NOT NULL,
            target     TEXT    NOT NULL,
            fired_at   INTEGER NOT NULL
        )`,
		`CREATE TABLE IF NOT EXISTS desktop_prefs (
            username   TEXT PRIMARY KEY,
            prefs      TEXT    NOT NULL,
            updated_at INTEGER NOT NULL
        )`,
		`CREATE TABLE IF NOT EXISTS replication_tasks (
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
        )`,
	}
	for _, s := range stmts {
		if _, err := c.db.Exec(s); err != nil {
			return err
		}
	}
	c.db.Exec("ALTER TABLE users ADD COLUMN totp_secret  TEXT NOT NULL DEFAULT ''")
	c.db.Exec("ALTER TABLE users ADD COLUMN totp_enabled INTEGER NOT NULL DEFAULT 0")
	c.db.Exec("ALTER TABLE replication_tasks ADD COLUMN retention INTEGER NOT NULL DEFAULT 14")

	var migrated string
	if err := c.db.QueryRow("SELECT value FROM meta WHERE key = 'users_role_migrated'").Scan(&migrated); err == nil && migrated == "1" {
		return nil
	}
	var roleType string
	rows, err := c.db.Query("PRAGMA table_info(users)")
	if err != nil {
		return err
	}
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var dflt any
		rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk)
		if name == "role" {
			roleType = typ
			break
		}
	}
	rows.Close()
	if strings.EqualFold(roleType, "TEXT") {
		for _, s := range []string{
			"CREATE TABLE users_backup AS SELECT * FROM users",
			`CREATE TABLE users_new (
                    id            INTEGER PRIMARY KEY,
                    username      TEXT    UNIQUE NOT NULL,
                    password_hash TEXT    NOT NULL,
                    role          INTEGER NOT NULL DEFAULT 1,
                    created_at    INTEGER NOT NULL,
                    totp_secret   TEXT    NOT NULL DEFAULT '',
                    totp_enabled  INTEGER NOT NULL DEFAULT 0
                )`,
			`INSERT INTO users_new (id, username, password_hash, role, created_at, totp_secret, totp_enabled)
                 SELECT id, username, password_hash,
                        CASE role
                            WHEN 'viewer' THEN 1
                            WHEN 'operator' THEN 2
                            WHEN 'admin' THEN 3
                            ELSE 1
                        END,
                        created_at,
                        totp_secret,
                        totp_enabled
                 FROM users`,
			"DROP TABLE users",
			"ALTER TABLE users_new RENAME TO users",
		} {
			if _, err := c.db.Exec(s); err != nil {
				return err
			}
		}
		c.db.Exec("DROP TABLE users_backup")
	}
	c.db.Exec("INSERT INTO meta (key, value, updated_at) VALUES ('users_role_migrated', '1', ?)", unixNow())
	return nil
}

// bcryptHash: Rust bcrypt 크레이트처럼 $2b$ 접두어.
func bcryptHash(pw string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(pw), 12)
	if err != nil {
		return "", err
	}
	s := string(h)
	if strings.HasPrefix(s, "$2a$") {
		s = "$2b$" + s[4:]
	}
	return s, nil
}

func bcryptVerify(pw, hash string) bool {
	h := hash
	if strings.HasPrefix(h, "$2b$") || strings.HasPrefix(h, "$2y$") {
		h = "$2a$" + h[4:]
	}
	// Rust bcrypt는 72바이트 넘는 비밀번호를 자른다
	p := []byte(pw)
	if len(p) > 72 {
		p = p[:72]
	}
	return bcrypt.CompareHashAndPassword([]byte(h), p) == nil
}

func (c *ConfigDB) BootstrapAdmin(pw string) error {
	c.mu.Lock()
	var n int64
	err := c.db.QueryRow("SELECT COUNT(*) FROM users").Scan(&n)
	c.mu.Unlock()
	if err != nil {
		return dbErr(err)
	}
	if n > 0 {
		return nil
	}
	return c.CreateUser("admin", pw, RoleAdmin)
}

func (c *ConfigDB) CreateUser(username, pw string, role Role) error {
	p := []byte(pw)
	if len(p) > 72 {
		pw = string(p[:72])
	}
	h, err := bcryptHash(pw)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err = c.db.Exec("INSERT INTO users (username, password_hash, role, created_at) VALUES (?, ?, ?, ?)", username, h, int64(role), unixNow())
	return dbErr(err)
}

func (c *ConfigDB) ListUsers() ([]DbUser, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	rows, err := c.db.Query("SELECT id, username, role, created_at FROM users ORDER BY id")
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	var out []DbUser
	for rows.Next() {
		var u DbUser
		var role, ca int64
		if err := rows.Scan(&u.ID, &u.Username, &role, &ca); err != nil {
			return nil, dbErr(err)
		}
		u.Role, u.CreatedAt = roleFromInt(role), fromUnix(ca)
		out = append(out, u)
	}
	return out, nil
}

func (c *ConfigDB) GetUser(username string) (*DbUser, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var u DbUser
	var role, ca int64
	err := c.db.QueryRow("SELECT id, username, role, created_at FROM users WHERE username = ?", username).Scan(&u.ID, &u.Username, &role, &ca)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, dbErr(err)
	}
	u.Role, u.CreatedAt = roleFromInt(role), fromUnix(ca)
	return &u, nil
}

func (c *ConfigDB) VerifyPassword(username, pw string) (*DbUser, error) {
	c.mu.Lock()
	var u DbUser
	var hash string
	var role, ca int64
	err := c.db.QueryRow("SELECT id, username, password_hash, role, created_at FROM users WHERE username = ?", username).Scan(&u.ID, &u.Username, &hash, &role, &ca)
	c.mu.Unlock()
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, dbErr(err)
	}
	if !bcryptVerify(pw, hash) {
		return nil, nil
	}
	u.Role, u.CreatedAt = roleFromInt(role), fromUnix(ca)
	return &u, nil
}

func (c *ConfigDB) UpdatePassword(username, pw string) error {
	p := []byte(pw)
	if len(p) > 72 {
		pw = string(p[:72])
	}
	h, err := bcryptHash(pw)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	res, err := c.db.Exec("UPDATE users SET password_hash = ? WHERE username = ?", h, username)
	if err != nil {
		return dbErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("user not found: %s", username)
	}
	return nil
}

func (c *ConfigDB) UpdateRole(username string, role Role) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	res, err := c.db.Exec("UPDATE users SET role = ? WHERE username = ?", int64(role), username)
	if err != nil {
		return dbErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("user not found: %s", username)
	}
	return nil
}

func (c *ConfigDB) DeleteUser(username string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	res, err := c.db.Exec("DELETE FROM users WHERE username = ?", username)
	if err != nil {
		return dbErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("user not found: %s", username)
	}
	_, err = c.db.Exec("DELETE FROM desktop_prefs WHERE username = ?", username)
	return dbErr(err)
}

// DesktopPrefsGet: 없으면 ("", false, nil).
func (c *ConfigDB) DesktopPrefsGet(username string) (string, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var p string
	err := c.db.QueryRow("SELECT prefs FROM desktop_prefs WHERE username = ?", username).Scan(&p)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, dbErr(err)
	}
	return p, true, nil
}

func (c *ConfigDB) DesktopPrefsSet(username, prefs string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err := c.db.Exec(`INSERT INTO desktop_prefs (username, prefs, updated_at) VALUES (?, ?, ?)
             ON CONFLICT(username) DO UPDATE SET prefs = excluded.prefs, updated_at = excluded.updated_at`, username, prefs, unixNow())
	return dbErr(err)
}

func (c *ConfigDB) MetaGet(key string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var v string
	err := c.db.QueryRow("SELECT value FROM meta WHERE key = ?", key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, dbErr(err)
}

func (c *ConfigDB) MetaSet(key, value string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err := c.db.Exec(`INSERT INTO meta (key, value, updated_at) VALUES (?, ?, ?)
             ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, key, value, unixNow())
	return dbErr(err)
}

func (c *ConfigDB) AlertFiredRecently(eventType, target string, cooldown time.Duration) bool {
	cutoff := time.Now().Add(-cooldown).Unix()
	c.mu.Lock()
	defer c.mu.Unlock()
	var n int64
	if err := c.db.QueryRow("SELECT COUNT(*) FROM alert_history WHERE event_type=? AND target=? AND fired_at > ?", eventType, target, cutoff).Scan(&n); err != nil {
		return false
	}
	return n > 0
}

func (c *ConfigDB) RecordAlert(eventType, target string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err := c.db.Exec("INSERT INTO alert_history (event_type, target, fired_at) VALUES (?, ?, ?)", eventType, target, unixNow())
	return dbErr(err)
}

func (c *ConfigDB) GetUserTOTP(username string) (string, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var s string
	var en int64
	err := c.db.QueryRow("SELECT totp_secret, totp_enabled FROM users WHERE username = ?", username).Scan(&s, &en)
	if err != nil {
		return "", false, dbErr(err)
	}
	return s, en == 1, nil
}

func (c *ConfigDB) SetTOTPSecret(username, secret string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err := c.db.Exec("UPDATE users SET totp_secret = ?, totp_enabled = 0 WHERE username = ?", secret, username)
	return dbErr(err)
}

func (c *ConfigDB) EnableTOTP(username string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err := c.db.Exec("UPDATE users SET totp_enabled = 1 WHERE username = ?", username)
	return dbErr(err)
}

func (c *ConfigDB) DisableTOTP(username string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err := c.db.Exec("UPDATE users SET totp_secret = '', totp_enabled = 0 WHERE username = ?", username)
	return dbErr(err)
}

func b2i(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

func (c *ConfigDB) CreateReplTask(t *DbReplTask) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	res, err := c.db.Exec(`INSERT INTO replication_tasks
             (name, source_dataset, target_path, recursive, schedule, retention, enabled, created_at)
             VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, t.Name, t.SourceDataset, t.TargetPath, b2i(t.Recursive), t.Schedule, t.Retention, b2i(t.Enabled), unixNow())
	if err != nil {
		return 0, dbErr(err)
	}
	id, _ := res.LastInsertId()
	return id, nil
}

func (c *ConfigDB) ListReplTasks() ([]DbReplTask, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	rows, err := c.db.Query(`SELECT id, name, source_dataset, target_path, recursive, schedule,
                    last_snapshot, last_run_at, last_status, last_error, enabled, created_at, retention
             FROM replication_tasks ORDER BY id`)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	var out []DbReplTask
	for rows.Next() {
		var t DbReplTask
		var rec, en, ca int64
		var lr sql.NullInt64
		if err := rows.Scan(&t.ID, &t.Name, &t.SourceDataset, &t.TargetPath, &rec, &t.Schedule, &t.LastSnapshot, &lr, &t.LastStatus, &t.LastError, &en, &ca, &t.Retention); err != nil {
			return nil, dbErr(err)
		}
		t.Recursive, t.Enabled, t.CreatedAt = rec == 1, en == 1, fromUnix(ca)
		if lr.Valid {
			x := fromUnix(lr.Int64)
			t.LastRunAt = &x
		}
		out = append(out, t)
	}
	return out, nil
}

func (c *ConfigDB) UpdateReplTaskResult(id int64, snap, status, errMsg string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err := c.db.Exec(`UPDATE replication_tasks
             SET last_snapshot=?, last_run_at=?, last_status=?, last_error=?
             WHERE id=?`, snap, unixNow(), status, errMsg, id)
	return dbErr(err)
}

func (c *ConfigDB) ToggleReplTask(id int64, enabled bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err := c.db.Exec("UPDATE replication_tasks SET enabled=? WHERE id=?", b2i(enabled), id)
	return dbErr(err)
}

func (c *ConfigDB) DeleteReplTask(id int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err := c.db.Exec("DELETE FROM replication_tasks WHERE id=?", id)
	return dbErr(err)
}

func (c *ConfigDB) DueReplTasks() ([]DbReplTask, error) {
	all, err := c.ListReplTasks()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	var out []DbReplTask
	for _, t := range all {
		if !t.Enabled || t.Schedule == "manual" {
			continue
		}
		if t.LastRunAt == nil || now.After(NextRunTime(t.Schedule, *t.LastRunAt)) {
			out = append(out, t)
		}
	}
	return out, nil
}

func (c *ConfigDB) CreateSchedule(s *DbSchedule) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var next any
	if s.NextRun != nil {
		next = s.NextRun.Unix()
	}
	res, err := c.db.Exec(`INSERT INTO schedules (type, target, frequency, retention, prefix, enabled, next_run, created_at)
             VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, s.Kind, s.Target, s.Frequency, s.Retention, s.Prefix, b2i(s.Enabled), next, unixNow())
	if err != nil {
		return 0, dbErr(err)
	}
	id, _ := res.LastInsertId()
	return id, nil
}

const schedCols = "id, type, target, frequency, retention, prefix, enabled, last_run, next_run, created_at"

func scanSchedule(sc interface{ Scan(...any) error }) (DbSchedule, error) {
	var s DbSchedule
	var en, ca int64
	var lr, nr sql.NullInt64
	if err := sc.Scan(&s.ID, &s.Kind, &s.Target, &s.Frequency, &s.Retention, &s.Prefix, &en, &lr, &nr, &ca); err != nil {
		return s, err
	}
	s.Enabled, s.CreatedAt = en == 1, fromUnix(ca)
	if lr.Valid {
		x := fromUnix(lr.Int64)
		s.LastRun = &x
	}
	if nr.Valid {
		x := fromUnix(nr.Int64)
		s.NextRun = &x
	}
	return s, nil
}

func (c *ConfigDB) ListSchedules() ([]DbSchedule, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	rows, err := c.db.Query("SELECT " + schedCols + " FROM schedules ORDER BY id")
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	var out []DbSchedule
	for rows.Next() {
		s, err := scanSchedule(rows)
		if err != nil {
			return nil, dbErr(err)
		}
		out = append(out, s)
	}
	return out, nil
}

func (c *ConfigDB) GetSchedule(id int64) (DbSchedule, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, err := scanSchedule(c.db.QueryRow("SELECT "+schedCols+" FROM schedules WHERE id=?", id))
	return s, dbErr(err)
}

func (c *ConfigDB) UpdateScheduleRun(id int64, last, next time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err := c.db.Exec("UPDATE schedules SET last_run=?, next_run=? WHERE id=?", last.Unix(), next.Unix(), id)
	return dbErr(err)
}

func (c *ConfigDB) ToggleSchedule(id int64, enabled bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err := c.db.Exec("UPDATE schedules SET enabled=? WHERE id=?", b2i(enabled), id)
	return dbErr(err)
}

func (c *ConfigDB) DeleteSchedule(id int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err := c.db.Exec("DELETE FROM schedules WHERE id=?", id)
	return dbErr(err)
}

func (c *ConfigDB) DueSchedules() ([]DbSchedule, error) {
	c.mu.Lock()
	rows, err := c.db.Query("SELECT id FROM schedules WHERE enabled=1 AND (next_run IS NULL OR next_run <= ?)", unixNow())
	if err != nil {
		c.mu.Unlock()
		return nil, dbErr(err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	c.mu.Unlock()
	var out []DbSchedule
	for _, id := range ids {
		if s, err := c.GetSchedule(id); err == nil {
			out = append(out, s)
		}
	}
	return out, nil
}
