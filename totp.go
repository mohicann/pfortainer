package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ── TOTP 코어 ─────────────────────────────────────────────────────────────────

func generateTOTPSecret() string {
	key := make([]byte, 20) // 160-bit secret
	rand.Read(key)
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(key)
}

func totpCode(secret string, t time.Time) (string, error) {
	secret = strings.ToUpper(strings.TrimSpace(secret))
	if pad := len(secret) % 8; pad != 0 {
		secret += strings.Repeat("=", 8-pad)
	}
	key, err := base32.StdEncoding.DecodeString(secret)
	if err != nil {
		return "", err
	}
	counter := uint64(t.Unix()) / 30
	msg := make([]byte, 8)
	binary.BigEndian.PutUint64(msg, counter)
	mac := hmac.New(sha1.New, key)
	mac.Write(msg)
	h := mac.Sum(nil)
	offset := h[len(h)-1] & 0x0f
	code := (uint32(h[offset])&0x7f)<<24 |
		uint32(h[offset+1])<<16 |
		uint32(h[offset+2])<<8 |
		uint32(h[offset+3])
	return fmt.Sprintf("%06d", code%1000000), nil
}

// verifyTOTP accepts ±1 time window (90-second tolerance).
func verifyTOTP(secret, input string) bool {
	now := time.Now()
	for _, d := range []time.Duration{0, -30 * time.Second, 30 * time.Second} {
		expected, err := totpCode(secret, now.Add(d))
		if err == nil && expected == strings.TrimSpace(input) {
			return true
		}
	}
	return false
}

func totpAuthURI(secret, username string) string {
	return fmt.Sprintf(
		"otpauth://totp/pfortainer:%s?secret=%s&issuer=pfortainer&digits=6&period=30",
		username, secret,
	)
}

// ── Pre-auth 쿠키 (비밀번호 인증 후 TOTP 단계 전달용) ─────────────────────────

const preAuthCookieName = "pfpreauth"

func setPreAuthCookie(w http.ResponseWriter, username, secret string) {
	expiry := time.Now().Add(5 * time.Minute).Unix()
	payload := username + "|" + strconv.FormatInt(expiry, 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	value := payload + "|" + hex.EncodeToString(mac.Sum(nil))
	http.SetCookie(w, &http.Cookie{
		Name:     preAuthCookieName,
		Value:    value,
		Path:     "/",
		MaxAge:   300,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func getPreAuthUser(r *http.Request, secret string) (string, bool) {
	c, err := r.Cookie(preAuthCookieName)
	if err != nil {
		return "", false
	}
	parts := strings.Split(c.Value, "|")
	if len(parts) != 3 {
		return "", false
	}
	username, expiryStr, sig := parts[0], parts[1], parts[2]
	expiry, err := strconv.ParseInt(expiryStr, 10, 64)
	if err != nil || time.Now().Unix() > expiry {
		return "", false
	}
	payload := username + "|" + expiryStr
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	expected := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(sig), []byte(expected)) {
		return "", false
	}
	return username, true
}

func clearPreAuthCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: preAuthCookieName, Value: "", Path: "/", MaxAge: -1})
}
