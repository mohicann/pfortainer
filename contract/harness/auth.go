package harness

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"database/sql"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

// Role 숫자(config.db users.role, 마이그레이션 뒤 INTEGER).
var RoleRank = map[string]int{"viewer": 1, "operator": 2, "admin": 3}

// SignCookie: Rust session::sign과 같은 "username:role.<hex hmac>" 형식.
func SignCookie(secret, username, role string) string {
	payload := username + ":" + role
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(payload))
	return "pfsession=" + payload + "." + hex.EncodeToString(m.Sum(nil))
}

// Cookie: 테스트 시크릿으로 서명한 세션 쿠키(로그인 없이 권한표 등에 사용).
func Cookie(username, role string) string { return SignCookie(SessionSecret, username, role) }

// PreAuthCookie: totp.rs set_pre_auth_cookie_header와 같은 형식.
func PreAuthCookie(secret, username string, expiry int64) string {
	payload := fmt.Sprintf("%s|%d", username, expiry)
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(payload))
	return "pfpreauth=" + payload + "|" + hex.EncodeToString(m.Sum(nil))
}

// TOTPCode: RFC 6238 (SHA1, 30초, 6자리).
func TOTPCode(secret string, at time.Time) string {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil {
		panic(err)
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(at.Unix()/30))
	m := hmac.New(sha1.New, key)
	m.Write(msg[:])
	h := m.Sum(nil)
	off := h[len(h)-1] & 0x0f
	code := (uint32(h[off])&0x7f)<<24 | uint32(h[off+1])<<16 | uint32(h[off+2])<<8 | uint32(h[off+3])
	return fmt.Sprintf("%06d", code%1000000)
}

// DB: config.db 핸들(테스트 종료 시 닫힘).
func (a *App) DB(t testing.TB) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+a.ConfigDB+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// AddUser: bcrypt(cost 4) 해시로 사용자를 직접 넣는다. totpSecret이 있으면 2FA 켬.
func (a *App) AddUser(t testing.TB, username, password, role, totpSecret string) {
	t.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte(password), 4)
	if err != nil {
		t.Fatal(err)
	}
	enabled := 0
	if totpSecret != "" {
		enabled = 1
	}
	_, err = a.DB(t).Exec(`INSERT INTO users (username, password_hash, role, created_at, totp_secret, totp_enabled) VALUES (?,?,?,?,?,?)`,
		username, string(h), RoleRank[role], 1763500000, totpSecret, enabled)
	if err != nil {
		t.Fatalf("AddUser %s: %v", username, err)
	}
}

// Exec: config.db에 SQL 실행.
func (a *App) Exec(t testing.TB, q string, args ...any) {
	t.Helper()
	if _, err := a.DB(t).Exec(q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

// Login: POST /login 후 세션 쿠키("pfsession=...")를 돌려준다.
func (a *App) Login(t testing.TB, username, password string) string {
	t.Helper()
	r := a.Client("").PostForm(t, "/login", "username", username, "password", password)
	if r.Status != 303 {
		t.Fatalf("login %s: %d %s", username, r.Status, r.Body)
	}
	for _, sc := range r.Header.Values("Set-Cookie") {
		if strings.HasPrefix(sc, "pfsession=") {
			return strings.SplitN(sc, ";", 2)[0]
		}
	}
	t.Fatalf("login %s: 쿠키 없음", username)
	return ""
}

// StdUsers: viewer/operator/admin 계정을 추가한다(admin은 부트스트랩 계정).
func (a *App) StdUsers(t testing.TB) {
	t.Helper()
	a.AddUser(t, "viewer1", "viewer-pw-1234", "viewer", "")
	a.AddUser(t, "operator1", "operator-pw-1234", "operator", "")
}
