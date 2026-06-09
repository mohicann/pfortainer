package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
)

const (
	cookieName   = "pfsession"
	cookieValue  = "authenticated"
	cookieMaxAge = 28800 // 8h
)

func setAuthCookie(w http.ResponseWriter, secret string) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    sign(secret, cookieValue),
		Path:     "/",
		MaxAge:   cookieMaxAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func clearAuthCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1})
}

func isAuthenticated(r *http.Request, secret string) bool {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return false
	}
	val, ok := verify(secret, c.Value)
	return ok && val == cookieValue
}

func sign(secret, value string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(value))
	return value + "." + hex.EncodeToString(mac.Sum(nil))
}

func verify(secret, signed string) (string, bool) {
	val, sig, ok := strings.Cut(signed, ".")
	if !ok {
		return "", false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(val))
	return val, hmac.Equal([]byte(sig), []byte(hex.EncodeToString(mac.Sum(nil))))
}
