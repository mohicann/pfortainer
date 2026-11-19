package pf

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mohicann/pfortainer/internal/httpx"
)

const (
	cookieName    = "pfsession"
	cookieMaxAge  = 28800
	preAuthCookie = "pfpreauth"
)

// SessionUser: 템플릿에서 current_user.
type SessionUser struct {
	Username string `json:"username"`
	Role     Role   `json:"role"`
}

func hmacHex(secret, val string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(val))
	return hex.EncodeToString(m.Sum(nil))
}

func sign(secret, val string) string { return val + "." + hmacHex(secret, val) }

func verify(secret, signed string) (string, bool) {
	i := strings.LastIndex(signed, ".")
	if i < 0 {
		return "", false
	}
	val, sig := signed[:i], signed[i+1:]
	exp := hmacHex(secret, val)
	if len(sig) != len(exp) || subtle.ConstantTimeCompare([]byte(sig), []byte(exp)) != 1 {
		return "", false
	}
	return val, true
}

func authCookieHeader(secret, username string, role Role) string {
	return fmt.Sprintf("%s=%s; Path=/; Max-Age=%d; HttpOnly; SameSite=Lax", cookieName, sign(secret, username+":"+role.Str()), cookieMaxAge)
}

func clearAuthCookieHeader() string {
	return cookieName + "=; Path=/; Max-Age=0; HttpOnly; SameSite=Lax"
}

// parseCookie: session::parse_cookie — 첫 번째로 이름이 맞는 쿠키.
func parseCookie(header, name string) (string, bool) {
	for _, part := range strings.Split(header, ";") {
		part = strings.TrimSpace(part)
		k, v, ok := strings.Cut(part, "=")
		if ok && strings.TrimSpace(k) == name {
			return strings.TrimSpace(v), true
		}
	}
	return "", false
}

// cookieHeader: axum headers.get(COOKIE) — 첫 Cookie 헤더(유효한 문자열일 때).
func cookieHeader(r *http.Request) (string, bool) {
	vs := r.Header.Values("Cookie")
	if len(vs) == 0 {
		return "", false
	}
	for i := 0; i < len(vs[0]); i++ {
		c := vs[0][i]
		if c < 0x20 && c != '\t' || c >= 0x7f {
			return "", false // to_str() 실패
		}
	}
	return vs[0], true
}

func sessionUser(r *http.Request, secret string) (SessionUser, bool) {
	h, ok := cookieHeader(r)
	if !ok {
		return SessionUser{}, false
	}
	raw, ok := parseCookie(h, cookieName)
	if !ok {
		return SessionUser{}, false
	}
	payload, ok := verify(secret, raw)
	if !ok {
		return SessionUser{}, false
	}
	username, roleStr, ok := strings.Cut(payload, ":")
	if !ok || username == "" {
		return SessionUser{}, false
	}
	role, ok := RoleFromStr(roleStr)
	if !ok {
		return SessionUser{}, false
	}
	return SessionUser{Username: username, Role: role}, true
}

// ── TOTP ─────────────────────────────────────────────────────────────────────

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

func generateTOTPSecret() string {
	k := make([]byte, 20)
	rand.Read(k)
	return b32.EncodeToString(k)
}

func totpCodeAt(secret string, unix int64) (string, bool) {
	cleaned := strings.ToUpper(strings.TrimSpace(secret))
	key, err := b32.DecodeString(cleaned)
	if err != nil {
		key, err = base32.StdEncoding.DecodeString(cleaned)
		if err != nil {
			return "", false
		}
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(unix/30))
	m := hmac.New(sha1.New, key)
	m.Write(msg[:])
	h := m.Sum(nil)
	off := h[len(h)-1] & 0x0f
	code := (uint32(h[off])&0x7f)<<24 | uint32(h[off+1])<<16 | uint32(h[off+2])<<8 | uint32(h[off+3])
	return fmt.Sprintf("%06d", code%1000000), true
}

func verifyTOTP(secret, input string) bool {
	now := time.Now().Unix()
	input = strings.TrimSpace(input)
	for _, d := range []int64{0, -30, 30} {
		if exp, ok := totpCodeAt(secret, now+d); ok && exp == input {
			return true
		}
	}
	return false
}

func totpAuthURI(secret, username string) string {
	return fmt.Sprintf("otpauth://totp/pfortainer:%s?secret=%s&issuer=pfortainer&digits=6&period=30", username, secret)
}

func preAuthCookieHeader(secret, username string) string {
	exp := time.Now().Unix() + 300
	payload := fmt.Sprintf("%s|%d", username, exp)
	return fmt.Sprintf("%s=%s|%s; Path=/; Max-Age=300; HttpOnly; SameSite=Lax", preAuthCookie, payload, hmacHex(secret, payload))
}

func clearPreAuthCookieHeader() string {
	return preAuthCookie + "=; Path=/; Max-Age=0; HttpOnly; SameSite=Lax"
}

func preAuthUser(r *http.Request, secret string) (string, bool) {
	h, ok := cookieHeader(r)
	if !ok {
		return "", false
	}
	var val string
	found := false
	for _, part := range strings.Split(h, ";") {
		part = strings.TrimSpace(part)
		if v, ok := strings.CutPrefix(part, preAuthCookie+"="); ok {
			val, found = v, true
			break
		}
	}
	if !found {
		return "", false
	}
	parts := strings.Split(val, "|")
	if len(parts) != 3 {
		return "", false
	}
	exp, msg := httpx.ParseI64(parts[1])
	if msg != "" {
		return "", false
	}
	if time.Now().Unix() > exp {
		return "", false
	}
	if hmacHex(secret, parts[0]+"|"+parts[1]) != parts[2] {
		return "", false
	}
	return parts[0], true
}

var _ = strconv.Itoa
